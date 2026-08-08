package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// campaignsMux builds the real route table for the campaign CRUD +
// link-membership + stats endpoints, guarded by RequireSession backed by the
// real *auth.Store and serving the real *campaigns.Store/*links.Store/
// *clicks.StatsStore, mirroring linksMux.
func campaignsMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	return campaignsMuxWithRules(t, pool, nil)
}

// campaignsMuxWithRules is campaignsMux with an explicit (possibly nil)
// ruleProvider, so #0105's batch-create filter-check tests can wire a real
// DB-backed *cache.RuleCache the way filterLinksMux does for single-create,
// while every other campaigns test keeps using the simpler nil-rules
// campaignsMux.
func campaignsMuxWithRules(t *testing.T, pool *pgxpool.Pool, rules ruleProvider) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewCampaignsHandler(campaigns.NewStore(pool), links.NewStore(pool), nil, clicks.NewStatsStore(pool), rules)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("GET /api/campaigns", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /api/campaigns/{slug}/stats", requireSession(http.HandlerFunc(h.Stats)))
	mux.Handle("GET /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(h.ListLinks)))
	mux.Handle("POST /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(h.AssignLinks)))
	mux.Handle("DELETE /api/campaigns/{slug}/links/{key}", requireSession(http.HandlerFunc(h.UnassignLink)))
	mux.Handle("POST /api/campaigns/{slug}/links/batch", requireSession(http.HandlerFunc(h.BatchCreateLinks)))
	// Bulk QR download (#0106) — see qr_test.go.
	mux.Handle("GET /api/campaigns/{slug}/qr.zip", requireSession(http.HandlerFunc(h.QRZip)))
	return mux
}

// createCampaign is a small helper that POSTs a campaign for the given
// authenticated session and returns the decoded response.
func createCampaign(t *testing.T, srv *httptest.Server, token, body string) campaignView {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", resp.StatusCode)
	}
	var c campaignView
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return c
}

// TestCampaignsCreate_DefaultUTMCampaignFromSlug asserts POST without an
// explicit default_utm_campaign returns a campaign whose default_utm_campaign
// equals its generated slug.
func TestCampaignsCreate_DefaultUTMCampaignFromSlug(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	if c.Slug != "summer-fair" {
		t.Errorf("slug = %q, want %q", c.Slug, "summer-fair")
	}
	if c.DefaultUTMCampaign != c.Slug {
		t.Errorf("default_utm_campaign = %q, want %q (the slug)", c.DefaultUTMCampaign, c.Slug)
	}
	if c.Archived {
		t.Errorf("archived = true, want false on create")
	}
}

// TestCampaignsCreate_SlugCollisionSuffixed asserts two campaigns with the
// same name for the same user get suffixed slugs via the HTTP path.
func TestCampaignsCreate_SlugCollisionSuffixed(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	first := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	second := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	if first.Slug != "summer-fair" {
		t.Errorf("first slug = %q, want %q", first.Slug, "summer-fair")
	}
	if second.Slug != "summer-fair-2" {
		t.Errorf("second slug = %q, want %q", second.Slug, "summer-fair-2")
	}
}

// TestCampaignsCreate_MissingNameRejected asserts an empty/absent name is a
// 400, not a silently-created campaign.
func TestCampaignsCreate_MissingNameRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns", jsonBody(`{"name":"  "}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCampaignsCreate_OverlongNameRejected asserts a name long enough to make
// its derived slug overflow the (user_id, slug) btree index — previously
// observed as a raw 500 ("index row size exceeds btree version 4 maximum
// 2704") — is instead rejected with a clean 400 before it ever reaches the
// database.
func TestCampaignsCreate_OverlongNameRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	longName := strings.Repeat("a", 3000)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns", jsonBody(`{"name":"`+longName+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (not a raw DB error)", resp.StatusCode)
	}
}

// TestCampaignsCreate_EndsBeforeStartsRejected asserts POST rejects an
// ends_at earlier than starts_at — #0102 uses this range as the default chart
// window, so an inverted range would silently produce an always-empty window.
func TestCampaignsCreate_EndsBeforeStartsRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	body := `{"name":"Summer Fair","starts_at":"2026-06-10T00:00:00Z","ends_at":"2026-06-01T00:00:00Z"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCampaignsPatch_EndsBeforeExistingStartsRejected asserts PATCH rejects
// an ends_at earlier than the campaign's EXISTING starts_at even when this
// particular PATCH body does not also touch starts_at — the effective
// resulting window is what must stay ordered, not just the fields present in
// one request.
func TestCampaignsPatch_EndsBeforeExistingStartsRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair","starts_at":"2026-06-10T00:00:00Z"}`)

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/campaigns/"+c.Slug, jsonBody(`{"ends_at":"2026-06-01T00:00:00Z"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (ends_at before the campaign's existing starts_at)", resp.StatusCode)
	}
}

// TestCampaignsList_EmptyCampaignHasZeroLinkCountAndClicks asserts the list
// response carries link_count and total_clicks as PRESENT and 0 for a
// campaign with no links/clicks — now REAL aggregates (#0102), not the
// hardcoded-0 stub #0098 shipped. This decodes into map[string]any rather
// than listCampaignsResponse: decoding into the typed struct cannot
// distinguish an absent JSON key from a present key with value 0 (both
// decode to the zero value), so a regression that adds `,omitempty` to the
// struct tags — dropping the keys from the wire entirely — would pass a
// struct-typed assertion. See TestCampaignsList_LinkCountAndTotalClicksAggregate
// below for the non-zero case.
func TestCampaignsList_EmptyCampaignHasZeroLinkCountAndClicks(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list, ok := body["campaigns"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("campaigns = %#v, want a 1-element array", body["campaigns"])
	}
	item, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("campaign item is not an object: %#v", list[0])
	}

	linkCount, present := item["link_count"]
	if !present {
		t.Fatal("\"link_count\" key is missing from the response — must be present and 0, not omitted")
	}
	if linkCount != float64(0) {
		t.Errorf("link_count = %v, want 0", linkCount)
	}

	totalClicks, present := item["total_clicks"]
	if !present {
		t.Fatal("\"total_clicks\" key is missing from the response — must be present and 0, not omitted")
	}
	if totalClicks != float64(0) {
		t.Errorf("total_clicks = %v, want 0", totalClicks)
	}
}

// TestCampaignsList_OwnershipScoped asserts GET /api/campaigns returns only
// the caller's own campaigns, never another user's.
func TestCampaignsList_OwnershipScoped(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)
	createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns", nil)
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body listCampaignsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Campaigns) != 1 || body.Campaigns[0].Name != "Bob Campaign" {
		t.Errorf("bob's list = %+v, want exactly [Bob Campaign]", body.Campaigns)
	}
}

