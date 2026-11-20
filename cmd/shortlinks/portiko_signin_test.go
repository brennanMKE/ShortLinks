package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
)

// TestPortikoSignInSwitch checks the production route table with PORTIKO_ENABLED
// off and on (#0132). Off, today's passkey routes answer as before and the
// Portiko ones are not served. On, the passkey routes are gone, the Portiko
// sign-in routes are live, and a protected route answers 401 without a session.
// The full sign-in round trip is internal/idp's TestPortikoSignInRoundTrip.
func TestPortikoSignInSwitch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	drop := func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS portiko CASCADE`) }
	drop()
	defer drop()

	boot := func(enabled bool) *httptest.Server {
		srv := httptest.NewUnstartedServer(nil)
		origin := "http://" + srv.Listener.Addr().String()
		cfg := &config.Config{
			BaseURL: origin, DatabaseURL: dsn, SessionSecret: "test-secret-test-secret-test-secret-1",
			WebAuthnRPID: "localhost", WebAuthnRPOrigin: origin, CacheMaxCost: 1000, CacheTTLSeconds: 60,
			DefaultTimezone: "UTC", PortikoEnabled: enabled,
		}
		if enabled {
			cfg.PortikoClientSecret = "0123456789abcdef0123"
			cfg.PortikoCryptoKey = [32]byte{4, 4, 4, 4, 1}
		}
		h, _, closeFn, err := newPostgresHandler(ctx, cfg, pool)
		if err != nil {
			t.Fatalf("newPostgresHandler(enabled=%v): %v", enabled, err)
		}
		t.Cleanup(closeFn)
		srv.Config.Handler = h
		srv.Start()
		t.Cleanup(srv.Close)
		return srv
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(srv *httptest.Server, method, path string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(""))
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	off := boot(false)
	if code, _, _ := do(off, "GET", "/auth/login/start"); code != http.StatusOK {
		t.Errorf("switch off: GET /auth/login/start = %d, want 200 (today's passkey login)", code)
	}
	if code, _, h := do(off, "GET", "/auth/portiko/login"); code == http.StatusFound && strings.Contains(h.Get("Location"), "/id/") {
		t.Errorf("switch off: /auth/portiko/login redirected to the provider")
	}
	if code, _, _ := do(off, "GET", "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("switch off: /api/me = %d, want 401", code)
	}

	on := boot(true)
	if code, _, _ := do(on, "GET", "/auth/login/start"); code != http.StatusNotFound {
		t.Errorf("switch on: GET /auth/login/start = %d, want 404", code)
	}
	for _, p := range []string{"/auth/register/start", "/auth/recover"} {
		if code, _, _ := do(on, "POST", p); code != http.StatusNotFound {
			t.Errorf("switch on: POST %s = %d, want 404", p, code)
		}
	}
	code, _, h := do(on, "GET", "/auth/portiko/login?return_to=/x")
	if loc := h.Get("Location"); code != http.StatusFound || !strings.Contains(loc, "/id/oauth/authorize") {
		t.Errorf("switch on: /auth/portiko/login = %d %q, want a redirect to /id/oauth/authorize", code, loc)
	}
	if code, body, _ := do(on, "GET", "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("switch on: /api/me without a session = %d %s, want 401", code, body)
	}
	if code, _, _ := do(on, "GET", "/admin/users"); code != http.StatusUnauthorized {
		t.Errorf("switch on: /admin/users without a session = %d, want 401", code)
	}
	if code, _, _ := do(on, "POST", "/auth/logout/all"); code != http.StatusUnauthorized {
		t.Errorf("switch on: POST /auth/logout/all without a session = %d, want 401", code)
	}
	if code, _, _ := do(on, "GET", "/health"); code != http.StatusOK {
		t.Errorf("switch on: /health = %d", code)
	}
}
