package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// jsonBody wraps a JSON literal as a request body reader.
func jsonBody(s string) io.Reader { return strings.NewReader(s) }

// credsTestPool connects to TEST_DATABASE_URL or skips. It truncates the auth
// tables before and after the test so each run starts clean and leaves the DB
// clean, matching the convention in internal/auth integration tests.
func credsTestPool(t *testing.T) *pgxpool.Pool {
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
	t.Cleanup(func() {
		truncateCredsTables(t, pool)
		pool.Close()
	})
	return pool
}

func truncateCredsTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx,
		`TRUNCATE webauthn_challenges, pending_registrations, sessions,
		          passkey_credentials, audit_log, clicks, links, users
		 RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
}

// seedUser inserts an active account and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

// seedCredential inserts a passkey_credentials row and returns its id. The
// credential_id and public_key are unique non-secret test bytes; aaguid is
// stored as the canonical Apple iCloud Keychain UUID so the device hint can be
// asserted.
func seedCredential(t *testing.T, pool *pgxpool.Pool, userID int64, label, credSuffix string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	aaguid := "fbfc3007-154e-4ecc-8c0b-6e020557d7bd"
	if err := pool.QueryRow(ctx,
		`INSERT INTO passkey_credentials
		     (user_id, credential_id, public_key, aaguid, sign_count, device_name, created_at, last_used_at)
		 VALUES ($1, $2, $3, $4, 0, $5, now(), now())
		 RETURNING id`,
		userID, []byte("cred-"+credSuffix), []byte("PUBKEY-SECRET-"+credSuffix), aaguid, label,
	).Scan(&id); err != nil {
		t.Fatalf("seed credential %q: %v", label, err)
	}
	return id
}

// seedSession inserts a live session for the user and returns the raw cookie
// token. The token is the value stored directly in the cookie (see
// auth.NewSessionToken / ResolveSession), so a request carrying it passes
// RequireSession for real.
func seedSession(t *testing.T, pool *pgxpool.Pool, userID int64, token string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token, created_at, expires_at, last_seen_at)
		 VALUES ($1, $2, now(), now() + interval '30 days', now())`,
		userID, token,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// credsMux builds the real route table for the credential-management endpoints,
// guarded by RequireSession backed by the real *auth.Store. Requests therefore
// flow through the genuine session middleware, proving the routes are protected.
func credsMux(store *auth.Store) http.Handler {
	h := NewCredentialsHandler(store)
	requireSession := middleware.RequireSession(store)
	mux := http.NewServeMux()
	mux.Handle("GET /account/credentials", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("PATCH /account/credentials/{id}", requireSession(http.HandlerFunc(h.Rename)))
	mux.Handle("DELETE /account/credentials/{id}", requireSession(http.HandlerFunc(h.Revoke)))
	return mux
}

func withCookie(req *http.Request, token string) *http.Request {
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	return req
}

// credentialExists reports whether a passkey_credentials row with the given id
// is still present.
func credentialExists(t *testing.T, pool *pgxpool.Pool, id int64) bool {
	t.Helper()
	ctx := context.Background()
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM passkey_credentials WHERE id = $1)`, id,
	).Scan(&exists); err != nil {
		t.Fatalf("check credential exists: %v", err)
	}
	return exists
}

func deviceNameOf(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	ctx := context.Background()
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT device_name FROM passkey_credentials WHERE id = $1`, id,
	).Scan(&name); err != nil {
		t.Fatalf("read device_name: %v", err)
	}
	return name
}