// seedClickForHandler inserts a click row directly (raw SQL — this test file
// has no recorder wired into campaignsMux), backing the #0102 handler-level
// aggregation/ownership/stats tests below. clicked_at is set to YESTERDAY,
// not now(): the default stats window's upper bound is today's UTC
// midnight, EXCLUSIVE (matching ClicksOverTime's existing, documented
// convention — see clicks.TestClicksOverTime_ZeroDefaults), so a click
// timestamped "now" during today falls outside every zero-from/to default
// window and would make these tests flaky depending on wall-clock time.
func seedClickForHandler(t *testing.T, pool *pgxpool.Pool, linkID, campaignID int64, isBot bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, campaign_id, is_bot, clicked_at) VALUES ($1, $2, $3, now() - interval '1 day')`,
		linkID, campaignID, isBot,
	); err != nil {
		t.Fatalf("seed click: %v", err)
	}
}

// TestCampaignsList_LinkCountAndTotalClicksAggregate asserts GET
// /api/campaigns now returns REAL link_count/total_clicks (#0102) —
// cross-checked against a second endpoint (GET /api/campaigns/{slug}/links)
// rather than only asserting an absolute number, per #0101's downstream
// constraint 3.
func TestCampaignsList_LinkCountAndTotalClicksAggregate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Aggregate"}`)
	campID := campaignRowID(t, pool, c.Slug)

	link1 := seedLinkWithCampaign(t, pool, alice, "agg0001", "https://example.com/1", &campID)
	link2 := seedLinkWithCampaign(t, pool, alice, "agg0002", "https://example.com/2", &campID)
	seedClickForHandler(t, pool, link1, campID, false)
	seedClickForHandler(t, pool, link1, campID, false)
	seedClickForHandler(t, pool, link2, campID, false)
	seedClickForHandler(t, pool, link2, campID, true) // bot, excluded

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body listCampaignsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Campaigns) != 1 {
		t.Fatalf("campaigns = %+v, want exactly 1", body.Campaigns)
	}
	got := body.Campaigns[0]
	if got.LinkCount != 2 {
		t.Errorf("link_count = %d, want 2", got.LinkCount)
	}
	if got.TotalClicks != 3 {
		t.Errorf("total_clicks = %d, want 3 (bot click excluded)", got.TotalClicks)
	}

	// Cross-check link_count AND total_clicks against the dedicated
	// links-in-campaign endpoint — linkView already carries each link's own
	// (independently-queried) click_count, so summing it here proves
	// campaigns.total_clicks agrees with links.Store's own numbers rather
	// than merely matching whatever absolute number this test expects.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/links", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	var linksBody campaignLinksResponse
	if err := json.NewDecoder(resp2.Body).Decode(&linksBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if int64(len(linksBody.Links)) != got.LinkCount {
		t.Errorf("GET .../links returned %d links, want link_count (%d) to match", len(linksBody.Links), got.LinkCount)
	}
	var linksClickTotal int64
	for _, l := range linksBody.Links {
		linksClickTotal += l.ClickCount
	}
	if linksClickTotal != got.TotalClicks {
		t.Errorf("sum of linkView.click_count over GET .../links = %d, want total_clicks (%d) to match", linksClickTotal, got.TotalClicks)
	}
}

// TestCampaignsList_BotOnlyCampaignStillAppears is the #0101 LEFT JOIN...ON-
// vs-WHERE trap, asserted directly at the endpoint: a campaign whose every
// click is bot-flagged must still appear in the list, with total_clicks = 0.
func TestCampaignsList_BotOnlyCampaignStillAppears(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Bot Only"}`)
	campID := campaignRowID(t, pool, c.Slug)
	link := seedLinkWithCampaign(t, pool, alice, "botonly1", "https://example.com", &campID)
	seedClickForHandler(t, pool, link, campID, true)
	seedClickForHandler(t, pool, link, campID, true)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body listCampaignsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Campaigns) != 1 {
		t.Fatalf("bot-only campaign vanished from the list: %+v", body.Campaigns)
	}
	if body.Campaigns[0].TotalClicks != 0 {
		t.Errorf("total_clicks = %d, want 0", body.Campaigns[0].TotalClicks)
	}
	if body.Campaigns[0].LinkCount != 1 {
		t.Errorf("link_count = %d, want 1", body.Campaigns[0].LinkCount)
	}
}

// TestCampaignsGet_OwnershipEnforced asserts user A cannot read user B's
// campaign by slug: GET returns 404, indistinguishable from a nonexistent
// slug.
func TestCampaignsGet_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	c := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob GET alice's campaign status = %d, want 404", resp.StatusCode)
	}

	// Confirm alice herself can still read it.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("alice GET own campaign status = %d, want 200", resp2.StatusCode)
	}
}

// TestCampaignsGet_ReturnsLinksAndStats asserts GET /api/campaigns/{slug}
// (#0102) additively extends the plain metadata response with link_count/
// total_clicks, the campaign's member links, and — since a stats provider is
// wired in campaignsMux — a stats object and a timeseries object, mirroring
// GET /api/links/{key}'s detail+utm_stats+timeseries shape.
func TestCampaignsGet_ReturnsLinksAndStats(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Detail"}`)
	campID := campaignRowID(t, pool, c.Slug)
	link := seedLinkWithCampaign(t, pool, alice, "detail01", "https://example.com", &campID)
	seedClickForHandler(t, pool, link, campID, false)
	seedClickForHandler(t, pool, link, campID, false)
	seedClickForHandler(t, pool, link, campID, true) // bot, excluded

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if lc, _ := body["link_count"].(float64); lc != 1 {
		t.Errorf("link_count = %v, want 1", body["link_count"])
	}
	if tc, _ := body["total_clicks"].(float64); tc != 2 {
		t.Errorf("total_clicks = %v, want 2 (bot click excluded)", body["total_clicks"])
	}
	links, ok := body["links"].([]any)
	if !ok || len(links) != 1 {
		t.Fatalf("links = %#v, want a 1-element array", body["links"])
	}
	linkObj, ok := links[0].(map[string]any)
	if !ok || linkObj["key"] != "detail01" {
		t.Errorf("links[0] = %#v, want key=detail01", links[0])
	}

	statsObj, ok := body["stats"].(map[string]any)
	if !ok {
		t.Fatalf("stats field missing or not an object: %#v", body["stats"])
	}
	if cc, _ := statsObj["click_count"].(float64); cc != 2 {
		t.Errorf("stats.click_count = %v, want 2", statsObj["click_count"])
	}
	if _, ok := body["timeseries"]; !ok {
		t.Error("timeseries field missing from GET /api/campaigns/{slug} response")
	}
}

