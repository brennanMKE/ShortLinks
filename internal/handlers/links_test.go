package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// linksMux builds the real route table for the link CRUD endpoints, guarded by
// RequireSession backed by the real *auth.Store and serving the real
// *links.Store. Requests therefore flow through the genuine session middleware
// and hit the live DB, proving both the guard and the data layer end to end.
func linksMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewLinksHandler(links.NewStore(pool), nil, nil, nil, nil, clicks.NewStatsStore(pool), campaigns.NewStore(pool), testBaseURL)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/links", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/links/{key}", requireSession(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/links/{key}", requireSession(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/links/{key}", requireSession(http.HandlerFunc(h.Delete)))
	// QR codes (#0106) — see qr_test.go.
	mux.Handle("GET /api/links/{key}/qr.svg", requireSession(http.HandlerFunc(h.QRSVG)))
	mux.Handle("GET /api/links/{key}/qr.png", requireSession(http.HandlerFunc(h.QRPNG)))
	return mux
}

// seedLink inserts an active, non-denied link for the user and returns its id.
func seedLink(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, TRUE, 0, now()) RETURNING id`,
		userID, key, dest,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

// seedClick inserts one non-bot clicks row for a link so click_count
// assertions have real data to aggregate.
func seedClick(t *testing.T, pool *pgxpool.Pool, linkID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, now(), FALSE)`, linkID,
	); err != nil {
		t.Fatalf("seed click: %v", err)
	}
}

// seedBotClick inserts one is_bot = TRUE clicks row for a link (#0101), so
// tests can assert click_count excludes it — and, for the LEFT JOIN sites
// (ListLinks/ListLinksForCampaign), that a link whose ONLY clicks are bot
// clicks still appears in the list rather than being silently dropped.
func seedBotClick(t *testing.T, pool *pgxpool.Pool, linkID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, now(), TRUE)`, linkID,
	); err != nil {
		t.Fatalf("seed bot click: %v", err)
	}
}

// linkRow reads the persisted state asserted by mutation tests.
func linkRow(t *testing.T, pool *pgxpool.Pool, key string) (dest, title string, active bool, found bool) {
	t.Helper()
	ctx := context.Background()
	var titleNull *string
	err := pool.QueryRow(ctx,
		`SELECT destination_url, title, active FROM links WHERE key = $1`, key,
	).Scan(&dest, &titleNull, &active)
	if err != nil {
		return "", "", false, false
	}
	if titleNull != nil {
		title = *titleNull
	}
	return dest, title, active, true
}

// TestLinksCreate_GeneratedKey asserts POST creates a link with a generated
// 6-char base-62 key, persists the row, and returns 201 with duplicate=false.
func TestLinksCreate_GeneratedKey(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://www.wikipedia.org","title":"Wiki"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body linkView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Duplicate == nil || *body.Duplicate {
		t.Errorf("duplicate = %v, want false", body.Duplicate)
	}
	if len(body.Key) != 6 {
		t.Errorf("key = %q (len %d), want 6 chars", body.Key, len(body.Key))
	}
	for _, c := range body.Key {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			t.Errorf("key %q has non-base62 char %q", body.Key, c)
		}
	}
	if body.DestinationURL != "https://www.wikipedia.org" {
		t.Errorf("destination_url = %q", body.DestinationURL)
	}
	if !body.Active || body.DeniedReason != 0 {
		t.Errorf("active=%v denied_reason=%d, want active=true denied=0", body.Active, body.DeniedReason)
	}
	dest, title, active, found := linkRow(t, pool, body.Key)
	if !found {
		t.Fatalf("row for key %q not found in DB", body.Key)
	}
	if dest != "https://www.wikipedia.org" || title != "Wiki" || !active {
		t.Errorf("DB row dest=%q title=%q active=%v", dest, title, active)
	}
}

// countUserURLLinks returns how many non-denied links a user has to a given
// destination — the dedup scope. It backs the "no new row inserted" assertions.
func countUserURLLinks(t *testing.T, pool *pgxpool.Pool, userID int64, dest string) int {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM links WHERE user_id = $1 AND destination_url = $2 AND denied_reason = 0`,
		userID, dest,
	).Scan(&n); err != nil {
		t.Fatalf("count links for user %d url %q: %v", userID, dest, err)
	}
	return n
}

