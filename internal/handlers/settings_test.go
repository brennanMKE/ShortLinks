package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// settingsTestPool connects to TEST_DATABASE_URL or skips. It truncates the auth
// tables AND resets the settings table to its seeded state before and after the
// test, so each run starts clean, the registrations_enabled gate begins
// disabled (matching the migration seed), and the DB is left clean. The settings
// table is not covered by the credentials suite's truncate, so it is reset
// explicitly here.
func settingsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncateCredsTables(t, pool)
	resetSettings(t, pool)
	t.Cleanup(func() {
		truncateCredsTables(t, pool)
		resetSettings(t, pool)
		pool.Close()
	})
	return pool
}

// resetSettings restores the settings table to exactly the migration seed:
// a single registrations_enabled=false row. This isolates each settings test
// from values left by earlier tests (the auth suite flips this row) and leaves
// the DB in the seeded state on teardown.
func resetSettings(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `DELETE FROM settings`); err != nil {
		t.Fatalf("clear settings: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value, updated_at)
		 VALUES ('registrations_enabled', 'false', now())`); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
}

// seedAdmin inserts an admin account and returns its id.
func seedAdmin(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, TRUE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed admin %s: %v", email, err)
	}
	return id
}

// settingValue reads the current value of a settings key.
func settingValue(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	ctx := context.Background()
	var v string
	if err := pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE key = $1`, key).Scan(&v); err != nil {
		t.Fatalf("read setting %q: %v", key, err)
	}
	return v
}

// settingUpdatedAt reads the current updated_at of a settings key.
func settingUpdatedAt(t *testing.T, pool *pgxpool.Pool, key string) time.Time {
	t.Helper()
	ctx := context.Background()
	var ts time.Time
	if err := pool.QueryRow(ctx,
		`SELECT updated_at FROM settings WHERE key = $1`, key).Scan(&ts); err != nil {
		t.Fatalf("read setting updated_at %q: %v", key, err)
	}
	return ts
}

// adminMux builds the real admin settings route table guarded by RequireSession
// then RequireAdmin, backed by the real *auth.Store. Requests therefore flow
// through the genuine session + admin middleware, proving the routes are
// protected exactly as wired in main.go.
func adminMux(store *auth.Store) http.Handler {
	h := NewSettingsHandler(store)
	requireSession := middleware.RequireSession(store)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/settings", requireAdmin(http.HandlerFunc(h.List)))
	mux.Handle("PATCH /admin/settings", requireAdmin(http.HandlerFunc(h.Patch)))
	return mux
}

// decodeSettings parses a settingsResponse body keyed by setting key.
func decodeSettings(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var resp struct {
		Settings []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode settings: %v (body=%s)", err, body)
	}
	out := make(map[string]string, len(resp.Settings))
	for _, s := range resp.Settings {
		out[s.Key] = s.Value
	}
	return out
}

