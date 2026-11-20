package mcpserver

// Excluded lists the /api routes that deliberately have no MCP tool, each with
// the reason. The parity test in cmd/shortlinks fails for any /api route that
// is neither covered by a tool's Routes nor listed here, so a new route cannot
// ship without a decision. An entry here need not exist on the mux yet: it may
// name a route a later issue adds (the OpenAPI spec, #0148).
var Excluded = map[string]string{
	// Binary or streaming formats a text-and-image tool call cannot carry
	// usefully. The PNG QR code is covered by get_link_qr.
	"GET /api/links/{key}/qr.svg":          "vector QR file for printing; get_link_qr returns the PNG",
	"GET /api/campaigns/{slug}/qr.zip":     "zip archive of every QR code; a download, not an agent result",
	"GET /api/campaigns/{slug}/export.csv": "CSV download; the report tools give agents the same numbers as data",
	"GET /api/events":                      "server-sent event stream for the web UI; MCP tools are request/response",
	"GET /api/openapi.yaml":                "machine-readable REST spec (#0148); agents use the tools instead",

	// DELETE /api/links/{key} is covered by purge_link (?purge=true); without
	// purge it deactivates, which deactivate_link does through PATCH.

	// get_campaign already returns the stats for the campaign's default
	// window; an explicit from/to window is what get_campaign_performance
	// (#0150) is for.
	"GET /api/campaigns/{slug}/stats": "windowed rollup; get_campaign returns the default window and get_campaign_performance takes an explicit window",

	// Not an authenticated action at all.
	"GET /api/public/home": "unauthenticated signed-out home page info for the web UI",
	"GET /api/version":     "unauthenticated build id the web UI polls to notice a new deploy",
}
