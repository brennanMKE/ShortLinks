package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// listServer serves GET /api/links with the real store and the given default
// time zone for the window parameter.
func listServer(t *testing.T, pool *pgxpool.Pool, defaultTZ string) *httptest.Server {
	t.Helper()
	h := NewLinksHandler(links.NewStore(pool), nil, nil, nil, nil, clicks.NewStatsStore(pool), campaigns.NewStore(pool), testBaseURL).WithDefaultTimezone(defaultTZ)
	mux := http.NewServeMux()
	mux.Handle("GET /api/links", middleware.RequireSession(auth.NewStore(pool))(http.HandlerFunc(h.List)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// getRaw GETs path as token and returns the status and raw body.
func getRaw(t *testing.T, srv *httptest.Server, token, path string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(buf.String())
}

type enrichedBody struct {
	Links []struct {
		Key          string  `json:"key"`
		ClickCount   int64   `json:"click_count"`
		ContentKey   string  `json:"content_key"`
		Label        string  `json:"label"`
		CampaignSlug *string `json:"campaign_slug"`
		ClicksWindow *int64  `json:"clicks_window"`
	} `json:"links"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Total   int64 `json:"total"`
	Window  *struct {
		Window string `json:"window"`
		From   string `json:"from"`
		To     string `json:"to"`
		TZ     string `json:"tz"`
	} `json:"window"`
}

func listKeys(t *testing.T, srv *httptest.Server, token, query string) ([]string, enrichedBody) {
	t.Helper()
	status, raw := getRaw(t, srv, token, "/api/links?"+query)
	if status != http.StatusOK {
		t.Fatalf("GET ?%s = %d: %s", query, status, raw)
	}
	var b enrichedBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := make([]string, 0, len(b.Links))
	for _, l := range b.Links {
		keys = append(keys, l.Key)
	}
	return keys, b
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func expectKeys(t *testing.T, got, want []string, ordered bool, ctx string) {
	t.Helper()
	if !ordered {
		got, want = sorted(got), sorted(want)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: keys = %v, want %v", ctx, got, want)
	}
}

// seedListLink inserts a link with explicit title and age.
func seedListLink(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest, title string, age time.Duration) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, title, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, $4, TRUE, 0, now() - make_interval(secs => $5)) RETURNING id`,
		userID, key, dest, title, age.Seconds(),
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

func execSQL(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func seedClickAt(t *testing.T, pool *pgxpool.Pool, linkID int64, at time.Time, bot bool) {
	t.Helper()
	execSQL(t, pool, `INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, $2, $3)`, linkID, at, bot)
}

// TestLinksList_DefaultResponseShapeUnchanged pins the response with no #0144
// parameter: top-level keys, per-item keys, and that none of the new item
// fields leak in. tz and per_page are not #0144 parameters.
func TestLinksList_DefaultResponseShapeUnchanged(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedListLink(t, pool, alice, "old", "https://example.com/old", "Old", 2*time.Hour)
	seedListLink(t, pool, alice, "new", "https://example.com/new", "New", time.Hour)

	for _, q := range []string{"", "per_page=10&tz=America/Los_Angeles"} {
		status, raw := getRaw(t, srv, "alice-token", "/api/links?"+q)
		if status != 200 {
			t.Fatalf("status %d: %s", status, raw)
		}
		var top map[string]json.RawMessage
		if err := json.Unmarshal(raw, &top); err != nil {
			t.Fatal(err)
		}
		var topKeys []string
		for k := range top {
			topKeys = append(topKeys, k)
		}
		expectKeys(t, topKeys, []string{"links", "page", "per_page", "total"}, false, "top-level keys")

		var items []map[string]json.RawMessage
		if err := json.Unmarshal(top["links"], &items); err != nil || len(items) != 2 {
			t.Fatalf("items: %v %d", err, len(items))
		}
		var itemKeys []string
		for k := range items[0] {
			itemKeys = append(itemKeys, k)
		}
		expectKeys(t, itemKeys, []string{
			"id", "key", "short_url", "destination_url", "title", "active", "denied_reason", "created_at", "expires_at",
			"click_count", "campaign_id", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "placement",
		}, false, "item keys")
		var k0 string
		_ = json.Unmarshal(items[0]["key"], &k0)
		if k0 != "new" {
			t.Errorf("default order first = %q, want newest (new)", k0)
		}
	}
}

func TestLinksList_QueryEscapesWildcards(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bob, "bob-token")

	seedListLink(t, pool, alice, "pct", "https://example.com/a", "Save 50% today", time.Hour)
	seedListLink(t, pool, alice, "pctx", "https://example.com/b", "Save 50x today", 2*time.Hour)
	seedListLink(t, pool, alice, "under", "https://example.com/c", "snake_case post", 3*time.Hour)
	seedListLink(t, pool, alice, "underx", "https://example.com/d", "snakeXcase post", 4*time.Hour)
	seedListLink(t, pool, alice, "back", "https://example.com/e", `path C:\dir`, 5*time.Hour)
	seedListLink(t, pool, alice, "byDest", "https://Dest.example.org/Needle", "", 6*time.Hour)
	seedListLink(t, pool, alice, "NeedleKey", "https://example.com/g", "", 7*time.Hour)
	seedListLink(t, pool, bob, "bobs", "https://example.com/h", "Save 50% today", time.Hour)

	cases := []struct {
		q    string
		want []string
	}{
		{"50%25", []string{"pct"}},                  // % is literal
		{"%25", []string{"pct"}},                    // lone % is not match-everything
		{"snake_case", []string{"under"}},           // _ is literal
		{"_", []string{"under"}},                    // lone _ is not match-any-char
		{"%5C", []string{"back"}},                   // backslash is literal
		{"needle", []string{"byDest", "NeedleKey"}}, // case-insensitive over destination and key
		{"SAVE", []string{"pct", "pctx"}},           // title, never Bob's
		{"zzz", nil},
	}
	for _, c := range cases {
		got, b := listKeys(t, srv, "alice-token", "q="+c.q)
		expectKeys(t, got, c.want, false, "q="+c.q)
		if b.Total != int64(len(c.want)) {
			t.Errorf("q=%s total = %d, want %d", c.q, b.Total, len(c.want))
		}
	}
}

func TestLinksList_CampaignSourceMediumStatusAndCombos(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bob, "bob-token")
	camp := seedCampaign(t, pool, alice, "Spring")
	bobCamp := seedCampaign(t, pool, bob, "Spring")

	seedListLink(t, pool, alice, "a1", "https://example.com/1", "A1", 1*time.Hour)
	seedListLink(t, pool, alice, "a2", "https://example.com/2", "A2", 2*time.Hour)
	seedListLink(t, pool, alice, "a3", "https://example.com/3", "A3", 3*time.Hour)
	seedListLink(t, pool, alice, "a4", "https://example.com/4", "A4", 4*time.Hour)
	seedListLink(t, pool, alice, "a5", "https://example.com/5", "A5", 5*time.Hour)
	b1 := seedListLink(t, pool, bob, "b1", "https://example.com/6", "B1", time.Hour)
	execSQL(t, pool, `UPDATE links SET campaign_id=$1, utm_source='mastodon', utm_medium='social' WHERE key IN ('a1','a2')`, camp.ID)
	execSQL(t, pool, `UPDATE links SET utm_source='bluesky', utm_medium='social' WHERE key='a3'`)
	execSQL(t, pool, `UPDATE links SET utm_source='newsletter', utm_medium='email' WHERE key='a4'`)
	execSQL(t, pool, `UPDATE links SET campaign_id=$1 WHERE id=$2`, bobCamp.ID, b1)
	// a2 deactivated, a4 expired (and also inactive: expired wins), a5 denied.
	execSQL(t, pool, `UPDATE links SET active=FALSE WHERE key='a2'`)
	execSQL(t, pool, `UPDATE links SET expires_at=now()-interval '1 hour', active=FALSE WHERE key='a4'`)
	execSQL(t, pool, `UPDATE links SET active=FALSE, denied_reason=1 WHERE key='a5'`)

	cases := []struct {
		q    string
		want []string
	}{
		{"campaign=" + camp.Slug, []string{"a1", "a2"}},
		{"campaign=none", []string{"a3", "a4", "a5"}},
		{"campaign=" + bobCamp.Slug + "-nope", nil},
		{"source=mastodon", []string{"a1", "a2"}},
		{"source=mastodon,bluesky", []string{"a1", "a2", "a3"}},
		{"medium=email", []string{"a4"}},
		{"source=mastodon&medium=social", []string{"a1", "a2"}},
		{"source=mastodon&medium=email", nil},
		{"status=active", []string{"a1", "a3"}},
		{"status=", []string{"a1", "a3"}}, // present but empty = active
		{"status=inactive", []string{"a2", "a5"}},
		{"status=expired", []string{"a4"}},
		{"status=all", []string{"a1", "a2", "a3", "a4", "a5"}},
		{"q=a", []string{"a1", "a2", "a3", "a4", "a5"}}, // status absent = all
		{"campaign=" + camp.Slug + "&status=active", []string{"a1"}},
		{"source=mastodon,bluesky&status=active&q=a3", []string{"a3"}},
	}
	for _, c := range cases {
		got, b := listKeys(t, srv, "alice-token", c.q)
		expectKeys(t, got, c.want, false, c.q)
		if b.Total != int64(len(c.want)) {
			t.Errorf("%s total = %d, want %d", c.q, b.Total, len(c.want))
		}
	}

	// Enriched fields.
	_, b := listKeys(t, srv, "alice-token", "campaign="+camp.Slug+"&sort=title")
	for _, l := range b.Links {
		if l.CampaignSlug == nil || *l.CampaignSlug != camp.Slug {
			t.Errorf("%s campaign_slug = %v, want %s", l.Key, l.CampaignSlug, camp.Slug)
		}
	}
	_, b = listKeys(t, srv, "alice-token", "campaign=none")
	for _, l := range b.Links {
		if l.CampaignSlug != nil {
			t.Errorf("%s campaign_slug = %v, want null", l.Key, *l.CampaignSlug)
		}
	}
	// Bob never sees Alice's links, and his campaign slug filter is his own.
	got, _ := listKeys(t, srv, "bob-token", "q=A1")
	expectKeys(t, got, nil, false, "bob q=A1")
	got, _ = listKeys(t, srv, "bob-token", "campaign="+camp.Slug)
	expectKeys(t, got, []string{"b1"}, false, "bob campaign (same slug, own campaign)")
}

func TestLinksList_ContentKeyAndPagination(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bob, "bob-token")

	// Five links on one content item (differing scheme, utm_*, slash, case),
	// interleaved with look-alikes on the same host.
	same := []string{
		"https://www.example.com/guides/x",
		"http://WWW.example.com/guides/x/?utm_source=a",
		"https://www.example.com/guides/x?utm_medium=b#top",
		"https://www.example.com:443/guides/x/",
		"https://www.example.com/guides/x?utm_campaign=c",
	}
	for i, d := range same {
		seedListLink(t, pool, alice, "same"+string(rune('1'+i)), d, "", time.Duration(2*i+1)*time.Hour)
		seedListLink(t, pool, alice, "other"+string(rune('1'+i)), "https://www.example.com/guides/y?id="+string(rune('1'+i)), "", time.Duration(2*i+2)*time.Hour)
	}
	seedListLink(t, pool, bob, "bobsame", same[0], "", time.Hour)
	ck := reports.ContentKey(same[0])
	if ck != "www.example.com/guides/x" {
		t.Fatalf("fixture content key = %q", ck)
	}

	q := "content_key=" + url.QueryEscape(ck)
	got, b := listKeys(t, srv, "alice-token", q+"&per_page=2&page=1")
	expectKeys(t, got, []string{"same1", "same2"}, true, "page 1")
	if b.Total != 5 {
		t.Errorf("total = %d, want 5 (exact, after the Go match)", b.Total)
	}
	got, _ = listKeys(t, srv, "alice-token", q+"&per_page=2&page=2")
	expectKeys(t, got, []string{"same3", "same4"}, true, "page 2")
	got, _ = listKeys(t, srv, "alice-token", q+"&per_page=2&page=3")
	expectKeys(t, got, []string{"same5"}, true, "page 3")
	got, _ = listKeys(t, srv, "alice-token", q+"&per_page=2&page=4")
	expectKeys(t, got, nil, true, "page 4")

	// Items carry the content key and label fields.
	_, b = listKeys(t, srv, "alice-token", q)
	for _, l := range b.Links {
		if l.ContentKey != ck {
			t.Errorf("%s content_key = %q, want %q", l.Key, l.ContentKey, ck)
		}
	}
	// A query-parameter content key selects only its own content.
	got, _ = listKeys(t, srv, "alice-token", "content_key="+url.QueryEscape("www.example.com/guides/y?id=2"))
	expectKeys(t, got, []string{"other2"}, false, "content key with query")
}

func TestLinksList_SortAndWindow(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "America/Los_Angeles")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bob, "bob-token")

	// Created newest to oldest: n, m, l, k.
	k := seedListLink(t, pool, alice, "k", "https://example.com/k", "banana", 4*time.Hour)
	l := seedListLink(t, pool, alice, "l", "https://example.com/l", "Apple", 3*time.Hour)
	m := seedListLink(t, pool, alice, "m", "https://example.com/m", "", 2*time.Hour)
	n := seedListLink(t, pool, alice, "n", "https://example.com/n", "cherry", 1*time.Hour)
	bl := seedListLink(t, pool, bob, "bl", "https://example.com/bl", "", time.Hour)

	now := time.Now()
	pv := url.Values{"tz": {"America/Los_Angeles"}, "range": {"7d"}, "compare": {"none"}}
	p, err := reports.ParseParams(pv, "")
	if err != nil {
		t.Fatal(err)
	}
	win := reports.ResolveWindows(p, now, nil).Current
	inside := win.Start.Add(time.Second)
	justBefore := win.Start.Add(-time.Second)
	// k: 1 inside (all-time 4); l: 3 inside (all-time 3); m: 2 inside, 1 bot;
	// n: only old clicks (all-time 5, window 0).
	for i := 0; i < 1; i++ {
		seedClickAt(t, pool, k, inside, false)
	}
	for i := 0; i < 3; i++ {
		seedClickAt(t, pool, k, justBefore, false)
		seedClickAt(t, pool, l, inside, false)
	}
	seedClickAt(t, pool, m, inside, false)
	seedClickAt(t, pool, m, now, false)
	seedClickAt(t, pool, m, now, true) // bot: counted nowhere
	for i := 0; i < 5; i++ {
		seedClickAt(t, pool, n, justBefore, false)
	}
	seedClickAt(t, pool, bl, now, false)

	order := func(q string) []string {
		got, _ := listKeys(t, srv, "alice-token", q)
		return got
	}
	expectKeys(t, order("sort=created"), []string{"n", "m", "l", "k"}, true, "sort=created (newest first)")
	expectKeys(t, order("sort=-created"), []string{"k", "l", "m", "n"}, true, "sort=-created (oldest first)")
	expectKeys(t, order("sort=title"), []string{"l", "k", "n", "m"}, true, "sort=title (A-Z, untitled last)")
	expectKeys(t, order("sort=clicks"), []string{"n", "k", "l", "m"}, true, "sort=clicks (n5 k4 l3 m2)")
	expectKeys(t, order("sort=clicks_window&window=7d"), []string{"l", "m", "k", "n"}, true, "sort=clicks_window")

	_, b := listKeys(t, srv, "alice-token", "window=7d")
	wantWin := map[string]int64{"k": 1, "l": 3, "m": 2, "n": 0}
	for _, it := range b.Links {
		if it.ClicksWindow == nil || *it.ClicksWindow != wantWin[it.Key] {
			t.Errorf("%s clicks_window = %v, want %d", it.Key, it.ClicksWindow, wantWin[it.Key])
		}
	}
	if b.Window == nil || b.Window.TZ != "America/Los_Angeles" || b.Window.From != win.From || b.Window.To != win.To || b.Window.Window != "7d" {
		t.Errorf("window info = %+v, want tz LA %s..%s", b.Window, win.From, win.To)
	}
	// A wider window brings the old clicks in; tz override is honoured.
	_, b = listKeys(t, srv, "alice-token", "window=30d&tz=UTC")
	for _, it := range b.Links {
		if it.Key == "n" && (it.ClicksWindow == nil || *it.ClicksWindow != 5) {
			t.Errorf("n clicks_window(30d) = %v, want 5", it.ClicksWindow)
		}
	}
	if b.Window == nil || b.Window.TZ != "UTC" {
		t.Errorf("tz override not applied: %+v", b.Window)
	}
	// clicks_window is absent without window.
	_, b = listKeys(t, srv, "alice-token", "sort=clicks")
	if b.Links[0].ClicksWindow != nil || b.Window != nil {
		t.Errorf("clicks_window/window present without window param")
	}
	// Bob's clicks and links stay Bob's.
	got, b := listKeys(t, srv, "bob-token", "window=7d")
	expectKeys(t, got, []string{"bl"}, true, "bob list")
	if *b.Links[0].ClicksWindow != 1 {
		t.Errorf("bob clicks_window = %d, want 1", *b.Links[0].ClicksWindow)
	}
}