// TestAdminSettings_NonAdminForbidden asserts a non-admin user with a VALID
// session is rejected with 403 on both GET and PATCH — proving RequireAdmin
// guards the routes and is reached only after RequireSession succeeds.
func TestAdminSettings_NonAdminForbidden(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	user := seedUser(t, pool, "regular@example.com") // is_admin = FALSE
	seedSession(t, pool, user, "user-token")

	// GET → 403
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/settings", nil)
	getResp, err := srv.Client().Do(withCookie(getReq, "user-token"))
	if err != nil {
		t.Fatalf("GET request: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET non-admin status = %d, want 403", getResp.StatusCode)
	}

	// PATCH → 403, and the row must not change.
	patchReq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"true"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := srv.Client().Do(withCookie(patchReq, "user-token"))
	if err != nil {
		t.Fatalf("PATCH request: %v", err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusForbidden {
		t.Fatalf("PATCH non-admin status = %d, want 403", patchResp.StatusCode)
	}
	if got := settingValue(t, pool, "registrations_enabled"); got != "false" {
		t.Errorf("registrations_enabled = %q after forbidden PATCH, want unchanged false", got)
	}
}

// TestAdminSettings_Unauthenticated asserts a request with no session cookie is
// rejected with 401 on both routes — proving the session guard runs first.
func TestAdminSettings_Unauthenticated(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/settings", nil)
	getResp, err := srv.Client().Do(getReq) // no cookie
	if err != nil {
		t.Fatalf("GET request: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET unauthenticated status = %d, want 401", getResp.StatusCode)
	}

	patchReq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"true"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := srv.Client().Do(patchReq) // no cookie
	if err != nil {
		t.Fatalf("PATCH request: %v", err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("PATCH unauthenticated status = %d, want 401", patchResp.StatusCode)
	}
}

// TestAdminSettings_GetReturnsSeeded asserts an admin GET returns the seeded
// registrations_enabled=false in the {"settings":[...]} shape.
func TestAdminSettings_GetReturnsSeeded(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/settings", nil)
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	settings := decodeSettings(t, body)
	if settings["registrations_enabled"] != "false" {
		t.Errorf("registrations_enabled = %q, want false", settings["registrations_enabled"])
	}
}

// TestAdminSettings_PatchUpdatesRow asserts an admin PATCH flips
// registrations_enabled to true: the DB value changes, updated_at advances, and
// the response carries the new value.
func TestAdminSettings_PatchUpdatesRow(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	before := settingUpdatedAt(t, pool, "registrations_enabled")
	// Ensure a measurable clock tick so the updated_at advance is observable.
	time.Sleep(2 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"true"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	settings := decodeSettings(t, body)
	if settings["registrations_enabled"] != "true" {
		t.Errorf("response registrations_enabled = %q, want true", settings["registrations_enabled"])
	}
	if got := settingValue(t, pool, "registrations_enabled"); got != "true" {
		t.Errorf("DB registrations_enabled = %q, want true", got)
	}
	if after := settingUpdatedAt(t, pool, "registrations_enabled"); !after.After(before) {
		t.Errorf("updated_at not advanced: before %v after %v", before, after)
	}
}

// TestAdminSettings_PatchInvalidValue asserts a non-boolean value for
// registrations_enabled is rejected with 400 and the row is left unchanged.
func TestAdminSettings_PatchInvalidValue(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"maybe"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := settingValue(t, pool, "registrations_enabled"); got != "false" {
		t.Errorf("registrations_enabled = %q after invalid PATCH, want unchanged false", got)
	}
}

// TestAdminSettings_PatchUnknownKey asserts a key absent from the settings table
// is rejected with 400 (no arbitrary key creation) and no new row is inserted.
func TestAdminSettings_PatchUnknownKey(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(adminMux(store))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"some_unknown_key","value":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	ctx := context.Background()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM settings WHERE key = 'some_unknown_key')`).Scan(&exists); err != nil {
		t.Fatalf("check unknown key: %v", err)
	}
	if exists {
		t.Error("an unknown key was created by PATCH; arbitrary key creation must be forbidden")
	}
}

// TestAdminSettings_PatchOpensRegistrationGate is the end-to-end tie-in: with the
// real registration handler mounted alongside the admin settings routes, a
// POST /auth/register/start is 403 (Registration closed) while
// registrations_enabled=false, then — after an admin PATCH sets it to true —
// the SAME request is no longer 403. This proves the registration gate reads the
// setting fresh from the DB on each attempt, so an admin toggle takes effect
// immediately without a restart.
func TestAdminSettings_PatchOpensRegistrationGate(t *testing.T) {
	pool := settingsTestPool(t)
	store := auth.NewStore(pool)

	// Wire the real registration service into the real AuthHandler so
	// /auth/register/start runs the genuine RegistrationsEnabled DB read.
	cfg := &config.Config{
		WebAuthnRPID:     "go.sstools.co",
		WebAuthnRPOrigin: "https://go.sstools.co",
	}
	wa, err := auth.NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	regSvc := auth.NewRegistrationService(store, wa, &gateMailer{}, cfg)
	authH := NewAuthHandler(regSvc, nil, nil)

	settingsH := NewSettingsHandler(store)
	requireSession := middleware.RequireSession(store)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/register/start", authH.RegisterStart)
	mux.Handle("PATCH /admin/settings", requireAdmin(http.HandlerFunc(settingsH.Patch)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	// 1. Gate closed (seeded false): register/start must be 403.
	startReq1, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/register/start",
		jsonBody(`{"email":"newuser@example.com"}`))
	startReq1.Header.Set("Content-Type", "application/json")
	startResp1, err := srv.Client().Do(startReq1)
	if err != nil {
		t.Fatalf("register/start (closed): %v", err)
	}
	startResp1.Body.Close()
	if startResp1.StatusCode != http.StatusForbidden {
		t.Fatalf("register/start while closed: status = %d, want 403", startResp1.StatusCode)
	}

	// 2. Admin opens registration.
	patchReq, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"true"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := srv.Client().Do(withCookie(patchReq, "admin-token"))
	if err != nil {
		t.Fatalf("PATCH open registration: %v", err)
	}
	patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH open registration: status = %d, want 200", patchResp.StatusCode)
	}

	// 3. Same register/start request must NO LONGER be 403 — the gate read the
	// new value immediately. (200 generic success here; the email is unknown so
	// the flow proceeds past the gate.)
	startReq2, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/register/start",
		jsonBody(`{"email":"newuser@example.com"}`))
	startReq2.Header.Set("Content-Type", "application/json")
	startResp2, err := srv.Client().Do(startReq2)
	if err != nil {
		t.Fatalf("register/start (open): %v", err)
	}
	startResp2.Body.Close()
	if startResp2.StatusCode == http.StatusForbidden {
		t.Fatalf("register/start still 403 after opening the gate; the setting was not read fresh")
	}
	if startResp2.StatusCode != http.StatusOK {
		t.Fatalf("register/start after open: status = %d, want 200", startResp2.StatusCode)
	}
}

// gateMailer is a no-op Mailer for the gate tie-in test: the registration flow
// reaches the mailer only once it passes the registrations_enabled gate, so a
// successful send confirms the gate opened.
type gateMailer struct{}

func (gateMailer) SendVerification(context.Context, string, string) error { return nil }
func (gateMailer) SendRecovery(context.Context, string, string) error      { return nil }
