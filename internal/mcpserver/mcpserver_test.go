package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/idempotency"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/reports"
)

const testBase = "https://go.example.test"

// env is the real API handlers behind the real guards over the test database,
// wired like cmd/shortlinks (requireAPI, the idempotency wrapper, the MCP
// handler dispatching back into the same mux), served by an httptest.Server.
type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	store *auth.Store
	h     *Handler
	srv   *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	truncate := func() {
		if _, err := pool.Exec(ctx, `TRUNCATE webauthn_challenges, pending_registrations, sessions,
			passkey_credentials, audit_log, campaigns, clicks, links, users RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	truncate()
	t.Cleanup(func() { truncate(); pool.Close() })

	store := auth.NewStore(pool)
	auditor := audit.New(pool)
	linkStore := links.NewStore(pool)
	campStore := campaigns.NewStore(pool)
	statsStore := clicks.NewStatsStore(pool)
	linksH := handlers.NewLinksHandler(linkStore, nil, nil, auditor, nil, statsStore, campStore, testBase)
	campH := handlers.NewCampaignsHandler(campStore, linkStore, auditor, statsStore, nil, testBase)
	meH := handlers.NewMeHandler(testBase)
	idem := idempotency.Middleware(idempotency.NewStore(pool))
	requireAPI := middleware.RequireSessionOrAPIToken(store, store, middleware.DefaultAPITokenLimits())

	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireAPI(idem(http.HandlerFunc(linksH.Create))))
	mux.Handle("GET /api/links", requireAPI(http.HandlerFunc(linksH.List)))
	mux.Handle("GET /api/links/{key}", requireAPI(http.HandlerFunc(linksH.Get)))
	mux.Handle("PATCH /api/links/{key}", requireAPI(http.HandlerFunc(linksH.Patch)))
	mux.Handle("GET /api/links/{key}/qr.png", requireAPI(http.HandlerFunc(linksH.QRPNG)))
	mux.Handle("GET /api/campaigns", requireAPI(http.HandlerFunc(campH.List)))
	mux.Handle("POST /api/campaigns", requireAPI(idem(http.HandlerFunc(campH.Create))))
	mux.Handle("GET /api/campaigns/{slug}", requireAPI(http.HandlerFunc(campH.Get)))
	mux.Handle("PATCH /api/campaigns/{slug}", requireAPI(http.HandlerFunc(campH.Patch)))
	mux.Handle("DELETE /api/campaigns/{slug}", requireAPI(http.HandlerFunc(campH.Delete)))
	mux.Handle("GET /api/campaigns/{slug}/links", requireAPI(http.HandlerFunc(campH.ListLinks)))
	mux.Handle("POST /api/campaigns/{slug}/links", requireAPI(http.HandlerFunc(campH.AssignLinks)))
	mux.Handle("DELETE /api/campaigns/{slug}/links/{key}", requireAPI(http.HandlerFunc(campH.UnassignLink)))
	mux.Handle("POST /api/campaigns/{slug}/links/batch", requireAPI(idem(http.HandlerFunc(campH.BatchCreateLinks))))
	mux.Handle("GET /api/me", requireAPI(http.HandlerFunc(meH.Me)))
	repSvc := reports.NewService(pool, "America/Los_Angeles")
	repSvc.BaseURL = testBase
	repH := handlers.NewReportsHandler(repSvc)
	mux.Handle("GET /api/reports", requireAPI(http.HandlerFunc(repH.List)))
	mux.Handle("GET /api/reports/{name}", requireAPI(http.HandlerFunc(repH.Get)))
	h := New(Options{BaseURL: testBase, Version: "test", Reports: repSvc, DefaultTimezone: "America/Los_Angeles"})
	mux.Handle(middleware.MCPPath, requireAPI(h))
	h.SetDispatcher(mux)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, pool: pool, store: store, h: h, srv: srv}
}

func (e *env) user(email string) int64 {
	var id int64
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at) VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) token(userID int64, name string, readOnly bool) (id int64, secret string) {
	t, secret, err := e.store.CreateAPIToken(context.Background(),
		auth.NewAPIToken{UserID: userID, Name: name, ReadOnly: readOnly}, nil, audit.Entry{})
	if err != nil {
		e.t.Fatal(err)
	}
	return t.ID, secret
}

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) connect(token string) *mcp.ClientSession {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             e.srv.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerRT{token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// do sends a raw request to the server.
func (e *env) do(method, path, token, body string, hdr map[string]string) (int, string) {
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func ok(t *testing.T, cs *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	res := call(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s: tool error: %s", name, textOf(res))
	}
	b, _ := json.Marshal(res.StructuredContent)
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: structured content %s: %v", name, b, err)
	}
	return m
}

func textOf(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	out := map[string]*mcp.Tool{}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out[tool.Name] = tool
	}
	return out
}

var allTools = []string{
	"whoami", "list_links", "get_link", "get_link_qr", "create_link", "update_link", "deactivate_link", "reactivate_link",
	"list_campaigns", "get_campaign", "list_campaign_links", "create_campaign", "update_campaign", "delete_campaign",
	"batch_create_campaign_links", "assign_links_to_campaign", "remove_link_from_campaign",
	"list_reports", "get_overview", "get_top_content", "get_top_links", "get_breakdown", "get_timeseries",
	"get_movers", "get_idle_links", "compare_campaigns", "get_campaign_performance", "get_link_performance",
}

func TestRoundTrip_InitializeListAndEveryTool(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)

	if got := cs.InitializeResult().ServerInfo.Name; got != "shortlinks" {
		t.Errorf("server name = %q", got)
	}
	tools := toolNames(t, cs)
	for _, n := range allTools {
		tool, found := tools[n]
		if !found {
			t.Errorf("tool %s missing from tools/list", n)
			continue
		}
		if tool.Description == "" || tool.Annotations == nil {
			t.Errorf("tool %s lacks description or annotations", n)
		}
		// Every input field carries a description.
		b, _ := json.Marshal(tool.InputSchema)
		var sch struct {
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		_ = json.Unmarshal(b, &sch)
		for f, p := range sch.Properties {
			if p.Description == "" {
				t.Errorf("tool %s field %s has no description", n, f)
			}
		}
	}
	if len(tools) != len(allTools) {
		t.Errorf("tools/list returned %d tools; want %d", len(tools), len(allTools))
	}
	// Annotations per the issue.
	if !tools["get_link"].Annotations.ReadOnlyHint {
		t.Error("get_link should be read-only")
	}
	if d := tools["delete_campaign"].Annotations.DestructiveHint; d == nil || !*d {
		t.Error("delete_campaign should be destructive")
	}
	if d := tools["deactivate_link"].Annotations.DestructiveHint; d == nil || !*d {
		t.Error("deactivate_link should be destructive")
	}
	if !tools["update_link"].Annotations.IdempotentHint || !tools["reactivate_link"].Annotations.IdempotentHint {
		t.Error("update_link and reactivate_link should be idempotent")
	}
	if d := tools["create_link"].Annotations.DestructiveHint; d == nil || *d {
		t.Error("create_link should not be destructive")
	}

	// whoami
	who := ok(t, cs, "whoami", map[string]any{})
	if who["email"] != "alice@example.com" || who["base_url"] != testBase {
		t.Errorf("whoami = %v", who)
	}

	// create_campaign, create_link (in it), get/list/update.
	camp := ok(t, cs, "create_campaign", map[string]any{"name": "Spring Posters", "description": "d", "idempotency_key": "c1"})
	slug, _ := camp["slug"].(string)
	if slug == "" {
		t.Fatalf("create_campaign = %v", camp)
	}
	link := ok(t, cs, "create_link", map[string]any{
		"destination_url": "https://example.com/one", "title": "One", "campaign_slug": slug,
		"utm_source": "poster", "idempotency_key": "l1",
	})
	key, _ := link["key"].(string)
	if key == "" || link["short_url"] != testBase+"/u/"+key {
		t.Fatalf("create_link = %v; want a short_url from BASE_URL", link)
	}
	if link["campaign_id"] == nil {
		t.Errorf("link not in campaign: %v", link)
	}
	res := call(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/one"})
	if res.IsError || !strings.Contains(textOf(res), "https://example.com/one") {
		t.Errorf("second create (duplicate) = %s", textOf(res))
	}

	// #0179: key_length makes a longer generated key; out of range is an error.
	long := ok(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/long", "key_length": 16})
	if k, _ := long["key"].(string); len(k) != 16 {
		t.Errorf("create_link key_length=16 key = %q, want 16 chars", k)
	}
	if res := call(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/bad", "key_length": 99}); !res.IsError {
		t.Errorf("create_link key_length=99 should be an error, got %s", textOf(res))
	}

	got := ok(t, cs, "get_link", map[string]any{"key": key})
	if got["short_url"] != testBase+"/u/"+key || got["title"] != "One" {
		t.Errorf("get_link = %v", got)
	}
	list := ok(t, cs, "list_links", map[string]any{"q": "one", "status": "active", "per_page": 5})
	items, _ := list["links"].([]any)
	if len(items) == 0 {
		t.Fatalf("list_links = %v", list)
	}
	for _, it := range items {
		if it.(map[string]any)["short_url"] == nil {
			t.Errorf("list item lacks short_url: %v", it)
		}
	}
	upd := ok(t, cs, "update_link", map[string]any{"key": key, "title": "Renamed"})
	if upd["title"] != "Renamed" {
		t.Errorf("update_link = %v", upd)
	}
	off := ok(t, cs, "deactivate_link", map[string]any{"key": key})
	if off["active"] != false {
		t.Errorf("deactivate_link = %v", off)
	}
	on := ok(t, cs, "reactivate_link", map[string]any{"key": key})
	if on["active"] != true {
		t.Errorf("reactivate_link = %v", on)
	}

	// QR as image content.
	qr := call(t, cs, "get_link_qr", map[string]any{"key": key})
	if qr.IsError || len(qr.Content) != 2 {
		t.Fatalf("get_link_qr = %v", qr)
	}
	img, isImg := qr.Content[1].(*mcp.ImageContent)
	if !isImg || img.MIMEType != "image/png" || len(img.Data) < 8 || string(img.Data[1:4]) != "PNG" {
		t.Errorf("get_link_qr image = %#v", qr.Content[1])
	}

	// Campaign reads and writes.
	if c := ok(t, cs, "get_campaign", map[string]any{"slug": slug}); c["slug"] != slug {
		t.Errorf("get_campaign = %v", c)
	}
	if l := ok(t, cs, "list_campaigns", map[string]any{}); len(l["campaigns"].([]any)) != 1 {
		t.Errorf("list_campaigns = %v", l)
	}
	if l := ok(t, cs, "list_campaign_links", map[string]any{"slug": slug}); len(l["links"].([]any)) != 1 {
		t.Errorf("list_campaign_links = %v", l)
	}
	if u := ok(t, cs, "update_campaign", map[string]any{"slug": slug, "description": "new", "archived": false}); u["description"] != "new" {
		t.Errorf("update_campaign = %v", u)
	}
	batch := ok(t, cs, "batch_create_campaign_links", map[string]any{
		"slug": slug, "idempotency_key": "b1",
		"rows": []map[string]any{
			{"destination_url": "https://example.com/two", "utm_source": "x", "placement": "a"},
			{"destination_url": "https://example.com/two", "utm_source": "y", "placement": "b"},
		},
	})
	bl, _ := batch["links"].([]any)
	if len(bl) != 2 || bl[0].(map[string]any)["short_url"] == nil {
		t.Errorf("batch_create_campaign_links = %v", batch)
	}
	// Unassign then assign the first link.
	ok(t, cs, "remove_link_from_campaign", map[string]any{"slug": slug, "key": key})
	if l := ok(t, cs, "list_campaign_links", map[string]any{"slug": slug}); len(l["links"].([]any)) != 2 {
		t.Errorf("after remove: %v", l)
	}
	ok(t, cs, "assign_links_to_campaign", map[string]any{"slug": slug, "keys": []string{key}})
	if l := ok(t, cs, "list_campaign_links", map[string]any{"slug": slug}); len(l["links"].([]any)) != 3 {
		t.Errorf("after assign: %v", l)
	}
	ok(t, cs, "delete_campaign", map[string]any{"slug": slug})
	res = call(t, cs, "get_campaign", map[string]any{"slug": slug})
	if !res.IsError || !strings.Contains(textOf(res), "HTTP 404") {
		t.Errorf("get deleted campaign = %s", textOf(res))
	}
}

func TestRESTErrorsBecomeToolErrors(t *testing.T) {
	e := newEnv(t)
	_, secret := e.token(e.user("alice@example.com"), "agent", false)
	cs := e.connect(secret)

	res := call(t, cs, "get_link", map[string]any{"key": "nosuch"})
	if !res.IsError || !strings.Contains(textOf(res), "HTTP 404") || !strings.Contains(textOf(res), "link not found") {
		t.Errorf("404 = %s", textOf(res))
	}
	res = call(t, cs, "create_link", map[string]any{"destination_url": "ftp://nope"})
	if !res.IsError || !strings.Contains(textOf(res), "HTTP 400") {
		t.Errorf("bad destination = %s", textOf(res))
	}
	res = call(t, cs, "list_links", map[string]any{"sort": "clicks_window"})
	if !res.IsError || !strings.Contains(textOf(res), "invalid_sort") {
		t.Errorf("invalid sort = %s", textOf(res))
	}
	res = call(t, cs, "get_link", map[string]any{"key": ""})
	if !res.IsError || !strings.Contains(textOf(res), "key is required") {
		t.Errorf("empty key = %s", textOf(res))
	}
	// Schema validation happens before the handler: missing required field.
	res = call(t, cs, "create_link", map[string]any{})
	if !res.IsError {
		t.Errorf("missing destination_url should be a tool error, got %s", textOf(res))
	}
}

func TestReadOnlyToken_SeesOnlyReadTools_AndWritesFailAtBothLayers(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, rw := e.token(uid, "rw", false)
	_, ro := e.token(uid, "ro", true)
	rwSession := e.connect(rw)
	link := ok(t, rwSession, "create_link", map[string]any{"destination_url": "https://example.com/ro"})
	key := link["key"].(string)

	cs := e.connect(ro)
	tools := toolNames(t, cs)
	writes := map[string]bool{"create_link": true, "update_link": true, "deactivate_link": true, "reactivate_link": true,
		"create_campaign": true, "update_campaign": true, "delete_campaign": true, "batch_create_campaign_links": true,
		"assign_links_to_campaign": true, "remove_link_from_campaign": true}
	for _, n := range allTools {
		_, listed := tools[n]
		if writes[n] && listed {
			t.Errorf("read-only session lists write tool %s", n)
		}
		if !writes[n] && !listed {
			t.Errorf("read-only session lacks read tool %s", n)
		}
	}
	if who := ok(t, cs, "whoami", map[string]any{}); who["api_token"].(map[string]any)["read_only"] != true {
		t.Errorf("whoami = %v", who)
	}
	ok(t, cs, "get_link", map[string]any{"key": key})

	// Layer 1: calling a hidden write tool is a tool error, and nothing changed.
	res := call(t, cs, "deactivate_link", map[string]any{"key": key})
	if !res.IsError || !strings.Contains(textOf(res), "read-only") {
		t.Errorf("forced write = %s", textOf(res))
	}
	if got := ok(t, rwSession, "get_link", map[string]any{"key": key}); got["active"] != true {
		t.Errorf("link changed by read-only call: %v", got)
	}

	// Layer 2: with the tool guard off, the inner REST request is still
	// refused by requireAPI because it is re-authenticated with the same token.
	e.h.skipToolGuard = true
	cs2 := e.connect(ro)
	res = call(t, cs2, "deactivate_link", map[string]any{"key": key})
	if !res.IsError || !strings.Contains(textOf(res), "HTTP 403") || !strings.Contains(textOf(res), "api_token_read_only") {
		t.Errorf("REST guard = %s", textOf(res))
	}
	res = call(t, cs2, "create_link", map[string]any{"destination_url": "https://example.com/blocked"})
	if !res.IsError || !strings.Contains(textOf(res), "api_token_read_only") {
		t.Errorf("REST guard create = %s", textOf(res))
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM links`).Scan(&n)
	if n != 1 {
		t.Errorf("links = %d; want 1", n)
	}
	e.h.skipToolGuard = false

	// The exemption is narrow: a read-only token still cannot POST to /api.
	if code, _ := e.do("POST", "/api/links", ro, `{"destination_url":"https://example.com/x"}`, map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Errorf("POST /api/links with read-only token = %d; want 403", code)
	}
	// ... nor PATCH /mcp (the exemption is POST only).
	if code, _ := e.do("PATCH", "/mcp", ro, `{}`, nil); code != 403 {
		t.Errorf("PATCH /mcp with read-only token = %d; want 403", code)
	}
}