// TestCampaignsGet_TotalClicksIsAllTimeStatsClickCountIsWindowed pins the
// split campaignDetailView's doc comment documents: total_clicks (from
// campaigns.Store, #0102) is ALL-TIME, while stats.click_count (from
// clicks.StatsStore, windowed via CampaignSummary) is NOT — so the two can
// legitimately disagree in the SAME response. Reproduces the review's exact
// scenario: a campaign with 5 clicks from 60 days ago and no starts_at/
// ends_at set (so the default window is the last 30 days) shows
// total_clicks=5 beside stats.click_count=0 and timeseries.days=[]. #0103/
// #0104 must not assume these two numbers are interchangeable; this test
// exists so a future change that makes them silently agree (e.g. by
// windowing total_clicks, or by widening the default window) is a
// deliberate, reviewed decision rather than an accidental behavior change.
func TestCampaignsGet_TotalClicksIsAllTimeStatsClickCountIsWindowed(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Old Clicks"}`)
	campID := campaignRowID(t, pool, c.Slug)
	link := seedLinkWithCampaign(t, pool, alice, "old00001", "https://example.com", &campID)

	// 5 clicks, 60 days ago — well outside the default 30-day window, but
	// still real, all-time history.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, campaign_id, is_bot, clicked_at)
		 SELECT $1, $2, FALSE, now() - interval '60 days' FROM generate_series(1, 5)`,
		link, campID,
	); err != nil {
		t.Fatalf("seed old clicks: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if tc, _ := body["total_clicks"].(float64); tc != 5 {
		t.Errorf("total_clicks = %v, want 5 (all-time)", body["total_clicks"])
	}
	statsObj, ok := body["stats"].(map[string]any)
	if !ok {
		t.Fatalf("stats field missing or not an object: %#v", body["stats"])
	}
	if cc, _ := statsObj["click_count"].(float64); cc != 0 {
		t.Errorf("stats.click_count = %v, want 0 (windowed to the last 30 days; the clicks are 60 days old)", statsObj["click_count"])
	}
	ts, ok := body["timeseries"].(map[string]any)
	if !ok {
		t.Fatalf("timeseries field missing or not an object: %#v", body["timeseries"])
	}
	days, ok := ts["days"].([]any)
	if !ok || len(days) != 0 {
		t.Errorf("timeseries.days = %#v, want empty (the clicks fall outside the windowed default)", ts["days"])
	}
}

// TestCampaignsGet_EmptyCampaignReturnsEmptyLinksNotNull asserts a campaign
// with no links yields "links": [] rather than null, matching this file's
// non-nil-empty-slice convention throughout.
func TestCampaignsGet_EmptyCampaignReturnsEmptyLinksNotNull(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Empty"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	links, present := body["links"]
	if !present {
		t.Fatal("\"links\" key missing")
	}
	arr, ok := links.([]any)
	if !ok {
		t.Fatalf("links = %#v (%T), want a JSON array, not null", links, links)
	}
	if len(arr) != 0 {
		t.Errorf("links = %#v, want empty", arr)
	}
}

// TestCampaignsStats_ReturnsRollup asserts GET /api/campaigns/{slug}/stats
// (#0102) returns the combined rollup: totals/breakdowns (embedded from
// CampaignStats), timeseries, by_link, and series_by_link.
func TestCampaignsStats_ReturnsRollup(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Stats"}`)
	campID := campaignRowID(t, pool, c.Slug)
	link := seedLinkWithCampaign(t, pool, alice, "stats001", "https://example.com", &campID)
	seedClickForHandler(t, pool, link, campID, false)
	seedClickForHandler(t, pool, link, campID, false)
	seedClickForHandler(t, pool, link, campID, true)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if cc, _ := body["click_count"].(float64); cc != 2 {
		t.Errorf("click_count = %v, want 2 (bot click excluded)", body["click_count"])
	}
	if ebc, _ := body["excluded_bot_count"].(float64); ebc != 1 {
		t.Errorf("excluded_bot_count = %v, want 1", body["excluded_bot_count"])
	}
	for _, key := range []string{"by_source", "by_medium", "by_content", "by_referer", "timeseries", "by_link", "series_by_link"} {
		if _, present := body[key]; !present {
			t.Errorf("%q key missing from stats response", key)
		}
	}
	byLink, ok := body["by_link"].([]any)
	if !ok || len(byLink) != 1 {
		t.Fatalf("by_link = %#v, want a 1-element array", body["by_link"])
	}
}

// TestCampaignsStats_OwnershipEnforced asserts user A cannot read stats for
// user B's campaign: 404, indistinguishable from a nonexistent slug.
func TestCampaignsStats_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Alice Stats"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats", nil)
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob GET alice's campaign stats status = %d, want 404", resp.StatusCode)
	}

	// Confirm alice herself can still read it.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("alice GET own campaign stats status = %d, want 200", resp2.StatusCode)
	}
}

// TestCampaignsStats_InvalidFromDateRejected asserts an unparseable ?from=
// value is a 400, not silently ignored.
func TestCampaignsStats_InvalidFromDateRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Bad Date"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats?from=not-a-date", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCampaignsStats_InvalidToDateRejected mirrors
// TestCampaignsStats_InvalidFromDateRejected for ?to=, which had no test of
// its own before this (review item 4).
func TestCampaignsStats_InvalidToDateRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Bad To Date"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats?to=not-a-date", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCampaignsStats_ToBeforeFromRejected asserts a ?to= earlier than ?from=
// (both present) is a 400, not silently accepted as an always-empty window.
func TestCampaignsStats_ToBeforeFromRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Inverted Window"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats?from=2026-06-10&to=2026-06-01", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (to before from)", resp.StatusCode)
	}
}

// TestCampaignsStats_ExplicitWindowReachesStore is review item 4's guard:
// ?from=/?to= were being parsed and forwarded to the store already, but no
// test asserted the parsed values actually reached CampaignRollup rather
// than being silently discarded in favor of the default window — a mutation
// that discards them left every other handler test green. The campaign has
// no starts_at/ends_at (default window = last 30 days), and the seeded
// click is 45 days old — OUTSIDE that default. An explicit ?from=/?to=
// window wide enough to include it (60 days back through today) must
// return click_count=1; if the handler silently used the default instead,
// this would report 0.
func TestCampaignsStats_ExplicitWindowReachesStore(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Explicit Window"}`)
	campID := campaignRowID(t, pool, c.Slug)
	link := seedLinkWithCampaign(t, pool, alice, "explw001", "https://example.com", &campID)

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, campaign_id, is_bot, clicked_at)
		 VALUES ($1, $2, FALSE, now() - interval '45 days')`,
		link, campID,
	); err != nil {
		t.Fatalf("seed old click: %v", err)
	}

	from := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
	to := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats?from="+from+"&to="+to, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cc, _ := body["click_count"].(float64); cc != 1 {
		t.Errorf("click_count = %v, want 1 — the explicit ?from=/?to= window (which includes the 45-day-old click) did not reach the store; "+
			"the default 30-day window would report 0", body["click_count"])
	}
}

// TestCampaignsPatch_OwnershipEnforced asserts user A cannot update user B's
// campaign: PATCH returns 404 and the campaign is left unchanged.
func TestCampaignsPatch_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	c := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/campaigns/"+c.Slug, jsonBody(`{"name":"Hijacked"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob PATCH alice's campaign status = %d, want 404", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	var got campaignView
	if err := json.NewDecoder(resp2.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "Alice Campaign" {
		t.Errorf("name = %q, want unchanged %q", got.Name, "Alice Campaign")
	}
}

// TestCampaignsPatch_ArchiveIsReversible asserts PATCH {"archived":true} then
// PATCH {"archived":false} round-trips, unlike a delete.
func TestCampaignsPatch_ArchiveIsReversible(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	patch := func(body string) campaignView {
		req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/campaigns/"+c.Slug, jsonBody(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("patch status = %d, want 200", resp.StatusCode)
		}
		var got campaignView
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got
	}

	archived := patch(`{"archived":true}`)
	if !archived.Archived {
		t.Fatalf("archived = false after PATCH {archived:true}")
	}
	unarchived := patch(`{"archived":false}`)
	if unarchived.Archived {
		t.Errorf("archived = true after PATCH {archived:false}, want reversible")
	}
}