func TestLinksList_ParameterErrors(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	cases := []struct{ q, code string }{
		{"sort=clicks_window", "invalid_sort"},
		{"sort=bogus", "invalid_sort"},
		{"status=bogus", "invalid_status"},
		{"window=14d", "invalid_window"},
		{"window=7d&tz=Mars/Base", "invalid_tz"},
	}
	for _, c := range cases {
		status, raw := getRaw(t, srv, "alice-token", "/api/links?"+c.q)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if status != 400 || body["error"] != c.code || body["message"] == "" {
			t.Errorf("%s = %d %s, want 400 %s with a message", c.q, status, raw, c.code)
		}
	}
	// Unauthenticated callers are rejected before any parsing.
	status, _ := getRaw(t, srv, "no-token", "/api/links?q=x")
	if status != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", status)
	}
}

func TestLinksList_PerPageCappedAt100(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	execSQL(t, pool, `INSERT INTO links (user_id, key, destination_url, active, denied_reason)
	                  SELECT $1, 'k' || g, 'https://example.com/' || g, TRUE, 0 FROM generate_series(1, 120) g`, alice)
	for _, q := range []string{"per_page=1000", "per_page=1000&q=k"} {
		_, b := listKeys(t, srv, "alice-token", q)
		if b.PerPage != 100 || len(b.Links) != 100 || b.Total != 120 {
			t.Errorf("%s: per_page=%d len=%d total=%d, want 100/100/120", q, b.PerPage, len(b.Links), b.Total)
		}
	}
}

