package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type groupedBody struct {
	Groups []struct {
		ContentKey   string `json:"content_key"`
		Title        string `json:"title"`
		Path         string `json:"path"`
		LinkCount    int    `json:"link_count"`
		Clicks       int64  `json:"clicks"`
		ClicksWindow *int64 `json:"clicks_window"`
		Links        []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"links"`
	} `json:"groups"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	Total      int64 `json:"total"`
	TotalLinks int64 `json:"total_links"`
	Truncated  bool  `json:"truncated"`
}

func listGroups(t *testing.T, srv *httptest.Server, token, query string) groupedBody {
	t.Helper()
	status, raw := getRaw(t, srv, token, "/api/links?"+query)
	if status != http.StatusOK {
		t.Fatalf("GET ?%s = %d: %s", query, status, raw)
	}
	var b groupedBody
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return b
}

// TestLinksList_GroupByContent covers #0189: group=content pages by content
// group, never splits a group across pages, and honours search, filters, sort
// and the window.
func TestLinksList_GroupByContent(t *testing.T) {
	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, bob, "bob-token")

	// Guide X: three links interleaved in time with the others, so a flat
	// page would split it. Guide Y: two links. Guide Z: one link.
	x := "https://www.example.com/guides/x"
	y := "https://www.example.com/guides/y"
	z := "https://www.example.com/guides/z"
	ids := map[string]int64{}
	seed := func(key, dest, title, source string, age time.Duration) {
		ids[key] = seedListLink(t, pool, alice, key, dest, title, age)
		execSQL(t, pool, `UPDATE links SET utm_source = $1 WHERE id = $2`, source, ids[key])
	}
	seed("x1", x+"?utm_source=mastodon", "Guide X - Mastodon post 1", "mastodon", 1*time.Hour)
	seed("y1", y, "Guide Y - Bluesky post 2", "bluesky", 2*time.Hour)
	seed("x2", x+"/", "Guide X - Bluesky post 3", "bluesky", 3*time.Hour)
	seed("z1", z, "Guide Z", "", 4*time.Hour)
	seed("y2", y+"?utm_source=a", "Guide Y - Mastodon post 4", "mastodon", 5*time.Hour)
	seed("x3", x+"#top", "Guide X - Poster 5", "poster", 6*time.Hour)
	seedListLink(t, pool, bob, "bobx", x, "Guide X", time.Hour)

	// One group per content item, every link in it, newest group first.
	b := listGroups(t, srv, "alice-token", "group=content")
	if b.Total != 3 || b.TotalLinks != 6 || len(b.Groups) != 3 {
		t.Fatalf("total=%d total_links=%d groups=%d, want 3/6/3", b.Total, b.TotalLinks, len(b.Groups))
	}
	g := b.Groups[0]
	if g.ContentKey != "www.example.com/guides/x" || g.Title != "Guide X" || g.Path != "/guides/x" || g.LinkCount != 3 || len(g.Links) != 3 {
		t.Errorf("group 0 = %+v", g)
	}
	if g.Links[0].Key != "x1" || g.Links[0].Label != "Mastodon post 1" {
		t.Errorf("group 0 first link = %+v", g.Links[0])
	}
	if b.Groups[1].ContentKey != "www.example.com/guides/y" || b.Groups[1].LinkCount != 2 || b.Groups[2].LinkCount != 1 {
		t.Errorf("groups 1,2 = %+v / %+v", b.Groups[1], b.Groups[2])
	}

	// Paging moves by groups: per_page=2 puts X and Y on page 1, Z on page 2.
	b = listGroups(t, srv, "alice-token", "group=content&per_page=2&page=1")
	if len(b.Groups) != 2 || b.Total != 3 || b.Groups[0].LinkCount != 3 || b.Groups[1].LinkCount != 2 {
		t.Errorf("page 1 = %+v", b)
	}
	b = listGroups(t, srv, "alice-token", "group=content&per_page=2&page=2")
	if len(b.Groups) != 1 || b.Groups[0].ContentKey != "www.example.com/guides/z" {
		t.Errorf("page 2 = %+v", b)
	}
	b = listGroups(t, srv, "alice-token", "group=content&per_page=2&page=3")
	if len(b.Groups) != 0 || b.Total != 3 {
		t.Errorf("page 3 = %+v", b)
	}

	// Search and filters narrow the links inside groups and drop empty groups.
	b = listGroups(t, srv, "alice-token", "group=content&source=mastodon")
	if b.Total != 2 || b.TotalLinks != 2 || b.Groups[0].LinkCount != 1 || b.Groups[1].LinkCount != 1 {
		t.Errorf("source=mastodon = %+v", b)
	}
	b = listGroups(t, srv, "alice-token", "group=content&q=guide+z")
	if b.Total != 1 || b.Groups[0].ContentKey != "www.example.com/guides/z" {
		t.Errorf("q = %+v", b)
	}

	// Sort by group clicks, with a window.
	now := time.Now()
	for i := 0; i < 3; i++ {
		seedClickAt(t, pool, ids["z1"], now.Add(-time.Hour), false)
	}
	seedClickAt(t, pool, ids["x1"], now.Add(-time.Hour), false)
	seedClickAt(t, pool, ids["x2"], now.Add(-48*24*time.Hour), false)
	seedClickAt(t, pool, ids["y1"], now.Add(-time.Hour), true) // bot, ignored
	b = listGroups(t, srv, "alice-token", "group=content&sort=clicks&window=7d")
	if b.Groups[0].ContentKey != "www.example.com/guides/z" || b.Groups[0].Clicks != 3 || b.Groups[0].ClicksWindow == nil || *b.Groups[0].ClicksWindow != 3 {
		t.Errorf("sort=clicks first = %+v", b.Groups[0])
	}
	if b.Groups[1].ContentKey != "www.example.com/guides/x" || b.Groups[1].Clicks != 2 || *b.Groups[1].ClicksWindow != 1 {
		t.Errorf("sort=clicks second = %+v", b.Groups[1])
	}
	b = listGroups(t, srv, "alice-token", "group=content&sort=title")
	if b.Groups[0].Title != "Guide X" || b.Groups[2].Title != "Guide Z" {
		t.Errorf("sort=title = %+v", b.Groups)
	}
	b = listGroups(t, srv, "alice-token", "group=content&sort=-created")
	if b.Groups[0].ContentKey != "www.example.com/guides/x" || b.Groups[2].ContentKey != "www.example.com/guides/z" {
		t.Errorf("sort=-created = %+v", b.Groups)
	}

	// group=none is the flat list; an unknown value is a 400.
	_, flat := listKeys(t, srv, "alice-token", "group=none")
	if flat.Total != 6 || len(flat.Links) != 6 {
		t.Errorf("group=none total=%d links=%d", flat.Total, len(flat.Links))
	}
	status, raw := getRaw(t, srv, "alice-token", "/api/links?group=bogus")
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid_group") {
		t.Errorf("group=bogus = %d %s", status, raw)
	}
}