// postLink POSTs the given JSON body as the session token and returns the
// decoded link view plus the HTTP status.
func postLink(t *testing.T, srv *httptest.Server, token, body string) (linkView, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links", jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var v linkView
	if resp.StatusCode == http.StatusCreated {
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return v, resp.StatusCode
}

// TestLinksCreate_DedupActiveDuplicate asserts the PRD's three deduplication
// branches for the generated-key path, plus per-user isolation:
//   - first POST of a URL → 201, duplicate=false, one row.
//   - second POST of the SAME URL by the SAME user → SAME link (id/key),
//     duplicate=true, NO new row.
//   - deactivate then POST again → REACTIVATED (active=true), same id,
//     duplicate=true, still one row.
//   - a DIFFERENT user POSTing the same URL → their OWN new link (two rows).
func TestLinksCreate_DedupActiveDuplicate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")

	const dest = "https://www.example.org/path"

	// First POST → fresh insert.
	first, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`","title":"First"}`)
	if status != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", status)
	}
	if first.Duplicate == nil || *first.Duplicate {
		t.Errorf("first duplicate = %v, want false", first.Duplicate)
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 1 {
		t.Fatalf("after first POST, row count = %d, want 1", n)
	}

	// Seed one human and two bot clicks on the freshly created link before
	// the dedup POST below. lockExisting (#0101 review) is the one
	// click_count call site the rest of #0101's coverage doesn't reach: it
	// backs this exact dedup response (POST /api/links returning
	// duplicate=true), which is reachable independently of GET
	// /api/links/{key} — a bug here would let the two disagree for the same
	// link, the same defect class the click_count fix was about.
	seedClick(t, pool, first.ID)
	seedBotClick(t, pool, first.ID)
	seedBotClick(t, pool, first.ID)

	// Second POST of the SAME URL by the SAME user → active duplicate.
	second, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`","title":"Second"}`)
	if status != http.StatusCreated {
		t.Fatalf("second POST status = %d, want 201", status)
	}
	if second.Duplicate == nil || !*second.Duplicate {
		t.Errorf("second duplicate = %v, want true", second.Duplicate)
	}
	if second.ID != first.ID || second.Key != first.Key {
		t.Errorf("second link id=%d key=%q, want same as first id=%d key=%q",
			second.ID, second.Key, first.ID, first.Key)
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 1 {
		t.Fatalf("after second POST, row count = %d, want 1 (no new row)", n)
	}
	// The dedup response's click_count must exclude the two bot clicks
	// (lockExisting's own COUNT, not UTMStatsForLink's) — 1, not 3.
	if second.ClickCount != 1 {
		t.Errorf("dedup response click_count = %d, want 1 (2 bot clicks excluded by lockExisting)", second.ClickCount)
	}

	// Deactivate the link, then POST the same URL again → reactivation.
	if _, err := pool.Exec(context.Background(),
		`UPDATE links SET active = FALSE WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	third, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`","title":"Third"}`)
	if status != http.StatusCreated {
		t.Fatalf("reactivate POST status = %d, want 201", status)
	}
	if third.Duplicate == nil || !*third.Duplicate {
		t.Errorf("reactivate duplicate = %v, want true", third.Duplicate)
	}
	if third.ID != first.ID {
		t.Errorf("reactivate id=%d, want same as first id=%d", third.ID, first.ID)
	}
	if !third.Active {
		t.Errorf("reactivate active = false, want true")
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 1 {
		t.Fatalf("after reactivate, row count = %d, want 1 (same row)", n)
	}

	// A DIFFERENT user POSTing the same URL → their OWN new link (dedup is
	// per-user).
	bobLink, status := postLink(t, srv, "bob-token", `{"destination_url":"`+dest+`","title":"Bob"}`)
	if status != http.StatusCreated {
		t.Fatalf("bob POST status = %d, want 201", status)
	}
	if bobLink.Duplicate == nil || *bobLink.Duplicate {
		t.Errorf("bob duplicate = %v, want false", bobLink.Duplicate)
	}
	if bobLink.ID == first.ID {
		t.Errorf("bob link id=%d collided with alice's id=%d", bobLink.ID, first.ID)
	}
	if n := countUserURLLinks(t, pool, bob, dest); n != 1 {
		t.Errorf("bob row count = %d, want 1", n)
	}
	// Two distinct rows across users for the same URL.
	var total int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM links WHERE destination_url = $1 AND denied_reason = 0`, dest,
	).Scan(&total); err != nil {
		t.Fatalf("count total: %v", err)
	}
	if total != 2 {
		t.Errorf("total rows across users = %d, want 2", total)
	}
}

// TestLinksCreate_CustomAliasNotDeduped asserts a custom alias to an
// already-shortened URL is NOT deduplicated: a new row with that alias is
// created even though the user already has an active link to the same URL.
func TestLinksCreate_CustomAliasNotDeduped(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	const dest = "https://www.example.net/dup"

	first, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", status)
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 1 {
		t.Fatalf("after first POST, row count = %d, want 1", n)
	}

	// Custom alias to the SAME URL → bypasses dedup, inserts a second row.
	aliased, status := postLink(t, srv, "alice-token",
		`{"destination_url":"`+dest+`","custom_key":"mybrand"}`)
	if status != http.StatusCreated {
		t.Fatalf("custom-alias POST status = %d, want 201", status)
	}
	if aliased.Key != "mybrand" {
		t.Errorf("alias key = %q, want mybrand", aliased.Key)
	}
	if aliased.Duplicate == nil || *aliased.Duplicate {
		t.Errorf("custom-alias duplicate = %v, want false (no dedup)", aliased.Duplicate)
	}
	if aliased.ID == first.ID {
		t.Errorf("custom-alias reused dedup row id=%d, want a new row", aliased.ID)
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 2 {
		t.Errorf("after custom-alias POST, row count = %d, want 2 (alias not deduped)", n)
	}
}

// TestLinksCreate_CustomAlias asserts a custom alias is accepted and stored.
func TestLinksCreate_CustomAlias(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://example.com","custom_key":"mylink"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body linkView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Key != "mylink" {
		t.Errorf("key = %q, want mylink", body.Key)
	}
}

// TestLinksCreate_DuplicateAlias409 asserts a custom alias already taken yields
// 409 (custom aliases are NOT deduplicated).
func TestLinksCreate_DuplicateAlias409(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "taken", "https://first.example.com")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://second.example.com","alias":"taken"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// TestLinksCreate_InvalidURL400 asserts a non-absolute / non-http(s) URL is
// rejected with 400 before any insert.
func TestLinksCreate_InvalidURL400(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	for _, bad := range []string{`"not a url"`, `"ftp://example.com"`, `"/relative/path"`, `""`} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
			jsonBody(`{"destination_url":`+bad+`}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("destination_url=%s status = %d, want 400", bad, resp.StatusCode)
		}
	}
}