// TestLinksList_DefaultResponseGolden compares the whole default body, values
// included, with the expected JSON. #0181 deliberately added `short_url` to
// each item (built from the non-default testBaseURL); that is the only change. Fixtures use fixed timestamps; ids are
// deterministic because the test tables restart their identities.
func TestLinksList_DefaultResponseGolden(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	execSQL(t, pool, `INSERT INTO links (user_id, key, destination_url, title, active, denied_reason, created_at, utm_source, placement)
	                  VALUES ($1, 'older', 'https://example.com/older', 'Older', TRUE, 0, '2026-01-01T10:00:00Z', 'mastodon', 'flyer')`, alice)
	execSQL(t, pool, `INSERT INTO links (user_id, key, destination_url, title, active, denied_reason, created_at, expires_at)
	                  VALUES ($1, 'newer', 'https://example.com/newer', NULL, FALSE, 0, '2026-01-02T10:00:00Z', '2030-01-01T00:00:00Z')`, alice)
	var older, newer int64
	_ = pool.QueryRow(context.Background(), `SELECT id FROM links WHERE key='older'`).Scan(&older)
	_ = pool.QueryRow(context.Background(), `SELECT id FROM links WHERE key='newer'`).Scan(&newer)
	seedClickAt(t, pool, older, time.Now(), false)
	seedClickAt(t, pool, older, time.Now(), true)

	status, raw := getRaw(t, srv, "alice-token", "/api/links?page=1&per_page=20")
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	want := `{
	  "links": [
	    {"id": ` + i64s(newer) + `, "key": "newer", "short_url": "https://example.test/u/newer", "destination_url": "https://example.com/newer", "title": "", "active": false,
	     "denied_reason": 0, "created_at": "2026-01-02T10:00:00Z", "expires_at": "2030-01-01T00:00:00Z", "click_count": 0,
	     "campaign_id": null, "utm_source": "", "utm_medium": "", "utm_campaign": "", "utm_term": "", "utm_content": "", "placement": ""},
	    {"id": ` + i64s(older) + `, "key": "older", "short_url": "https://example.test/u/older", "destination_url": "https://example.com/older", "title": "Older", "active": true,
	     "denied_reason": 0, "created_at": "2026-01-01T10:00:00Z", "expires_at": null, "click_count": 1,
	     "campaign_id": null, "utm_source": "mastodon", "utm_medium": "", "utm_campaign": "", "utm_term": "", "utm_content": "", "placement": "flyer"}
	  ],
	  "page": 1, "per_page": 20, "total": 2
	}`
	var g, w any
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	// The driver returns timestamps in the DB session zone; compare instants.
	for _, it := range g.(map[string]any)["links"].([]any) {
		m := it.(map[string]any)
		for _, f := range []string{"created_at", "expires_at"} {
			if str, ok := m[f].(string); ok {
				ts, err := time.Parse(time.RFC3339Nano, str)
				if err != nil {
					t.Fatal(err)
				}
				m[f] = ts.UTC().Format(time.RFC3339)
			}
		}
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("default body changed.\n got: %s\nwant: %s", raw, want)
	}
}