func TestAuthRejections(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	tid, secret := e.token(uid, "agent", false)
	initBody := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"1"}}}`
	hdr := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}

	if code, _ := e.do("POST", "/mcp", secret, initBody, hdr); code != 200 {
		t.Fatalf("valid token = %d", code)
	}
	// Missing token.
	if code, _ := e.do("POST", "/mcp", "", initBody, hdr); code != 401 {
		t.Errorf("no token = %d; want 401", code)
	}
	// Unknown token.
	if code, _ := e.do("POST", "/mcp", "slk_bogus", initBody, hdr); code != 401 {
		t.Errorf("bad token = %d; want 401", code)
	}
	// Cookie session: authenticated by RequireSession, refused by /mcp.
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO sessions (token, user_id, created_at, last_seen_at, expires_at) VALUES ('cookie-tok', $1, now(), now(), now() + interval '1 day')`, uid); err != nil {
		t.Skipf("cannot seed session (schema): %v", err)
	}
	h2 := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Cookie": auth.SessionCookieName + "=cookie-tok"}
	code, body := e.do("POST", "/mcp", "", initBody, h2)
	if code != 403 || !strings.Contains(body, "api_token_required") {
		t.Errorf("cookie session = %d %s; want 403 api_token_required", code, body)
	}
	// Revoked token.
	if _, err := e.store.RevokeAPIToken(context.Background(), uid, tid, time.Now(), nil, audit.Entry{}); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do("POST", "/mcp", secret, initBody, hdr); code != 401 {
		t.Errorf("revoked token = %d; want 401", code)
	}
	// GET and DELETE on /mcp are not part of the stateless transport.
	_, secret2 := e.token(uid, "agent2", false)
	if code, _ := e.do("GET", "/mcp", secret2, "", nil); code != 405 {
		t.Errorf("GET /mcp = %d; want 405", code)
	}
}