// TestLinksList_ScopedAndPaginated asserts the list returns only the caller's
// links, newest first, and honors ?page=/?per_page=.
func TestLinksList_ScopedAndPaginated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	// Alice gets three links; insert sequentially so created_at ordering is
	// deterministic (a1 oldest, a3 newest).
	seedLink(t, pool, alice, "a1", "https://a1.example.com")
	time.Sleep(2 * time.Millisecond)
	seedLink(t, pool, alice, "a2", "https://a2.example.com")
	time.Sleep(2 * time.Millisecond)
	seedLink(t, pool, alice, "a3", "https://a3.example.com")
	seedLink(t, pool, bob, "b1", "https://b1.example.com")

	// Page 1, per_page 2 → newest two (a3, a2).
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links?page=1&per_page=2", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body listLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 3 {
		t.Errorf("total = %d, want 3 (only Alice's)", body.Total)
	}
	if len(body.Links) != 2 {
		t.Fatalf("page len = %d, want 2", len(body.Links))
	}
	if body.Links[0].Key != "a3" || body.Links[1].Key != "a2" {
		t.Errorf("page order = [%s,%s], want [a3,a2]", body.Links[0].Key, body.Links[1].Key)
	}
	for _, l := range body.Links {
		if l.Key == "b1" {
			t.Fatalf("Bob's link leaked into Alice's list")
		}
	}

	// Page 2 → the remaining oldest (a1).
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links?page=2&per_page=2", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	var body2 listLinksResponse
	if err := json.NewDecoder(resp2.Body).Decode(&body2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body2.Links) != 1 || body2.Links[0].Key != "a1" {
		t.Errorf("page 2 = %+v, want single [a1]", body2.Links)
	}
}

