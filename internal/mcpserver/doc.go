// Package mcpserver is the Model Context Protocol server embedded in the
// binary (#0149), served at /mcp over Streamable HTTP so AI agents can use
// ShortLinks as a tool. It uses the official Go SDK in stateless mode: every
// POST is authenticated on its own and gets a server built for that caller,
// so there is no session to hijack, expire or outlive a revoked token.
//
// # Auth
//
// /mcp is mounted behind the same requireAPI guard as /api. It accepts only
// API tokens ("Authorization: Bearer slk_…"); a cookie or bearer session is
// refused with 403 api_token_required, because MCP is for scripts and agents.
//
// # One code path
//
// Link and campaign tools never reimplement the REST handlers. Each builds an
// *http.Request that carries the caller's Authorization header and serves it
// through the same mux as the network, so requireAPI, validation, the URL
// filter, idempotency, audit rows and cache eviction behave exactly as for a
// direct REST call (see Caller.call). Report tools (#0150) read
// internal/reports directly and register through the same Builder.
//
// # Read-only tokens
//
// MCP carries every call in a POST, so requireAPI's method check exempts
// POST /mcp (and only that). Read-only is enforced per tool instead: a
// read-only session's tools/list omits the write tools, calling one anyway
// is a tool error, and the REST layer still refuses the write as a second
// guard because the inner request is re-authenticated.
//
// # Adding tools
//
// Write a Registrar that calls AddTool on the Builder and append it to
// registrars. Declare the REST routes the tool covers (none for a tool that
// bypasses REST); the parity test in cmd/shortlinks fails for any /api route
// that is neither covered nor on Excluded.
package mcpserver
