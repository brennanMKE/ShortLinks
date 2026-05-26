package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// filterTestPool connects to TEST_DATABASE_URL (or skips) and truncates the auth
// tables AND url_filter_rules before and after the test so each run starts and
// leaves the DB clean. url_filter_rules is not covered by the credentials
// suite's truncate, so it is cleared explicitly here.
func filterTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := credsTestPool(t) // skips when TEST_DATABASE_URL unset; truncates auth tables.
	clearFilterRules(t, pool)
	t.Cleanup(func() { clearFilterRules(t, pool) })
	return pool
}

func clearFilterRules(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE url_filter_rules RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate url_filter_rules: %v", err)
	}
}

// seedFilterRule inserts an active url_filter_rules row and returns its id.
func seedFilterRule(t *testing.T, pool *pgxpool.Pool, pattern string, reasonCode int16) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO url_filter_rules (pattern, reason_code, active, created_at)
		 VALUES ($1, $2, TRUE, now()) RETURNING id`,
		pattern, reasonCode,
	).Scan(&id); err != nil {
		t.Fatalf("seed filter rule %q: %v", pattern, err)
	}
	return id
}

// countDeniedLinks returns how many denied rows (denied_reason = code) a user has
// for a destination — backs the per-attempt denied-row assertions.
func countDeniedLinks(t *testing.T, pool *pgxpool.Pool, userID int64, dest string, code int16) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM links
		  WHERE user_id = $1 AND destination_url = $2 AND active = FALSE AND denied_reason = $3`,
		userID, dest, code,
	).Scan(&n); err != nil {
		t.Fatalf("count denied links: %v", err)
	}
	return n
}

// filterLinksMux builds the link-create route guarded by the real RequireSession
// with the given rule cache wired into the handler, so the #0024 filter check
// runs against the live DB-backed rules.
func filterLinksMux(t *testing.T, pool *pgxpool.Pool, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewLinksHandler(links.NewStore(pool), nil, ruleCache, nil, nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(h.Create)))
	return mux
}

// newFilterRuleCache builds a RuleCache backed by the live filters store.
func newFilterRuleCache(pool *pgxpool.Pool) *cache.RuleCache {
	fs := filters.NewStore(pool)
	return cache.NewRuleCache(func(ctx context.Context) ([]filters.Rule, error) {
		rules, err := fs.LoadActive(ctx)
		if err != nil {
			return nil, err
		}
		return filters.CompileRules(rules, nil), nil
	})
}

// TestLinksCreate_FilterDeniesAndRecords asserts the full #0024 flow end to end:
// a URL matching an active rule yields 422 {error:"url_denied",reason,label}, a
// denied link row (active=false, denied_reason=code) is created, a SECOND
// submission of the same blocked URL creates ANOTHER denied row (count=2), and a
// non-matching URL still creates a normal active link.
func TestLinksCreate_FilterDeniesAndRecords(t *testing.T) {
	pool := filterTestPool(t)
	ruleCache := newFilterRuleCache(pool)
	srv := httptest.NewServer(filterLinksMux(t, pool, ruleCache))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedFilterRule(t, pool, `evil\.com`, int16(filters.ReasonMalware))

	const blocked = "http://evil.com/x"

	// First submission of the blocked URL → 422 with the expected body.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links", jsonBody(`{"destination_url":"`+blocked+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var body struct {
		Error  string `json:"error"`
		Reason int    `json:"reason"`
		Label  string `json:"label"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if body.Error != "url_denied" {
		t.Errorf("error = %q, want url_denied", body.Error)
	}
	if body.Reason != filters.ReasonMalware {
		t.Errorf("reason = %d, want %d (malware)", body.Reason, filters.ReasonMalware)
	}
	if body.Label != filters.ReasonLabel(filters.ReasonMalware) {
		t.Errorf("label = %q, want %q", body.Label, filters.ReasonLabel(filters.ReasonMalware))
	}
	if n := countDeniedLinks(t, pool, alice, blocked, int16(filters.ReasonMalware)); n != 1 {
		t.Fatalf("denied row count = %d, want 1", n)
	}

	// Second submission of the SAME blocked URL → another denied row (count=2).
	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links", jsonBody(`{"destination_url":"`+blocked+`"}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("second status = %d, want 422", resp2.StatusCode)
	}
	resp2.Body.Close()
	if n := countDeniedLinks(t, pool, alice, blocked, int16(filters.ReasonMalware)); n != 2 {
		t.Fatalf("denied row count after 2nd submit = %d, want 2 (one row per attempt)", n)
	}

	// A non-matching URL still creates a normal active link (201, active=true).
	good, status := postLink(t, srv, "alice-token", `{"destination_url":"https://www.wikipedia.org"}`)
	if status != http.StatusCreated {
		t.Fatalf("good URL status = %d, want 201", status)
	}
	if !good.Active || good.DeniedReason != 0 {
		t.Errorf("good link active=%v denied=%d, want active=true denied=0", good.Active, good.DeniedReason)
	}
}

