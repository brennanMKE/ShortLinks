package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// These tests cover the API-token path (RequireSessionOrAPIToken) and the two
// session-only guards' refusal of tokens. They use an in-memory fake resolver;
// the DB-backed resolution is covered in internal/auth/apitokens_test.go.

type fakeTokenResolver struct {
	tokens map[string]auth.APITokenUser
	calls  int
}

func (f *fakeTokenResolver) ResolveAPIToken(_ context.Context, token, _ string, _ time.Time) (auth.APITokenUser, error) {
	f.calls++
	u, ok := f.tokens[token]
	if !ok {
		return auth.APITokenUser{}, auth.ErrAPITokenInvalid
	}
	return u, nil
}

const (
	goodToken     = "slk_good"
	readOnlyToken = "slk_readonly"
)

func newFakeTokens() *fakeTokenResolver {
	return &fakeTokenResolver{tokens: map[string]auth.APITokenUser{
		goodToken: {
			SessionUser: auth.SessionUser{ID: 42, Email: "agent-owner@example.com", IsAdmin: true},
			TokenID:     7, TokenName: "agent",
		},
		readOnlyToken: {
			SessionUser: auth.SessionUser{ID: 42, Email: "agent-owner@example.com"},
			TokenID:     8, TokenName: "reports", ReadOnly: true,
		},
	}}
}

func tokenReq(method, authHeader string) *http.Request {
	r := httptest.NewRequest(method, "/api/links", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	return r
}

func noLimits() APITokenLimits { return APITokenLimits{} }

// A valid token authenticates as its owner, never as an admin, and carries
// the token identity.
func TestAPIToken_Authenticates(t *testing.T) {
	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSessionOrAPIToken(newFakeDevStore(), newFakeTokens(), noLimits())(next).
		ServeHTTP(rec, tokenReq(http.MethodPost, "Bearer "+goodToken))

	if rec.Code != http.StatusOK || next.user == nil {
		t.Fatalf("status = %d, want 200 with a user", rec.Code)
	}
	if next.user.ID != 42 || next.user.APITokenID != 7 || next.user.APITokenName != "agent" {
		t.Errorf("user = %+v, want owner 42 via token 7 'agent'", next.user)
	}
	if next.user.IsAdmin {
		t.Error("API token principal must never be admin, even for an admin's token")
	}
	if !next.user.ViaAPIToken() {
		t.Error("ViaAPIToken() = false, want true")
	}
}

// Session cookies (and session bearers) still work on the same routes.
func TestAPIToken_SessionStillAccepted(t *testing.T) {
	sessions := storeWith("sess-token", 5)
	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSessionOrAPIToken(sessions, newFakeTokens(), noLimits())(next).
		ServeHTTP(rec, reqAuth("", "sess-token"))
	if rec.Code != http.StatusOK || next.user == nil || next.user.ID != 5 || next.user.ViaAPIToken() {
		t.Fatalf("status = %d user = %+v, want session user 5", rec.Code, next.user)
	}
}

// Unknown tokens get the same 401 body as any other auth failure.
func TestAPIToken_InvalidIs401(t *testing.T) {
	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSessionOrAPIToken(newFakeDevStore(), newFakeTokens(), noLimits())(next).
		ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer slk_nope"))
	if rec.Code != http.StatusUnauthorized || next.ran {
		t.Fatalf("status = %d ran = %v, want 401 and no handler", rec.Code, next.ran)
	}
	if got := rec.Body.String(); got != `{"error":"unauthenticated"}` {
		t.Errorf("body = %s", got)
	}
}

// A token is honoured only in the Authorization header — never as a cookie.
func TestAPIToken_NotAcceptedAsCookie(t *testing.T) {
	tokens := newFakeTokens()
	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSessionOrAPIToken(newFakeDevStore(), tokens, noLimits())(next).
		ServeHTTP(rec, reqAuth("", goodToken))
	if next.ran {
		t.Fatal("token in a cookie must not authenticate")
	}
	if tokens.calls != 0 {
		t.Error("token resolver must not be consulted for a cookie value")
	}
}

// A read-only token may read but not write.
func TestAPIToken_ReadOnly(t *testing.T) {
	guard := RequireSessionOrAPIToken(newFakeDevStore(), newFakeTokens(), noLimits())
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		guard(&captureHandler{}).ServeHTTP(rec, tokenReq(m, "Bearer "+readOnlyToken))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", m, rec.Code)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
		next := &captureHandler{}
		rec := httptest.NewRecorder()
		guard(next).ServeHTTP(rec, tokenReq(m, "Bearer "+readOnlyToken))
		if rec.Code != http.StatusForbidden || next.ran {
			t.Errorf("%s: status = %d ran = %v, want 403", m, rec.Code, next.ran)
		}
	}
}