// TestCampaignsDelete_OwnershipEnforced asserts user A cannot delete user B's
// campaign: DELETE returns 404 and the campaign still exists for its owner.
func TestCampaignsDelete_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	c := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob DELETE alice's campaign status = %d, want 404", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("alice's campaign missing after bob's failed delete: status = %d, want 200", resp2.StatusCode)
	}
}

// TestCampaignsDelete_RemovesCampaign asserts a successful delete by the
// owner is permanent: a subsequent GET 404s.
func TestCampaignsDelete_RemovesCampaign(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", resp.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug, nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete status = %d, want 404", resp2.StatusCode)
	}
}

// TestCampaignsSlug_UniquePerUserNotGlobal asserts two different users can
// each hold a campaign with the same slug over the HTTP path.
func TestCampaignsSlug_UniquePerUserNotGlobal(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	a := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	b := createCampaign(t, srv, "bob-token", `{"name":"Summer Fair"}`)
	if a.Slug != "summer-fair" || b.Slug != "summer-fair" {
		t.Errorf("slugs = %q, %q, want both %q", a.Slug, b.Slug, "summer-fair")
	}
}

// TestCampaignsUnauthenticated_401 asserts every route requires a session.
func TestCampaignsUnauthenticated_401(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/campaigns"},
		{http.MethodPost, "/api/campaigns"},
		{http.MethodGet, "/api/campaigns/some-slug"},
		{http.MethodPatch, "/api/campaigns/some-slug"},
		{http.MethodDelete, "/api/campaigns/some-slug"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, jsonBody(`{}`))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

// ── #0099: link membership (list/assign/unassign) ───────────────────────────

// TestCampaignsLinksUnauthenticated_401 asserts the three link-membership
// routes also require a session, matching TestCampaignsUnauthenticated_401.
func TestCampaignsLinksUnauthenticated_401(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/campaigns/some-slug/links"},
		{http.MethodPost, "/api/campaigns/some-slug/links"},
		{http.MethodDelete, "/api/campaigns/some-slug/links/some-key"},
		{http.MethodPost, "/api/campaigns/some-slug/links/batch"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, jsonBody(`{}`))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestCampaignsListLinks_ReturnsAssignedLinksOnly asserts GET
// /api/campaigns/{slug}/links returns exactly the links currently assigned
// to that campaign, not the caller's other links.
func TestCampaignsListLinks_ReturnsAssignedLinksOnly(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	campaignID := campaignRowID(t, pool, c.Slug)
	seedLinkWithCampaign(t, pool, alice, "incamp", "https://example.com/1", &campaignID)
	seedLink(t, pool, alice, "notincamp", "https://example.com/2")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/links", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body campaignLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Links) != 1 || body.Links[0].Key != "incamp" {
		t.Errorf("links = %+v, want exactly [incamp]", body.Links)
	}
}

// TestCampaignsListLinks_ClickCountExcludesBotsAndBotOnlyLinkStillAppears
// mirrors TestLinksList_ClickCountExcludesBotsAndBotOnlyLinkStillAppears for
// ListLinksForCampaign (#0101 review): it shares the exact same LEFT JOIN
// shape as ListLinks, so it is exposed to the exact same ON-vs-WHERE trap —
// a link assigned to the campaign whose only clicks are bot clicks must
// still appear in the campaign's link list, with click_count = 0.
func TestCampaignsListLinks_ClickCountExcludesBotsAndBotOnlyLinkStillAppears(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Bot Trap"}`)
	campaignID := campaignRowID(t, pool, c.Slug)

	botOnly := seedLinkWithCampaign(t, pool, alice, "cbotonly", "https://example.com/bot", &campaignID)
	seedBotClick(t, pool, botOnly)
	seedBotClick(t, pool, botOnly)

	mixed := seedLinkWithCampaign(t, pool, alice, "cmixed", "https://example.com/mixed", &campaignID)
	seedClick(t, pool, mixed)
	seedBotClick(t, pool, mixed)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/links", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body campaignLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Links) != 2 {
		t.Fatalf("links = %+v, want 2 (bot-only link must not be dropped from its campaign's list)", body.Links)
	}
	byKey := map[string]int64{}
	for _, l := range body.Links {
		byKey[l.Key] = l.ClickCount
	}
	if count, ok := byKey["cbotonly"]; !ok || count != 0 {
		t.Errorf("cbotonly click_count = %d (present=%v), want 0", count, ok)
	}
	if count, ok := byKey["cmixed"]; !ok || count != 1 {
		t.Errorf("cmixed click_count = %d (present=%v), want 1", count, ok)
	}
}

// TestCampaignsListLinks_OwnershipEnforced asserts user A cannot list user
// B's campaign's links: 404, indistinguishable from a nonexistent slug.
func TestCampaignsListLinks_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/links", nil)
	resp, err := srv.Client().Do(withCookie(req, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("bob list alice's campaign links status = %d, want 404", resp.StatusCode)
	}
}