func TestIdempotencyReplayThroughCreateLink(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)

	args := map[string]any{"destination_url": "https://example.com/idem", "idempotency_key": "retry-1"}
	first := ok(t, cs, "create_link", args)
	second := ok(t, cs, "create_link", args)
	if first["key"] != second["key"] || first["id"] != second["id"] {
		t.Errorf("replay differs: %v vs %v", first, second)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM links WHERE user_id = $1`, uid).Scan(&n)
	if n != 1 {
		t.Errorf("links = %d; want 1", n)
	}
	// Same key, different body: 422 idempotency_key_reused, as a tool error.
	res := call(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/other", "idempotency_key": "retry-1"})
	if !res.IsError || !strings.Contains(textOf(res), "idempotency_key_reused") || !strings.Contains(textOf(res), "HTTP 422") {
		t.Errorf("reused key = %s", textOf(res))
	}
}

func TestAuditCarriesViaMCP(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	tid, secret := e.token(uid, "my-agent", false)
	cs := e.connect(secret)

	ok(t, cs, "create_link", map[string]any{"destination_url": "https://example.com/audit"})
	ok(t, cs, "create_campaign", map[string]any{"name": "Audited"})
	// A direct REST call keeps the existing via.
	if code, body := e.do("POST", "/api/links", secret, `{"destination_url":"https://example.com/direct"}`, map[string]string{"Content-Type": "application/json"}); code != 201 {
		t.Fatalf("REST create = %d %s", code, body)
	}

	rows, err := e.pool.Query(context.Background(),
		`SELECT action, metadata->>'via', (metadata->>'api_token_id')::bigint, metadata->>'api_token_name' FROM audit_log
		 WHERE action IN ('link.created','campaign.created') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var vias []string
	for rows.Next() {
		var action, via, name string
		var id int64
		if err := rows.Scan(&action, &via, &id, &name); err != nil {
			t.Fatal(err)
		}
		if id != tid || name != "my-agent" {
			t.Errorf("%s attributed to token %d %q", action, id, name)
		}
		vias = append(vias, action+":"+via)
	}
	want := []string{"link.created:mcp", "campaign.created:mcp", "link.created:api_token"}
	if strings.Join(vias, ",") != strings.Join(want, ",") {
		t.Errorf("audit via = %v; want %v", vias, want)
	}
}

