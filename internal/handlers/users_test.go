package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// adminUsersMux wires the real admin user-management routes guarded by
// RequireSession then RequireAdmin, backed by the real *auth.Store and a real
// audit.Logger. Requests therefore flow through the genuine session + admin
// middleware (proving the routes are protected exactly as wired in main.go) and
// the deactivate/reactivate audit rows are written for real.
func adminUsersMux(pool *pgxpool.Pool) http.Handler {
	store := auth.NewStore(pool)
	h := NewAdminUsersHandler(store, audit.New(pool))
	requireSession := middleware.RequireSession(store)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/users", requireAdmin(http.HandlerFunc(h.List)))
	mux.Handle("GET /admin/users/{id}", requireAdmin(http.HandlerFunc(h.Get)))
	mux.Handle("POST /admin/users/{id}/deactivate", requireAdmin(http.HandlerFunc(h.Deactivate)))
	mux.Handle("POST /admin/users/{id}/reactivate", requireAdmin(http.HandlerFunc(h.Reactivate)))
	return mux
}

// userActive reads the current active flag of a users row.
func userActive(t *testing.T, pool *pgxpool.Pool, id int64) bool {
	t.Helper()
	var active bool
	if err := pool.QueryRow(context.Background(),
		`SELECT active FROM users WHERE id = $1`, id).Scan(&active); err != nil {
		t.Fatalf("read user active %d: %v", id, err)
	}
	return active
}

// sessionCountFor reports how many sessions rows exist for a user.
func sessionCountFor(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count sessions for %d: %v", id, err)
	}
	return n
}

// linkCountFor reports how many links rows exist for a user.
func linkCountFor(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM links WHERE user_id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count links for %d: %v", id, err)
	}
	return n
}

// TestAdminUsers_NonAdminForbidden asserts a non-admin with a VALID session is
// rejected with 403 on all four routes — proving RequireAdmin guards them and is
// reached only after RequireSession succeeds.
func TestAdminUsers_NonAdminForbidden(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	user := seedUser(t, pool, "regular@example.com") // is_admin = FALSE
	target := seedUser(t, pool, "target@example.com")
	seedSession(t, pool, user, "user-token")

	cases := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodGet, "/admin/users/" + itoa(target), ""},
		{http.MethodPost, "/admin/users/" + itoa(target) + "/deactivate", `{"reason":"spam"}`},
		{http.MethodPost, "/admin/users/" + itoa(target) + "/reactivate", `{}`},
	}
	for _, c := range cases {
		var req *http.Request
		if c.body != "" {
			req, _ = http.NewRequest(c.method, srv.URL+c.path, jsonBody(c.body))
		} else {
			req, _ = http.NewRequest(c.method, srv.URL+c.path, nil)
		}
		resp, err := srv.Client().Do(withCookie(req, "user-token"))
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s non-admin status = %d, want 403", c.method, c.path, resp.StatusCode)
		}
	}
	// The target must still be active — no forbidden request mutated state.
	if !userActive(t, pool, target) {
		t.Errorf("target was deactivated by a forbidden request")
	}
}