// TestCampaignsAssignLinks_AssignsAndMovesAlreadyAssigned covers both the
// happy path and the DECIDED "moves it" behavior at the HTTP layer: assigning
// a link already in campaign A into campaign B succeeds (200) and the link
// ends up in B.
func TestCampaignsAssignLinks_AssignsAndMovesAlreadyAssigned(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	a := createCampaign(t, srv, "alice-token", `{"name":"Campaign A"}`)
	b := createCampaign(t, srv, "alice-token", `{"name":"Campaign B"}`)
	seedLink(t, pool, alice, "mylink", "https://example.com")

	assign := func(slug, key string) (campaignLinksResponse, int) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+slug+"/links", jsonBody(`{"key":"`+key+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		var body campaignLinksResponse
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return body, resp.StatusCode
	}

	got, status := assign(a.Slug, "mylink")
	if status != http.StatusOK {
		t.Fatalf("assign to A status = %d, want 200", status)
	}
	if len(got.Links) != 1 || got.Links[0].CampaignID == nil || *got.Links[0].CampaignID != a.ID {
		t.Fatalf("after assign to A, campaign_id = %+v, want %d", got.Links, a.ID)
	}

	got, status = assign(b.Slug, "mylink")
	if status != http.StatusOK {
		t.Fatalf("assign to B (already in A) status = %d, want 200 (moves, does not reject)", status)
	}
	if len(got.Links) != 1 || got.Links[0].CampaignID == nil || *got.Links[0].CampaignID != b.ID {
		t.Fatalf("after assign to B, campaign_id = %+v, want %d (moved, not left in A)", got.Links, b.ID)
	}
}

// TestCampaignsAssignLinks_CampaignOwnershipEnforced asserts user A cannot
// assign ANY link into user B's campaign: 404, and no link is assigned.
func TestCampaignsAssignLinks_CampaignOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)
	seedLink(t, pool, alice, "alices-link", "https://example.com")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+bobCampaign.Slug+"/links", jsonBody(`{"key":"alices-link"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("alice assign into bob's campaign status = %d, want 404", resp.StatusCode)
	}
	if _, _, _, found := linkRow(t, pool, "alices-link"); !found {
		t.Fatal("alice's link disappeared")
	}
	campaignID := campaignRowID(t, pool, bobCampaign.Slug)
	linked := linksInCampaign(t, pool, campaignID)
	if len(linked) != 0 {
		t.Errorf("bob's campaign has links after alice's rejected assign: %v", linked)
	}
}

// TestCampaignsAssignLinks_LinkOwnershipEnforced asserts user A cannot
// assign user B's link into A's OWN campaign: 404, and bob's link is
// untouched (still unassigned).
func TestCampaignsAssignLinks_LinkOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	aliceCampaign := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)
	seedLink(t, pool, bob, "bobs-link", "https://example.com")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+aliceCampaign.Slug+"/links", jsonBody(`{"key":"bobs-link"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("alice assign bob's link status = %d, want 404", resp.StatusCode)
	}

	var campaignID *int64
	if err := pool.QueryRow(context.Background(), `SELECT campaign_id FROM links WHERE key = $1`, "bobs-link").Scan(&campaignID); err != nil {
		t.Fatalf("reading bob's link: %v", err)
	}
	if campaignID != nil {
		t.Errorf("bob's link campaign_id = %v, want still NULL", *campaignID)
	}
}

// TestCampaignsAssignLinks_KeysCapEnforced asserts a request naming more
// than maxAssignLinksKeys keys is rejected with 400 before any DB work runs
// for those keys — review item 8, since each key costs multiple sequential
// queries with no upper bound otherwise.
func TestCampaignsAssignLinks_KeysCapEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	keys := make([]string, maxAssignLinksKeys+1)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d", i)
	}
	body, err := json.Marshal(map[string][]string{"keys": keys})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+c.Slug+"/links", jsonBody(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%d keys exceeds the %d cap)", resp.StatusCode, len(keys), maxAssignLinksKeys)
	}
}