func i64s(n int64) string { return strconv.FormatInt(n, 10) }

func TestLinksList_LabelAndPastTheEndTotal(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	id := seedListLink(t, pool, alice, "lab", "https://example.com/guide", "Soldering header pins — Bluesky guide post 81", time.Hour)
	execSQL(t, pool, `UPDATE links SET utm_source='bluesky' WHERE id=$1`, id)
	seedListLink(t, pool, alice, "plain", "https://example.com/p", "Just a title", 2*time.Hour)

	_, b := listKeys(t, srv, "alice-token", "q=example&sort=created")
	labels := map[string]string{}
	for _, l := range b.Links {
		labels[l.Key] = l.Label
	}
	_, wantLabel := reports.LinkLabel("Soldering header pins — Bluesky guide post 81", "bluesky")
	if wantLabel != "Bluesky guide post 81" || labels["lab"] != wantLabel || labels["plain"] != "" {
		t.Errorf("labels = %v, want lab=%q plain=\"\"", labels, wantLabel)
	}

	// A page past the end still reports the real total.
	for _, q := range []string{"q=example&per_page=1&page=5", "status=all&per_page=1&page=9", "content_key=example.com/p&per_page=1&page=3"} {
		got, b := listKeys(t, srv, "alice-token", q)
		wantTotal := int64(2)
		if strings.HasPrefix(q, "content_key") {
			wantTotal = 1
		}
		if len(got) != 0 || b.Total != wantTotal {
			t.Errorf("%s: keys=%v total=%d, want none and total %d", q, got, b.Total, wantTotal)
		}
	}
}