func TestInnerCallsDoNotConsumeRateLimit(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	cs := e.connect(secret)
	// Default burst is 60 per token. Each outer POST /mcp costs one; if the
	// inner REST call cost one too, 40 sequential tool calls (80 charges plus
	// the handshake) would exhaust it.
	for i := 0; i < 40; i++ {
		res := call(t, cs, "whoami", map[string]any{})
		if res.IsError {
			t.Fatalf("call %d: %s", i, textOf(res))
		}
	}
}

func TestDescribeCoversRoutes(t *testing.T) {
	seen := map[string]bool{}
	for _, ti := range Describe() {
		if seen[ti.Name] {
			t.Errorf("duplicate tool %s", ti.Name)
		}
		seen[ti.Name] = true
		if ti.Write == false && len(ti.Routes) == 0 {
			t.Errorf("read tool %s declares no routes", ti.Name)
		}
	}
	for _, n := range allTools {
		if !seen[n] {
			t.Errorf("Describe lacks %s", n)
		}
	}
}

func TestAddShortURLs(t *testing.T) {
	in := []byte(`{"links":[{"key":"a b","destination_url":"https://x.test/?a=1&b=2","id":12345678901234567}],"other":{"key":"no-dest"}}`)
	out := string(addShortURLs(in, "https://go.test/"))
	if !strings.Contains(out, `"short_url":"https://go.test/u/a%20b"`) {
		t.Errorf("out = %s", out)
	}
	if strings.Contains(out, `"other":{"key":"no-dest","short_url"`) {
		t.Errorf("added short_url to a non-link: %s", out)
	}
	if !strings.Contains(out, "12345678901234567") || !strings.Contains(out, "a=1&b=2") {
		t.Errorf("number or URL mangled: %s", out)
	}
}

