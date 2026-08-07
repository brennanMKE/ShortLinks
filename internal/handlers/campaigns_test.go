package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// campaignsMux builds the real route table for the campaign CRUD endpoints,
// guarded by RequireSession backed by the real *auth.Store and serving the
// real *campaigns.Store, mirroring linksMux.
func campaignsMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewCampaignsHandler(campaigns.NewStore(pool), nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("GET /api/campaigns", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Delete)))
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

// TestCampaignsList_ReturnsZeroLinkCountAndClicks asserts the list response
// carries link_count and total_clicks as PRESENT and 0 — the issue's
// deliberate scope boundary (#0102 populates real values later). This
// decodes into map[string]any rather than listCampaignsResponse: decoding
// into the typed struct cannot distinguish an absent JSON key from a present
// key with value 0 (both decode to the zero value), so a regression that adds
// `,omitempty` to the struct tags — dropping the keys from the wire entirely
// — would pass a struct-typed assertion. #0103's contract is that these keys
// are always present, so presence is checked explicitly here.
func TestCampaignsList_ReturnsZeroLinkCountAndClicks(t *testing.T) {
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