// TestLinksList_ClickCountExcludesBotsAndBotOnlyLinkStillAppears is the
// #0101 review's ON-clause trap, made concrete: ListLinks joins clicks onto
// links with a LEFT JOIN so every link appears even with zero clicks. The
// is_bot = FALSE exclusion MUST live in that JOIN's ON clause rather than a
// WHERE clause — a WHERE clause runs after the join and would filter out
// the joined row entirely for a link whose only clicks are bot clicks,
// making GROUP BY collapse to nothing and the link silently vanish from its
// own owner's list. This test seeds a link with ONLY bot clicks (no human
// clicks at all) and asserts it still appears, with click_count = 0 — plus
// a normal link with a mix of human and bot clicks, asserting only the
// human clicks count.
func TestLinksList_ClickCountExcludesBotsAndBotOnlyLinkStillAppears(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	botOnly := seedLink(t, pool, alice, "botonly", "https://botonly.example.com")
	seedBotClick(t, pool, botOnly)
	seedBotClick(t, pool, botOnly)

	mixed := seedLink(t, pool, alice, "mixed", "https://mixed.example.com")
	seedClick(t, pool, mixed)
	seedClick(t, pool, mixed)
	seedBotClick(t, pool, mixed)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links?page=1&per_page=10", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body listLinksResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 {
		t.Fatalf("total = %d, want 2 — the bot-only link must not be dropped from its owner's list", body.Total)
	}
	byKey := map[string]int64{}
	for _, l := range body.Links {
		byKey[l.Key] = l.ClickCount
	}
	count, ok := byKey["botonly"]
	if !ok {
		t.Fatal("botonly link is missing from the list entirely — the LEFT JOIN's is_bot filter must be in the ON clause, not WHERE")
	}
	if count != 0 {
		t.Errorf("botonly click_count = %d, want 0", count)
	}
	if count, ok := byKey["mixed"]; !ok || count != 2 {
		t.Errorf("mixed click_count = %d (present=%v), want 2", count, ok)
	}
}

// TestLinksGet_DetailWithClickCount asserts detail returns the correct click
// count, and another user's key 404s.
//
// It also seeds two bot clicks (#0101) alongside the three human ones and
// asserts click_count stays 3, not 5 — links.Store.GetLink's click_count
// must exclude is_bot = TRUE the same way clicks.UTMStatsForLink's
// utm_stats.click_count does, so a single GET /api/links/{key} response
// never carries two disagreeing totals for the same link.
func TestLinksGet_DetailWithClickCount(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	aliceLink := seedLink(t, pool, alice, "alc", "https://alice.example.com")
	seedLink(t, pool, bob, "bob", "https://bob.example.com")
	seedClick(t, pool, aliceLink)
	seedClick(t, pool, aliceLink)
	seedClick(t, pool, aliceLink)
	seedBotClick(t, pool, aliceLink)
	seedBotClick(t, pool, aliceLink)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/alc", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body linkDetailView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ClickCount != 3 {
		t.Errorf("click_count = %d, want 3 (2 bot clicks must be excluded)", body.ClickCount)
	}
	// The headline click_count and the utm_stats breakdown's total must agree
	// — this is exactly the "two contradictory totals in one payload" defect
	// #0101's review caught: click_count used to be a raw COUNT(*) while
	// utm_stats.click_count already excluded bots.
	if body.UTMStats == nil {
		t.Fatal("utm_stats missing from detail response")
	}
	if body.UTMStats.ClickCount != body.ClickCount {
		t.Errorf("utm_stats.click_count = %d, click_count = %d — must agree", body.UTMStats.ClickCount, body.ClickCount)
	}
	if body.UTMStats.ExcludedBotCount != 2 {
		t.Errorf("utm_stats.excluded_bot_count = %d, want 2", body.UTMStats.ExcludedBotCount)
	}

	// Foreign key → 404.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/bob", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("foreign key status = %d, want 404", resp2.StatusCode)
	}
}

