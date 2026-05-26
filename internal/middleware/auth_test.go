package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// testPool connects to TEST_DATABASE_URL or skips. It truncates the users and
// sessions tables before and after the test so runs are deterministic and leave
// no rows behind. Mirrors the helper in internal/auth.
func testPool(t *testing.T) *pgxpool.Pool {
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

	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})
	return pool
}

// truncate clears the tables this suite touches (sessions depends on users).
func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`TRUNCATE sessions, passkey_credentials, audit_log, clicks, links, users
		 RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// insertUser creates a users row and returns its id.
func insertUser(t *testing.T, pool *pgxpool.Pool, email string, isAdmin, active bool) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, $2, $3, now()) RETURNING id`,
		email, isAdmin, active,
	).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return id
}

// insertSession creates a sessions row for userID with the given token and
// expiry. created_at and last_seen_at are anchored 1 hour in the past so a
// later sliding-window bump is observably newer.
func insertSession(t *testing.T, pool *pgxpool.Pool, userID int64, token string, expiresAt time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	past := time.Now().Add(-time.Hour)
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token, created_at, expires_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $3)`,
		userID, token, past, expiresAt,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

// sessionTimes reads the current last_seen_at and expires_at for a token.
func sessionTimes(t *testing.T, pool *pgxpool.Pool, token string) (lastSeen, expires time.Time) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx,
		`SELECT last_seen_at, expires_at FROM sessions WHERE token = $1`, token,
	).Scan(&lastSeen, &expires); err != nil {
		t.Fatalf("read session times: %v", err)
	}
	return lastSeen, expires
}

func countSessions(t *testing.T, pool *pgxpool.Pool, token string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sessions WHERE token = $1`, token).Scan(&n); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return n
}

// captureHandler records whether it ran and the user it saw via UserFromContext.
type captureHandler struct {
	ran  bool
	user *AuthUser
	ok   bool
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.ran = true
	h.user, h.ok = UserFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

// reqWithCookie builds a GET request, optionally carrying the session cookie.
func reqWithCookie(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/links", nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	return r
}

func TestRequireSession_NoCookie401(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	next := &captureHandler{}
	h := RequireSession(store)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie("")) // no cookie at all

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran despite missing cookie")
	}
	if got := rec.Body.String(); got != `{"error":"unauthenticated"}` {
		t.Errorf("body = %q, want unauthenticated JSON", got)
	}
}

func TestRequireSession_UnknownToken401(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	next := &captureHandler{}
	h := RequireSession(store)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie("garbage-token-not-in-db"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran despite unknown token")
	}
}

func TestRequireSession_ExpiredSession401AndReaped(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	uid := insertUser(t, pool, "expired@example.com", false, true)
	const token = "expired-session-token"
	insertSession(t, pool, uid, token, time.Now().Add(-time.Minute)) // already expired

	next := &captureHandler{}
	h := RequireSession(store)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie(token))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran for expired session")
	}
	if n := countSessions(t, pool, token); n != 0 {
		t.Errorf("expired session rows = %d, want 0 (should be reaped)", n)
	}
}

func TestRequireSession_ValidSessionAttachesUserAndSlides(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	uid := insertUser(t, pool, "valid@example.com", false, true)
	const token = "valid-session-token"
	originalExpiry := time.Now().Add(24 * time.Hour) // valid, but well short of 30d
	insertSession(t, pool, uid, token, originalExpiry)
	beforeSeen, beforeExp := sessionTimes(t, pool, token)

	next := &captureHandler{}
	h := RequireSession(store)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie(token))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !next.ran {
		t.Fatal("next handler did not run for valid session")
	}
	if !next.ok || next.user == nil {
		t.Fatal("UserFromContext returned no user in the wrapped handler")
	}
	if next.user.ID != uid {
		t.Errorf("context user id = %d, want %d", next.user.ID, uid)
	}
	if next.user.Email != "valid@example.com" {
		t.Errorf("context user email = %q, want valid@example.com", next.user.Email)
	}
	if next.user.IsAdmin {
		t.Error("context user IsAdmin = true, want false")
	}

	// Sliding window: last_seen_at advanced and expires_at extended toward 30d.
	afterSeen, afterExp := sessionTimes(t, pool, token)
	if !afterSeen.After(beforeSeen) {
		t.Errorf("last_seen_at not bumped: before %v after %v", beforeSeen, afterSeen)
	}
	if !afterExp.After(beforeExp) {
		t.Errorf("expires_at not extended: before %v after %v", beforeExp, afterExp)
	}
	// New expiry should be ~30 days out, far beyond the original 24h.
	if afterExp.Before(time.Now().Add(29 * 24 * time.Hour)) {
		t.Errorf("expires_at = %v, want ~30 days from now (sliding window)", afterExp)
	}
}

func TestRequireSession_DeactivatedUser401(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	uid := insertUser(t, pool, "deactivated@example.com", false, false) // active = false
	const token = "deactivated-session-token"
	insertSession(t, pool, uid, token, time.Now().Add(24*time.Hour)) // session itself is valid

	next := &captureHandler{}
	h := RequireSession(store)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie(token))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran for deactivated user")
	}
}

func TestRequireAdmin_NonAdmin403_Admin200(t *testing.T) {
	pool := testPool(t)
	store := auth.NewStore(pool)

	adminID := insertUser(t, pool, "admin@example.com", true, true)
	const adminToken = "admin-session-token"
	insertSession(t, pool, adminID, adminToken, time.Now().Add(24*time.Hour))

	userID := insertUser(t, pool, "regular@example.com", false, true)
	const userToken = "regular-session-token"
	insertSession(t, pool, userID, userToken, time.Now().Add(24*time.Hour))

	// Compose the real guard chain: RequireSession then RequireAdmin.
	next := &captureHandler{}
	h := RequireSession(store)(RequireAdmin(next))

	// Non-admin → 403, next must not run.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithCookie(userToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran for non-admin behind RequireAdmin")
	}
	if got := rec.Body.String(); got != `{"error":"forbidden"}` {
		t.Errorf("non-admin body = %q, want forbidden JSON", got)
	}

	// Admin → 200, next runs and sees the admin user.
	next2 := &captureHandler{}
	h2 := RequireSession(store)(RequireAdmin(next2))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, reqWithCookie(adminToken))
	if rec2.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", rec2.Code)
	}
	if !next2.ran {
		t.Fatal("next handler did not run for admin")
	}
	if !next2.ok || next2.user == nil || !next2.user.IsAdmin {
		t.Errorf("admin context user = %+v, ok=%v, want IsAdmin user", next2.user, next2.ok)
	}
}

// TestRequireAdmin_NoSession401 confirms RequireAdmin alone (no upstream session
// on the context) denies with 401 rather than panicking or 403.
func TestRequireAdmin_NoSession401(t *testing.T) {
	next := &captureHandler{}
	h := RequireAdmin(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran with no session on context")
	}
}