// The per-token limit answers 429 with Retry-After once the burst is spent.
func TestAPIToken_PerTokenRateLimit(t *testing.T) {
	limits := APITokenLimits{PerToken: NewRateLimiter(rate.Every(time.Hour), 2)}
	guard := RequireSessionOrAPIToken(newFakeDevStore(), newFakeTokens(), limits)
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		guard(&captureHandler{}).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer "+goodToken))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	guard(&captureHandler{}).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer "+goodToken))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After")
	}
	// A different token has its own bucket.
	rec = httptest.NewRecorder()
	guard(&captureHandler{}).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer "+readOnlyToken))
	if rec.Code != http.StatusOK {
		t.Errorf("other token: status = %d, want 200", rec.Code)
	}
}

// Repeated invalid tokens from one IP are throttled before any lookup, and a
// valid token from that IP is throttled too until the bucket refills.
func TestAPIToken_FailedAttemptsThrottled(t *testing.T) {
	tokens := newFakeTokens()
	limits := APITokenLimits{FailuresPerIP: NewRateLimiter(rate.Every(time.Hour), 3)}
	guard := RequireSessionOrAPIToken(newFakeDevStore(), tokens, limits)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		guard(&captureHandler{}).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer slk_guess"))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %d, want 401", i, rec.Code)
		}
	}
	callsBefore := tokens.calls
	rec := httptest.NewRecorder()
	guard(&captureHandler{}).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer "+goodToken))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once failures are exhausted", rec.Code)
	}
	if tokens.calls != callsBefore {
		t.Error("throttled IP must be refused without a token lookup")
	}
}

// Session-only routes (account, admin) refuse an API token outright, without
// resolving it.
func TestRequireSession_RefusesAPIToken(t *testing.T) {
	next := &captureHandler{}
	rec := httptest.NewRecorder()
	RequireSession(newFakeDevStore())(next).ServeHTTP(rec, tokenReq(http.MethodGet, "Bearer "+goodToken))
	if rec.Code != http.StatusForbidden || next.ran {
		t.Fatalf("status = %d ran = %v, want 403", rec.Code, next.ran)
	}
}

// Token management accepts only the browser cookie: any Authorization header
// is refused, even alongside a valid cookie.
func TestRequireCookieSession(t *testing.T) {
	sessions := storeWith("sess-token", 5)
	guard := RequireCookieSession(sessions)

	rec := httptest.NewRecorder()
	next := &captureHandler{}
	guard(next).ServeHTTP(rec, reqAuth("", "sess-token"))
	if rec.Code != http.StatusOK || !next.ran {
		t.Fatalf("cookie: status = %d, want 200", rec.Code)
	}

	for _, h := range []string{"Bearer " + goodToken, "Bearer sess-token"} {
		rec := httptest.NewRecorder()
		next := &captureHandler{}
		guard(next).ServeHTTP(rec, reqAuth(h, "sess-token"))
		if rec.Code != http.StatusForbidden || next.ran {
			t.Errorf("%q: status = %d ran = %v, want 403", h, rec.Code, next.ran)
		}
	}
}