// TestLinksPatch_UpdatesOwn asserts PATCH updates title and destination on the
// caller's own link (and persists), while a foreign key 404s.
func TestLinksPatch_UpdatesOwn(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "alc", "https://old.example.com")
	seedLink(t, pool, bob, "bob", "https://bob.example.com")

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/links/alc",
		jsonBody(`{"title":"Updated","destination_url":"https://new.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body linkView
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Title != "Updated" || body.DestinationURL != "https://new.example.com" {
		t.Errorf("response title=%q dest=%q", body.Title, body.DestinationURL)
	}
	dest, title, _, _ := linkRow(t, pool, "alc")
	if dest != "https://new.example.com" || title != "Updated" {
		t.Errorf("DB dest=%q title=%q, not persisted", dest, title)
	}

	// Invalid destination on PATCH → 400.
	reqBad, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/links/alc",
		jsonBody(`{"destination_url":"javascript:alert(1)"}`))
	reqBad.Header.Set("Content-Type", "application/json")
	respBad, err := srv.Client().Do(withCookie(reqBad, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Errorf("bad dest PATCH status = %d, want 400", respBad.StatusCode)
	}

	// Foreign key → 404.
	req2, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/links/bob",
		jsonBody(`{"title":"Hijacked"}`))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("foreign PATCH status = %d, want 404", resp2.StatusCode)
	}
	if _, ti, _, _ := linkRow(t, pool, "bob"); ti != "" {
		t.Errorf("Bob's link was mutated: title=%q", ti)
	}
}

// TestLinksDelete_Deactivates asserts DELETE sets active=false (row retained),
// and a foreign key 404s.
func TestLinksDelete_Deactivates(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "alc", "https://alice.example.com")
	seedLink(t, pool, bob, "bob", "https://bob.example.com")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/links/alc", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_, _, active, found := linkRow(t, pool, "alc")
	if !found {
		t.Fatalf("link row was hard-deleted; want soft delete (row retained)")
	}
	if active {
		t.Errorf("active = true after DELETE, want false")
	}

	// Foreign key → 404, and Bob's link untouched.
	req2, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/links/bob", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("foreign DELETE status = %d, want 404", resp2.StatusCode)
	}
	if _, _, bobActive, _ := linkRow(t, pool, "bob"); !bobActive {
		t.Errorf("Bob's link was deactivated by Alice")
	}
}

// TestLinks_Unauthenticated asserts every route answers 401 without a session
// cookie, proving the RequireSession guard.
func TestLinks_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	cases := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/links"},
		{http.MethodGet, "/api/links"},
		{http.MethodGet, "/api/links/abc123"},
		{http.MethodPatch, "/api/links/abc123"},
		{http.MethodDelete, "/api/links/abc123"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, jsonBody(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req) // no cookie
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

// ── #0099: discrete UTM columns, campaign resolution ────────────────────────

// seedCampaign creates a campaign directly via the store (bypassing HTTP) so
// link tests can set up a campaign fixture without standing up campaignsMux.
func seedCampaign(t *testing.T, pool *pgxpool.Pool, userID int64, name string) campaigns.Campaign {
	t.Helper()
	c, err := campaigns.NewStore(pool).CreateCampaign(context.Background(), campaigns.NewCampaign{UserID: userID, Name: name}, nil, audit.Entry{})
	if err != nil {
		t.Fatalf("seedCampaign: %v", err)
	}
	return c
}

// patchLink PATCHes the given JSON body for the given key and returns the
// decoded link view plus the HTTP status.
func patchLink(t *testing.T, srv *httptest.Server, token, key, body string) (linkView, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/api/links/"+key, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var v linkView
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return v, resp.StatusCode
}

// getLinkDetail GETs the given key and returns the decoded detail view plus
// the HTTP status.
func getLinkDetail(t *testing.T, srv *httptest.Server, token, key string) (linkDetailView, int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/"+key, nil)
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	var v linkDetailView
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return v, resp.StatusCode
}

// TestLinksCreate_DiscreteUTMColumnsMatchBakedURL is the acceptance
// criterion's exact test: mirrors what the frontend's composeUtmUrl bakes
// into destination_url (see web/src/lib/utm.ts) by sending the SAME five
// values both baked into the query string AND as discrete fields, then
// parses the STORED destination_url's query string and compares it
// field-by-field against both the JSON response and a direct DB read — not
// just that the fields are non-empty, but that they match what was actually
// baked.
func TestLinksCreate_DiscreteUTMColumnsMatchBakedURL(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	body := `{
		"destination_url":"https://example.com/landing?utm_source=email&utm_medium=newsletter&utm_campaign=summer-fair&utm_term=shoes&utm_content=banner1",
		"utm_source":"email","utm_medium":"newsletter","utm_campaign":"summer-fair","utm_term":"shoes","utm_content":"banner1",
		"placement":"18th and Texas board"
	}`
	created, status := postLink(t, srv, "alice-token", body)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}

	parsed, err := url.Parse(created.DestinationURL)
	if err != nil {
		t.Fatalf("parsing stored destination_url %q: %v", created.DestinationURL, err)
	}
	q := parsed.Query()

	respFields := map[string]string{
		"utm_source":   created.UTMSource,
		"utm_medium":   created.UTMMedium,
		"utm_campaign": created.UTMCampaign,
		"utm_term":     created.UTMTerm,
		"utm_content":  created.UTMContent,
	}
	for key, got := range respFields {
		if want := q.Get(key); got != want {
			t.Errorf("response linkView.%s = %q, want %q (parsed from the stored destination_url)", key, got, want)
		}
	}
	if created.Placement != "18th and Texas board" {
		t.Errorf("placement = %q, want %q", created.Placement, "18th and Texas board")
	}

	// Also read the row directly — the response could theoretically echo the
	// request without ever persisting it.
	var dbSource, dbMedium, dbCampaign, dbTerm, dbContent, dbPlacement string
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(utm_source,''), COALESCE(utm_medium,''), COALESCE(utm_campaign,''),
		        COALESCE(utm_term,''), COALESCE(utm_content,''), COALESCE(placement,'')
		   FROM links WHERE key = $1`,
		created.Key,
	).Scan(&dbSource, &dbMedium, &dbCampaign, &dbTerm, &dbContent, &dbPlacement); err != nil {
		t.Fatalf("querying link row: %v", err)
	}
	if dbSource != q.Get("utm_source") || dbMedium != q.Get("utm_medium") || dbCampaign != q.Get("utm_campaign") ||
		dbTerm != q.Get("utm_term") || dbContent != q.Get("utm_content") || dbPlacement != "18th and Texas board" {
		t.Errorf("DB columns (%q,%q,%q,%q,%q,%q) do not match the baked URL's params / request placement",
			dbSource, dbMedium, dbCampaign, dbTerm, dbContent, dbPlacement)
	}
}

