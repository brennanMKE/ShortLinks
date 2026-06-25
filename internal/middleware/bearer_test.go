package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// These tests exercise the #0077 bearer-token path of RequireSession. They use
// the in-memory fakeDevStore (which satisfies SessionResolver) so they run
// without a database — they verify token *extraction* and precedence, not the
// DB-backed resolution already covered by the live tests in auth_test.go.

// storeWith returns a fake resolver seeded with one valid, non-expired token.
func storeWith(token string, userID int64) *fakeDevStore {
	return &fakeDevStore{sessions: map[string]testSession{
		token: {userID: userID, expiresAt: time.Now().Add(time.Hour)},
	}}
}

// reqAuth builds a GET /api/links request with the given Authorization header
// (skipped when empty) and optional session cookie (skipped when empty).
func reqAuth(authHeader, cookieToken string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/links", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	if cookieToken != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieToken})
	}
	return r
}

// TestRequireSession_BearerAuthenticates: a valid token in the Authorization
// header authenticates with no cookie present at all.
func TestRequireSession_BearerAuthenticates(t *testing.T) {
	const token = "good-bearer-token"
	store := storeWith(token, 42)

	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSession(store)(next).ServeHTTP(rec, reqAuth("Bearer "+token, ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !next.ran || next.user == nil {
		t.Fatal("next handler did not run with an authenticated user")
	}
	if next.user.ID != 42 {
		t.Errorf("user id = %d, want 42", next.user.ID)
	}
}

// TestRequireSession_BearerSchemeCaseInsensitive: the scheme matches per RFC
// 6750 regardless of case ("bearer", "BEARER").
func TestRequireSession_BearerSchemeCaseInsensitive(t *testing.T) {
	const token = "good-bearer-token"
	store := storeWith(token, 7)

	for _, scheme := range []string{"bearer", "Bearer", "BEARER", "BeArEr"} {
		next := &captureHandler{}
		rec := httptest.NewRecorder()
		RequireSession(store)(next).ServeHTTP(rec, reqAuth(scheme+" "+token, ""))
		if rec.Code != http.StatusOK {
			t.Errorf("scheme %q: status = %d, want 200", scheme, rec.Code)
		}
	}
}

// TestRequireSession_BearerWinsOverCookie: when both are present, the bearer
// header is used. The cookie carries a token that does NOT exist in the store,
// so authentication can only succeed if the (valid) bearer token was chosen.
func TestRequireSession_BearerWinsOverCookie(t *testing.T) {
	const bearer = "good-bearer-token"
	store := storeWith(bearer, 99)

	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSession(store)(next).ServeHTTP(rec, reqAuth("Bearer "+bearer, "unknown-cookie-token"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bearer should win over the bad cookie)", rec.Code)
	}
	if next.user == nil || next.user.ID != 99 {
		t.Fatalf("authenticated as %+v, want user id 99 from the bearer token", next.user)
	}
}

// TestRequireSession_MalformedAuthFallsBackToCookie: a present-but-unusable
// Authorization header (empty token, or a non-Bearer scheme) must not break the
// browser SPA — it falls through to the cookie.
func TestRequireSession_MalformedAuthFallsBackToCookie(t *testing.T) {
	const cookieTok = "good-cookie-token"
	store := storeWith(cookieTok, 5)

	for _, hdr := range []string{
		"Bearer ",        // scheme but empty token
		"Bearer    ",     // scheme but whitespace-only token
		"Basic dXNlcjpw", // wrong scheme entirely
		"token123",       // no scheme
	} {
		next := &captureHandler{}
		rec := httptest.NewRecorder()
		RequireSession(store)(next).ServeHTTP(rec, reqAuth(hdr, cookieTok))
		if rec.Code != http.StatusOK {
			t.Errorf("Authorization %q: status = %d, want 200 (cookie fallback)", hdr, rec.Code)
		}
		if next.user == nil || next.user.ID != 5 {
			t.Errorf("Authorization %q: authenticated as %+v, want cookie user id 5", hdr, next.user)
		}
	}
}

// TestRequireSession_UnknownBearer401: an unknown bearer token with no cookie is
// rejected with the same 401 JSON as a bad cookie.
func TestRequireSession_UnknownBearer401(t *testing.T) {
	store := storeWith("the-only-valid-token", 1)

	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSession(store)(next).ServeHTTP(rec, reqAuth("Bearer not-a-real-token", ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if next.ran {
		t.Error("next handler ran for an unknown bearer token")
	}
	if got := rec.Body.String(); got != `{"error":"unauthenticated"}` {
		t.Errorf("body = %q, want unauthenticated JSON", got)
	}
}
