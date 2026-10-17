# Using API Tokens

How to give a script, another app, or an AI agent access to ShortLinks so it
can do what you can do on the website: create campaigns, create short links,
and read stats. For the security design and internals, see
[api-tokens.md](api-tokens.md).

---

## 1. Create a token

1. Sign in and open **Account** (`/#/account`).
2. Under **API tokens**, click **New API token**.
3. Fill in:
   - **Name.** Name it after the client, e.g. `marketing-agent` or
     `nightly-export`. The name shows up in the audit log next to everything
     the token does. Each of your active tokens needs a different name.
   - **Expiration.** 30 days, 90 days (the default), 1 year, a custom date, or
     **Never expires**. Choose the shortest one that fits the job.
   - **Read-only.** Tick this when the client only needs to read links,
     campaigns, and stats.
4. Click **Create token** and copy it right away. It looks like
   `slk_6gMlv0P2…`. **You will not be able to see it again.** If you lose it,
   revoke it and create a new one.
5. Click **Done, I've saved it**.

---

## 2. Store it safely

A token acts as you on links and campaigns, so treat it like a password.

- Put it in an environment variable or a secret manager (1Password, AWS
  Secrets Manager, GitHub Actions secrets, an agent's secret store), not in
  source code.
- Never paste it into a URL, a chat, an issue, or a log. The server ignores
  tokens in query strings anyway. It only reads the `Authorization` header.
- Give each script or agent its own token. Then you can revoke one without
  breaking the others, and the audit log shows which client did what.
- If a token might be exposed, **revoke it** in Account → API tokens. It
  stops working on the next request.

Tokens start with `slk_`, so you can add that pattern to secret scanners, for
example as a custom pattern in GitHub secret scanning.

---

## 3. Make requests

Send the token as a bearer token on every request:

```
Authorization: Bearer slk_…
```

The examples use these variables:

```sh
export SHORTLINKS_URL=https://go.sstools.co
export SHORTLINKS_TOKEN=slk_…
```

### Check the token

```sh
curl -s -H "Authorization: Bearer $SHORTLINKS_TOKEN" "$SHORTLINKS_URL/api/me"
```

```json
{"id":1,"email":"you@example.com","is_admin":false,"base_url":"https://go.sstools.co",
 "api_token":{"id":3,"name":"marketing-agent","read_only":false}}
```

`is_admin` is always `false` for a token, even one created by an admin.

### Create a campaign

```sh
curl -s -H "Authorization: Bearer $SHORTLINKS_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"name":"Spring Sale","default_utm_source":"newsletter","default_utm_medium":"email"}' \
     "$SHORTLINKS_URL/api/campaigns"
```

The response includes the campaign's `slug` (here `spring-sale`). Later calls
use the slug.

### Create a short link

```sh
curl -s -H "Authorization: Bearer $SHORTLINKS_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"destination_url":"https://example.com/spring","title":"Spring landing page",
          "campaign_slug":"spring-sale","utm_source":"newsletter","utm_medium":"email"}' \
     "$SHORTLINKS_URL/api/links"
```

The short URL is `$SHORTLINKS_URL/u/<key>`, where `key` comes from the
response. Optional fields include `key` (a custom alias) and `expires_at`
(RFC 3339). Creating a link for a destination you already shortened returns
the existing link with `"duplicate": true`.

### Create many links in one campaign

Up to 50 rows per request. Either every row is created or none are:

```sh
curl -s -H "Authorization: Bearer $SHORTLINKS_TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"rows":[
           {"destination_url":"https://example.com/spring","utm_source":"instagram","utm_medium":"social"},
           {"destination_url":"https://example.com/spring","utm_source":"poster","utm_medium":"print","placement":"lobby"}
         ]}' \
     "$SHORTLINKS_URL/api/campaigns/spring-sale/links/batch"
```

Batch create does **not** deduplicate. Sending the same batch twice creates
two sets of links, so don't blindly retry a batch whose response you didn't
receive. List the campaign's links first (below).

### Read things

| What | Request |
|---|---|
| Your links (paged) | `GET /api/links?page=1&per_page=20` |
| One link + click stats | `GET /api/links/{key}` |
| Your campaigns | `GET /api/campaigns` |
| One campaign + summary | `GET /api/campaigns/{slug}` |
| Full campaign stats | `GET /api/campaigns/{slug}/stats` |
| Links in a campaign | `GET /api/campaigns/{slug}/links` |
| QR codes | `GET /api/links/{key}/qr.svg`, `.../qr.png`, `GET /api/campaigns/{slug}/qr.zip` |
| CSV export | `GET /api/campaigns/{slug}/export.csv` |

### Change things

| What | Request |
|---|---|
| Edit a link | `PATCH /api/links/{key}` with any of `title`, `destination_url`, `expires_at`, `utm_*`, `placement` |
| Deactivate a link | `DELETE /api/links/{key}` |
| Add existing links to a campaign | `POST /api/campaigns/{slug}/links` `{"keys":["abc123"]}` |
| Remove a link from a campaign | `DELETE /api/campaigns/{slug}/links/{key}` |
| Edit a campaign | `PATCH /api/campaigns/{slug}` |
| Delete a campaign | `DELETE /api/campaigns/{slug}` (its links are kept, just unassigned) |

Full field references: [links.md](links.md), [campaigns.md](campaigns.md),
[utm.md](utm.md).

---

## 4. Examples in code

### Python

```python
import os, requests

BASE = os.environ["SHORTLINKS_URL"]
s = requests.Session()
s.headers["Authorization"] = f"Bearer {os.environ['SHORTLINKS_TOKEN']}"

campaign = s.post(f"{BASE}/api/campaigns", json={"name": "Spring Sale"}).json()
link = s.post(f"{BASE}/api/links", json={
    "destination_url": "https://example.com/spring",
    "campaign_slug": campaign["slug"],
}).json()
print(f"{BASE}/u/{link['key']}")
```

### JavaScript (Node 18+)

```js
const BASE = process.env.SHORTLINKS_URL;
const headers = {
  Authorization: `Bearer ${process.env.SHORTLINKS_TOKEN}`,
  'Content-Type': 'application/json',
};

const res = await fetch(`${BASE}/api/links`, {
  method: 'POST',
  headers,
  body: JSON.stringify({ destination_url: 'https://example.com/spring' }),
});
if (!res.ok) throw new Error(`${res.status} ${await res.text()}`);
const link = await res.json();
console.log(`${BASE}/u/${link.key}`);
```

### Instructions for an AI agent

Put the token in the agent's secret or environment configuration, not in the
prompt. You can then give it instructions like these:

> You can manage short links through the ShortLinks REST API at
> `$SHORTLINKS_URL`. Authenticate every request with the header
> `Authorization: Bearer $SHORTLINKS_TOKEN`, and never print the token.
> Start with `GET /api/me` to confirm access. Create campaigns with
> `POST /api/campaigns` and links with `POST /api/links`, or with
> `POST /api/campaigns/{slug}/links/batch` for several at once. Don't
> retry a batch request without first checking
> `GET /api/campaigns/{slug}/links`. On `429`, wait for the number of seconds
> in `Retry-After`. On `401`, stop and report that the token is invalid or
> revoked.

---

## 5. Errors and limits

Errors are JSON: `{"error":"<code>"}`.

| Status | `error` | What to do |
|---|---|---|
| `401` | `unauthenticated` | The token is wrong, expired, or revoked, or your account was deactivated. Create a new token. |
| `403` | `api_token_read_only` | This token is read-only. Use a token without the read-only flag. |
| `403` | `api_token_not_permitted` | This route is browser-only: passkeys, sign out everywhere, admin pages. |
| `403` | `cookie_session_required` | Token management can only be done in the web UI. |
| `429` | `rate_limit_exceeded` | Wait `Retry-After` seconds, then retry. |
| `400` / `404` / `409` | varies | Bad input, not found, or a conflict (e.g. a custom key already taken). |

**Rate limits:**

- 600 requests per minute per token (bursts up to 60).
- After 20 failed attempts with invalid tokens within a minute, that IP gets
  `429` for a while, even for a valid token.

**What a token can't do:** create or revoke tokens, manage passkeys, sign out
sessions, or use any admin page (users, settings, URL filters, audit log).
Use the web UI for those.

---

## 6. Rotate and revoke

- **Rotate** a token before it expires: create a new token (it needs a
  different name while the old one is still active), switch the client to it,
  confirm with `GET /api/me`, then revoke the old one.
- **Revoke** in Account → API tokens. The token fails with `401` on the next
  request.
- The token list shows when and from which IP each token was **last used**.
  Revoke tokens nobody uses anymore.
- An expired token stays in the list, marked **Expired**, and can't be
  renewed. Create a new one.
- Admins: the audit log (Admin → Audit) records token creation and
  revocation, and every action taken with a token carries `via: api_token`
  and the token's name.
