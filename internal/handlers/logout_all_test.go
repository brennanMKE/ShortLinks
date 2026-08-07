package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// logoutAllRecordingMailer is a minimal Mailer fake for the HTTP-level #0094
// tests: it records SendSessionsRevoked calls and can be stubbed to error, so
// a test can assert the handler still returns 200 even when mail delivery
// fails. The other two Mailer methods are unused by LogoutAll and are no-ops.
type logoutAllRecordingMailer struct {
	calls int
	to    string
	err   error
}

func (m *logoutAllRecordingMailer) SendVerification(context.Context, string, string) error {
	return nil
}
func (m *logoutAllRecordingMailer) SendRecovery(context.Context, string, string) error { return nil }
func (m *logoutAllRecordingMailer) SendSessionsRevoked(_ context.Context, toEmail string, _ time.Time) error {
	m.calls++
	m.to = toEmail
	return m.err
}

// logoutAllMux wires the real POST /auth/logout/all route behind the real
// RequireSession middleware, backed by a real *auth.LoginService (audited,
// with the given mailer) over the live DB — matching how every other
// RequireSession-guarded route in this package is tested (credentials_test.go,
// me_test.go): requests flow through genuine session validation, not a fake.
func logoutAllMux(t *testing.T, pool *pgxpool.Pool, mailer auth.Mailer) http.Handler {
	t.Helper()
	store := auth.NewStore(pool)
	cfg := &config.Config{WebAuthnRPID: "localhost", WebAuthnRPOrigin: "http://localhost"}
	wa, err := auth.NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	loginSvc := auth.NewLoginService(store, wa, mailer, audit.New(pool), nil)
	h := NewAuthHandler(nil, loginSvc, nil)
	requireSession := middleware.RequireSession(store)
	mux := http.NewServeMux()
	mux.Handle("POST /auth/logout/all", requireSession(http.HandlerFunc(h.LogoutAll)))
	return mux
}

// countSessionsForUser returns the live sessions row count for userID.
func countSessionsForUser(t *testing.T, pool *pgxpool.Pool, userID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userID).Scan(&n); err != nil {
		t.Fatalf("count sessions for user %d: %v", userID, err)
	}
	return n
}

// TestLogoutAll_HTTP_RevokesAllSessionsAndClearsCookie is the primary
// end-to-end proof at the HTTP layer for #0094. Alice has two live sessions
// (two devices); a single POST /auth/logout/all with one of her cookies must:
//   - return 200 with a revoked_count of (at least) 2,
//   - clear the session cookie in the response,
//   - leave zero sessions for alice in the DB,
//   - leave bob's session (a second seeded account) untouched,
//   - reject the very next request that reuses the old cookie (401),
//   - leave alice's users/passkey_credentials rows untouched.
func TestLogoutAll_HTTP_RevokesAllSessionsAndClearsCookie(t *testing.T) {
	pool := credsTestPool(t)
	mailer := &logoutAllRecordingMailer{}
	srv := httptest.NewServer(logoutAllMux(t, pool, mailer))
	defer srv.Close()

	alice := seedUser(t, pool, "alice-everywhere@example.com")
	seedSession(t, pool, alice, "alice-token-1")
	seedSession(t, pool, alice, "alice-token-2")
	aliceCred := seedCredential(t, pool, alice, "Alice MacBook", "ae1")

	bob := seedUser(t, pool, "bob-everywhere@example.com")
	seedSession(t, pool, bob, "bob-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout/all", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token-1"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Message      string `json:"message"`
		RevokedCount int64  `json:"revoked_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RevokedCount != 2 {
		t.Errorf("revoked_count = %d, want 2", body.RevokedCount)
	}

	// Cookie must be cleared exactly like Logout.
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 (clearing) cookie, got %d", len(cookies))
	}
	if c := cookies[0]; c.Name != auth.SessionCookieName || c.MaxAge != -1 {
		t.Errorf("clearing cookie = %s MaxAge=%d, want %s MaxAge=-1", c.Name, c.MaxAge, auth.SessionCookieName)
	}

	// DB state: alice has zero sessions, bob's is untouched.
	if got := countSessionsForUser(t, pool, alice); got != 0 {
		t.Errorf("alice sessions = %d, want 0", got)
	}
	if got := countSessionsForUser(t, pool, bob); got != 1 {
		t.Errorf("bob sessions = %d, want 1 (untouched)", got)
	}

	// users and passkey_credentials rows untouched.
	if !credentialExists(t, pool, aliceCred) {
		t.Error("alice's credential must NOT be deleted by LogoutAll")
	}
	var aliceStillExists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, alice).Scan(&aliceStillExists); err != nil {
		t.Fatalf("check alice user row: %v", err)
	}
	if !aliceStillExists {
		t.Error("alice's users row must NOT be deleted by LogoutAll")
	}

	// The next request with the OLD cookie (the one just used, or the sibling
	// one) is rejected — the session is really gone, not just from the response.
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout/all", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token-2"))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status for reused old cookie = %d, want 401", resp2.StatusCode)
	}

	// Notification email was sent to alice's address.
	if mailer.calls != 1 {
		t.Errorf("SendSessionsRevoked calls = %d, want 1", mailer.calls)
	}
	if mailer.to != "alice-everywhere@example.com" {
		t.Errorf("notified address = %q, want alice-everywhere@example.com", mailer.to)
	}

	// session.revoked_all audit row, attributed to alice, with the count.
	row := lastAuditFor(t, pool, audit.ActionSessionsRevokedAll)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.Metadata["revoked_count"] != float64(2) {
		t.Errorf("metadata.revoked_count = %v, want 2", row.Metadata["revoked_count"])
	}
}

// TestLogoutAll_HTTP_Unauthenticated asserts a request with no session cookie
// is rejected 401 — proving the route is guarded by RequireSession, exactly
// like every other authenticated route in this package.
func TestLogoutAll_HTTP_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(logoutAllMux(t, pool, &logoutAllRecordingMailer{}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout/all", nil)
	resp, err := srv.Client().Do(req) // no cookie
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestLogoutAll_HTTP_MailerErrorStillReturns200 asserts that when the mailer
// fails, the HTTP response is still 200 and the session is still revoked — the
// notification is best-effort and must never surface as a request failure.
func TestLogoutAll_HTTP_MailerErrorStillReturns200(t *testing.T) {
	pool := credsTestPool(t)
	mailer := &logoutAllRecordingMailer{err: context.DeadlineExceeded}
	srv := httptest.NewServer(logoutAllMux(t, pool, mailer))
	defer srv.Close()

	alice := seedUser(t, pool, "mailer-fails-http@example.com")
	seedSession(t, pool, alice, "alice-mailer-fail-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/logout/all", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-mailer-fail-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite mailer failure", resp.StatusCode)
	}
	if got := countSessionsForUser(t, pool, alice); got != 0 {
		t.Errorf("sessions after LogoutAll = %d, want 0 (still revoked despite mailer error)", got)
	}
	if mailer.calls != 1 {
		t.Errorf("SendSessionsRevoked calls = %d, want 1 (attempted)", mailer.calls)
	}
}
