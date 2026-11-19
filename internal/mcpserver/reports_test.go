package mcpserver

import (
	"net/http"
	"net/http/httptest"

	"context"
	"encoding/json"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// TestEveryReportHasATool is the drift guard (#0150): a report registered in
// reports.NewDefaultRegistry without a tool fails here, and so does a tool
// that maps to a report that no longer exists.
func TestEveryReportHasATool(t *testing.T) {
	tools := map[string]bool{}
	for _, ti := range Describe() {
		tools[ti.Name] = true
	}
	cat := reports.NewDefaultRegistry().Catalogue()
	seen := map[string]bool{}
	for _, r := range cat.Reports {
		tool, ok := reportTools[r.Name]
		if !ok {
			t.Errorf("report %q is registered but has no MCP tool; add it to reportTools and registerReportTools", r.Name)
			continue
		}
		if !tools[tool] {
			t.Errorf("report %q maps to tool %q, which is not registered", r.Name, tool)
		}
		seen[r.Name] = true
	}
	for name := range reportTools {
		if !seen[name] {
			t.Errorf("reportTools lists %q, which is not a registered report", name)
		}
	}
	if !tools["list_reports"] {
		t.Error("list_reports is not registered")
	}
	if len(reportTools) != len(cat.Reports) {
		t.Errorf("reportTools has %d entries for %d reports", len(reportTools), len(cat.Reports))
	}
}

func schemaOf(t *testing.T, tool *mcp.Tool) map[string]map[string]any {
	t.Helper()
	b, _ := json.Marshal(tool.InputSchema)
	var sch struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &sch); err != nil {
		t.Fatal(err)
	}
	return sch.Properties
}

func TestReportTools_SchemaEnumsAndAnnotations(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, ro := e.token(uid, "ro", true)
	tools := toolNames(t, e.connect(ro)) // a read-only token must see every report tool

	for _, n := range append([]string{"list_reports"}, valuesOf(reportTools)...) {
		tool, ok := tools[n]
		if !ok {
			t.Errorf("read-only token does not see %s", n)
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s is not annotated read-only", n)
		}
		if n := len(strings.Split(strings.TrimSpace(tool.Description), ". ")); n > 3 {
			t.Errorf("%s description is too long (%d sentences)", tool.Name, n)
		}
	}
	p := schemaOf(t, tools["get_breakdown"])
	if enum, _ := p["range"]["enum"].([]any); len(enum) != 5 {
		t.Errorf("range enum = %v", p["range"])
	}
	if p["from"]["format"] != "date" || p["to"]["format"] != "date" {
		t.Errorf("from/to format = %v / %v", p["from"], p["to"])
	}
	if p["tz"]["default"] != "America/Los_Angeles" {
		t.Errorf("tz default = %v", p["tz"]["default"])
	}
	if enum, _ := p["dimension"]["enum"].([]any); len(enum) != len(reports.BreakdownDimensions) {
		t.Errorf("dimension enum = %v", p["dimension"])
	}
	if ty, _ := json.Marshal(p["source"]["type"]); !strings.Contains(string(ty), "array") {
		t.Errorf("source = %v", p["source"])
	}
	for tool, field := range map[string]string{"get_top_content": "sort", "get_top_links": "sort", "get_timeseries": "split_by",
		"get_movers": "unit", "get_campaign_performance": "columns"} {
		tl := tools[tool]
		if enum, _ := schemaOf(t, tl)[field]["enum"].([]any); len(enum) == 0 {
			t.Errorf("%s.%s has no enum", tool, field)
		}
	}
}