// TestLinksCreate_CampaignSlugAssignsLink asserts POST /api/links with
// campaign_slug resolves it to the caller's own campaign and stores its id.
func TestLinksCreate_CampaignSlugAssignsLink(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")

	created, status := postLink(t, srv, "alice-token",
		`{"destination_url":"https://example.com","campaign_slug":"`+c.Slug+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if created.CampaignID == nil || *created.CampaignID != c.ID {
		t.Errorf("campaign_id = %v, want %d", created.CampaignID, c.ID)
	}
}

// TestLinksCreate_CampaignIDAssignsLink is the same as above via the
// campaign_id field instead of campaign_slug.
func TestLinksCreate_CampaignIDAssignsLink(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")

	created, status := postLink(t, srv, "alice-token",
		fmt.Sprintf(`{"destination_url":"https://example.com","campaign_id":%d}`, c.ID))
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}
	if created.CampaignID == nil || *created.CampaignID != c.ID {
		t.Errorf("campaign_id = %v, want %d", created.CampaignID, c.ID)
	}
}

// TestLinksCreate_CampaignOwnershipEnforced asserts user A cannot create a
// link into user B's campaign — neither via campaign_slug nor campaign_id —
// both report 404, the same indistinguishable response a nonexistent
// campaign would produce, and no link is created.
func TestLinksCreate_CampaignOwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := seedCampaign(t, pool, bob, "Bob Campaign")

	_, status := postLink(t, srv, "alice-token",
		`{"destination_url":"https://example.com/by-slug","campaign_slug":"`+bobCampaign.Slug+`"}`)
	if status != http.StatusNotFound {
		t.Errorf("campaign_slug into bob's campaign: status = %d, want 404", status)
	}

	_, status = postLink(t, srv, "alice-token",
		fmt.Sprintf(`{"destination_url":"https://example.com/by-id","campaign_id":%d}`, bobCampaign.ID))
	if status != http.StatusNotFound {
		t.Errorf("campaign_id into bob's campaign: status = %d, want 404", status)
	}

	if n := countUserURLLinks(t, pool, alice, "https://example.com/by-slug"); n != 0 {
		t.Errorf("links created despite ownership rejection (slug) = %d, want 0", n)
	}
	if n := countUserURLLinks(t, pool, alice, "https://example.com/by-id"); n != 0 {
		t.Errorf("links created despite ownership rejection (id) = %d, want 0", n)
	}
}