// TestAdminUsers_Unauthenticated asserts requests with no session cookie get 401
// on all four routes — proving the session guard runs first.
func TestAdminUsers_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	target := seedUser(t, pool, "target@example.com")

	cases := []struct {
		method, path string
		body         string
	}{
		{http.MethodGet, "/admin/users", ""},
		{http.MethodGet, "/admin/users/" + itoa(target), ""},
		{http.MethodPost, "/admin/users/" + itoa(target) + "/deactivate", `{"reason":"spam"}`},
		{http.MethodPost, "/admin/users/" + itoa(target) + "/reactivate", `{}`},
	}
	for _, c := range cases {
		var req *http.Request
		if c.body != "" {
			req, _ = http.NewRequest(c.method, srv.URL+c.path, jsonBody(c.body))
		} else {
			req, _ = http.NewRequest(c.method, srv.URL+c.path, nil)
		}
		resp, err := srv.Client().Do(req) // no cookie
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated status = %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestAdminUsers_ListReturnsSeeded asserts an admin GET /admin/users returns the
// seeded accounts with status (active) and last-login fields in the
// {"users":[...]} shape.
func TestAdminUsers_ListReturnsSeeded(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/users", nil)
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Users []struct {
			ID          int64   `json:"id"`
			Email       string  `json:"email"`
			IsAdmin     bool    `json:"is_admin"`
			Active      bool    `json:"active"`
			CreatedAt   string  `json:"created_at"`
			LastLoginAt *string `json:"last_login_at"`
		} `json:"users"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Users) != 2 {
		t.Fatalf("got %d users, want 2", len(body.Users))
	}
	byID := map[int64]bool{} // id -> is_admin
	for _, u := range body.Users {
		byID[u.ID] = u.IsAdmin
		if u.CreatedAt == "" {
			t.Errorf("user %d missing created_at", u.ID)
		}
		// Neither seeded user has logged in: last_login_at must be absent/null.
		if u.LastLoginAt != nil {
			t.Errorf("user %d last_login_at = %v, want null", u.ID, *u.LastLoginAt)
		}
		if !u.Active {
			t.Errorf("seeded user %d should be active", u.ID)
		}
	}
	if !byID[admin] {
		t.Errorf("admin %d not flagged is_admin in list", admin)
	}
	if byID[alice] {
		t.Errorf("alice %d wrongly flagged is_admin", alice)
	}
}

// TestAdminUsers_DetailAndNotFound asserts the detail route returns the account
// with link/passkey counts, and 404 for a missing id.
func TestAdminUsers_DetailAndNotFound(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")
	seedLink(t, pool, alice, "lk0001", "https://a.example.com")
	seedLink(t, pool, alice, "lk0002", "https://b.example.com")
	seedCredential(t, pool, alice, "Alice MacBook", "a1")

	// Detail for an existing user.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/users/"+itoa(alice), nil)
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("detail request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", resp.StatusCode)
	}
	var detail struct {
		ID           int64  `json:"id"`
		Email        string `json:"email"`
		LinkCount    int64  `json:"link_count"`
		PasskeyCount int64  `json:"passkey_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != alice || detail.Email != "alice@example.com" {
		t.Errorf("detail id/email = %d/%q, want %d/alice@example.com", detail.ID, detail.Email, alice)
	}
	if detail.LinkCount != 2 {
		t.Errorf("link_count = %d, want 2", detail.LinkCount)
	}
	if detail.PasskeyCount != 1 {
		t.Errorf("passkey_count = %d, want 1", detail.PasskeyCount)
	}

	// 404 for a nonexistent id.
	missReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/users/999999", nil)
	missResp, err := srv.Client().Do(withCookie(missReq, "admin-token"))
	if err != nil {
		t.Fatalf("miss request: %v", err)
	}
	defer missResp.Body.Close()
	if missResp.StatusCode != http.StatusNotFound {
		t.Errorf("missing-id status = %d, want 404", missResp.StatusCode)
	}
}