func valuesOf(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestReportTools_RoundTripEveryTool(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)

	camp := ok(t, cs, "create_campaign", map[string]any{"name": "Spring"})
	slug := camp["slug"].(string)
	link := ok(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/a", "title": "A"})
	key := link["key"].(string)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO clicks (link_id, clicked_at, utm_source, is_bot)
		SELECT l.id, now() - interval '1 day', 'news', FALSE FROM links l, generate_series(1, 6) WHERE l.key = $1`, key); err != nil {
		t.Fatal(err)
	}

	list := ok(t, cs, "list_reports", map[string]any{})
	if n := len(list["reports"].([]any)); n != len(reportTools) {
		t.Errorf("list_reports returned %d reports, want %d", n, len(reportTools))
	}
	if txt := textOf(call(t, cs, "list_reports", map[string]any{})); !strings.Contains(txt, "overview (get_overview)") {
		t.Errorf("list_reports text = %s", txt)
	}

	calls := map[string]map[string]any{
		"get_overview":             {"range": "7d"},
		"get_top_content":          {"range": "7d", "sort": "clicks", "limit": 5},
		"get_top_links":            {"range": "7d", "source": []string{"news"}},
		"get_breakdown":            {"dimension": "source", "range": "30d", "include_bots": true},
		"get_timeseries":           {"granularity": "week", "split_by": "source", "range": "30d"},
		"get_movers":               {"unit": "link", "min_clicks": 0},
		"get_idle_links":           {"min_age_days": 0},
		"compare_campaigns":        {"compare": "none"},
		"get_campaign_performance": {"slug": slug, "columns": "medium"},
		"get_link_performance":     {"key": key, "from": "2020-01-01", "to": "2020-01-31", "tz": "UTC"},
	}
	if len(calls) != len(reportTools) {
		t.Fatalf("test covers %d tools, there are %d report tools", len(calls), len(reportTools))
	}
	for tool, args := range calls {
		res := call(t, cs, tool, args)
		if res.IsError {
			t.Errorf("%s: %s", tool, textOf(res))
			continue
		}
		b, _ := json.Marshal(res.StructuredContent)
		var env reports.Envelope
		if err := json.Unmarshal(b, &env); err != nil {
			t.Errorf("%s: envelope %s: %v", tool, b, err)
		}
		if env.Report == "" || env.Window.From == "" || env.Data == nil {
			t.Errorf("%s: incomplete envelope %s", tool, b)
		}
		if len(res.Content) != 2 {
			t.Errorf("%s: %d content blocks, want highlights + JSON", tool, len(res.Content))
			continue
		}
		first := res.Content[0].(*mcp.TextContent).Text
		if len(env.Highlights) > 0 && first != strings.Join(env.Highlights, "\n") {
			t.Errorf("%s: text %q is not the highlights %q", tool, first, env.Highlights)
		}
		if !json.Valid([]byte(res.Content[1].(*mcp.TextContent).Text)) {
			t.Errorf("%s: second block is not the JSON mirror", tool)
		}
	}

	// tz defaulted from the instance: no tz argument echoes DEFAULT_TIMEZONE.
	ov := ok(t, cs, "get_overview", map[string]any{})
	if tz := ov["window"].(map[string]any)["tz"]; tz != "America/Los_Angeles" {
		t.Errorf("default tz = %v", tz)
	}
	// Real counts flow through.
	bd := ok(t, cs, "get_breakdown", map[string]any{"dimension": "source"})
	if !strings.Contains(string(mustJSON(bd)), `"news"`) {
		t.Errorf("breakdown = %s", mustJSON(bd))
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestReportTools_Errors(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	other := e.user("bob@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)

	// ParamError carries field and code (the enum is checked by the SDK for
	// range, so use a value the schema allows but the report rejects).
	res := call(t, cs, "get_overview", map[string]any{"tz": "Mars/Base"})
	if !res.IsError || !strings.Contains(textOf(res), "invalid_tz") || !strings.Contains(textOf(res), "field tz") {
		t.Errorf("bad tz = %s", textOf(res))
	}
	if st, _ := res.StructuredContent.(map[string]any); st["field"] != "tz" || st["error"] != "invalid_tz" {
		t.Errorf("structured = %v", res.StructuredContent)
	}
	res = call(t, cs, "get_overview", map[string]any{"from": "2999-01-01"})
	if !res.IsError || !strings.Contains(textOf(res), "invalid_date") {
		t.Errorf("future from = %s", textOf(res))
	}
	res = call(t, cs, "get_idle_links", map[string]any{"min_age_days": 99999})
	if !res.IsError || !strings.Contains(textOf(res), "min_age_days") {
		t.Errorf("min_age_days = %s", textOf(res))
	}

	// NotFoundError carries its code; another user's link is not found.
	res = call(t, cs, "get_campaign_performance", map[string]any{"slug": "nope"})
	if !res.IsError || !strings.Contains(textOf(res), "campaign_not_found") {
		t.Errorf("unknown campaign = %s", textOf(res))
	}
	var otherKey string
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, 'zzother', 'https://example.com/', TRUE, 0, now()) RETURNING key`, other).Scan(&otherKey); err != nil {
		t.Fatal(err)
	}
	res = call(t, cs, "get_link_performance", map[string]any{"key": otherKey})
	if !res.IsError || !strings.Contains(textOf(res), "link_not_found") {
		t.Errorf("other user's link = %s", textOf(res))
	}
	// Missing required argument is a schema error from the SDK.
	rejected := func(tool string, args map[string]any) bool {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		return err != nil || res.IsError
	}
	if !rejected("get_breakdown", map[string]any{}) {
		t.Error("get_breakdown without dimension should be rejected")
	}
	// A value outside an enum is rejected by the schema.
	if !rejected("get_overview", map[string]any{"range": "5d"}) {
		t.Error("range=5d should be rejected")
	}
}

