# API Tokens

API tokens let a script, another app, or an AI agent call the ShortLinks REST
API as a user — create campaigns and short links, read stats, export CSVs —
without a passkey ceremony. A user mints a named token in the web UI
(**Account → API tokens**), copies it once, and hands it to the client.

> **Looking for how to use a token?** See [using-api-tokens.md](using-api-tokens.md)
> for the step-by-step guide with examples. This page is the design reference.

---

## Using a token

Send it in the `Authorization` header, nowhere else:

```sh
export SHORTLINKS_TOKEN=slk_…            # from Account → API tokens
B=https://go.sstools.co

# Who am I? (also confirms which token is in use)
curl -H "Authorization: Bearer $SHORTLINKS_TOKEN" $B/api/me

# Create a campaign
curl -H "Authorization: Bearer $SHORTLINKS_TOKEN" -H 'Content-Type: application/json' \
     -d '{"name":"Spring Sale"}' $B/api/campaigns

# Create a short link in it
curl -H "Authorization: Bearer $SHORTLINKS_TOKEN" -H 'Content-Type: application/json' \
     -d '{"destination_url":"https://example.com/spring","campaign_slug":"spring-sale"}' \
     $B/api/links
```

A token is **never** read from a cookie or a query string, so it can't leak
through URLs, `Referer` headers, or access logs.

### What a token can reach

| Routes | With an API token |
|---|---|
| `/api/*` — links, campaigns, stats, QR codes, CSV export, `/api/me`, `/api/events` | ✅ as the token's owner |
| `/account/credentials*` (passkeys), `/auth/logout/all` | ❌ `403 api_token_not_permitted` |
| `/admin/*` (users, settings, URL filters, audit) | ❌ `403 api_token_not_permitted` — even for an admin's token |
| `/account/api-tokens*` (token management) | ❌ `403 cookie_session_required` |

Endpoint details live in [links.md](links.md) and [campaigns.md](campaigns.md).
`GET /api/me` returns `is_admin: false` and an `api_token: {id, name, read_only}`
object when called with a token.

A **read-only** token may use `GET`/`HEAD` only; anything else is
`403 api_token_read_only`.

### Errors and limits

| Status | Body | Meaning |
|---|---|---|
| 401 | `{"error":"unauthenticated"}` | Unknown, revoked, or expired token, or the owner is deactivated (indistinguishable on purpose) |
| 403 | `{"error":"api_token_read_only"}` | Write attempted with a read-only token |
| 403 | `{"error":"api_token_not_permitted"}` / `cookie_session_required` | Route is browser-only (see table) |
| 429 | `{"error":"rate_limit_exceeded"}` + `Retry-After` | Rate limit hit |

Rate limits (`middleware.DefaultAPITokenLimits`):

- **Per token:** 600 requests/minute sustained, burst 60. Keyed by token id,
  so a client behind a shared IP isn't throttled by its neighbours.
- **Failed attempts per IP:** 20 invalid tokens/minute, burst 20. Once spent,
  that IP gets 429 *before* any database lookup — this slows online guessing.

Limiter state is in-process (single instance behind Apache), like the auth
endpoint limiters.

---

## Managing tokens (web UI)

**Account → API tokens**:

- **New API token** — a name (unique among your active tokens), an
  expiration (30 days, 90 days, 1 year, a custom date, or **never**), and an
  optional read-only flag. The token is shown **once** with a copy button and
  a sample `curl`; it cannot be retrieved again.
- The list shows each token's name, a short prefix (`slk_AbCdEfGh…`), status
  (active / expired / revoked), created, expires, and last used (time and IP).
- **Revoke** takes effect on the next request; there is no cache.

The UI calls three browser-only endpoints, mounted behind
`middleware.RequireCookieSession`, which refuses any request carrying an
`Authorization` header — so neither a token nor a native-app bearer session can
mint or revoke tokens, and a leaked token can't create more of itself:

| Method | Path | Notes |
|---|---|---|
| `GET` | `/account/api-tokens` | Metadata only, never secrets |
| `POST` | `/account/api-tokens` | `{name, read_only, expires_at}` (`expires_at` RFC 3339 or `null` = never). `201 {token, api_token}`, `Cache-Control: no-store`. `409 api_token_name_taken` |
| `DELETE` | `/account/api-tokens/{id}` | Soft revoke; `404` if not yours or already revoked |

---

## Security design

- **Format:** `slk_` + 32 bytes from `crypto/rand`, base64url. The prefix lets
  the middleware route the credential without a speculative lookup, and makes
  leaked tokens easy to find with secret scanners or `grep`.
- **Storage:** only `sha256(token)` is stored (`api_tokens.token_hash`), plus a
  12-character display prefix. A plain SHA-256 is correct for 256-bit random
  secrets (no KDF needed; lookup is by exact digest). A database dump or backup
  contains no usable tokens.
- **Once-only disclosure:** the plaintext exists in the `POST` response and the
  browser tab's memory until the user clicks *Done*. It is never logged, put in
  a URL, or written to browser storage.
- **No admin authority:** a token's principal always has `IsAdmin=false`.
- **Lifecycle:** expiry is fixed at creation (no sliding window); `NULL` means
  never. Revocation is a soft delete (`revoked_at`) so audit rows keep their
  meaning. Deactivating a user revokes all of their tokens in the same
  transaction, so reactivation doesn't silently restore old credentials.
- **Bookkeeping:** `last_used_at` / `last_used_ip` are written at most once a
  minute per token, so a busy client doesn't turn every call into a write.

### Audit

- `api_token.created` / `api_token.revoked` (target type `api_token`), written
  in the same transaction as the change.
- Every audit row written while serving a token-authenticated request — e.g.
  `link.created`, `campaign.created` — gets
  `{"via":"api_token","api_token_id":…,"api_token_name":…}` merged into its
  metadata. The middleware marks the request context (`audit.WithAPIToken`) and
  `audit.insert` applies it, so no call site has to know.

---

## Key files

| File | Role |
|---|---|
| `migrations/000014_create_api_tokens.up.sql` | Schema |
| `internal/auth/apitokens.go` | Mint, list, revoke, resolve |
| `internal/middleware/apitoken.go` | `RequireSessionOrAPIToken`, `RequireCookieSession`, rate limits |
| `internal/middleware/auth.go` | `RequireSession` refuses tokens; `AuthUser` token fields |
| `internal/handlers/apitokens.go` | `/account/api-tokens` endpoints |
| `internal/audit/audit.go` | `WithAPIToken` attribution |
| `web/src/lib/ApiTokens.svelte`, `web/src/lib/apiTokens.ts` | UI |