// TestAdminUsers_DeactivateNormalUser is the core proof: deactivating a normal
// user flips active to false, DELETES all of that user's sessions (2 seeded → 0
// after), leaves their links intact, and writes an account.deactivated audit row
// with {reason, note} attributed to the admin and affecting the target.
func TestAdminUsers_DeactivateNormalUser(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")
	// Two live sessions for alice + a link that must survive deactivation.
	seedSession(t, pool, alice, "alice-token-1")
	seedSession(t, pool, alice, "alice-token-2")
	seedLink(t, pool, alice, "keepme", "https://keep.example.com")

	if got := sessionCountFor(t, pool, alice); got != 2 {
		t.Fatalf("precondition: alice should have 2 sessions, got %d", got)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(alice)+"/deactivate",
		jsonBody(`{"reason":"spam","note":"too many junk links"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var updated userView
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Active {
		t.Errorf("response active = true, want false")
	}

	// active flipped in the DB.
	if userActive(t, pool, alice) {
		t.Errorf("alice still active in DB after deactivate")
	}
	// ALL of alice's sessions deleted.
	if got := sessionCountFor(t, pool, alice); got != 0 {
		t.Errorf("alice sessions = %d after deactivate, want 0 (all deleted)", got)
	}
	// The admin's own session is untouched.
	if got := sessionCountFor(t, pool, admin); got != 1 {
		t.Errorf("admin sessions = %d, want 1 (admin must not be logged out)", got)
	}
	// Links remain.
	if got := linkCountFor(t, pool, alice); got != 1 {
		t.Errorf("alice links = %d after deactivate, want 1 (links must remain)", got)
	}

	// account.deactivated audit row with the right shape.
	row := lastAuditFor(t, pool, audit.ActionAccountDeactivated)
	if row.ActorID == nil || *row.ActorID != admin {
		t.Errorf("audit actor_id = %v, want admin %d", row.ActorID, admin)
	}
	if row.UserID == nil || *row.UserID != alice {
		t.Errorf("audit user_id = %v, want target %d", row.UserID, alice)
	}
	if row.TargetType == nil || *row.TargetType != audit.TargetUser {
		t.Errorf("audit target_type = %v, want %q", row.TargetType, audit.TargetUser)
	}
	if row.TargetID == nil || *row.TargetID != alice {
		t.Errorf("audit target_id = %v, want %d", row.TargetID, alice)
	}
	if row.Metadata["reason"] != "spam" || row.Metadata["note"] != "too many junk links" {
		t.Errorf("audit metadata = %v, want reason=spam note=...", row.Metadata)
	}
}

// TestAdminUsers_DeactivateInvalidReason asserts an unknown reason is rejected
// with 400 and the user is left active.
func TestAdminUsers_DeactivateInvalidReason(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(alice)+"/deactivate",
		jsonBody(`{"reason":"because_i_said_so"}`))
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !userActive(t, pool, alice) {
		t.Errorf("alice was deactivated despite an invalid reason")
	}
}

// TestAdminUsers_DeactivateOtherRequiresNote asserts reason=other with an empty
// note is rejected with 400 and the user stays active.
func TestAdminUsers_DeactivateOtherRequiresNote(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")

	// other + empty note → 400.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(alice)+"/deactivate",
		jsonBody(`{"reason":"other"}`))
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("other-without-note status = %d, want 400", resp.StatusCode)
	}
	if !userActive(t, pool, alice) {
		t.Errorf("alice was deactivated despite missing required note")
	}

	// other + note → 200 (proves the note requirement is the only blocker).
	okReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(alice)+"/deactivate",
		jsonBody(`{"reason":"other","note":"manual review"}`))
	okResp, err := srv.Client().Do(withCookie(okReq, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer okResp.Body.Close()
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("other-with-note status = %d, want 200", okResp.StatusCode)
	}
	if userActive(t, pool, alice) {
		t.Errorf("alice should be deactivated after a valid other+note request")
	}
}

// TestAdminUsers_DeactivateAdminRefused asserts deactivating an ADMIN target is
// refused (403) and that target admin stays active.
func TestAdminUsers_DeactivateAdminRefused(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	otherAdmin := seedAdmin(t, pool, "other-admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(otherAdmin)+"/deactivate",
		jsonBody(`{"reason":"terms_violation"}`))
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (admin target refused)", resp.StatusCode)
	}
	if !userActive(t, pool, otherAdmin) {
		t.Errorf("the admin target must NOT be deactivated")
	}
}

// TestAdminUsers_Reactivate asserts reactivate flips active back to true and
// writes account.reactivated with {note}; sessions stay gone (none are restored).
func TestAdminUsers_Reactivate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminUsersMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")

	// Start alice deactivated (active=false), with no sessions.
	if _, err := pool.Exec(context.Background(),
		`UPDATE users SET active = FALSE WHERE id = $1`, alice); err != nil {
		t.Fatalf("pre-deactivate alice: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/users/"+itoa(alice)+"/reactivate",
		jsonBody(`{"note":"appeal granted"}`))
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var updated userView
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !updated.Active {
		t.Errorf("response active = false, want true")
	}
	if !userActive(t, pool, alice) {
		t.Errorf("alice not active in DB after reactivate")
	}
	// No session restoration.
	if got := sessionCountFor(t, pool, alice); got != 0 {
		t.Errorf("alice sessions = %d after reactivate, want 0 (sessions stay deleted)", got)
	}

	row := lastAuditFor(t, pool, audit.ActionAccountReactivated)
	if row.ActorID == nil || *row.ActorID != admin {
		t.Errorf("audit actor_id = %v, want admin %d", row.ActorID, admin)
	}
	if row.UserID == nil || *row.UserID != alice {
		t.Errorf("audit user_id = %v, want target %d", row.UserID, alice)
	}
	if row.Metadata["note"] != "appeal granted" {
		t.Errorf("audit metadata.note = %v, want 'appeal granted'", row.Metadata["note"])
	}
}
