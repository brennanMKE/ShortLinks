package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// These run the real handler behind the real guards over the in-memory dev
// store, so they exercise routing, the cookie-only rule, and the end-to-end
// "mint in the UI, use from a script" flow without Postgres.

type tokenHarness struct {
	mux    *http.ServeMux
	cookie *http.Cookie
}

func newTokenHarness(t *testing.T) *tokenHarness {
	t.Helper()
	ds := devstore.New("owner@example.com")
	sess, _, err := ds.CreateDevSession(ds.AdminID(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	h := NewAPITokensHandler(ds, nil)
	requireCookie := middleware.RequireCookieSession(ds)
	requireAPI := middleware.RequireSessionOrAPIToken(ds, ds, middleware.APITokenLimits{})
	mux := http.NewServeMux()
	mux.Handle("GET /account/api-tokens", requireCookie(http.HandlerFunc(h.List)))
	mux.Handle("POST /account/api-tokens", requireCookie(http.HandlerFunc(h.Create)))
	mux.Handle("DELETE /account/api-tokens/{id}", requireCookie(http.HandlerFunc(h.Revoke)))
	mux.Handle("GET /api/me", requireAPI(http.HandlerFunc(NewMeHandler("https://go.example").Me)))
	return &tokenHarness{mux: mux, cookie: &http.Cookie{Name: auth.SessionCookieName, Value: sess}}
}

func (h *tokenHarness) do(method, path, body, bearer string, withCookie bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if withCookie {
		r.AddCookie(h.cookie)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, r)
	return rec
}

func TestAPITokens_CreateUseRevoke(t *testing.T) {
	h := newTokenHarness(t)

	rec := h.do(http.MethodPost, "/account/api-tokens", `{"name":"  agent  ","read_only":true}`, "", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("create response must be Cache-Control: no-store")
	}
	var created createAPITokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, auth.APITokenPrefix) || created.APIToken.Name != "agent" ||
		!created.APIToken.ReadOnly || created.APIToken.ExpiresAt != nil || created.APIToken.Status != "active" {
		t.Fatalf("created = %+v", created)
	}

	// The list never contains the secret.
	rec = h.do(http.MethodGet, "/account/api-tokens", "", "", true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Token) {
		t.Fatalf("list: %d, leaks secret = %v", rec.Code, strings.Contains(rec.Body.String(), created.Token))
	}

	// A script can use it…
	rec = h.do(http.MethodGet, "/api/me", "", created.Token, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"api_token":{"id":`) ||
		!strings.Contains(rec.Body.String(), `"is_admin":false`) {
		t.Fatalf("me via token: %d %s", rec.Code, rec.Body)
	}
	// …but never to manage tokens, even with the session cookie alongside.
	for _, withCookie := range []bool{false, true} {
		rec = h.do(http.MethodPost, "/account/api-tokens", `{"name":"more"}`, created.Token, withCookie)
		if rec.Code != http.StatusForbidden {
			t.Errorf("mint with token (cookie=%v): %d, want 403", withCookie, rec.Code)
		}
	}

	path := "/account/api-tokens/" + strconv.FormatInt(created.APIToken.ID, 10)
	if rec = h.do(http.MethodDelete, path, "", "", true); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec = h.do(http.MethodGet, "/api/me", "", created.Token, false); rec.Code != http.StatusUnauthorized {
		t.Errorf("me after revoke: %d, want 401", rec.Code)
	}
	if rec = h.do(http.MethodDelete, path, "", "", true); rec.Code != http.StatusNotFound {
		t.Errorf("second revoke: %d, want 404", rec.Code)
	}
}

func TestAPITokens_CreateValidation(t *testing.T) {
	h := newTokenHarness(t)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		body string
		want int
	}{
		{`{"name":""}`, http.StatusBadRequest},
		{`{"name":"   "}`, http.StatusBadRequest},
		{`{"name":"` + strings.Repeat("x", auth.MaxAPITokenNameLen+1) + `"}`, http.StatusBadRequest},
		{`{"name":"x","expires_at":"` + past + `"}`, http.StatusBadRequest},
		{`{"name":"x","bogus":1}`, http.StatusBadRequest},
		{`{"name":"dated","expires_at":"` + future + `"}`, http.StatusCreated},
		{`{"name":"dated"}`, http.StatusConflict},
	}
	for _, c := range cases {
		if rec := h.do(http.MethodPost, "/account/api-tokens", c.body, "", true); rec.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.body, rec.Code, c.want, rec.Body)
		}
	}
}

func TestAPITokens_RequiresSignIn(t *testing.T) {
	h := newTokenHarness(t)
	if rec := h.do(http.MethodGet, "/account/api-tokens", "", "", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("no session: %d, want 401", rec.Code)
	}
}

// The production store must satisfy the handler's interface.
var _ apiTokenStore = (*auth.Store)(nil)