func TestPrompts_RoundTrip(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, ro := e.token(uid, "ro", true) // prompts are available to read-only tokens
	cs := e.connect(ro)
	ctx := context.Background()

	names := map[string]*mcp.Prompt{}
	for p, err := range cs.Prompts(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names[p.Name] = p
	}
	for _, n := range []string{"weekly_summary", "campaign_report", "whats_working"} {
		if names[n] == nil || names[n].Description == "" {
			t.Errorf("prompt %s missing or undescribed", n)
		}
	}
	get := func(name string, args map[string]string) (string, error) {
		res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
		if err != nil {
			return "", err
		}
		if len(res.Messages) != 1 {
			t.Fatalf("%s: %d messages", name, len(res.Messages))
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text, nil
	}
	tools := toolNames(t, cs)
	check := func(name string, args map[string]string, wants ...string) {
		t.Helper()
		txt, err := get(name, args)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			return
		}
		for _, w := range wants {
			if !strings.Contains(txt, w) {
				t.Errorf("%s text lacks %q:\n%s", name, w, txt)
			}
		}
		// Every tool a prompt names exists.
		for _, f := range strings.Fields(strings.NewReplacer("\n", " ", ",", " ", ".", " ").Replace(txt)) {
			if (strings.HasPrefix(f, "get_") || f == "list_campaigns") && tools[f] == nil {
				t.Errorf("%s names unknown tool %s", name, f)
			}
		}
	}
	check("weekly_summary", nil, "get_overview", "get_movers", "get_top_content", "range=7d")
	check("weekly_summary", map[string]string{"tz": "America/Los_Angeles"}, `tz="America/Los_Angeles"`)
	check("campaign_report", map[string]string{"slug": "spring-launch"}, "get_campaign_performance", `slug="spring-launch"`, "idle")
	check("whats_working", nil, "get_top_content", "get_breakdown", "dimension=source", "range=30d")
	check("whats_working", map[string]string{"range": "7d"}, "range=7d")

	if _, err := get("campaign_report", nil); err == nil {
		t.Error("campaign_report without slug should fail")
	}
	if _, err := get("whats_working", map[string]string{"range": "2d"}); err == nil {
		t.Error("whats_working range=2d should fail")
	}
	if _, err := get("weekly_summary", map[string]string{"tz": "Mars/Base"}); err == nil {
		t.Error("weekly_summary bad tz should fail")
	}
}

// Dev mode builds the handler without a report service; the report tools
// must answer with a tool error rather than panic.
func TestReportTools_NoServiceIsToolError(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	bare := New(Options{BaseURL: testBase, Version: "test"}) // no Reports
	mux := http.NewServeMux()
	mux.Handle(middleware.MCPPath, middleware.RequireSessionOrAPIToken(e.store, e.store, middleware.DefaultAPITokenLimits())(bare))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{secret}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tool := range []string{"get_overview", "list_reports"} {
		res := call(t, cs, tool, map[string]any{})
		if !res.IsError || !strings.Contains(textOf(res), "not available") {
			t.Errorf("%s without a service = %v %s", tool, res.IsError, textOf(res))
		}
	}
}