// TestCampaignsAssignLinks_PartialFailureIsNonAtomic asserts the documented
// non-atomic semantics: keys are processed in the given order, and a key
// that fails ownership stops the request WITHOUT rolling back keys already
// assigned earlier in the same call.
func TestCampaignsAssignLinks_PartialFailureIsNonAtomic(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	seedLink(t, pool, alice, "alicelnk", "https://example.com/alice")
	seedLink(t, pool, bob, "boblink1", "https://example.com/bob")

	// alice's own key first (should succeed), then bob's key (should 404) —
	// the request as a whole must report the failure, but alice's key stays
	// assigned rather than being rolled back.
	body := `{"keys":["alicelnk","boblink1"]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+c.Slug+"/links", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (second key is bob's, not alice's)", resp.StatusCode)
	}

	var got *int64
	if err := pool.QueryRow(context.Background(), `SELECT campaign_id FROM links WHERE key = $1`, "alicelnk").Scan(&got); err != nil {
		t.Fatalf("reading alice's link: %v", err)
	}
	campaignID := campaignRowID(t, pool, c.Slug)
	if got == nil || *got != campaignID {
		t.Errorf("alice's link campaign_id = %v, want %d (NOT rolled back — non-atomic across keys, as documented)", got, campaignID)
	}
}

// TestCampaignsUnassignLink_ClearsAssignment asserts DELETE
// /api/campaigns/{slug}/links/{key} clears the link's campaign_id and
// returns 200.
func TestCampaignsUnassignLink_ClearsAssignment(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)
	campaignID := campaignRowID(t, pool, c.Slug)
	seedLinkWithCampaign(t, pool, alice, "mylink", "https://example.com", &campaignID)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/campaigns/"+c.Slug+"/links/mylink", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got *int64
	if err := pool.QueryRow(context.Background(), `SELECT campaign_id FROM links WHERE key = $1`, "mylink").Scan(&got); err != nil {
		t.Fatalf("reading link: %v", err)
	}
	if got != nil {
		t.Errorf("campaign_id = %v, want NULL", *got)
	}
}

// TestCampaignsUnassignLink_CampaignOwnershipEnforced asserts user A cannot
// unassign a link via user B's campaign slug: 404.
func TestCampaignsUnassignLink_CampaignOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)
	bobCampaignID := campaignRowID(t, pool, bobCampaign.Slug)
	seedLinkWithCampaign(t, pool, bob, "bobs-link", "https://example.com", &bobCampaignID)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/campaigns/"+bobCampaign.Slug+"/links/bobs-link", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("alice unassign via bob's campaign status = %d, want 404", resp.StatusCode)
	}

	var got *int64
	if err := pool.QueryRow(context.Background(), `SELECT campaign_id FROM links WHERE key = $1`, "bobs-link").Scan(&got); err != nil {
		t.Fatalf("reading bob's link: %v", err)
	}
	if got == nil || *got != bobCampaignID {
		t.Errorf("bob's link campaign_id = %v, want still %d (unaffected)", got, bobCampaignID)
	}
}

// TestCampaignsUnassignLink_LinkOwnershipEnforced asserts user A cannot
// unassign user B's link via A's OWN campaign slug: 404.
//
// LOAD-BEARING FIXTURE (review-caught vacuous test): seeding bob's link
// UNASSIGNED would make this pass even with every ownership check deleted,
// because UnassignLinkFromCampaign's own `AND campaign_id = $3` already
// rejects a link that isn't currently in alice's campaign — the request
// would 404 for a reason that has nothing to do with WHO owns the link.
// Instead, bob's link is seeded with campaign_id ALREADY set to alice's
// campaign via raw SQL — a state the application can never produce itself
// (AssignLinkToCampaign checks link ownership before ever writing it), but
// exactly what a real ownership bypass elsewhere would leave behind. With
// that fixture, the store-level campaign_id match SUCCEEDS, so a 404 can
// only come from the handler's own link-ownership check (GetLink, scoped to
// alice) OR the store's `WHERE user_id = $2` on the UPDATE — this test is
// load-bearing against that PAIR of gates, not either alone.
//
// Confirmed by mutation, and note the asymmetry: removing EITHER gate on its
// own leaves this test passing, because the other still rejects and the
// response is a 404 either way (0 rows affected maps to
// campaigns.ErrLinkNotFound, which the handler renders as 404 — see
// campaigns.go's unassign path). Removing BOTH gates at once yields 200 and
// fails this test on both its status assertion and its DB-state assertion.
// So a green run here proves the pair holds; it does not prove either gate
// individually, and anyone deleting one of them must not read this test's
// passing as cover.
func TestCampaignsUnassignLink_LinkOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	aliceCampaign := createCampaign(t, srv, "alice-token", `{"name":"Alice Campaign"}`)
	aliceCampaignID := campaignRowID(t, pool, aliceCampaign.Slug)
	seedLinkWithCampaign(t, pool, bob, "bobs-link", "https://example.com", &aliceCampaignID)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/campaigns/"+aliceCampaign.Slug+"/links/bobs-link", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("alice unassign bob's link via her own campaign status = %d, want 404", resp.StatusCode)
	}

	// Confirm the request truly did nothing: bob's link is still assigned to
	// alice's campaign at the DB level (unaffected), not merely that the
	// HTTP status looked right.
	var got *int64
	if err := pool.QueryRow(context.Background(), `SELECT campaign_id FROM links WHERE key = $1`, "bobs-link").Scan(&got); err != nil {
		t.Fatalf("reading bob's link: %v", err)
	}
	if got == nil || *got != aliceCampaignID {
		t.Errorf("bob's link campaign_id = %v, want still %d (unaffected by alice's rejected request)", got, aliceCampaignID)
	}
}

// campaignRowID reads a campaign's id by slug directly, for test fixtures
// that need the numeric id to seed a link's campaign_id.
func campaignRowID(t *testing.T, pool *pgxpool.Pool, slug string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM campaigns WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatalf("reading campaign id for slug %q: %v", slug, err)
	}
	return id
}

// seedLinkWithCampaign is seedLink (links_test.go) plus an initial
// campaign_id, for fixtures that need a link already assigned.
func seedLinkWithCampaign(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string, campaignID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, campaign_id)
		 VALUES ($1, $2, $3, TRUE, 0, now(), $4) RETURNING id`,
		userID, key, dest, campaignID,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

// linksInCampaign returns the keys of every link currently assigned to
// campaignID, for assertions that a rejected assign left no trace.
func linksInCampaign(t *testing.T, pool *pgxpool.Pool, campaignID int64) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT key FROM links WHERE campaign_id = $1`, campaignID)
	if err != nil {
		t.Fatalf("querying links in campaign: %v", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scanning key: %v", err)
		}
		keys = append(keys, k)
	}
	return keys
}

// ── Batch create (#0105) ────────────────────────────────────────────────

// batchCreate POSTs a batch-create request and returns the decoded response
// body plus the raw status code, so callers can assert either a success body
// or an error status without duplicating the request plumbing.
func batchCreate(t *testing.T, srv *httptest.Server, token, slug, body string) (batchCreateLinksResponse, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+slug+"/links/batch", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var got batchCreateLinksResponse
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return got, resp.StatusCode
}

// TestCampaignsBatchCreate_CreatesOneLinkPerNonBlankRow asserts N filled-in
// rows produce N links in ONE call, each carrying its own discrete UTM
// columns/placement/title AND the campaign's id — matching what single
// create records (#0099), and each with a unique generated key.
func TestCampaignsBatchCreate_CreatesOneLinkPerNonBlankRow(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/promo?utm_source=newsletter","title":"Newsletter blast","utm_source":"newsletter","utm_medium":"email","utm_campaign":"summer-fair","utm_content":"hero-cta"},
		{"destination_url":"https://example.com/promo?utm_source=twitter","title":"Tweet","utm_source":"twitter","utm_medium":"social","utm_campaign":"summer-fair","utm_content":"launch-post"},
		{"destination_url":"https://example.com/promo","title":"Flyer","utm_source":"flyer","utm_medium":"print","utm_campaign":"summer-fair","placement":"18th & Texas board"}
	]}`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if len(got.Links) != 3 {
		t.Fatalf("links = %d, want 3", len(got.Links))
	}
	if got.SkippedBlankRows != 0 {
		t.Errorf("skipped_blank_rows = %d, want 0", got.SkippedBlankRows)
	}

	keys := make(map[string]bool, 3)
	campaignID := campaignRowID(t, pool, c.Slug)
	for i, l := range got.Links {
		if keys[l.Key] {
			t.Errorf("row %d: key %q reused", i, l.Key)
		}
		keys[l.Key] = true
		if l.CampaignID == nil || *l.CampaignID != campaignID {
			t.Errorf("row %d: campaign_id = %v, want %d", i, l.CampaignID, campaignID)
		}
		if !l.Active || l.DeniedReason != 0 {
			t.Errorf("row %d: active=%v denied=%d, want active=true denied=0", i, l.Active, l.DeniedReason)
		}
	}
	if got.Links[2].Placement != "18th & Texas board" {
		t.Errorf("row 2 placement = %q, want %q", got.Links[2].Placement, "18th & Texas board")
	}

	inCampaign := linksInCampaign(t, pool, campaignID)
	if len(inCampaign) != 3 {
		t.Errorf("links in campaign = %d, want 3", len(inCampaign))
	}
}

