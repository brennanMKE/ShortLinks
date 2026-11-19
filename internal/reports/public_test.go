package reports

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// pubLink inserts a link with explicit state and returns its id.
func (f *fixture) pubLink(userID int64, key, title, dest, source string, active bool, denied int, expires *time.Time, campaign int64) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, title, destination_url, utm_source, active, denied_reason, expires_at, campaign_id, created_at)
		 VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), $6, $7, $8, NULLIF($9::bigint, 0), now()) RETURNING id`,
		userID, key, title, dest, source, active, denied, expires, campaign).Scan(&id); err != nil {
		f.t.Fatalf("seed link %s: %v", key, err)
	}
	return id
}

func (f *fixture) summary(limit int) *PublicActivity {
	f.t.Helper()
	a, err := PublicSummary(context.Background(), f.pool, testNow, "America/Los_Angeles", "https://go.example.com", limit)
	if err != nil {
		f.t.Fatalf("PublicSummary: %v", err)
	}
	return a
}

func TestPublicSummary_SumsAcrossUsersExcludesBotsAndFillsDays(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("a@example.com"), f.user("b@example.com")
	l1 := f.link(u1, "aaa111", "")
	l2 := f.link(u2, "bbb222", "")

	// testNow is 12:41 PST on Nov 18; the window is Nov 12..Nov 18.
	f.clicks(3, click{link: l1, at: daysAgo(0, 18)}) // today (10:00 PST)
	f.clicks(4, click{link: l2, at: daysAgo(0, 19)}) // today, another user
	f.clicks(2, click{link: l1, at: daysAgo(6, 20)}) // first day of the window
	f.clicks(5, click{link: l1, at: daysAgo(0, 18), bot: true})
	f.clicks(9, click{link: l1, at: daysAgo(7, 20)}) // before the window
	// 2026-11-12 06:00 UTC is Nov 11 22:00 PST: outside the window by tz.
	f.clicks(6, click{link: l1, at: time.Date(2026, 11, 12, 6, 0, 0, 0, time.UTC)})

	a := f.summary(3)
	if a.Window.From != "2026-11-12" || a.Window.To != "2026-11-18" || a.Window.TZ != "America/Los_Angeles" || !a.Window.PartialLastDay {
		t.Errorf("window = %+v", a.Window)
	}
	if len(a.Timeseries) != 7 {
		t.Fatalf("timeseries has %d days, want 7: %v", len(a.Timeseries), a.Timeseries)
	}
	if a.Timeseries[0].Date != "2026-11-12" || a.Timeseries[0].Clicks != 2 {
		t.Errorf("first day = %+v, want 2026-11-12 with 2", a.Timeseries[0])
	}
	if last := a.Timeseries[6]; last.Date != "2026-11-18" || last.Clicks != 7 {
		t.Errorf("today = %+v, want 7 (3+4 across two users, bots excluded)", last)
	}
	if a.Timeseries[3].Clicks != 0 {
		t.Errorf("empty day = %+v, want zero-filled", a.Timeseries[3])
	}
	if a.TotalClicks != 9 {
		t.Errorf("total = %d, want 9", a.TotalClicks)
	}
}

func TestPublicSummary_TopLinksEligibilityGroupingAndPrivacy(t *testing.T) {
	f := newFixture(t)
	u1, u2 := f.user("a@example.com"), f.user("b@example.com")
	past, future := testNow.Add(-time.Hour), testNow.Add(24*time.Hour)
	camp := f.campaign(u1, "Guides", "guides")
	at := daysAgo(1, 18)

	// Two links to the same content (different platforms) collapse into one.
	mast := f.pubLink(u1, "mast01", "Measuring current — Mastodon guide post 84", "https://example.com/guide/current?utm_source=mastodon", "mastodon", true, 0, nil, camp)
	blue := f.pubLink(u2, "blue01", "Measuring current — Bluesky guide post 12", "https://example.com/guide/current?utm_source=bluesky", "bluesky", true, 0, nil, camp)
	f.clicks(10, click{link: mast, at: at})
	f.clicks(4, click{link: blue, at: at})

	solo := f.pubLink(u1, "solo01", "Soldering basics", "https://example.com/solder", "", true, 0, nil, camp)
	f.clicks(12, click{link: solo, at: at})
	untitled := f.pubLink(u2, "bare01", "", "https://example.com/bare", "", true, 0, nil, camp)
	f.clicks(2, click{link: untitled, at: at})

	// Ineligible: inactive, denied, expired and bot-only links never appear,
	// however many clicks they have.
	for i, c := range []struct {
		key     string
		active  bool
		denied  int
		expires *time.Time
	}{{"off001", false, 0, nil}, {"den001", true, 1, nil}, {"exp001", true, 0, &past}} {
		id := f.pubLink(u1, c.key, "Hidden "+c.key, "https://example.com/hidden/"+string(rune('a'+i)), "", c.active, c.denied, c.expires, camp)
		f.clicks(100, click{link: id, at: at})
	}
	// An expiring link (an invite, say) and a link in no campaign are excluded
	// even when active and heavily clicked; their clicks still count in the total.
	expiring := f.pubLink(u1, "inv001", "Invite", "https://example.com/invite", "", true, 0, &future, camp)
	f.clicks(100, click{link: expiring, at: at})
	nocamp := f.pubLink(u1, "nocmp1", "No campaign", "https://example.com/nocampaign", "", true, 0, nil, 0)
	f.clicks(100, click{link: nocamp, at: at})
	botOnly := f.pubLink(u1, "bot001", "Bots love this", "https://example.com/bots", "", true, 0, nil, camp)
	f.clicks(100, click{link: botOnly, at: at, bot: true})

	a := f.summary(3)
	if len(a.TopLinks) != 3 {
		t.Fatalf("top links = %+v, want 3", a.TopLinks)
	}
	want := []PublicLink{
		{Title: "Measuring current", ShortURL: "https://go.example.com/u/mast01", Clicks: 14},
		{Title: "Soldering basics", ShortURL: "https://go.example.com/u/solo01", Clicks: 12},
		{Title: "https://go.example.com/u/bare01", ShortURL: "https://go.example.com/u/bare01", Clicks: 2},
	}
	for i, w := range want {
		if a.TopLinks[i] != w {
			t.Errorf("top[%d] = %+v, want %+v", i, a.TopLinks[i], w)
		}
	}

	// The total counts every counted click on every link, listed or not.
	if a.TotalClicks != 14+12+2+500 {
		t.Errorf("total = %d, want %d", a.TotalClicks, 14+12+2+500)
	}

	// No destination, campaign, UTM or channel suffix anywhere in the JSON.
	b, _ := json.Marshal(a)
	for _, bad := range []string{"example.com/guide", "example.com/solder", "destination", "utm", "campaign", "Mastodon", "Bluesky", "hidden", "Hidden", "bot001", "inv001", "Invite", "nocmp1", "No campaign"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("JSON leaks %q: %s", bad, b)
		}
	}
}

func TestPublicSummary_EmptyAndLimitZero(t *testing.T) {
	f := newFixture(t)
	a := f.summary(3)
	if a.TotalClicks != 0 || len(a.Timeseries) != 7 || a.TopLinks == nil || len(a.TopLinks) != 0 {
		t.Errorf("empty summary = %+v", a)
	}
	b, _ := json.Marshal(a)
	if !strings.Contains(string(b), `"top_links":[]`) {
		t.Errorf("top_links not an empty array: %s", b)
	}
}

func TestPublicSummary_NotARegisteredReport(t *testing.T) {
	for _, name := range []string{"public", "public-summary", "activity"} {
		if _, ok := NewDefaultRegistry().Get(name); ok {
			t.Errorf("report %q is registered; the public summary must not be reachable via /api/reports", name)
		}
	}
}