// TestLinksPatch_UpdatesDiscreteUTMColumns proves editing a link updates the
// discrete UTM columns in lockstep with a changed destination_url — closing
// the #0048 edit-repopulation limitation end to end (not just reading old
// values back, but writing new ones). Verified via a follow-up GET so the
// assertion is against persisted state, not PATCH's own echoed response.
func TestLinksPatch_UpdatesDiscreteUTMColumns(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	created, status := postLink(t, srv, "alice-token",
		`{"destination_url":"https://example.com?utm_source=email","utm_source":"email"}`)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", status)
	}

	patched, status := patchLink(t, srv, "alice-token", created.Key,
		`{"destination_url":"https://example.com?utm_source=twitter&utm_medium=social","utm_source":"twitter","utm_medium":"social"}`)
	if status != http.StatusOK {
		t.Fatalf("patch status = %d, want 200", status)
	}
	if patched.UTMSource != "twitter" || patched.UTMMedium != "social" {
		t.Errorf("PATCH response utm_source=%q utm_medium=%q, want twitter/social", patched.UTMSource, patched.UTMMedium)
	}

	detail, status := getLinkDetail(t, srv, "alice-token", created.Key)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", status)
	}
	if detail.UTMSource != "twitter" || detail.UTMMedium != "social" {
		t.Errorf("GET after PATCH utm_source=%q utm_medium=%q, want twitter/social (not persisted)", detail.UTMSource, detail.UTMMedium)
	}
}

// TestLinksGet_PreMigrationLinkHasEmptyUTMFields is the acceptance
// criterion's other half: "a link created before this migration (all-NULL
// columns) opens with empty fields rather than erroring". seedLink inserts a
// row the same way the pre-#0099 schema would have (no campaign_id/utm_*/
// placement columns populated), so GetLink's LEFT JOIN and NULL-mapping must
// handle every one of the seven new columns being NULL without a scan error.
func TestLinksGet_PreMigrationLinkHasEmptyUTMFields(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "oldlink", "https://example.com/pre-migration")

	detail, status := getLinkDetail(t, srv, "alice-token", "oldlink")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (must not error scanning NULL UTM/campaign columns)", status)
	}
	if detail.UTMSource != "" || detail.UTMMedium != "" || detail.UTMCampaign != "" ||
		detail.UTMTerm != "" || detail.UTMContent != "" || detail.Placement != "" {
		t.Errorf("UTM/placement fields = %+v, want all empty for a pre-migration (all-NULL) link", detail.linkView)
	}
	if detail.CampaignID != nil {
		t.Errorf("campaign_id = %v, want nil", detail.CampaignID)
	}
	if detail.CampaignName != "" || detail.CampaignSlug != "" {
		t.Errorf("campaign_name=%q campaign_slug=%q, want both empty", detail.CampaignName, detail.CampaignSlug)
	}
}

// TestLinksGet_IncludesCampaignNameAndSlug asserts the link-detail response
// carries the assigned campaign's name and slug (#0099's extension to the
// detail response), not just its id.
func TestLinksGet_IncludesCampaignNameAndSlug(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")

	created, status := postLink(t, srv, "alice-token",
		`{"destination_url":"https://example.com","campaign_id":`+strconv.FormatInt(c.ID, 10)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", status)
	}

	detail, status := getLinkDetail(t, srv, "alice-token", created.Key)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", status)
	}
	if detail.CampaignName != "Summer Fair" || detail.CampaignSlug != c.Slug {
		t.Errorf("campaign_name=%q campaign_slug=%q, want %q/%q", detail.CampaignName, detail.CampaignSlug, "Summer Fair", c.Slug)
	}
}