// adminURLFiltersMux builds the admin URL-filter routes guarded by RequireSession
// + RequireAdmin, sharing the given rule cache so a CRUD mutation invalidates the
// same cache the link-create path reads.
func adminURLFiltersMux(t *testing.T, pool *pgxpool.Pool, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewURLFiltersHandler(filters.NewStore(pool), ruleCache, nil)
	requireSession := middleware.RequireSession(authStore)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/url-filters", requireAdmin(http.HandlerFunc(h.List)))
	mux.Handle("POST /admin/url-filters", requireAdmin(http.HandlerFunc(h.Create)))
	mux.Handle("POST /admin/url-filters/test", requireAdmin(http.HandlerFunc(h.Test)))
	mux.Handle("PATCH /admin/url-filters/{id}", requireAdmin(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /admin/url-filters/{id}", requireAdmin(http.HandlerFunc(h.Delete)))
	return mux
}

// TestAdminURLFilters_CRUDInvalidatesCache asserts the admin CRUD path: creating
// a rule (201) makes a URL blocked, and deleting it (after the cache is
// invalidated) makes the same URL allowed again — all within one test, proving
// the mutation→invalidate→re-evaluate loop. Also exercises the /test endpoint and
// non-admin/regex-validation guards.
func TestAdminURLFilters_CRUDInvalidatesCache(t *testing.T) {
	pool := filterTestPool(t)
	ruleCache := newFilterRuleCache(pool)

	adminSrv := httptest.NewServer(adminURLFiltersMux(t, pool, ruleCache))
	defer adminSrv.Close()
	linkSrv := httptest.NewServer(filterLinksMux(t, pool, ruleCache))
	defer linkSrv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	_ = admin
	seedSession(t, pool, admin, "admin-token")
	regular := seedUser(t, pool, "user@example.com")
	seedSession(t, pool, regular, "user-token")

	const target = "http://blocked.example/x"

	// Non-admin cannot create a rule → 403.
	reqNon, _ := http.NewRequest(http.MethodPost, adminSrv.URL+"/admin/url-filters",
		jsonBody(`{"pattern":"blocked\\.example","reason_code":3}`))
	reqNon.Header.Set("Content-Type", "application/json")
	respNon, err := adminSrv.Client().Do(withCookie(reqNon, "user-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respNon.Body.Close()
	if respNon.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin create status = %d, want 403", respNon.StatusCode)
	}

	// Invalid regex → 400.
	reqBad, _ := http.NewRequest(http.MethodPost, adminSrv.URL+"/admin/url-filters",
		jsonBody(`{"pattern":"(unclosed","reason_code":3}`))
	reqBad.Header.Set("Content-Type", "application/json")
	respBad, err := adminSrv.Client().Do(withCookie(reqBad, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid-regex create status = %d, want 400", respBad.StatusCode)
	}

	// Admin creates a valid rule → 201; cache is invalidated.
	reqCreate, _ := http.NewRequest(http.MethodPost, adminSrv.URL+"/admin/url-filters",
		jsonBody(`{"pattern":"blocked\\.example","reason_code":3,"description":"spam host"}`))
	reqCreate.Header.Set("Content-Type", "application/json")
	respCreate, err := adminSrv.Client().Do(withCookie(reqCreate, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if respCreate.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", respCreate.StatusCode)
	}
	var created ruleView
	if err := json.NewDecoder(respCreate.Body).Decode(&created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	respCreate.Body.Close()
	if created.ID == 0 || created.ReasonCode != int16(filters.ReasonSpam) {
		t.Fatalf("created rule = %+v, want id>0 reason=3", created)
	}

	// /test reports the URL now matches the rule.
	reqTest, _ := http.NewRequest(http.MethodPost, adminSrv.URL+"/admin/url-filters/test",
		jsonBody(`{"url":"`+target+`"}`))
	reqTest.Header.Set("Content-Type", "application/json")
	respTest, err := adminSrv.Client().Do(withCookie(reqTest, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	var testBody testRuleResponse
	if err := json.NewDecoder(respTest.Body).Decode(&testBody); err != nil {
		t.Fatalf("decode test: %v", err)
	}
	respTest.Body.Close()
	if !testBody.Matched || testBody.ReasonCode == nil || *testBody.ReasonCode != filters.ReasonSpam {
		t.Errorf("test = %+v, want matched with reason 3", testBody)
	}

	// The URL is now blocked at creation (cache picked up the new rule) → 422.
	_, status := postLink(t, linkSrv, "user-token", `{"destination_url":"`+target+`"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("create-after-rule status = %d, want 422 (blocked)", status)
	}

	// Delete the rule → cache invalidated.
	reqDel, _ := http.NewRequest(http.MethodDelete, adminSrv.URL+"/admin/url-filters/"+itoa(created.ID), nil)
	respDel, err := adminSrv.Client().Do(withCookie(reqDel, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respDel.Body.Close()
	if respDel.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", respDel.StatusCode)
	}

	// Same URL is now ALLOWED — the rule deletion invalidated the cache, so the
	// next creation re-evaluates against an empty rule set → 201 active link.
	allowed, status := postLink(t, linkSrv, "user-token", `{"destination_url":"`+target+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create-after-delete status = %d, want 201 (allowed)", status)
	}
	if !allowed.Active || allowed.DeniedReason != 0 {
		t.Errorf("allowed link active=%v denied=%d, want active=true denied=0", allowed.Active, allowed.DeniedReason)
	}
}

// itoa is a tiny int64→string helper for building URL paths in tests.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
