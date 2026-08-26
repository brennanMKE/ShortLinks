package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// meMux builds the real route table for GET /api/me, guarded by RequireSession
// backed by the real *auth.Store, so requests flow through the genuine session
// middleware — proving the route is protected and the context user is real.
func meMux(store *auth.Store) http.Handler {
	h := NewMeHandler(testBaseURL)
	requireSession := middleware.RequireSession(store)
	mux := http.NewServeMux()
	mux.Handle("GET /api/me", requireSession(http.HandlerFunc(h.Me)))
	return mux
}

// seedAdminUser inserts an active admin account and returns its id. seedUser
// (in credentials_test.go) always inserts is_admin=FALSE, so admin coverage
// needs its own seed.
func seedAdminUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, TRUE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed admin user %s: %v", email, err)
	}
	return id
}

// TestMe_AdminUser asserts an authenticated admin gets 200 with the correct
// id/email and is_admin:true.
func TestMe_AdminUser(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(meMux(store))
	defer srv.Close()

	adminID := seedAdminUser(t, pool, "admin@example.com")
	seedSession(t, pool, adminID, "admin-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		ID      int64  `json:"id"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != adminID {
		t.Errorf("id = %d, want %d", body.ID, adminID)
	}
	if body.Email != "admin@example.com" {
		t.Errorf("email = %q, want admin@example.com", body.Email)
	}
	if !body.IsAdmin {
		t.Errorf("is_admin = false, want true for admin user")
	}
	if body.BaseURL != testBaseURL {
		t.Errorf("base_url = %q, want %q", body.BaseURL, testBaseURL)
	}
}

// TestMe_NormalUser asserts an authenticated non-admin gets 200 with the correct
// id/email and is_admin:false.
func TestMe_NormalUser(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(meMux(store))
	defer srv.Close()

	userID := seedUser(t, pool, "user@example.com")
	seedSession(t, pool, userID, "user-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	resp, err := srv.Client().Do(withCookie(req, "user-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		ID      int64  `json:"id"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != userID {
		t.Errorf("id = %d, want %d", body.ID, userID)
	}
	if body.Email != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", body.Email)
	}
	if body.IsAdmin {
		t.Errorf("is_admin = true, want false for normal user")
	}
	// #0117: the SPA builds every displayed short URL from this, so the
	// non-admin profile must carry it too — it is not an admin-only field.
	if body.BaseURL != testBaseURL {
		t.Errorf("base_url = %q, want %q", body.BaseURL, testBaseURL)
	}
}

// TestMe_Unauthenticated asserts a request with no session cookie is rejected
// with 401 — proving the route is guarded by RequireSession.
func TestMe_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	srv := httptest.NewServer(meMux(store))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	resp, err := srv.Client().Do(req) // no cookie
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestMe_BaseURLTrailingSlashTrimmed asserts a BASE_URL configured with a
// trailing slash is normalized before it reaches the SPA (#0117). The SPA
// concatenates "/u/{key}" onto this value, so an untrimmed slash would produce
// "https://example.test//u/abc" — a URL that still resolves but disagrees,
// character for character, with the QR payload internal/qr encodes for the
// same link.
func TestMe_BaseURLTrailingSlashTrimmed(t *testing.T) {
	pool := credsTestPool(t)
	store := auth.NewStore(pool)
	h := NewMeHandler("https://example.test/")
	requireSession := middleware.RequireSession(store)
	mux := http.NewServeMux()
	mux.Handle("GET /api/me", requireSession(http.HandlerFunc(h.Me)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	userID := seedUser(t, pool, "slash@example.com")
	seedSession(t, pool, userID, "slash-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/me", nil)
	resp, err := srv.Client().Do(withCookie(req, "slash-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "https://example.test"; body.BaseURL != want {
		t.Errorf("base_url = %q, want %q", body.BaseURL, want)
	}
}
