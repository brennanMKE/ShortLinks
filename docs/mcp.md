# MCP server

ShortLinks embeds a [Model Context Protocol](https://modelcontextprotocol.io)
server at **`/mcp`**, so an AI agent can create and manage links and campaigns
as tools. It is part of the one binary; there is nothing extra to deploy.

## Create a token

Sign in, open **Account** (`/#/account`), and create a token under **API
tokens** ([using-api-tokens.md](using-api-tokens.md) has the full steps and
storage advice). Copy it at once; it is shown once. Two kinds matter here:

- **Full token.** Every tool, including create, update, deactivate and delete.
- **Read-only token.** Tick **Read-only** when creating it. `tools/list` leaves
  out every write tool, the report and list tools and the three prompts all
  work, and calling a write tool anyway returns a tool error ("this API token
  is read-only, so write tools such as ... are not available"). The REST
  layer's `403 api_token_read_only` is the second line of defence. Use one for an agent that only answers
  questions about link activity.

Give each agent its own token and keep it in an environment variable:

```bash
export SHORTLINKS_TOKEN=slk_…
```

Tokens are the only credential. A browser cookie or session bearer gets
`403 api_token_required`; a missing, revoked or expired token gets `401`.

## Connect

Any MCP client that speaks Streamable HTTP and can send a header works.
Replace `<host>` with your instance, for example `go.sstools.co`.

### Claude Code

```bash
claude mcp add --transport http shortlinks https://<host>/mcp \
  --header "Authorization: Bearer $SHORTLINKS_TOKEN"
```

That command writes the expanded token into your Claude config. To keep it
out of the file, share the setup through a project `.mcp.json` that reads the
token from the environment instead:

```json
{
  "mcpServers": {
    "shortlinks": {
      "type": "http",
      "url": "https://<host>/mcp",
      "headers": { "Authorization": "Bearer ${SHORTLINKS_TOKEN}" }
    }
  }
}
```

Claude Code expands `${SHORTLINKS_TOKEN}` when it starts, so launch it from a
shell where the variable is set. For a one-off run, pass the same JSON with
`claude --mcp-config file.json --strict-mcp-config`. Run `/mcp` inside Claude
Code to see the server and its tools.

### Claude Desktop

Claude Desktop's config file launches local servers, and the connector UI does
not take a custom `Authorization` header, so bridge with
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote). Add this to
`claude_desktop_config.json` (needs Node.js), then restart Desktop:

```json
{
  "mcpServers": {
    "shortlinks": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "https://<host>/mcp",
               "--header", "Authorization:${AUTH_HEADER}"],
      "env": { "AUTH_HEADER": "Bearer slk_…" }
    }
  }
}
```

The token sits in that file, so use a read-only or short-lived token.

### MCP Inspector

```bash
npx @modelcontextprotocol/inspector
```

Choose transport **Streamable HTTP**, URL `https://<host>/mcp`, add a custom
header `Authorization: Bearer slk_…`, and click Connect. You can then list
tools and prompts and call them by hand.

### Check it with the smoke script

```bash
SHORTLINKS_TOKEN=slk_… scripts/mcp-smoke.sh https://<host>
```

It sends `initialize`, `tools/list` and a `whoami` call with curl, prints the
tool count, and exits non-zero on any failure. The token comes from the
environment, never from an argument, so it does not appear in `ps`.

The server is **stateless**. Every POST is authenticated on its own and gets a
server built for that token, so there is no session id, nothing to expire, and
a revoked token stops working on its next call. `GET` and `DELETE` on `/mcp`
answer `405`, which clients treat as "no server-to-client stream".

## Example prompts

| Ask the agent | Tool it uses |
|---|---|
| "What were my top links this week?" | `get_top_links` (`range: 7d`) |
| "Which links got no clicks in the last 30 days?" | `get_idle_links` |
| "Compare the spring-launch and summer-sale campaigns." | `compare_campaigns` |
| "Make a short link to https://example.com/pricing with the key `pricing`." | `create_link` |
| "Make an unguessable invite link to https://example.com/join/42, with a 16-character key." | `create_link` (`key_length: 16`) |
| "Create one link per channel (email, twitter, print) for the launch page in the `spring-launch` campaign." | `batch_create_campaign_links` |
| "Turn off the `old-promo` link." | `deactivate_link` |
| "Give me the QR code for `pricing`." | `get_link_qr` |
| "Write me the weekly summary." | the `weekly_summary` prompt, which calls the report tools |

## Limits

- **Rate limit.** `POST /mcp` goes through the same API-token limiter as
  `/api`: 600 requests per minute per token, bursts of 60. A tool call costs
  one request (the in-process REST call it makes is not charged again). Twenty
  invalid-token attempts a minute from one IP trip a separate failure limit
  that answers `429` even for a valid token. See [api-tokens.md](api-tokens.md).
- **Body size.** A request body is capped at 256 KiB; larger gets `413`.
- **No batches.** A JSON-RPC batch array gets `400`, because one batch would
  run many tools for one rate-limit charge. Send one request per POST.
- **No streaming.** Responses are plain JSON, not SSE.

## Troubleshooting

| Status | Meaning | Fix |
|---|---|---|
| `401` `unauthenticated` | No `Authorization` header, or the token is wrong, revoked or expired. A `.mcp.json` with an unset variable sends the literal text `${SHORTLINKS_TOKEN}`. | Check `echo "${SHORTLINKS_TOKEN:0:8}"` prints `slk_`; mint a new token in Account. |
| `403` `api_token_required` | The request carried a browser session cookie or session bearer, not an API token. Typical when a page in the signed-in browser calls `/mcp`, or in dev mode with auto-login and no header. | Send `Authorization: Bearer slk_…`. |
| `403` `cross-origin request detected…` | The request had an `Origin` header that does not match the host, which is how a browser page on another site looks. Agents and curl send no `Origin`. | Call `/mcp` from a non-browser client. Behind a proxy, keep `ProxyPreserveHost On` so `Host` is the public name. |
| tool error "this API token is read-only, so write tools such as X are not available" | A read-only token called a write tool. The REST `403 api_token_read_only` behind it is only the second layer. | Use a full token for that agent. |
| `400` JSON-RPC batches are not supported | The client sent an array. | Send one request per POST. |
| `405` on `GET` / `DELETE` | Expected: the server has no stream or session. | None. Clients ignore it. |
| `413` | Body over 256 KiB. | Send smaller input. |
| `429` | Rate limit. The `Retry-After` header says how many seconds to wait. | Slow the agent down, or give it its own token. |

## Tools

| Tool | REST route it calls | Hints |
|---|---|---|
| `whoami` | `GET /api/me` | read-only |
| `list_links` | `GET /api/links` (all the [links.md](links.md) search params) | read-only |
| `get_link` | `GET /api/links/{key}` | read-only |
| `get_link_qr` | `GET /api/links/{key}/qr.png`, returned as image content | read-only |
| `create_link` | `POST /api/links`. `key` sets a custom key (1-32 characters), `key_length` (6-32, default 6) makes a longer generated one for links that must not be guessable | write |
| `update_link` | `PATCH /api/links/{key}` | write, idempotent |
| `deactivate_link` | `PATCH /api/links/{key}` `{"active":false}` | write, destructive |
| `reactivate_link` | `PATCH /api/links/{key}` `{"active":true}` | write, idempotent |
| `list_campaigns`, `get_campaign`, `list_campaign_links` | the campaign GETs | read-only |
| `create_campaign` | `POST /api/campaigns` | write |
| `update_campaign` | `PATCH /api/campaigns/{slug}` | write, idempotent |
| `delete_campaign` | `DELETE /api/campaigns/{slug}` | write, destructive |
| `batch_create_campaign_links` | `POST /api/campaigns/{slug}/links/batch`. `key_length` (6-32) applies to every row | write |
| `assign_links_to_campaign`, `remove_link_from_campaign` | the campaign link routes | write, idempotent |
| `list_reports` | `GET /api/reports` | read-only |
| `get_overview`, `get_top_content`, `get_top_links`, `get_breakdown`, `get_timeseries`, `get_movers`, `get_idle_links`, `compare_campaigns`, `get_campaign_performance`, `get_link_performance` | `GET /api/reports/{name}`, but they call `internal/reports` directly | read-only |

## Report tools and prompts

Each report is a typed tool. Shared inputs: `range` (enum), `from` / `to`
(dates), `tz` (defaults to the instance `DEFAULT_TIMEZONE`), `compare`,
`campaign`, `source` / `medium` / `utm_campaign` / `content` / `term` /
`placement` (string arrays), `include_bots`, `limit`; plus the report's own
(`sort`, `dimension`, `granularity`, `split_by`, `unit`, `columns`, `slug`,
`key`, ...), as enums where the report defines them. The text content is the
report's `highlights` followed by the JSON; the structured content is the full
envelope. A bad parameter is a tool error carrying the field and code
(`invalid_tz`, field `tz`); an unknown campaign or link carries `campaign_not_found`
or `link_not_found`.

Filter values are matched whole, so a value that contains a comma (for example
`utm_content` = `spring, 2026`) works: the tools send each array element as its
own repeated parameter (`content=spring%2C+2026&content=...`) and, for a single
element, escape the comma as `\,`. The HTTP reports take the same two forms: a
repeated parameter is never split, while a parameter given once is still read as
the older comma-separated list (`content=a,b`) with `\,` for a literal comma and
`\\` for a backslash. See `GET /api/reports/{name}` in `openapi.yaml`.

A test fails if a report is registered in `reports.NewDefaultRegistry()` without
a tool (`reportTools` in `tools_reports.go`).

Prompts (`prompts.go`, available to read-only tokens too): `weekly_summary(tz?)`,
`campaign_report(slug)` and `whats_working(range?)`. They return one message
telling the model which tools to call and what to write.

Routes with no tool are listed, each with its reason, in `mcpserver.Excluded`
(`internal/mcpserver/parity.go`): the SVG QR code, the QR zip, the CSV export,
the SSE stream, campaign `/stats`, `DELETE /api/links/{key}` and the public home
info. The parity test keeps that
list complete.

**Limits.** Rate limit, body size and batches are described once, under [Limits](#limits) above. Keys and slugs of `.`, `..` or containing `/` are refused. Cross-origin
browser requests are refused by Go's `http.CrossOriginProtection`. A panic in
a tool or the dispatched handler becomes an `internal_error` tool error.

The three create tools take an optional `idempotency_key`, passed to REST as
the `Idempotency-Key` header ([using-api-tokens.md](using-api-tokens.md)).

**Results.** A successful call returns a one-line text summary, the REST JSON
as a second text block (for clients that ignore structured content), and the
same JSON as structured content. Every link object gains a **`short_url`**
built from `BASE_URL`. A failed call is a tool error (`isError: true`) whose
text is `ShortLinks API error: HTTP <status> <code>: <message>`, plus any extra
fields (`reason`, `label` on a 422 `destination_denied`), so the agent can
correct itself. Tool arguments are validated against the tool's schema first.

## How it works

Link and campaign tools never reimplement REST behaviour. `Caller.call` builds
an `*http.Request` carrying the caller's own `Authorization` header and serves
it through the same mux as the network, so `requireAPI`, request validation,
the URL filter, idempotency, audit rows and cache eviction all run. The inner
request always targets an `/api` path, so it cannot recurse into `/mcp`.

**Read-only tokens.** MCP puts every call in a POST, so `requireAPI` exempts
`POST /mcp` (and only that: not `/mcp/…`, not other methods) from its
method-based read-only check. Read-only is then enforced twice:

1. a read-only session's `tools/list` omits the write tools, and calling one
   anyway returns a tool error;
2. the inner REST request is re-authenticated and the REST layer refuses the
   write with `403 api_token_read_only`.

**Rate limiting.** The outer `POST /mcp` counts against the token's limit like
any API request. The inner REST call does not (it is marked with
`middleware.WithInternalDispatch`), otherwise every tool call would cost two
requests. Authentication, the read-only check and everything downstream still
run for it.

**Audit.** Rows written during a tool call carry the token's id and name like
any API call, with `"via":"mcp"` instead of `"api_token"`
(`audit.WithVia`).

**Dev mode.** `/mcp` is mounted with `STORAGE=json` too and serves the dev
store's link and campaign routes. With the dev auto-login, a request with no
`Authorization` header looks like a cookie session and gets
`403 api_token_required`; mint a token at `/account/api-tokens` first.

## Adding tools

Write a `Registrar` that calls `mcpserver.AddTool(b, Spec{…}, fn)` and append
it to `registrars` in `internal/mcpserver/tools.go`. Set `Write` for anything
that changes data (hidden from read-only tokens automatically), and list the
REST `Routes` the tool covers. A tool that reads `internal/reports` directly
still names the route it replaces, and the caller's identity is on `c.UserID`. The parity
test (`cmd/shortlinks/mcp_test.go`) fails if an `/api` route is neither
covered by a tool nor listed, with a reason, in `mcpserver.Excluded`.

## Key files

| File | Role |
|---|---|
| `internal/mcpserver/server.go` | The handler: token check, per-request server |
| `internal/mcpserver/caller.go` | `Caller`, in-process dispatch, error mapping |
| `internal/mcpserver/tools*.go` | `AddTool`, the tools, short URLs |
| `internal/mcpserver/tools_reports.go` | The report tools and their schema enums |
| `internal/mcpserver/prompts.go` | The three prompts |
| `internal/mcpserver/parity.go` | `Excluded` |
| `cmd/shortlinks/main.go` | `buildMux` records route patterns; mounts `/mcp` |
