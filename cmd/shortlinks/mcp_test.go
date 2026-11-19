package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/mcpserver"
)

func identity(h http.Handler) http.Handler { return h }

// routePatterns returns the patterns buildMux registers, using zero-value
// handlers: only the registration table is read, nothing is served.
func routePatterns() []string {
	_, patterns := buildMux(&config.Config{BaseURL: "https://go.example.test"}, nil,
		&handlers.AuthHandler{}, &handlers.CredentialsHandler{}, &handlers.SettingsHandler{},
		&handlers.AdminUsersHandler{}, &handlers.AdminAuditHandler{}, &handlers.URLFiltersHandler{},
		&handlers.EventsHandler{}, &handlers.RedirectHandler{}, &handlers.LinksHandler{},
		&handlers.CampaignsHandler{}, &handlers.ReportsHandler{}, &handlers.MeHandler{},
		&handlers.HomeHandler{}, &handlers.APITokensHandler{},
		identity, identity, identity, identity, nil, nil)
	return patterns
}

// TestMCPToolParity fails when an /api route registered in buildMux is neither
// covered by an MCP tool nor on mcpserver.Excluded, so a new route cannot ship
// without an explicit decision about agents (#0149).
func TestMCPToolParity(t *testing.T) {
	patterns := routePatterns()
	registered := map[string]bool{}
	for _, p := range patterns {
		registered[p] = true
	}
	for _, m := range []string{"POST", "GET", "DELETE"} {
		if !registered[m+" /mcp"] {
			t.Errorf("%s /mcp is not registered", m)
		}
	}

	covered := map[string][]string{}
	for _, ti := range mcpserver.Describe() {
		for _, r := range ti.Routes {
			covered[r] = append(covered[r], ti.Name)
		}
	}

	var missing []string
	for _, p := range patterns {
		path := p
		if i := strings.IndexByte(p, ' '); i >= 0 {
			path = p[i+1:]
		}
		if !strings.HasPrefix(path, "/api/") {
			continue
		}
		_, isCovered := covered[p]
		_, isExcluded := mcpserver.Excluded[p]
		switch {
		case isCovered && isExcluded:
			t.Errorf("route %q is both covered by a tool (%v) and excluded", p, covered[p])
		case !isCovered && !isExcluded:
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these /api routes have no MCP tool and are not in mcpserver.Excluded; add a tool or an excluded entry with a reason:\n  %s",
			strings.Join(missing, "\n  "))
	}

	// A tool must not claim a route that does not exist (a typo would
	// otherwise hide a gap).
	for r, tools := range covered {
		if !registered[r] {
			t.Errorf("tools %v claim route %q, which buildMux does not register", tools, r)
		}
	}
	for r, why := range mcpserver.Excluded {
		if strings.TrimSpace(why) == "" {
			t.Errorf("excluded route %q has no reason", r)
		}
	}
}

// TestMCPRealMux serves the real production mux (newPostgresHandler) over the
// test database and drives it with the SDK client: initialize, tools/list, and
// a tool call that goes through requireAPI, the idempotency wrapper and audit.
func TestMCPRealMux(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	clean := func() {
		_, _ = pool.Exec(ctx, `TRUNCATE audit_log, campaigns, clicks, links, users RESTART IDENTITY CASCADE`)
	}
	clean()
	defer clean()

	cfg := &config.Config{
		BaseURL: "https://go.example.test", SessionSecret: "test-secret-test-secret-test-secret-1",
		WebAuthnRPID: "localhost", WebAuthnRPOrigin: "http://localhost", CacheMaxCost: 1000, CacheTTLSeconds: 60,
		DefaultTimezone: "UTC",
	}
	handler, _, closeFn, err := newPostgresHandler(ctx, cfg, pool)
	if err != nil {
		t.Fatalf("newPostgresHandler: %v", err)
	}
	defer closeFn()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	var uid int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, is_admin, active, created_at) VALUES ('mcp@example.com', FALSE, TRUE, now()) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	_, secret, err := auth.NewStore(pool).CreateAPIToken(ctx, auth.NewAPIToken{UserID: uid, Name: "agent"}, nil, audit.Entry{})
	if err != nil {
		t.Fatal(err)
	}

	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil)
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport(secret)},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cs.Close()

	n := 0
	for _, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if want := len(mcpserver.Describe()); n != want {
		t.Errorf("tools/list = %d tools; want %d", n, want)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_link",
		Arguments: map[string]any{"destination_url": "https://example.com/real", "idempotency_key": "k1"}})
	if err != nil || res.IsError {
		t.Fatalf("create_link: %v %+v", err, res)
	}
	var cnt int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'link.created' AND metadata->>'via' = 'mcp'`).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("audit rows via mcp = %d; want 1", cnt)
	}
	// A report tool runs on the real mux with the real service.
	rep, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_overview", Arguments: map[string]any{"range": "7d"}})
	if err != nil || rep.IsError {
		t.Fatalf("get_overview: %v %+v", err, rep)
	}
	if len(rep.Content) != 2 || rep.StructuredContent == nil {
		t.Errorf("get_overview content = %d blocks, structured = %v", len(rep.Content), rep.StructuredContent)
	}
	// A cookie-less, token-less request is refused by the real guard.
	resp, err := http.Post(srv.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /mcp = %d; want 401", resp.StatusCode)
	}
}

type bearerTransport string

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}
