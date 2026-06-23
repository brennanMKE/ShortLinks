# Links — key generation, redirect, and caching

This document covers the core link domain: how short links are created (key
generation, deduplication, custom aliases, expiry), how the public redirect
path resolves a key to a destination, how the Ristretto cache sits in front of
the database on that path, and how the displayed short URL is composed.

UTM parameter passthrough on redirect is covered in `docs/utm.md`.

---

## Data model

Migration `migrations/000002_create_links.up.sql` defines the `links` table:

```sql
CREATE TABLE links (
    id              BIGSERIAL PRIMARY KEY,
    user_id         BIGINT NOT NULL REFERENCES users(id),
    key             VARCHAR(12) UNIQUE NOT NULL,
    destination_url TEXT NOT NULL,
    title           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ,                        -- NULL = never expires
    active          BOOLEAN DEFAULT TRUE,
    denied_reason   SMALLINT NOT NULL DEFAULT 0         -- 0 = permitted
);
```

The `key` column has a `UNIQUE` constraint that spans all users; it also
provides the primary index for the redirect-path lookup. Two additional partial
indexes support deduplication and admin denied-link queries:

- `idx_links_user_destination` on `(user_id, destination_url) WHERE denied_reason = 0`
  — the dedup lookup only considers non-denied links.
- `idx_links_denied_reason` on `(denied_reason) WHERE denied_reason > 0`
  — admin queries over blocked submissions.

Effective link state is encoded by two columns together:

| `active` | `denied_reason` | Effective state |
|---|---|---|
| `true` | `0` | Active — redirects normally |
| `false` | `0` | Inactive — redirect returns 404 |
| `false` | `> 0` | Denied — blocked by URL filter, redirect returns 404 |

---

## Link creation — `POST /api/links`

Handler: `internal/handlers/links.go` `LinksHandler.Create`.

The create path runs three ordered steps before committing any row:

### 1. URL filter check (runs first)

When a URL filter rule cache is wired, `filters.Evaluate` tests the destination
URL against the active rule set. On a match the handler calls
`Store.CreateDeniedLink` (which mints a unique generated key and inserts a row
with `active=false`, `denied_reason=<code>`) and returns `422 url_denied`. The
deduplication and insert steps below never run for a denied URL.

### 2. Key resolution

The request body accepts a custom alias in any of three fields (`key`,
`custom_key`, or `alias`; the first non-empty value wins). When a custom alias
is present the handler validates and uses it directly. When no alias is
supplied the store generates one.

**Custom alias validation** (`validKey` in `internal/handlers/links.go`): 1–12
characters drawn from `[a-zA-Z0-9\-_]`. The 12-character ceiling matches the
`links.key` column definition (`VARCHAR(12)`).

**Generated key** (`internal/links/keygen.go`): a 6-character base-62 string
drawn from the alphabet `abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ`.
Characters are selected using `crypto/rand` with rejection sampling (bytes
`>= 248` are discarded) to avoid modulo bias across the 62-character alphabet.
The constant `KeyLength = 6` controls the length.

`GenerateUniqueKey` wraps `GenerateKey`, retrying up to `maxKeyAttempts` (5)
times with a caller-supplied `exists` callback to confirm the key is free. It
returns `ErrKeyCollision` if every attempt collides. The callback is a
`func(key string) (bool, error)` so the logic is testable without a database.

### 3. Deduplication (generated-key path only)

Custom aliases are never deduplicated. For generated keys,
`Store.CreateOrReactivateLink` (`internal/links/store.go`) runs the full
dedup decision atomically in a single transaction:

1. `SELECT ... FOR UPDATE` finds an existing non-denied link for
   `(user_id, destination_url)`.
2. **Active match** — returned unchanged; `duplicate=true`; no SSE broadcast.
3. **Inactive match** — reactivated (`active=true`); `duplicate=true`; SSE
   broadcast fires.
4. **No match** — a new key is minted (key-existence check runs inside the
   transaction) and a fresh row is inserted; `duplicate=false`; SSE broadcast
   fires.

The `FOR UPDATE` lock ensures two concurrent creates of the same URL serialize
rather than racing to insert a duplicate.

