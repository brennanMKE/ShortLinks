# URL Filtering & Safety

URL filtering screens every destination URL at link-creation time against a set
of admin-managed regex rules. A matching rule blocks the creation, records a
denied link row, and returns a 422 to the caller. The filter runs before
deduplication, before any successful insert, and before any SSE event is fired.

---

## The rule model — `url_filter_rules`

Migration: `migrations/000008_create_url_filter_rules.up.sql`

```sql
CREATE TABLE url_filter_rules (
    id          BIGSERIAL PRIMARY KEY,
    pattern     TEXT NOT NULL,
    reason_code SMALLINT NOT NULL,
    description TEXT,
    active      BOOLEAN NOT NULL DEFAULT TRUE,
    created_by  BIGINT REFERENCES users(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_url_filter_rules_active ON url_filter_rules (active);
```

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key; evaluation order is ascending `id` (first match wins). |
| `pattern` | `TEXT` | Go-compatible regular expression tested against the destination URL. Must compile or the rule is skipped at load time. |
| `reason_code` | `SMALLINT` | One of the denial reason codes 1–6 (see below). Zero is not valid for a rule; a rule always denies. |
| `description` | `TEXT` | Optional human note; stored as SQL `NULL` when empty. |
| `active` | `BOOLEAN` | Only `active = TRUE` rows are loaded into the evaluation cache. |
| `created_by` | `BIGINT` | Foreign key to `users.id`; the admin who created the rule. |
| `created_at` | `TIMESTAMPTZ` | Set to `now()` on insert. |

New rules are inserted with `active = TRUE` by default (`store.Create` in
`internal/filters/store.go`).

---

## Denial reason codes

Defined as typed constants in `internal/filters/filters.go`. The numeric
values are pinned by tests (`filters_test.go:TestReasonCodeConstants`) so they
never drift from the PRD table or from consumers of `links.denied_reason`.

| Constant | Value | Label |
|---|---|---|
| `ReasonNone` | `0` | Not denied (URL passed — no rule matched) |
| `ReasonMalware` | `1` | Malware or ransomware |
| `ReasonPhishing` | `2` | Phishing |
| `ReasonSpam` | `3` | Spam |
| `ReasonAdultContent` | `4` | Adult content |
| `ReasonPolicyViolation` | `5` | Policy violation |
| `ReasonOther` | `6` | Other |

`ReasonNone` (0) is never assigned to a rule (`ValidReasonCode` rejects it).
Valid rule reason codes are 1–6 inclusive.

`filters.ReasonLabel(code int) string` maps a code to its human-readable label.
An unknown code falls back to the "Other" label so the API always emits a
non-empty string.

The denial reason is stored in `links.denied_reason` (`SMALLINT NOT NULL DEFAULT 0`),
defined in `migrations/000002_create_links.up.sql`. A value of 0 means the link
was not denied. A partial index `idx_links_denied_reason` covers rows where
`denied_reason > 0` for admin queries over all denied links.

---

## Evaluation

**Source**: `internal/filters/filters.go` — `Evaluate`, `CompileRules`,
`LoadActiveRules`.

### Loading active rules

`LoadActiveRules(ctx, q)` queries:

```sql
SELECT id, pattern, reason_code
  FROM url_filter_rules
 WHERE active = TRUE
 ORDER BY id
```

Rules are returned in ascending `id` order, establishing a deterministic
first-match-wins evaluation order. The result is uncompiled; callers pass it
through `CompileRules` before evaluation.

### Compiling rules

`CompileRules(rules []Rule, logger *slog.Logger) []Rule` pre-compiles each
rule's pattern once. A rule whose pattern fails `regexp.Compile` is **skipped**
(excluded from the compiled slice) and logged at `Warn` level. One bad rule
never breaks evaluation of the rest. The input slice is not mutated.

### Evaluating a URL

```go
func Evaluate(rules []Rule, url string) (reasonCode int, ruleID int64, matched bool)
```

Iterates rules in order, testing each compiled regex against the destination
URL string. Returns on the **first match** with the matching rule's
`ReasonCode` and `ID`, and `matched = true`. When no rule matches it returns
`(ReasonNone, 0, false)`.

