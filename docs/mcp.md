# MCP server

ShortLinks embeds a [Model Context Protocol](https://modelcontextprotocol.io)
server at **`/mcp`**, so an AI agent can create and manage links and campaigns
as tools. It is part of the one binary; there is nothing extra to deploy.

## Connect

Mint an API token in the web UI ([api-tokens.md](api-tokens.md)), then:

```bash
claude mcp add --transport http shortlinks https://<host>/mcp \
  --header "Authorization: Bearer slk_…"
```

Any MCP client that speaks Streamable HTTP and can send a header works the
same way. Tokens are the only credential: a browser cookie or session bearer
gets `403 api_token_required`, a missing, revoked or expired token gets `401`.

The server is **stateless**. Every POST is authenticated on its own and gets a
server built for that token, so there is no session id, nothing to expire, and
a revoked token stops working on its next call. `GET` and `DELETE` on `/mcp`
answer `405`, which clients treat as "no server-to-client stream".

## Tools

| Tool | REST route it calls | Hints |
|---|---|---|
| `whoami` | `GET /api/me` | read-only |
| `list_links` | `GET /api/links` (all the [links.md](links.md) search params) | read-only |
| `get_link` | `GET /api/links/{key}` | read-only |
| `get_link_qr` | `GET /api/links/{key}/qr.png`, returned as image content | read-only |
| `create_link` | `POST /api/links` | write |
| `update_link` | `PATCH /api/links/{key}` | write, idempotent |
| `deactivate_link` | `PATCH /api/links/{key}` `{"active":false}` | write, destructive |
| `reactivate_link` | `PATCH /api/links/{key}` `{"active":true}` | write, idempotent |
| `list_campaigns`, `get_campaign`, `list_campaign_links` | the campaign GETs | read-only |
| `create_campaign` | `POST /api/campaigns` | write |
| `update_campaign` | `PATCH /api/campaigns/{slug}` | write, idempotent |
| `delete_campaign` | `DELETE /api/campaigns/{slug}` | write, destructive |
| `batch_create_campaign_links` | `POST /api/campaigns/{slug}/links/batch` | write |
| `assign_links_to_campaign`, `remove_link_from_campaign` | the campaign link routes | write, idempotent |

Routes with no tool are listed, each with its reason, in `mcpserver.Excluded`
(`internal/mcpserver/parity.go`): the SVG QR code, the QR zip, the CSV export,
the SSE stream, campaign `/stats`, `DELETE /api/links/{key}`, the report routes
(tools come with #0150) and the public home info. The parity test keeps that
list complete.

**Limits.** A request body is capped at 256 KiB (`413` above that). JSON-RPC
batch arrays are rejected with `400` and a JSON-RPC error, because a batch
would run many tools for one rate-limit charge: send one request per POST.
Keys and slugs of `.`, `..` or containing `/` are refused. Cross-origin
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

## Adding tools (for #0150 and later)

Write a `Registrar` that calls `mcpserver.AddTool(b, Spec{…}, fn)` and append
it to `registrars` in `internal/mcpserver/tools.go`. Set `Write` for anything
that changes data (hidden from read-only tokens automatically), and list the
REST `Routes` the tool covers. A tool that reads `internal/reports` directly
leaves `Routes` empty, and the caller's identity is on `c.UserID`. The parity
test (`cmd/shortlinks/mcp_test.go`) fails if an `/api` route is neither
covered by a tool nor listed, with a reason, in `mcpserver.Excluded`; when a
report tool lands, remove its excluded route entry.

## Key files

| File | Role |
|---|---|
| `internal/mcpserver/server.go` | The handler: token check, per-request server |
| `internal/mcpserver/caller.go` | `Caller`, in-process dispatch, error mapping |
| `internal/mcpserver/tools*.go` | `AddTool`, the tools, short URLs |
| `internal/mcpserver/parity.go` | `Excluded` |
| `cmd/shortlinks/main.go` | `buildMux` records route patterns; mounts `/mcp` |