// TestLinksList_GroupByContentTruncates lowers the cap: only the first
// maxGroupedLinks links (in the list order) are grouped and truncated is set.
func TestLinksList_GroupByContentTruncates(t *testing.T) {
	old := maxGroupedLinks
	maxGroupedLinks = 3
	t.Cleanup(func() { maxGroupedLinks = old })

	pool := credsTestPool(t)
	srv := listServer(t, pool, "UTC")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	for i, k := range []string{"a1", "a2", "a3", "a4", "a5"} {
		seedListLink(t, pool, alice, k, "https://www.example.com/g/"+string(rune('a'+i%2)), "T "+k, time.Duration(i+1)*time.Hour)
	}
	b := listGroups(t, srv, "alice-token", "group=content")
	if !b.Truncated || b.TotalLinks != 3 || b.Total != 2 {
		t.Errorf("capped = %+v", b)
	}
	if b.Groups[0].LinkCount+b.Groups[1].LinkCount != 3 {
		t.Errorf("groups cover %d links, want 3", b.Groups[0].LinkCount+b.Groups[1].LinkCount)
	}
	maxGroupedLinks = 5 // exactly the cap is not truncated
	if b = listGroups(t, srv, "alice-token", "group=content"); b.Truncated || b.TotalLinks != 5 {
		t.Errorf("at cap = %+v", b)
	}
}
