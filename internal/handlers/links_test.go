package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
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
	h := NewLinksHandler(links.NewStore(pool), nil, nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/links", requireSession(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/links/{key}", requireSession(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/links/{key}", requireSession(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/links/{key}", requireSession(http.HandlerFunc(h.Delete)))
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

// seedClick inserts one clicks row for a link so click_count assertions have
// real data to aggregate.
func seedClick(t *testing.T, pool *pgxpool.Pool, linkID int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO clicks (link_id, clicked_at) VALUES ($1, now())`, linkID,
	); err != nil {
		t.Fatalf("seed click: %v", err)
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

// TestLinksGet_DetailWithClickCount asserts detail returns the correct click
// count, and another user's key 404s.
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

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/alc", nil)
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
	if body.ClickCount != 3 {
		t.Errorf("click_count = %d, want 3", body.ClickCount)
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
