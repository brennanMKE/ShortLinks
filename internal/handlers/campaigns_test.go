package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// campaignsMux builds the real route table for the campaign CRUD +
// link-membership endpoints, guarded by RequireSession backed by the real
// *auth.Store and serving the real *campaigns.Store/*links.Store, mirroring
// linksMux.
func campaignsMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewCampaignsHandler(campaigns.NewStore(pool), links.NewStore(pool), nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("GET /api/campaigns", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/campaigns/{slug}", requireSession(http.HandlerFunc(h.Delete)))
	mux.Handle("GET /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(h.ListLinks)))
	mux.Handle("POST /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(h.AssignLinks)))
	mux.Handle("DELETE /api/campaigns/{slug}/links/{key}", requireSession(http.HandlerFunc(h.UnassignLink)))
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