Rules in the standard path carry pre-compiled regexes (from `CompileRules`).
If a rule has no compiled form, `Evaluate` compiles it on the fly and skips it
if compilation fails — the on-the-fly path is only exercised by direct callers
that pass raw rules.

---

## Where evaluation occurs in the request flow

### Link creation — `POST /api/links`

**Handler**: `internal/handlers/links.go` — `LinksHandler.Create`

The filter check is the **first substantive step** after request validation,
before deduplication and before any DB insert:

1. `h.rules.Rules(ctx)` fetches the active compiled rules from the cache
   (cache → DB on miss/expiry; see [Caching](#the-filter-rule-cache) below).
2. `filters.Evaluate(rules, dest)` tests the destination URL.
3. **On a match**: `h.store.CreateDeniedLink(...)` inserts a denied link row
   (`active = false`, `denied_reason = <code>`) under a generated unique key.
   The denied row is committed; dedup and SSE are skipped entirely.
4. The audit logger records a `link.denied` entry with metadata
   `{destination_url, reason_code, reason_label, matched_rule_id}`.
5. The handler returns:
   ```json
   HTTP 422 Unprocessable Entity
   {"error":"url_denied","reason":<code>,"label":"<label>"}
   ```
6. **On no match**: execution falls through to the normal dedup/insert path.

The filter runs in **both** the generated-key path and the custom-alias path —
a blocked URL is denied regardless of which key type was requested.

### Redirect — `GET /u/{key}`

**Handler**: `internal/handlers/redirect.go` — `RedirectHandler.ServeHTTP`

Denied links are stored with `active = false`. The redirect handler's step 3
treats any inactive link (whether deactivated or denied) as not found:

```go
if !found || link.Negative || !link.Active {
    http.NotFound(w, r)
    return
}
```

The redirect path performs no filter evaluation itself; the check happens at
creation time.

---

## The filter-rule cache

**Source**: `internal/cache/rules.go` — `RuleCache`

```go
const FilterRuleTTL = 60 * time.Second
```

`RuleCache` holds a single in-memory snapshot of the active compiled rules.
Unlike the per-key redirect cache (backed by Ristretto), the rule set is small
and shared, so it is stored as one whole snapshot guarded by a `sync.Mutex`.

### Loading

The snapshot is loaded **lazily** on the first `Rules` call and refreshed
when it is older than `FilterRuleTTL`. The loader closure is wired at startup
in `cmd/shortlinks/main.go`:

```go
ruleCache := cache.NewRuleCache(func(ctx context.Context) ([]filters.Rule, error) {
    rules, err := filterStore.LoadActive(ctx)
    if err != nil {
        return nil, err
    }
    return filters.CompileRules(rules, slog.Default()), nil
})
```

The loader compiles the rules once (uncompilable patterns skipped and logged).
On a DB error the stale snapshot is left in place; the caller (the links
handler) propagates the error as a 500.

### Invalidation

`RuleCache.Invalidate()` drops the snapshot immediately (`valid = false`,
`rules = nil`) so the next `Rules` call reloads from the DB. The admin CRUD
handler calls `Invalidate` after every successful create, update, or delete,
ensuring rule changes take effect at once rather than waiting up to 60 seconds.

The `ruleCacheInvalidator` interface in `internal/handlers/url_filters.go`
formalises this contract:

```go
type ruleCacheInvalidator interface {
    Invalidate()
}
```

A nil invalidator (used in unit tests) is a no-op — the handler guards the
call with a nil check via `URLFiltersHandler.invalidate()`.

---

## Admin management

All admin endpoints are mounted behind `requireSession` + `requireAdmin`
middleware and wired in `cmd/shortlinks/main.go`.

### Endpoints

| Method | Path | Handler | Description |
|---|---|---|---|
| `GET` | `/admin/url-filters` | `URLFiltersHandler.List` | List all rules (active and inactive) in `id` order. |
| `POST` | `/admin/url-filters` | `URLFiltersHandler.Create` | Create a new rule. Returns 201 with the new rule. |
| `PATCH` | `/admin/url-filters/{id}` | `URLFiltersHandler.Patch` | Partial update of an existing rule. |
| `DELETE` | `/admin/url-filters/{id}` | `URLFiltersHandler.Delete` | Delete a rule. Returns `{"message":"Rule deleted"}`. |
| `POST` | `/admin/url-filters/test` | `URLFiltersHandler.Test` | Dry-run: evaluate a URL against the current active rules. Never writes anything. |

**Source**: `internal/handlers/url_filters.go`

### Create (`POST /admin/url-filters`)

Request body:
```json
{"pattern":"<go-regex>","reason_code":<1-6>,"description":"<optional>"}
```

Validation before insert:
- `pattern` must not be empty and must compile as a Go regular expression
  (`regexp.Compile`). A pattern that fails compilation is rejected 400.
- `reason_code` must be 1–6 (`filters.ValidReasonCode`). Zero and values
  outside that range are rejected 400.

On success: inserts a new active rule attributed to the calling admin, calls
`Invalidate`, records a `url_filter.created` audit entry with metadata
`{pattern, reason_code, description}`, and returns 201 with the rule as JSON.

### Patch (`PATCH /admin/url-filters/{id}`)

Request body (all fields optional — absent means "leave unchanged"):
```json
{"pattern":"<go-regex>","reason_code":<1-6>,"description":"<string>","active":<bool>}
```

Setting `"description":""` clears the description to SQL `NULL`.

Validation mirrors Create for `pattern` and `reason_code` when present. Reads
the pre-update row for audit metadata (old/new pattern and reason_code).
Returns 404 (`ErrRuleNotFound`) when the id does not exist. On success calls
`Invalidate` and records a `url_filter.updated` audit entry.

### Delete (`DELETE /admin/url-filters/{id}`)

Reads the rule before deleting (for audit metadata), then deletes it.
Returns 404 when absent. On success calls `Invalidate` and records a
`url_filter.deleted` audit entry with `{pattern, reason_code, description}`.

### Test (`POST /admin/url-filters/test`)

Request body:
```json
{"url":"<destination-url>"}
```

Loads active rules from the DB directly (bypassing the cache, so it always
reflects the current committed state), compiles them, and evaluates the URL.

Response on a match:
```json
{"matched":true,"reason_code":<code>,"rule_id":<id>}
```

Response on no match:
```json
{"matched":false}
```

`reason_code` and `rule_id` are omitted when `matched` is false. This endpoint
never inserts a link or fires an audit entry.

### JSON rule shape (all read endpoints)

```json
{
  "id": 1,
  "pattern": "evil\\.com",
  "reason_code": 1,
  "reason_label": "Malware or ransomware",
  "description": "Known malware domain",
  "active": true,
  "created_by": 42,
  "created_at": "2026-06-01T00:00:00Z"
}
```

`reason_label` is derived by `filters.ReasonLabel` and is always non-empty.

---

## Audit trail

Every admin mutation records an audit entry via `audit.Logger.Record` (fire-
and-forget after the write is committed). The action and target constants are
defined in `internal/audit/actions.go`:

| Audit action | Trigger |
|---|---|
| `url_filter.created` | Rule created via `POST /admin/url-filters` |
| `url_filter.updated` | Rule patched via `PATCH /admin/url-filters/{id}` |
| `url_filter.deleted` | Rule deleted via `DELETE /admin/url-filters/{id}` |
| `link.denied` | Destination URL matched a filter rule on `POST /api/links` |

The `link.denied` entry carries `{destination_url, reason_code, reason_label,
matched_rule_id}` so denied attempts are traceable back to the rule that
blocked them.

---

## Data-access layer

`internal/filters/store.go` — `Store`

| Method | Description |
|---|---|
| `List(ctx)` | Returns all rules (active and inactive) in `id` order. |
| `Create(ctx, NewRule)` | Inserts a new active rule; returns the full row. |
| `Get(ctx, id)` | Returns one rule by id; `ErrRuleNotFound` when absent. |
| `Update(ctx, id, RuleUpdate)` | Partial update; `ErrRuleNotFound` when absent. |
| `Delete(ctx, id)` | Deletes by id; `ErrRuleNotFound` when no row matched. |
| `LoadActive(ctx)` | Loads active rules for the cache loader (delegates to `LoadActiveRules`). |

`ErrRuleNotFound` is mapped to HTTP 404 by the admin handler.