// TestCredentialsList_ScopedToCaller seeds two users, each with credentials, and
// asserts the list returns ONLY the caller's credentials and the expected
// fields — and that the public_key is never present in the JSON.
func TestCredentialsList_ScopedToCaller(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	aliceCred1 := seedCredential(t, pool, alice, "Alice MacBook", "a1")
	aliceCred2 := seedCredential(t, pool, alice, "Alice YubiKey", "a2")
	bobCred := seedCredential(t, pool, bob, "Bob iPhone", "b1")
	_ = aliceCred1
	_ = aliceCred2

	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/account/credentials", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Decode into raw maps so we can also assert public_key is absent.
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d credentials, want 2 (only Alice's)", len(raw))
	}
	for _, c := range raw {
		// Cross-user isolation: Bob's credential id must never appear.
		if int64(c["id"].(float64)) == bobCred {
			t.Fatalf("Bob's credential leaked into Alice's list")
		}
		if _, ok := c["public_key"]; ok {
			t.Fatalf("public_key must NOT be present in the response: %v", c)
		}
		// Expected display fields present.
		for _, field := range []string{"id", "device_name", "aaguid", "device_hint", "sign_count", "created_at"} {
			if _, ok := c[field]; !ok {
				t.Errorf("missing field %q in %v", field, c)
			}
		}
		if c["device_hint"] != "iCloud Keychain" {
			t.Errorf("device_hint = %v, want iCloud Keychain", c["device_hint"])
		}
	}
}

// TestCredentialsRename_UpdatesOwn asserts a rename updates device_name in the
// DB and returns the updated record.
func TestCredentialsRename_UpdatesOwn(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	credID := seedCredential(t, pool, alice, "Old Name", "a1")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/account/credentials/"+strconv.FormatInt(credID, 10),
		jsonBody(`{"device_name":"New Name"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body credentialView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.DeviceName != "New Name" {
		t.Errorf("response device_name = %q, want New Name", body.DeviceName)
	}
	if got := deviceNameOf(t, pool, credID); got != "New Name" {
		t.Errorf("DB device_name = %q, want New Name", got)
	}
}

// TestCredentialsRename_OtherUser404 asserts renaming another user's credential
// returns 404 and does not mutate the row.
func TestCredentialsRename_OtherUser404(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	bobCred := seedCredential(t, pool, bob, "Bob iPhone", "b1")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPatch,
		srv.URL+"/account/credentials/"+strconv.FormatInt(bobCred, 10),
		jsonBody(`{"device_name":"Hijacked"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := deviceNameOf(t, pool, bobCred); got != "Bob iPhone" {
		t.Errorf("Bob's credential was mutated: device_name = %q", got)
	}
}

// TestCredentialsRevoke_NonLast asserts revoking when the user has more than one
// credential removes exactly that row.
func TestCredentialsRevoke_NonLast(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	cred1 := seedCredential(t, pool, alice, "MacBook", "a1")
	cred2 := seedCredential(t, pool, alice, "YubiKey", "a2")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/account/credentials/"+strconv.FormatInt(cred1, 10), nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if credentialExists(t, pool, cred1) {
		t.Errorf("cred1 should have been deleted")
	}
	if !credentialExists(t, pool, cred2) {
		t.Errorf("cred2 should still exist")
	}
}

// TestCredentialsRevoke_LastRefused asserts revoking the user's ONLY credential
// is refused with 409 cannot_revoke_last_credential and the row still exists.
func TestCredentialsRevoke_LastRefused(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	onlyCred := seedCredential(t, pool, alice, "Only Passkey", "a1")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/account/credentials/"+strconv.FormatInt(onlyCred, 10), nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (last credential refused)", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "cannot_revoke_last_credential" {
		t.Errorf("error = %q, want cannot_revoke_last_credential", body["error"])
	}
	if !credentialExists(t, pool, onlyCred) {
		t.Errorf("the last credential must NOT be deleted")
	}
}

// TestCredentialsRevoke_OtherUser404 asserts revoking another user's credential
// returns 404 and leaves the row intact.
func TestCredentialsRevoke_OtherUser404(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	// Bob has two credentials so this is not a last-credential case — the only
	// reason for the 404 must be ownership.
	bobCred := seedCredential(t, pool, bob, "Bob iPhone", "b1")
	seedCredential(t, pool, bob, "Bob iPad", "b2")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/account/credentials/"+strconv.FormatInt(bobCred, 10), nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if !credentialExists(t, pool, bobCred) {
		t.Errorf("Bob's credential must NOT be deleted by Alice")
	}
}

// TestCredentials_Unauthenticated asserts a request with no session cookie is
// rejected with 401 — proving the route is guarded by RequireSession.
func TestCredentials_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(credsMux(store))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/account/credentials", nil)
	resp, err := srv.Client().Do(req) // no cookie
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}
