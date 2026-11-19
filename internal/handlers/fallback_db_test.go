package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

func fallbackMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	rules := newFilterRuleCache(pool)
	lh := NewLinksHandler(links.NewStore(pool), nil, rules, nil, nil, nil, campaigns.NewStore(pool), testBaseURL)
	ch := NewCampaignsHandler(campaigns.NewStore(pool), links.NewStore(pool), nil, clicks.NewStatsStore(pool), rules, testBaseURL)
	guard := middleware.RequireSessionOrAPIToken(authStore, authStore, middleware.APITokenLimits{})
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", guard(http.HandlerFunc(lh.Create)))
	mux.Handle("PATCH /api/links/{key}", guard(http.HandlerFunc(lh.Patch)))
	mux.Handle("GET /api/links/{key}", guard(http.HandlerFunc(lh.Get)))
	mux.Handle("POST /api/campaigns", guard(http.HandlerFunc(ch.Create)))
	mux.Handle("PATCH /api/campaigns/{slug}", guard(http.HandlerFunc(ch.Patch)))
	return mux
}

func call(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFallbackURL_LinkValidation(t *testing.T) {
	pool := filterTestPool(t)
	seedFilterRule(t, pool, `evil\.example\.com`, int16(filters.ReasonMalware))
	h := fallbackMux(t, pool)
	tok := mintToken(t, pool, seedUser(t, pool, "a@example.com"), "rw", false)

	bad := []string{`/expired`, `ftp://app.example/x`, `https:///x`, testBaseURL + `/u/abc123`, testBaseURL + `/u`}
	for i, fb := range bad {
		rec := call(t, h, "POST", "/api/links", tok, `{"destination_url":"https://ok.example.com/`+strconv.Itoa(i)+`","fallback_url":"`+fb+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400: %s", fb, rec.Code, rec.Body)
		}
	}
	rec := call(t, h, "POST", "/api/links", tok, `{"destination_url":"https://ok.example.com/f","fallback_url":"https://evil.example.com/x"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "fallback_denied") {
		t.Errorf("filtered fallback: %d %s", rec.Code, rec.Body)
	}
	// Same instance host but a non-/u/ path is fine.
	rec = call(t, h, "POST", "/api/links", tok, `{"destination_url":"https://ok.example.com/g","fallback_url":"`+testBaseURL+`/expired"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid fallback: %d %s", rec.Code, rec.Body)
	}
	var v linkView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.FallbackURL != testBaseURL+"/expired" {
		t.Errorf("fallback_url = %q", v.FallbackURL)
	}
}

// PATCH must filter the fallback (the gap #0109 describes for destinations).
func TestFallbackURL_PatchValidatesAndClears(t *testing.T) {
	pool := filterTestPool(t)
	seedFilterRule(t, pool, `evil\.example\.com`, int16(filters.ReasonMalware))
	h := fallbackMux(t, pool)
	alice := seedUser(t, pool, "a@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "k1", "https://ok.example.com")

	for _, fb := range []string{"https://evil.example.com/x", testBaseURL + "/u/k1", "nope"} {
		rec := call(t, h, "PATCH", "/api/links/k1", tok, `{"fallback_url":"`+fb+`"}`)
		if rec.Code < 400 {
			t.Errorf("patch %q accepted: %d", fb, rec.Code)
		}
	}
	rec := call(t, h, "PATCH", "/api/links/k1", tok, `{"fallback_url":"https://app.example/gone"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"fallback_url":"https://app.example/gone"`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	rec = call(t, h, "GET", "/api/links/k1", tok, "")
	if !strings.Contains(rec.Body.String(), "https://app.example/gone") {
		t.Errorf("get lacks fallback: %s", rec.Body)
	}
	rec = call(t, h, "PATCH", "/api/links/k1", tok, `{"fallback_url":""}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"fallback_url":""`) {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
}

func TestFallbackURL_CampaignValidation(t *testing.T) {
	pool := filterTestPool(t)
	seedFilterRule(t, pool, `evil\.example\.com`, int16(filters.ReasonMalware))
	h := fallbackMux(t, pool)
	tok := mintToken(t, pool, seedUser(t, pool, "a@example.com"), "rw", false)

	for _, fb := range []string{"https://evil.example.com/x", testBaseURL + "/u/abc", "nope"} {
		if rec := call(t, h, "POST", "/api/campaigns", tok, `{"name":"C","default_fallback_url":"`+fb+`"}`); rec.Code < 400 {
			t.Errorf("create accepted %q: %d", fb, rec.Code)
		}
	}
	rec := call(t, h, "POST", "/api/campaigns", tok, `{"name":"Invites","default_fallback_url":"https://app.example/expired"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"default_fallback_url":"https://app.example/expired"`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if rec = call(t, h, "PATCH", "/api/campaigns/invites", tok, `{"default_fallback_url":"https://evil.example.com/"}`); rec.Code != 422 {
		t.Errorf("patch filtered = %d", rec.Code)
	}
	rec = call(t, h, "PATCH", "/api/campaigns/invites", tok, `{"default_fallback_url":""}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"default_fallback_url":""`) {
		t.Errorf("clear: %d %s", rec.Code, rec.Body)
	}
}

// End to end against the real store and resolver: own fallback beats the
// campaign default, the campaign default applies, denied never falls back, no
// fallback keeps 404/410, and no fallback hit becomes a click.
func TestRedirectFallback_EndToEnd(t *testing.T) {
	pool := filterTestPool(t)
	ctx := context.Background()
	alice := seedUser(t, pool, "a@example.com")
	camp := seedCampaign(t, pool, alice, "Invites")
	if _, err := pool.Exec(ctx, `UPDATE campaigns SET default_fallback_url='https://camp.example/expired' WHERE id=$1`, camp.ID); err != nil {
		t.Fatal(err)
	}
	mk := func(key string, active bool, denied int, expired bool, own string, inCamp bool) {
		var campID *int64
		if inCamp {
			campID = &camp.ID
		}
		var exp any
		if expired {
			exp = past
		}
		var fb any
		if own != "" {
			fb = own
		}
		if _, err := pool.Exec(ctx, `INSERT INTO links (user_id,key,destination_url,active,denied_reason,created_at,expires_at,campaign_id,fallback_url)
			VALUES ($1,$2,'https://d.example',$3,$4,now(),$5,$6,$7)`, alice, key, active, denied, exp, campID, fb); err != nil {
			t.Fatal(err)
		}
	}
	mk("campexp", true, 0, true, "", true)
	mk("ownexp", true, 0, true, "https://own.example/gone", true)
	mk("campoff", false, 0, false, "", true)
	mk("deniedc", false, 1, false, "https://own.example/gone", true)
	mk("nofb", true, 0, true, "", false)
	mk("nofboff", false, 0, false, "", false)

	rec := &recordingClicker{clicks: make(chan ClickInfo, 4)}
	rh := NewRedirectHandler(links.NewResolver(nil, links.NewStore(pool)), rec)
	rh.now = func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) }
	want := map[string][2]string{
		"campexp": {"302", "https://camp.example/expired?reason=expired"},
		"ownexp":  {"302", "https://own.example/gone?reason=expired"},
		"campoff": {"302", "https://camp.example/expired?reason=inactive"},
		"deniedc": {"404", ""},
		"nofb":    {"410", ""},
		"nofboff": {"404", ""},
		"missing": {"404", ""},
	}
	for key, w := range want {
		r := serve(rh, httptest.NewRequest(http.MethodGet, "/u/"+key, nil))
		if got := strconv.Itoa(r.Code); got != w[0] || r.Header().Get("Location") != w[1] {
			t.Errorf("%s: %d %q, want %s %q", key, r.Code, r.Header().Get("Location"), w[0], w[1])
		}
	}
	rec.expectNoClick(t)
}