func TestBatchRejectedAndOversizeBody(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)
	hdr := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
	one := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_link","arguments":{"destination_url":"https://example.com/batch"}}}`
	code, body := e.do("POST", "/mcp", secret, "  \n["+one+","+one+"]", hdr)
	if code != 400 || !strings.Contains(body, "batches are not supported") {
		t.Errorf("batch = %d %s; want 400", code, body)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM links`).Scan(&n)
	if n != 0 {
		t.Errorf("batch ran tools: %d links", n)
	}
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", MaxBodyBytes) + `"}}`
	if code, _ := e.do("POST", "/mcp", secret, big, hdr); code != 413 {
		t.Errorf("oversize = %d; want 413", code)
	}
}

func TestPanicsBecomeToolErrors(t *testing.T) {
	e := newEnv(t)
	uid := e.user("alice@example.com")
	_, secret := e.token(uid, "agent", false)

	saved := registrars
	registrars = append(append([]Registrar{}, saved...), func(b *Builder) {
		AddTool(b, Spec{Name: "boom", Description: "panics"}, func(context.Context, *Caller, noInput) (*mcp.CallToolResult, error) {
			panic("tool body")
		})
	})
	defer func() { registrars = saved }()
	cs := e.connect(secret)

	res := call(t, cs, "boom", map[string]any{})
	if !res.IsError || !strings.Contains(textOf(res), "internal_error") {
		t.Errorf("panicking tool = %s", textOf(res))
	}

	e.h.SetDispatcher(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("dispatch") }))
	res = call(t, cs, "whoami", map[string]any{})
	if !res.IsError || !strings.Contains(textOf(res), "internal_error") {
		t.Errorf("panicking dispatcher = %s", textOf(res))
	}
	// Still serving: restore the real dispatcher and call again.
	e.h.SetDispatcher(e.srv.Config.Handler)
	cs2 := e.connect(secret)
	if res := call(t, cs2, "whoami", map[string]any{}); res.IsError {
		t.Errorf("after panic = %s", textOf(res))
	}
}

func TestDotKeysAreRejected(t *testing.T) {
	e := newEnv(t)
	_, secret := e.token(e.user("alice@example.com"), "agent", false)
	cs := e.connect(secret)
	for _, k := range []string{".", "..", "a/b"} {
		res := call(t, cs, "get_link", map[string]any{"key": k})
		if !res.IsError || !strings.Contains(textOf(res), "invalid") || strings.Contains(textOf(res), "<a href") {
			t.Errorf("key %q = %s", k, textOf(res))
		}
	}
	// A redirect from the dispatcher is mapped to invalid_request.
	e.h.SetDispatcher(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
	}))
	cs2 := e.connect(secret)
	res := call(t, cs2, "whoami", map[string]any{})
	if !res.IsError || !strings.Contains(textOf(res), "invalid_request") || strings.Contains(textOf(res), "href") {
		t.Errorf("3xx = %s", textOf(res))
	}
}