// TestLinksCreate_DedupForwardMergesCampaignAndUTMOnActiveDuplicate is the
// #0099 review's item 4 decision, proven at the HTTP layer: a second
// POST for a URL that already has an ACTIVE link is a no-write dedup match
// (duplicate:true), but campaign_id/utm_*/placement supplied on THIS request
// are forward-merged onto the existing row rather than silently discarded —
// otherwise a user who picks a campaign on the create form and happens to
// hit an existing URL would see it silently not applied.
func TestLinksCreate_DedupForwardMergesCampaignAndUTMOnActiveDuplicate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")
	const dest = "https://example.org/dedup-merge"

	first, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", status)
	}
	if first.CampaignID != nil || first.UTMSource != "" {
		t.Fatalf("precondition: first link already has campaign/UTM set")
	}

	second, status := postLink(t, srv, "alice-token",
		fmt.Sprintf(`{"destination_url":"%s","campaign_id":%d,"utm_source":"email","placement":"18th and Texas board"}`, dest, c.ID))
	if status != http.StatusCreated {
		t.Fatalf("second (duplicate) POST status = %d, want 201", status)
	}
	if second.Duplicate == nil || !*second.Duplicate {
		t.Errorf("duplicate = %v, want true (still a dedup match, not a second row)", second.Duplicate)
	}
	if second.ID != first.ID {
		t.Fatalf("second id=%d, want same row as first id=%d", second.ID, first.ID)
	}
	if second.CampaignID == nil || *second.CampaignID != c.ID {
		t.Errorf("response campaign_id = %v, want %d (forward-merged, not discarded)", second.CampaignID, c.ID)
	}
	if second.UTMSource != "email" || second.Placement != "18th and Texas board" {
		t.Errorf("response utm_source=%q placement=%q, want email / 18th and Texas board", second.UTMSource, second.Placement)
	}

	// Confirm it is persisted, not just echoed.
	detail, status := getLinkDetail(t, srv, "alice-token", first.Key)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", status)
	}
	if detail.CampaignID == nil || *detail.CampaignID != c.ID || detail.UTMSource != "email" {
		t.Errorf("persisted campaign_id=%v utm_source=%q, want %d / email", detail.CampaignID, detail.UTMSource, c.ID)
	}
	if n := countUserURLLinks(t, pool, alice, dest); n != 1 {
		t.Errorf("row count = %d, want 1 (still a dedup match, no second row created)", n)
	}
}

// TestLinksCreate_DedupDoesNotClearExistingMetadataOnBareResubmit is the
// other half of the item 4 decision: the merge is FORWARD-ONLY. A bare
// re-submission (no campaign_id, no UTM fields) of a URL that already has
// campaign/UTM metadata set must NOT null them out — only a request that
// actually supplies a value may change one.
func TestLinksCreate_DedupDoesNotClearExistingMetadataOnBareResubmit(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")
	const dest = "https://example.org/dedup-no-clear"

	first, status := postLink(t, srv, "alice-token",
		fmt.Sprintf(`{"destination_url":"%s","campaign_id":%d,"utm_source":"email"}`, dest, c.ID))
	if status != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", status)
	}
	if first.CampaignID == nil || first.UTMSource != "email" {
		t.Fatalf("precondition: first link should already carry campaign/UTM")
	}

	// Bare re-submission: no campaign_id, no UTM fields at all.
	second, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("second (bare) POST status = %d, want 201", status)
	}
	if second.CampaignID == nil || *second.CampaignID != c.ID {
		t.Errorf("campaign_id after bare resubmit = %v, want still %d (forward merge must not clear)", second.CampaignID, c.ID)
	}
	if second.UTMSource != "email" {
		t.Errorf("utm_source after bare resubmit = %q, want still %q", second.UTMSource, "email")
	}
}

// TestLinksCreate_DedupForwardMergesOnReactivate proves the same forward
// merge applies on the OutcomeReactivated branch, not just active-duplicate.
func TestLinksCreate_DedupForwardMergesOnReactivate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := seedCampaign(t, pool, alice, "Summer Fair")
	const dest = "https://example.org/dedup-reactivate-merge"

	first, status := postLink(t, srv, "alice-token", `{"destination_url":"`+dest+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("first POST status = %d, want 201", status)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE links SET active = FALSE WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	reactivated, status := postLink(t, srv, "alice-token",
		fmt.Sprintf(`{"destination_url":"%s","campaign_id":%d,"utm_medium":"newsletter"}`, dest, c.ID))
	if status != http.StatusCreated {
		t.Fatalf("reactivate POST status = %d, want 201", status)
	}
	if !reactivated.Active {
		t.Fatalf("reactivated link is not active")
	}
	if reactivated.CampaignID == nil || *reactivated.CampaignID != c.ID {
		t.Errorf("campaign_id on reactivate = %v, want %d", reactivated.CampaignID, c.ID)
	}
	if reactivated.UTMMedium != "newsletter" {
		t.Errorf("utm_medium on reactivate = %q, want newsletter", reactivated.UTMMedium)
	}
}