`CreateOutcome` (`OutcomeInserted`, `OutcomeActiveDuplicate`, `OutcomeReactivated`)
tells the handler which branch was taken so it can set the `duplicate` response
field and decide whether to fire the SSE and audit seams.

### 4. Expiry

`expires_at` is an optional `*time.Time` in the `POST /api/links` body (RFC
3339). `nil` means the link never expires. The value is stored as `TIMESTAMPTZ`
in the database and carried through the cache to the redirect handler, which
enforces it at request time (see below). Expiry can be cleared by `PATCH`ing
`expires_at: null`.

### 5. `Store.CreateLink` — the insert step

For a fresh insert (custom alias or the no-match branch of the generated-key
path), `Store.CreateLink` (`internal/links/store.go`) runs:

```sql
INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at)
VALUES ($1, $2, $3, $4, $5, TRUE, 0, now())
RETURNING id, created_at, expires_at
```

A UNIQUE constraint violation on `key` (possible for a custom alias that was
taken between the caller's pre-check and the insert) surfaces as `ErrKeyTaken`,
which the handler maps to `409 Conflict`.

---

## Redirect path — `GET /u/{key}`

Handler: `internal/handlers/redirect.go` `RedirectHandler.ServeHTTP`.

The handler follows a seven-step flow:

1. Extract `key` from the URL path (`r.PathValue("key")`). Empty key → `404`.
2. **Resolve via cache → DB** (see Ristretto cache section below). A non-nil
   error → `500`; not-found (negative entry or genuine DB miss) → `404`.
3. **Unknown / inactive / denied link** — `!found || link.Negative || !link.Active` → `404`.
   A denied link (`denied_reason > 0`) has `active=false` and is caught here.
4. **Expired link** — `link.ExpiresAt != nil && !link.ExpiresAt.After(now())` → `410 Gone`.
5. **Record click asynchronously** — `buildClickInfo` snapshots request metadata
   (IP from `X-Forwarded-For` first, then `RemoteAddr`; user-agent; referer;
   UTM params). `go h.recorder.RecordClick(info)` fires in a goroutine so the
   database write never blocks the redirect.
6. **Merge inbound UTM parameters** onto the destination URL via `mergeUTM`.
   Inbound `utm_*` values override the destination's own values for the same
   key; all other query parameters in the request are ignored.
7. **`302 Found`** with `Location` set to the merged destination URL.

The `now` function is injectable (`RedirectHandler.now`) so expiry checks are
deterministic in tests.

---

## Ristretto redirect cache — `internal/cache/cache.go`

The cache is a `*ristretto.Cache[string, *CachedLink]` wrapping Dgraph's
Ristretto v2. It maps short-link keys to `CachedLink` values and is safe for
concurrent use.

### What is cached

`CachedLink` carries the fields the redirect handler needs:

| Field | Type | Notes |
|---|---|---|
| `DestinationURL` | `string` | Target URL |
| `Active` | `bool` | Whether the link is active |
| `ExpiresAt` | `*time.Time` | Expiry time; `nil` = never expires |
| `DeniedReason` | `int16` | Non-zero = blocked by URL filter |
| `Negative` | `bool` | `true` = key is known absent from DB |

Negative entries record that a key does not exist in the database. The resolver
(`internal/links/resolver.go`) converts a negative cache hit to `found=false`
before returning it to the handler, so the handler never sees the `Negative`
field directly — it treats both a negative entry and a genuine DB miss as `404`.

### TTLs

| Entry type | TTL | Constant |
|---|---|---|
| Positive (link found) | 300 s default | `DefaultTTL` |
| Negative (key absent) | 30 s | `NegativeTTL` |

The positive TTL is configurable via `CACHE_TTL_SECONDS` (parsed at startup;
non-positive values fall back to `DefaultTTL`).

### Cache sizing — `CACHE_MAX_COST`

Ristretto sizes the cache by cost. Every entry costs exactly 1 unit
(`entryCost = 1`), so `CACHE_MAX_COST` is effectively the maximum number of
entries. The default is `DefaultMaxCost = 10000`. Ristretto's `NumCounters` is
set to `MaxCost * 10` per the library's recommended ratio for accurate eviction.

### Hit / miss flow

`Resolver.Resolve` (`internal/links/resolver.go`) implements the cache→DB
read-through:

1. Call `cache.Get(key)`.
   - **Positive hit** — return the entry to the handler.
   - **Negative hit** (`entry.Negative == true`) — return `found=false` without
     querying the DB.
2. **Miss** — call `Store.ResolveByKey`, which queries:
   ```sql
   SELECT destination_url, active, expires_at, denied_reason
     FROM links
    WHERE key = $1
   ```
   - **Row found** — store a positive entry in the cache with the configured
     TTL, return it.
   - **`ErrLinkNotFound`** — store a negative entry (`SetNegative`) with
     `NegativeTTL`, return `found=false`. This absorbs burst lookups of invalid
     keys without hammering the database.
   - **DB error** — return the error (no cache write).

`ResolveByKey` is NOT user-scoped: the redirect path is public and a key
uniquely identifies a link across all users.

### Invalidation

The cache does not use TTL-only invalidation for mutations. The `LinksHandler`
calls `cache.Delete(key)` (via `h.evict`) immediately after:

- **`DELETE /api/links/{key}`** (deactivation) — evict so the next redirect
  sees `active=false` and returns `404`.
- **`PATCH /api/links/{key}`** (update) — evict so a changed destination URL
  or expiry is reflected immediately rather than waiting for the TTL.

`Cache.Delete` calls `ristretto.Cache.Del`. Because Ristretto's writes
(including deletions) are buffered asynchronously, tests that must observe the
deletion via `Get` call `cache.Wait()` first.

### Tuning

| Variable | Default | Effect |
|---|---|---|
| `CACHE_MAX_COST` | `10000` | Maximum number of cached entries |
| `CACHE_TTL_SECONDS` | `300` | Positive-entry TTL in seconds |

Negative entries always use the 30-second `NegativeTTL` and are not tunable.
Increasing `CACHE_MAX_COST` above the number of active links has no benefit
because Ristretto will simply never evict. Decreasing `CACHE_TTL_SECONDS` makes
cache misses more frequent, increasing DB load on the redirect path.

---

## Short URL composition

The displayed short URL is built in `web/src/lib/links.ts`:

```typescript
export const SHORT_URL_BASE = 'https://go.sstools.co';

export function shortUrl(key: string): string {
  return `${SHORT_URL_BASE}/u/${encodeURIComponent(key)}`;
}
```

`SHORT_URL_BASE` is hardcoded to the production branded domain regardless of
the environment the backend is running in. The rationale (from the source
comment) is that the dashboard shows users the URL they will share, which is
always `go.sstools.co`; a copied `localhost` link is useless outside the
development machine.

The `encodeURIComponent` call is defensive. Generated keys are base-62
(`[a-z0-9A-Z]`) and custom aliases are validated server-side to
`[a-zA-Z0-9\-_]`, so encoding is normally a no-op for all valid keys.

The redirect namespace is always `/u/`. The full URL pattern is:

```
https://go.sstools.co/u/{key}
```

---

## Key files

| Path | Role |
|---|---|
| `internal/links/keygen.go` | `GenerateKey`, `GenerateUniqueKey`, alphabet, rejection sampling |
| `internal/links/store.go` | `Store`: `CreateLink`, `CreateOrReactivateLink`, `CreateDeniedLink`, `DeactivateLink`, `UpdateLink`, `KeyExists` |
| `internal/links/resolver.go` | `Resolver`: cache→DB read-through, negative-entry path |
| `internal/cache/cache.go` | `Cache`, `CachedLink`, TTL constants, `Delete`, `SetNegative` |
| `internal/handlers/redirect.go` | `RedirectHandler`: 7-step redirect flow, UTM merge, async click recording |
| `internal/handlers/links.go` | `LinksHandler`: `Create`, `Patch`, `Delete`, cache eviction |
| `web/src/lib/links.ts` | `SHORT_URL_BASE`, `shortUrl`, client-side validation and notice mapping |
| `migrations/000002_create_links.up.sql` | `links` table schema and indexes |
