package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// testSession holds a token's user and expiry for the fake store.
type testSession struct {
	userID    int64
	expiresAt time.Time
}

// fakeDevStore is a minimal in-memory devSessionCreator that also satisfies
// middleware.SessionResolver — no Postgres required for these tests.
type fakeDevStore struct {
	sessions map[string]testSession
}

func newFakeDevStore() *fakeDevStore {
	return &fakeDevStore{sessions: make(map[string]testSession)}
}

func (f *fakeDevStore) CreateDevSession(userID int64, ttl time.Duration) (string, time.Time, error) {
	tok, err := auth.NewSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(ttl)
	f.sessions[tok] = testSession{userID: userID, expiresAt: exp}
	return tok, exp, nil
}

// ResolveSession satisfies middleware.SessionResolver so the same fake can back
// RequireSession in the composed chain test.
func (f *fakeDevStore) ResolveSession(_ context.Context, token string, now time.Time) (auth.SessionUser, error) {
	s, ok := f.sessions[token]
	if !ok || !s.expiresAt.After(now) {
		return auth.SessionUser{}, auth.ErrSessionInvalid
	}
	return auth.SessionUser{
		ID:      s.userID,
		Email:   "admin@localhost",
		IsAdmin: true,
	}, nil
}

// TestDevAutoLogin_PanicsOutsideDevMode verifies the hard guardrail: constructing
// the middleware with devMode=false must panic, making it structurally impossible
// to wire into the production path.
func TestDevAutoLogin_PanicsOutsideDevMode(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("DevAutoLogin(devMode=false) did not panic — guardrail missing")
		}
	}()
	DevAutoLogin(newFakeDevStore(), false) // must panic
}

// TestDevAutoLogin_SetsCookieOnRequest verifies that DevAutoLogin injects a
// session cookie into the request (so downstream RequireSession can read it)
// and also sets it on the response (so the browser stores it).
func TestDevAutoLogin_SetsCookieOnRequest(t *testing.T) {
	store := newFakeDevStore()
	mw := DevAutoLogin(store, true)

	var seenCookie string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	// No cookie on the request — auto-login should kick in.
	mw(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// The cookie must be present on the injected request.
	if seenCookie == "" {
		t.Fatal("session cookie was not injected into the request")
	}

	// The same cookie must also be set on the response.
	var respCookie string
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			respCookie = c.Value
			break
		}
	}
	if respCookie == "" {
		t.Fatal("session cookie was not set on the response")
	}
	if seenCookie != respCookie {
		t.Errorf("request cookie %q != response cookie %q", seenCookie, respCookie)
	}

	// The token must be in the store's session map.
	if _, ok := store.sessions[seenCookie]; !ok {
		t.Errorf("token %q not found in dev session store", seenCookie)
	}
}

// TestDevAutoLogin_SkipsWhenCookiePresent verifies that DevAutoLogin is a no-op
// when the request already carries a session cookie — it must not overwrite it.
func TestDevAutoLogin_SkipsWhenCookiePresent(t *testing.T) {
	store := newFakeDevStore()
	mw := DevAutoLogin(store, true)

	const existingToken = "already-present-token"
	var seenCookie string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			seenCookie = c.Value
		}
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: existingToken})

	mw(next).ServeHTTP(rec, req)

	if seenCookie != existingToken {
		t.Errorf("cookie = %q, want original %q (must not replace existing cookie)", seenCookie, existingToken)
	}
	if len(store.sessions) != 0 {
		t.Errorf("dev sessions created = %d, want 0 (should have skipped existing cookie)", len(store.sessions))
	}
}

// TestDevAutoLogin_ComposesWithRequireSession verifies the full chain:
// DevAutoLogin (outermost) → RequireSession (inner) → handler.
// A request with no cookie must arrive at the inner handler fully authenticated.
func TestDevAutoLogin_ComposesWithRequireSession(t *testing.T) {
	store := newFakeDevStore()
	devMW := DevAutoLogin(store, true)

	// RequireSession backed by the same store (which also implements SessionResolver).
	requireSession := RequireSession(store)

	inner := &captureHandler{}
	chain := devMW(requireSession(inner))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; DevAutoLogin + RequireSession chain failed", rec.Code)
	}
	if !inner.ran {
		t.Fatal("inner handler did not run — RequireSession rejected the auto-login session")
	}
	if !inner.ok || inner.user == nil {
		t.Fatal("UserFromContext returned no user after DevAutoLogin chain")
	}
	if inner.user.ID != 1 {
		t.Errorf("user ID = %d, want 1 (mock admin)", inner.user.ID)
	}
	if !inner.user.IsAdmin {
		t.Errorf("IsAdmin = false, want true for mock admin")
	}
}
