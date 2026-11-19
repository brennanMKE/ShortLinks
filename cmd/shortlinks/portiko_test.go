package main

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

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
)

// TestPortikoEmbeddedUnderID boots the real production mux with the Portiko
// provider mounted under /id (#0130) and checks that ShortLinks' own routes
// (/health, /u/{key}) are unaffected, that discovery serves under /id with the
// issuer BASE_URL/id, and that with the switch off nothing of Portiko's is
// served and its schema is never created.
func TestPortikoEmbeddedUnderID(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	dropSchema := func() { _, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS portiko CASCADE`) }
	cleanLinks := func() { _, _ = pool.Exec(ctx, `TRUNCATE links, users RESTART IDENTITY CASCADE`) }
	dropSchema()
	cleanLinks()
	defer dropSchema()
	defer cleanLinks()

	var uid int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, is_admin, active, created_at) VALUES ('idp@example.com', FALSE, TRUE, now()) RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO links (user_id, key, destination_url) VALUES ($1, 'idptest1', 'https://example.com/dest')`, uid); err != nil {
		t.Fatal(err)
	}

	newCfg := func(enabled bool) *config.Config {
		cfg := &config.Config{
			BaseURL: "http://localhost:8211", DatabaseURL: dsn, SessionSecret: "test-secret-test-secret-test-secret-1",
			WebAuthnRPID: "localhost", WebAuthnRPOrigin: "http://localhost:8211", CacheMaxCost: 1000, CacheTTLSeconds: 60,
			DefaultTimezone: "UTC", PortikoEnabled: enabled,
		}
		if enabled {
			cfg.PortikoClientSecret = "0123456789abcdef0123"
			cfg.PortikoCryptoKey = [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
		}
		return cfg
	}
	serve := func(enabled bool) *httptest.Server {
		h, _, closeFn, err := newPostgresHandler(ctx, newCfg(enabled), pool)
		if err != nil {
			t.Fatalf("newPostgresHandler(enabled=%v): %v", enabled, err)
		}
		t.Cleanup(closeFn)
		return httptest.NewServer(h)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(srv *httptest.Server, path string) (int, string, http.Header) {
		resp, err := noFollow.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}
	schemaExists := func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'portiko'`).Scan(&n)
		return n == 1
	}

	var versionBefore int64 // zero when the test DB was not migrated by golang-migrate
	_ = pool.QueryRow(ctx, `SELECT version FROM public.schema_migrations`).Scan(&versionBefore)

	// Switch off: no provider, no schema.
	off := serve(false)
	defer off.Close()
	if code, body, hdr := get(off, "/id/.well-known/openid-configuration"); strings.Contains(body, "issuer") || strings.Contains(hdr.Get("Content-Type"), "json") {
		t.Errorf("provider answered with the switch off: %d %q", code, body)
	}
	if code, body, _ := get(off, "/id/health"); strings.Contains(body, `"status":"ok"`) {
		t.Errorf("/id/health answered with the switch off: %d %q", code, body)
	}
	if schemaExists() {
		t.Error("portiko schema was created with the switch off")
	}

	// Switch on.
	on := serve(true)
	defer on.Close()
	if !schemaExists() {
		t.Error("portiko schema was not created")
	}
	// The provider keeps its own schema_migrations in its own schema; the one in
	// public (golang-migrate's, when the test DB was migrated with it) must still
	// hold ShortLinks' version, not Portiko's.
	var inPortiko int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'portiko' AND table_name = 'schema_migrations'`).Scan(&inPortiko)
	if inPortiko != 1 {
		t.Error("portiko.schema_migrations does not exist")
	}
	if versionBefore != 0 {
		var after int64
		_ = pool.QueryRow(ctx, `SELECT version FROM public.schema_migrations`).Scan(&after)
		if after != versionBefore {
			t.Errorf("public.schema_migrations version %d -> %d", versionBefore, after)
		}
	}

	code, body, _ := get(on, "/id/.well-known/openid-configuration")
	var disc struct {
		Issuer string `json:"issuer"`
	}
	if code != http.StatusOK || json.Unmarshal([]byte(body), &disc) != nil || disc.Issuer != "http://localhost:8211/id" {
		t.Errorf("discovery = %d %q; want issuer http://localhost:8211/id", code, body)
	}
	if code, body, _ := get(on, "/id/health"); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("/id/health = %d %q", code, body)
	}
	if code, body, _ := get(on, "/id/signin"); code != http.StatusOK || !strings.Contains(body, "ShortLinks") {
		t.Errorf("/id/signin = %d (want the ShortLinks sign-in page)", code)
	}

	// ShortLinks' own routes are unchanged, on and off.
	for name, srv := range map[string]*httptest.Server{"off": off, "on": on} {
		if code, body, _ := get(srv, "/health"); code != http.StatusOK || strings.Contains(body, "issuer") {
			t.Errorf("%s: /health = %d %q", name, code, body)
		}
		if code, _, hdr := get(srv, "/u/idptest1"); code != http.StatusFound || hdr.Get("Location") != "https://example.com/dest" {
			t.Errorf("%s: /u/idptest1 = %d Location=%q", name, code, hdr.Get("Location"))
		}
	}
	// A path that merely starts with "id" is not the provider's.
	if _, body, _ := get(on, "/idx"); strings.Contains(body, `"status":"ok"`) {
		t.Error("/idx reached the provider")
	}

	// A provider that cannot start must not take the instance down: reopen the
	// same schema with a different crypto key, which cannot unseal the stored
	// signing key. ShortLinks' routes keep working and /id answers 503.
	badCfg := newCfg(true)
	badCfg.PortikoCryptoKey = [32]byte{0xde, 0xad}
	h, _, closeBad, err := newPostgresHandler(ctx, badCfg, pool)
	if err != nil {
		t.Fatalf("a failing provider must not fail newPostgresHandler: %v", err)
	}
	defer closeBad()
	bad := httptest.NewServer(h)
	defer bad.Close()
	if code, _, _ := get(bad, "/id/health"); code != http.StatusServiceUnavailable {
		t.Errorf("/id/health with a failed provider = %d; want 503", code)
	}
	if code, _, _ := get(bad, "/health"); code != http.StatusOK {
		t.Errorf("/health with a failed provider = %d; want 200", code)
	}
	if code, _, hdr := get(bad, "/u/idptest1"); code != http.StatusFound || hdr.Get("Location") != "https://example.com/dest" {
		t.Errorf("/u/idptest1 with a failed provider = %d Location=%q", code, hdr.Get("Location"))
	}
}