// TestCampaignsBatchCreate_BlankRowSkipped asserts a row left entirely
// empty (source/medium/content/placement/title all blank — utm_campaign
// alone, matching the shared campaign default, must NOT count) is skipped
// rather than creating a link with empty UTM values, and is reported via
// skipped_blank_rows rather than silently vanishing.
func TestCampaignsBatchCreate_BlankRowSkipped(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/promo","utm_source":"newsletter","utm_medium":"email","utm_campaign":"summer-fair"},
		{"destination_url":"https://example.com/promo","utm_campaign":"summer-fair"},
		{"destination_url":"https://example.com/promo","utm_source":"twitter","utm_medium":"social","utm_campaign":"summer-fair"}
	]}`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if len(got.Links) != 2 {
		t.Fatalf("links = %d, want 2 (the blank middle row must be skipped, not create an empty-UTM link)", len(got.Links))
	}
	if got.SkippedBlankRows != 1 {
		t.Errorf("skipped_blank_rows = %d, want 1", got.SkippedBlankRows)
	}
	for _, l := range got.Links {
		if l.UTMSource == "" {
			t.Errorf("created link %q has empty utm_source — the blank row was not the one skipped", l.Key)
		}
	}
}

// TestCampaignsBatchCreate_TwoRowsDifferingOnlyInPlacementCreateTwoLinks is
// the CENTRAL test for #0105 at the HTTP boundary — the confirmed trap from
// #0099's review: two rows sharing a byte-identical destination_url
// (placement is never baked into the URL) must produce TWO links, not one.
// See links.Store.CreateLinksBatch's doc comment for the store-level version
// of this same test and its manual mutation check.
func TestCampaignsBatchCreate_TwoRowsDifferingOnlyInPlacementCreateTwoLinks(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	const sharedDest = "https://example.com/summer-sale?utm_source=flyer&utm_medium=print"
	body := `{"rows":[
		{"destination_url":"` + sharedDest + `","utm_source":"flyer","utm_medium":"print","placement":"18th & Texas board"},
		{"destination_url":"` + sharedDest + `","utm_source":"flyer","utm_medium":"print","placement":"Congress & 6th board"}
	]}`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if len(got.Links) != 2 {
		t.Fatalf("links = %d, want 2 (dedup must be bypassed — see the trap this issue exists to fix)", len(got.Links))
	}
	if got.Links[0].Key == got.Links[1].Key {
		t.Fatal("both rows got the SAME key")
	}
	if got.Links[0].DestinationURL != got.Links[1].DestinationURL {
		t.Errorf("destination_url differs (%q vs %q) — the test setup requires them identical",
			got.Links[0].DestinationURL, got.Links[1].DestinationURL)
	}
	if got.Links[0].Placement == got.Links[1].Placement {
		t.Fatal("both rows report the same placement")
	}

	campaignID := campaignRowID(t, pool, c.Slug)
	inCampaign := linksInCampaign(t, pool, campaignID)
	if len(inCampaign) != 2 {
		t.Fatalf("links in campaign = %d, want 2", len(inCampaign))
	}
}

// TestCampaignsBatchCreate_DuplicateRowsRejectedAtomically asserts two rows
// identical in source+medium+content+placement (a genuine accidental
// double-entry, NOT the placement-only case above) are rejected with 400 and
// that NOTHING from the batch — including the row(s) that would otherwise
// have been fine — is created.
func TestCampaignsBatchCreate_DuplicateRowsRejectedAtomically(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/a","utm_source":"newsletter","utm_medium":"email","utm_content":"hero-cta"},
		{"destination_url":"https://example.com/b","utm_source":"newsletter","utm_medium":"email","utm_content":"hero-cta"}
	]}`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if len(got.Links) != 0 {
		t.Errorf("links = %d, want 0 (rejected atomically before any insert)", len(got.Links))
	}

	campaignID := campaignRowID(t, pool, c.Slug)
	if inCampaign := linksInCampaign(t, pool, campaignID); len(inCampaign) != 0 {
		t.Errorf("links in campaign = %v, want none", inCampaign)
	}
}

// TestCampaignsBatchCreate_CapEnforced asserts a request over
// maxBatchCreateRows non-blank rows is rejected with 400 and creates
// nothing, mirroring TestCampaignsAssignLinks_KeysCapEnforced.
func TestCampaignsBatchCreate_CapEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	var b strings.Builder
	b.WriteString(`{"rows":[`)
	for i := 0; i < maxBatchCreateRows+1; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"destination_url":"https://example.com/x","utm_source":"s%d","utm_medium":"email"}`, i)
	}
	b.WriteString(`]}`)

	got, status := batchCreate(t, srv, "alice-token", c.Slug, b.String())
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%d rows exceeds the %d cap)", status, maxBatchCreateRows+1, maxBatchCreateRows)
	}
	if len(got.Links) != 0 {
		t.Errorf("links = %d, want 0", len(got.Links))
	}
	campaignID := campaignRowID(t, pool, c.Slug)
	if inCampaign := linksInCampaign(t, pool, campaignID); len(inCampaign) != 0 {
		t.Errorf("links in campaign = %v, want none", inCampaign)
	}
}

// TestCampaignsBatchCreate_InvalidDestinationURLRejectsWholeBatch asserts
// one row with a syntactically invalid destination_url fails the ENTIRE
// batch (400) with nothing created — including the otherwise-valid row(s)
// alongside it, per the atomic decision.
func TestCampaignsBatchCreate_InvalidDestinationURLRejectsWholeBatch(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/good","utm_source":"newsletter","utm_medium":"email"},
		{"destination_url":"not-a-url","utm_source":"twitter","utm_medium":"social"}
	]}`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if len(got.Links) != 0 {
		t.Errorf("links = %d, want 0", len(got.Links))
	}
	campaignID := campaignRowID(t, pool, c.Slug)
	if inCampaign := linksInCampaign(t, pool, campaignID); len(inCampaign) != 0 {
		t.Errorf("links in campaign = %v, want none (the good row must not survive alongside the bad one)", inCampaign)
	}
}

// TestCampaignsBatchCreate_CampaignOwnershipEnforced asserts a slug
// belonging to another user 404s and creates nothing — the same
// indistinguishable-404 contract every other campaign endpoint uses.
func TestCampaignsBatchCreate_CampaignOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)

	body := `{"rows":[{"destination_url":"https://example.com/x","utm_source":"newsletter","utm_medium":"email"}]}`
	got, status := batchCreate(t, srv, "alice-token", bobCampaign.Slug, body)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if len(got.Links) != 0 {
		t.Errorf("links = %d, want 0", len(got.Links))
	}
	bobCampaignID := campaignRowID(t, pool, bobCampaign.Slug)
	if inCampaign := linksInCampaign(t, pool, bobCampaignID); len(inCampaign) != 0 {
		t.Errorf("links in bob's campaign = %v, want none", inCampaign)
	}
}

