package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
)

func TestDevPortikoLogin(t *testing.T) {
	ds := devstore.New("admin@localhost")
	h := devPortikoLogin(ds, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(auth.SessionCookieName); err == nil {
			t.Error("pass-through request must not be signed in")
		}
		w.WriteHeader(204)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/me", nil))
	if rec.Code != 204 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("other paths must pass through unsigned: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/portiko/login?return_to=%2F%23%2Fcampaigns%2Fx", nil))
	if rec.Code != 302 || rec.Header().Get("Location") != "/#/campaigns/x" || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("login: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	for _, bad := range []string{"//evil.com", "https://evil.com", "/\\evil.com", ""} {
		if got := devLocalPath(bad); got != "/" {
			t.Errorf("devLocalPath(%q)=%q", bad, got)
		}
	}
}

func TestDevPortikoLoginPanicsOutsideDevMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	devPortikoLogin(nil, false)
}

func TestDevPortikoLogout(t *testing.T) {
	ds := devstore.New("admin@localhost")
	h := devPortikoLogin(ds, true)(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/portiko/login?return_to=%2F", nil))
	var sess *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("login set no session cookie")
	}

	req := httptest.NewRequest("POST", "/auth/logout", nil)
	req.AddCookie(sess)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 302 || rec.Header().Get("Location") != "/" {
		t.Fatalf("logout: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout must clear the session cookie")
	}
	if uid, _ := ds.DeleteSession(req.Context(), sess.Value); uid != 0 {
		t.Error("session still existed after logout")
	}

	// A GET is not a logout and passes through.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/auth/logout", nil))
	if rec.Code != 404 {
		t.Errorf("GET /auth/logout must pass through, got %d", rec.Code)
	}
}
