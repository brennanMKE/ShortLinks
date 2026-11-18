package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// reportsMux mounts the reports routes exactly as main.go does, behind the
// real requireAPI guard, over the real auth store and reports service.
func reportsMux(pool *pgxpool.Pool) http.Handler {
	authStore := auth.NewStore(pool)
	h := NewReportsHandler(reports.NewService(pool, "America/Los_Angeles"))
	requireAPI := middleware.RequireSessionOrAPIToken(authStore, authStore, middleware.APITokenLimits{})
	mux := http.NewServeMux()
	mux.Handle("GET /api/reports", requireAPI(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/reports/{name}", requireAPI(http.HandlerFunc(h.Get)))
	return mux
}

func mintToken(t *testing.T, pool *pgxpool.Pool, userID int64, name string, readOnly bool) string {
	t.Helper()
	_, secret, err := auth.NewStore(pool).CreateAPIToken(context.Background(),
		auth.NewAPIToken{UserID: userID, Name: name, ReadOnly: readOnly}, nil, audit.Entry{})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	return secret
}

func reportsGet(t *testing.T, h http.Handler, path string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func seedReportClicks(t *testing.T, pool *pgxpool.Pool, userID int64, key string, n int, bot bool) {
	t.Helper()
	linkID := seedLink(t, pool, userID, key, "https://example.com/")
	for i := 0; i < n; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at, is_bot, utm_source) VALUES ($1, $2, $3, 'mastodon')`,
			linkID, time.Now().Add(-time.Minute), bot); err != nil {
			t.Fatalf("seed click: %v", err)
		}
	}
}

func TestReports_AuthModes(t *testing.T) {
	pool := credsTestPool(t)
	h := reportsMux(pool)
	alice, bob := seedUser(t, pool, "alice@example.com"), seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-session")
	seedReportClicks(t, pool, alice, "aaa", 3, false)
	seedReportClicks(t, pool, bob, "bbb", 10, false)
	full := mintToken(t, pool, alice, "full", false)
	ro := mintToken(t, pool, alice, "ro", true)

	cases := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"cookie session", func(r *http.Request) { withCookie(r, "alice-session") }, http.StatusOK},
		{"full token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+full) }, http.StatusOK},
		{"read-only token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ro) }, http.StatusOK},
		{"anonymous", nil, http.StatusUnauthorized},
		{"bad token", func(r *http.Request) { r.Header.Set("Authorization", "Bearer slk_nope") }, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := reportsGet(t, h, "/api/reports/overview", tc.mutate)
			if rec.Code != tc.status {
				t.Fatalf("overview status = %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if rec = reportsGet(t, h, "/api/reports", tc.mutate); rec.Code != tc.status {
				t.Fatalf("catalogue status = %d, want %d", rec.Code, tc.status)
			}
		})
	}

	// The body: scoped to alice, envelope shape, tz from DEFAULT_TIMEZONE.
	rec := reportsGet(t, h, "/api/reports/overview", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+ro) })
	var env struct {
		Report string `json:"report"`
		Window struct {
			TZ      string `json:"tz"`
			Partial bool   `json:"partial_last_day"`
		} `json:"window"`
		Highlights []string `json:"highlights"`
		Data       struct {
			Totals struct {
				Clicks int64 `json:"clicks"`
			} `json:"totals"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Report != "overview" || env.Window.TZ != "America/Los_Angeles" || !env.Window.Partial || env.Data.Totals.Clicks != 3 || len(env.Highlights) == 0 {
		t.Errorf("body = %s", rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
}

func TestReports_CatalogueAndErrors(t *testing.T) {
	pool := credsTestPool(t)
	h := reportsMux(pool)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-session")
	cookie := func(r *http.Request) { withCookie(r, "alice-session") }

	rec := reportsGet(t, h, "/api/reports", cookie)
	var cat reports.Catalogue
	if err := json.Unmarshal(rec.Body.Bytes(), &cat); err != nil {
		t.Fatal(err)
	}
	if len(cat.Reports) == 0 || cat.Reports[0].Name != "overview" || cat.Reports[0].Path != "/api/reports/overview" || cat.Reports[0].Description == "" || len(cat.CommonParams) == 0 {
		t.Errorf("catalogue = %s", rec.Body)
	}

	errs := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/reports/nope", 404, "report_not_found"},
		{"/api/reports/overview?range=2w", 400, "invalid_range"},
		{"/api/reports/overview?tz=Mars/Base", 400, "invalid_tz"},
		{"/api/reports/overview?from=yesterday", 400, "invalid_date"},
		{"/api/reports/overview?compare=x", 400, "invalid_compare"},
		{"/api/reports/overview?from=2999-01-01", 400, "invalid_date"},
		{"/api/reports/overview?limit=0", 400, "invalid_limit"},
		{"/api/reports/overview?include_bots=maybe", 400, "invalid_include_bots"},
	}
	for _, tc := range errs {
		t.Run(tc.path, func(t *testing.T) {
			rec := reportsGet(t, h, tc.path, cookie)
			var body map[string]string
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != tc.status || body["error"] != tc.code {
				t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			if tc.status == 400 && body["field"] == "" {
				t.Errorf("400 body lacks field: %s", rec.Body)
			}
		})
	}
}

func TestReports_BotsAndFilterOverHTTP(t *testing.T) {
	pool := credsTestPool(t)
	h := reportsMux(pool)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-session")
	seedReportClicks(t, pool, alice, "aaa", 4, false)
	seedReportClicks(t, pool, alice, "bbb", 2, true)
	cookie := func(r *http.Request) { withCookie(r, "alice-session") }

	clicksFor := func(q string) (int64, int64) {
		rec := reportsGet(t, h, "/api/reports/overview?"+q, cookie)
		var env struct {
			Data struct {
				Totals struct {
					Clicks       int64 `json:"clicks"`
					ExcludedBots int64 `json:"excluded_bots"`
				} `json:"totals"`
			} `json:"data"`
		}
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Data.Totals.Clicks, env.Data.Totals.ExcludedBots
	}
	if c, b := clicksFor("range=7d"); c != 4 || b != 2 {
		t.Errorf("default = %d clicks / %d bots", c, b)
	}
	if c, b := clicksFor("include_bots=true&source=mastodon"); c != 6 || b != 0 {
		t.Errorf("include_bots = %d / %d", c, b)
	}
	if c, _ := clicksFor("source=other"); c != 0 {
		t.Errorf("source=other = %d", c)
	}
}