// TestCampaignsBatchCreate_AllBlankRowsRejected asserts a request whose
// every row is blank (e.g. the user clicked "Create" without filling in
// anything) is rejected with 400 rather than silently succeeding with zero
// links created.
func TestCampaignsBatchCreate_AllBlankRowsRejected(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[{"destination_url":"https://example.com/x"},{"destination_url":"https://example.com/x"}]}`
	_, status := batchCreate(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (every row is blank)", status)
	}
}

// campaignsBatchFilterMux mirrors filterLinksMux (url_filters_test.go) but
// for the campaigns batch-create route: the given ruleCache is wired into
// NewCampaignsHandler so BatchCreateLinks' #0024 filter check runs against
// the live DB-backed rules.
func campaignsBatchFilterMux(t *testing.T, pool *pgxpool.Pool, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	return campaignsMuxWithRules(t, pool, ruleCache)
}

// TestCampaignsBatchCreate_FilterDeniedRowRejectsWholeBatch asserts a batch
// containing one row whose destination_url matches an active URL-filter rule
// (#0024) fails the WHOLE request with 422, creating nothing — including any
// good rows alongside it, per the atomic decision. Mirrors
// TestLinksCreate_FilterDeniesAndRecords's setup but asserts atomicity
// instead of the (batch-create does NOT record a denied-link row; see
// BatchCreateLinks' doc comment for why).
func TestCampaignsBatchCreate_FilterDeniedRowRejectsWholeBatch(t *testing.T) {
	pool := filterTestPool(t)
	ruleCache := newFilterRuleCache(pool)
	srv := httptest.NewServer(campaignsBatchFilterMux(t, pool, ruleCache))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedFilterRule(t, pool, `evil\.com`, int16(filters.ReasonMalware))
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/good","utm_source":"newsletter","utm_medium":"email"},
		{"destination_url":"http://evil.com/x","utm_source":"twitter","utm_medium":"social"}
	]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+c.Slug+"/links/batch", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	campaignID := campaignRowID(t, pool, c.Slug)
	if inCampaign := linksInCampaign(t, pool, campaignID); len(inCampaign) != 0 {
		t.Errorf("links in campaign = %v, want none (the good row must not survive alongside the denied one)", inCampaign)
	}
}

// campaignsAuditBatchMux mirrors campaignsBatchFilterMux but ALSO wires a
// real *audit.Logger (campaignsMuxWithRules leaves the auditor nil, since no
// other campaigns test asserts against audit_log), so
// TestCampaignsBatchCreate_FilterDeniedRowWritesAuditEntry can assert a
// link.denied row actually landed.
func campaignsAuditBatchMux(t *testing.T, pool *pgxpool.Pool, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewCampaignsHandler(campaigns.NewStore(pool), links.NewStore(pool), audit.New(pool), clicks.NewStatsStore(pool), ruleCache)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("POST /api/campaigns/{slug}/links/batch", requireSession(http.HandlerFunc(h.BatchCreateLinks)))
	return mux
}

// TestCampaignsBatchCreate_FilterDeniedRowWritesAuditEntry is review finding
// 1's regression test: a filter-denied row in a batch must write a
// link.denied audit entry, even though (unlike single-create) no denied link
// row is created to attribute it to. Asserts the entry's actor, nil
// target_id (there is no link row), and metadata (destination_url,
// campaign_slug, batch:true, and the row's ORIGINAL 1-based position).
//
// MUTATION CHECK (performed manually, not committed): commenting out the
// h.auditor.Record call in BatchCreateLinks' filter-check block makes this
// test fail at lastAuditFor's t.Fatalf("no audit_log row for action %q"),
// since nothing else in this test's flow writes a link.denied row. Restoring
// the call makes it pass again — confirming the test actually exercises the
// new Record call rather than something else in the request path.
func TestCampaignsBatchCreate_FilterDeniedRowWritesAuditEntry(t *testing.T) {
	pool := filterTestPool(t)
	ruleCache := newFilterRuleCache(pool)
	srv := httptest.NewServer(campaignsAuditBatchMux(t, pool, ruleCache))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedFilterRule(t, pool, `evil\.com`, int16(filters.ReasonMalware))
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	const blocked = "http://evil.com/x"
	// A LEADING BLANK ROW is deliberate. Without it the denied row's original
	// index (3) equals its post-blank-filter index (2), so the metadata.row
	// assertion below passes under either implementation — it could not
	// express the very defect origIndex exists to prevent (review nit 3, the
	// same shape #0104's review flagged). With the blank row the two indices
	// differ, so dropping origIndex here fails this test.
	body := `{"rows":[
		{"destination_url":"","utm_source":"","utm_medium":""},
		{"destination_url":"https://example.com/good","utm_source":"newsletter","utm_medium":"email"},
		{"destination_url":"` + blocked + `","utm_source":"twitter","utm_medium":"social"}
	]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+c.Slug+"/links/batch", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionLinkDenied)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.TargetID != nil {
		t.Errorf("target_id = %v, want nil (no denied link row is created for a batch)", *row.TargetID)
	}
	if row.TargetType == nil || *row.TargetType != audit.TargetLink {
		t.Errorf("target_type = %v, want %q", row.TargetType, audit.TargetLink)
	}
	if got := row.Metadata["destination_url"]; got != blocked {
		t.Errorf("metadata.destination_url = %v, want %q", got, blocked)
	}
	if got := row.Metadata["campaign_slug"]; got != c.Slug {
		t.Errorf("metadata.campaign_slug = %v, want %q", got, c.Slug)
	}
	if batch, _ := row.Metadata["batch"].(bool); !batch {
		t.Errorf("metadata.batch = %v, want true", row.Metadata["batch"])
	}
	// JSON numbers decode as float64 through lastAuditFor's json.Unmarshal.
	if rowNum, _ := row.Metadata["row"].(float64); rowNum != 3 {
		t.Errorf("metadata.row = %v, want 3 — the ORIGINAL submitted position of the denied row. A value of 2 means the post-blank-filter index leaked out instead of origIndex", row.Metadata["row"])
	}
}

// batchCreateError POSTs a batch-create request expected to fail and returns
// the decoded error body's "error" message alongside the status code, for
// tests asserting on the message text (row numbers) rather than just the
// status.
func batchCreateError(t *testing.T, srv *httptest.Server, token, slug, body string) (string, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/campaigns/"+slug+"/links/batch", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var got struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got.Error, resp.StatusCode
}

// TestCampaignsBatchCreate_ErrorRowNumberIsOriginalIndex is review finding
// 5's regression test: [filled, blank, blank, dup-of-1] must report the
// duplicate as "row 4 duplicates row 1" — origIndex, the row's position in
// what the client actually submitted (and what the UI labels Row 4) — not
// "row 2 duplicates row 1", which is what the row's position in the
// post-blank-filter slice used to report.
func TestCampaignsBatchCreate_ErrorRowNumberIsOriginalIndex(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Summer Fair"}`)

	body := `{"rows":[
		{"destination_url":"https://example.com/a","utm_source":"flyer","utm_medium":"print","utm_content":"c1"},
		{"destination_url":"https://example.com/a"},
		{"destination_url":"https://example.com/a"},
		{"destination_url":"https://example.com/b","utm_source":"flyer","utm_medium":"print","utm_content":"c1"}
	]}`
	msg, status := batchCreateError(t, srv, "alice-token", c.Slug, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	const want = "row 4 duplicates row 1 (same source, medium, content, and placement)"
	if msg != want {
		t.Errorf("error = %q, want %q (rows 2/3 are blank and must not shift the reported index)", msg, want)
	}

	campaignID := campaignRowID(t, pool, c.Slug)
	if inCampaign := linksInCampaign(t, pool, campaignID); len(inCampaign) != 0 {
		t.Errorf("links in campaign = %v, want none", inCampaign)
	}
}
