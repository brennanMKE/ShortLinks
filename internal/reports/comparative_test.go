package reports

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestCampaignStatus(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	tokyo := mustLoc(t, "Asia/Tokyo")
	d := func(s string) *time.Time {
		v, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := []struct {
		name       string
		start, end *time.Time
		archived   bool
		now        time.Time
		loc        *time.Location
		want       string
	}{
		{"no dates is live", nil, nil, false, at("2026-11-18T12:00:00Z"), time.UTC, "live"},
		{"no dates but archived", nil, nil, true, at("2026-11-18T12:00:00Z"), time.UTC, "archived"},
		{"archived beats ended", d("2026-01-01"), d("2026-02-01"), true, at("2026-11-18T12:00:00Z"), time.UTC, "archived"},
		{"before start", d("2026-11-19"), d("2026-11-30"), false, at("2026-11-18T23:59:59Z"), time.UTC, "scheduled"},
		{"start day is live", d("2026-11-18"), d("2026-11-30"), false, at("2026-11-18T00:00:00Z"), time.UTC, "live"},
		{"end day is live", d("2026-11-01"), d("2026-11-18"), false, at("2026-11-18T23:59:59Z"), time.UTC, "live"},
		{"day after end", d("2026-11-01"), d("2026-11-18"), false, at("2026-11-19T00:00:00Z"), time.UTC, "ended"},
		{"only start, passed", d("2026-11-01"), nil, false, at("2026-11-18T12:00:00Z"), time.UTC, "live"},
		{"only start, future", d("2026-12-01"), nil, false, at("2026-11-18T12:00:00Z"), time.UTC, "scheduled"},
		{"only end, future", nil, d("2026-12-01"), false, at("2026-11-18T12:00:00Z"), time.UTC, "live"},
		{"only end, past", nil, d("2026-11-01"), false, at("2026-11-18T12:00:00Z"), time.UTC, "ended"},
		// 07:30Z on Nov 18 is still Nov 17 in Los Angeles.
		{"tz: start is tomorrow in LA", d("2026-11-18"), nil, false, at("2026-11-18T07:30:00Z"), la, "scheduled"},
		{"tz: same instant, UTC is live", d("2026-11-18"), nil, false, at("2026-11-18T07:30:00Z"), time.UTC, "live"},
		{"tz: end day still live in LA", nil, d("2026-11-17"), false, at("2026-11-18T07:30:00Z"), la, "live"},
		{"tz: same instant, UTC is ended", nil, d("2026-11-17"), false, at("2026-11-18T07:30:00Z"), time.UTC, "ended"},
		// 16:00Z on Nov 17 is already Nov 18 in Tokyo.
		{"tz: Tokyo ahead, ended", nil, d("2026-11-17"), false, at("2026-11-17T16:00:00Z"), tokyo, "ended"},
		{"nil loc is UTC", d("2026-11-18"), nil, false, at("2026-11-18T07:30:00Z"), nil, "live"},
	}
	for _, c := range cases {
		if got := CampaignStatus(c.start, c.end, c.archived, c.now, c.loc); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// link seeds a link with full control over its lifecycle fields.
func (f *fixture) lifecycleLink(uid int64, key, dest string, created time.Time, active bool, denied int, expires *time.Time, campaign int64, source string) int64 {
	f.t.Helper()
	var camp *int64
	if campaign != 0 {
		camp = &campaign
	}
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, title, active, denied_reason, created_at, expires_at, campaign_id, utm_source)
		 VALUES ($1, $2, $3, $10, $4, $5, $6, $7, $8, $9) RETURNING id`,
		uid, key, dest, active, denied, created, expires, camp, nz(source), key).Scan(&id); err != nil {
		f.t.Fatalf("seed link: %v", err)
	}
	return id
}

func moversData(t *testing.T, e *Envelope) MoversData {
	t.Helper()
	d, ok := e.Data.(MoversData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func seedMovers(f *fixture) int64 {
	uid := seedGuides(f)
	mk := func(key, path string) int64 {
		return f.lifecycleLink(uid, key, "https://www.opencircuitsf.com/guides/"+path, daysAgo(60, 9), true, 0, nil, 0, "")
	}
	fresh, quiet, tiny, small := mk("fresh1", "fresh"), mk("quiet1", "quiet"), mk("tiny1", "tiny"), mk("small1", "small")
	f.clicks(6, click{link: fresh, at: daysAgo(1, 10)})
	f.clicks(7, click{link: quiet, at: daysAgo(35, 10)})
	f.clicks(1, click{link: tiny, at: daysAgo(1, 10)})
	f.clicks(1, click{link: small, at: daysAgo(40, 10)})
	f.clicks(3, click{link: small, at: daysAgo(2, 10)})
	return uid
}

func TestMovers_Content(t *testing.T) {
	f := newFixture(t)
	uid := seedMovers(f)
	env := f.run(uid, "movers", "")
	d := moversData(t, env)
	if d.Unit != "content" || d.MinClicks != 5 || !d.Compared {
		t.Fatalf("header = %+v", d)
	}
	// guide A: 14 vs 8; guide B: 4 vs 8 (falls); small 3 vs 1 is below min_clicks.
	if len(d.Rising) != 1 || d.Rising[0].Title != "Measuring current without blowing the fuse" || d.Rising[0].Clicks != 14 ||
		d.Rising[0].PreviousClicks != 8 || d.Rising[0].Change != 6 || d.Rising[0].ChangePct == nil || *d.Rising[0].ChangePct != 75 {
		t.Errorf("rising = %+v", d.Rising)
	}
	if len(d.Falling) != 1 || d.Falling[0].ContentKey != "www.opencircuitsf.com/guides/deep-sleep" || d.Falling[0].Change != -4 {
		t.Errorf("falling = %+v", d.Falling)
	}
	if len(d.New) != 1 || d.New[0].ContentKey != "www.opencircuitsf.com/guides/fresh" || d.New[0].Clicks != 6 || d.New[0].ChangePct != nil {
		t.Errorf("new = %+v", d.New)
	}
	if len(d.GoneQuiet) != 1 || d.GoneQuiet[0].ContentKey != "www.opencircuitsf.com/guides/quiet" || d.GoneQuiet[0].PreviousClicks != 7 || d.GoneQuiet[0].Clicks != 0 {
		t.Errorf("gone quiet = %+v", d.GoneQuiet)
	}
	h := strings.Join(env.Highlights, " ")
	if !strings.Contains(h, "Measuring current without blowing the fuse") || !strings.Contains(h, "up 75%") {
		t.Errorf("highlights = %v", env.Highlights)
	}

	// Numbers match top-content exactly.
	top := contentData(t, f.run(uid, "top-content", "limit=50"))
	for _, g := range top.Groups {
		if g.ContentKey == d.Rising[0].ContentKey && (g.Clicks != d.Rising[0].Clicks || *g.PreviousClicks != d.Rising[0].PreviousClicks || g.Title != d.Rising[0].Title) {
			t.Errorf("movers %+v disagrees with top-content %+v", d.Rising[0], g)
		}
	}
}

func TestMovers_MinClicksLimitAndLink(t *testing.T) {
	f := newFixture(t)
	uid := seedMovers(f)
	d := moversData(t, f.run(uid, "movers", "min_clicks=1"))
	if len(d.Rising) != 2 || d.Rising[0].Change != 6 || d.Rising[1].ContentKey != "www.opencircuitsf.com/guides/small" {
		t.Errorf("min_clicks=1 rising = %+v", d.Rising)
	}
	if len(d.New) != 2 {
		t.Errorf("min_clicks=1 new = %+v", d.New)
	}
	if d := moversData(t, f.run(uid, "movers", "min_clicks=1&limit=1")); len(d.Rising) != 1 || len(d.New) != 1 {
		t.Errorf("limit=1: %+v", d)
	}
	if d := moversData(t, f.run(uid, "movers", "min_clicks=50")); len(d.Rising)+len(d.Falling)+len(d.New)+len(d.GoneQuiet) != 0 {
		t.Errorf("min_clicks=50: %+v", d)
	}

	l := moversData(t, f.run(uid, "movers", "unit=link"))
	if l.Unit != "link" || len(l.Rising) != 2 || l.Rising[0].Key != "blue84" || l.Rising[0].Clicks != 5 || l.Rising[0].PreviousClicks != 2 {
		t.Errorf("link rising = %+v", l.Rising)
	}
	// Both rise by 3; the percentage (150% vs 50%) breaks the tie.
	if len(l.Rising) != 2 {
		t.Errorf("link rising count = %d, want 2: %+v", len(l.Rising), l.Rising)
	}
	if len(l.Falling) != 1 || l.Falling[0].Key != "mast75" || l.Falling[0].ShortURL == "" {
		t.Errorf("link falling = %+v", l.Falling)
	}

	if d := moversData(t, f.run(uid, "movers", "compare=none")); d.Compared || len(d.Rising) != 0 {
		t.Errorf("compare=none: %+v", d)
	}
	if d := moversData(t, f.run(uid, "movers", "range=all")); d.Compared || len(d.Rising)+len(d.Falling)+len(d.New)+len(d.GoneQuiet) != 0 {
		t.Errorf("range=all: %+v", d)
	}
	for _, q := range []string{"unit=bogus", "min_clicks=-1", "min_clicks=x"} {
		if _, err := f.svc.Run(context.Background(), uid, "movers", mustQuery(q)); err == nil {
			t.Errorf("%q: want error", q)
		}
	}
}

func TestIdleLinks(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	other := f.user("b@example.com")
	c1 := f.campaign(uid, "Posters", "posters")
	past, future := daysAgo(5, 0), testNow.Add(48*time.Hour)

	f.lifecycleLink(uid, "old1", "https://example.com/a", daysAgo(100, 9), true, 0, nil, 0, "")
	idleMid := f.lifecycleLink(uid, "mid1", "https://example.com/b", daysAgo(30, 9), true, 0, &future, c1, "mastodon")
	f.lifecycleLink(uid, "new1", "https://example.com/c", daysAgo(2, 9), true, 0, nil, 0, "")                                   // too young
	f.lifecycleLink(uid, "edge1", "https://example.com/d", testNow.Add(-7*24*time.Hour), true, 0, nil, 0, "")                   // 7 calendar days old
	f.lifecycleLink(uid, "edge2", "https://example.com/d2", time.Date(2026, 11, 12, 0, 1, 0, 0, time.UTC), true, 0, nil, 0, "") // 6 calendar days old in UTC
	f.lifecycleLink(uid, "off1", "https://example.com/e", daysAgo(50, 9), false, 0, nil, 0, "")                                 // inactive
	f.lifecycleLink(uid, "den1", "https://example.com/f", daysAgo(50, 9), true, 2, nil, 0, "")                                  // denied
	f.lifecycleLink(uid, "exp1", "https://example.com/g", daysAgo(50, 9), true, 0, &past, 0, "")                                // expired
	busy := f.lifecycleLink(uid, "busy1", "https://example.com/h", daysAgo(50, 9), true, 0, nil, 0, "")
	botOnly := f.lifecycleLink(uid, "bot1", "https://example.com/i", daysAgo(40, 9), true, 0, nil, 0, "")
	f.lifecycleLink(other, "other1", "https://example.com/j", daysAgo(90, 9), true, 0, nil, 0, "")
	f.clicks(2, click{link: busy, at: daysAgo(3, 10)})
	f.clicks(3, click{link: botOnly, at: daysAgo(3, 10), bot: true})
	f.clicks(1, click{link: idleMid, at: daysAgo(45, 10)})
	f.clicks(1, click{link: idleMid, at: daysAgo(60, 10)})

	env := f.run(uid, "idle-links", "")
	d := env.Data.(IdleLinksData)
	var keys []string
	for _, l := range d.Links {
		keys = append(keys, l.Key)
	}
	// Oldest first; bot-only link is idle because bots are not counted.
	if got := strings.Join(keys, ","); got != "old1,bot1,mid1,edge1" || d.TotalIdle != 4 || d.MinAgeDays != 7 {
		t.Fatalf("idle keys = %s (total %d), want old1,bot1,mid1,edge1", got, d.TotalIdle)
	}
	if d.Links[0].LastClickedAt != nil {
		t.Errorf("old1 last_clicked_at = %v, want nil", d.Links[0].LastClickedAt)
	}
	if lc := d.Links[2].LastClickedAt; lc == nil || !lc.Equal(daysAgo(45, 10)) {
		t.Errorf("mid1 last_clicked_at = %v, want all-time latest (45 days ago)", lc)
	}
	if d.Links[2].Campaign == nil || *d.Links[2].Campaign != "posters" || d.Links[0].ContentKey != "example.com/a" || d.Links[0].AgeDays != 100 {
		t.Errorf("rows = %+v", d.Links[:3])
	}
	if d.Links[1].LastClickedAt != nil {
		t.Errorf("bot1 last click should ignore bots: %v", d.Links[2].LastClickedAt)
	}
	h := strings.Join(env.Highlights, " ")
	if !strings.Contains(h, "4 links have had no clicks in 30 days") || !strings.Contains(h, "old1") {
		t.Errorf("highlights = %v", env.Highlights)
	}

	// include_bots counts the bot clicks, so bot1 is no longer idle.
	if d := f.run(uid, "idle-links", "include_bots=true").Data.(IdleLinksData); d.TotalIdle != 3 {
		t.Errorf("include_bots total = %d, want 3", d.TotalIdle)
	}
	// min_age_days counts calendar days in tz: edge2 (Nov 12 00:01Z) is 6 days
	// old in UTC but created Nov 11 in Los Angeles, so it qualifies there.
	if d := f.run(uid, "idle-links", "tz=America/Los_Angeles").Data.(IdleLinksData); d.TotalIdle != 5 {
		t.Errorf("tz=LA total = %d, want 5 (edge2 included)", d.TotalIdle)
	}
	if d := f.run(uid, "idle-links", "min_age_days=0").Data.(IdleLinksData); d.TotalIdle != 6 || d.Links[5].Key != "new1" {
		t.Errorf("min_age_days=0: %+v", d)
	}
	if d := f.run(uid, "idle-links", "min_age_days=60").Data.(IdleLinksData); d.TotalIdle != 1 || d.Links[0].Key != "old1" {
		t.Errorf("min_age_days=60: %+v", d)
	}
	// A shorter window makes idle-mid... still idle; a wider one catches its clicks.
	if d := f.run(uid, "idle-links", "range=90d").Data.(IdleLinksData); d.TotalIdle != 3 {
		t.Errorf("range=90d total = %d, want 3 (mid1 clicked 45 days ago)", d.TotalIdle)
	}
	// limit keeps the total.
	if d := f.run(uid, "idle-links", "limit=2").Data.(IdleLinksData); len(d.Links) != 2 || d.TotalIdle != 4 {
		t.Errorf("limit=2: %d links, total %d", len(d.Links), d.TotalIdle)
	}
	// Filters match the link's own fields.
	if d := f.run(uid, "idle-links", "campaign=posters").Data.(IdleLinksData); d.TotalIdle != 1 || d.Links[0].Key != "mid1" {
		t.Errorf("campaign filter: %+v", d)
	}
	if d := f.run(uid, "idle-links", "campaign=none").Data.(IdleLinksData); d.TotalIdle != 3 {
		t.Errorf("campaign=none total = %d, want 3", d.TotalIdle)
	}
	if d := f.run(uid, "idle-links", "source=mastodon").Data.(IdleLinksData); d.TotalIdle != 1 || d.Links[0].Key != "mid1" {
		t.Errorf("source filter: %+v", d)
	}
	if d := f.run(uid, "idle-links", "source=~none").Data.(IdleLinksData); d.TotalIdle != 3 {
		t.Errorf("source=~none total = %d, want 3", d.TotalIdle)
	}
	// Another user's links never appear.
	if d := f.run(other, "idle-links", "").Data.(IdleLinksData); d.TotalIdle != 1 || d.Links[0].Key != "other1" {
		t.Errorf("other user: %+v", d)
	}
	for _, q := range []string{"min_age_days=-1", "min_age_days=x"} {
		if _, err := f.svc.Run(context.Background(), uid, "idle-links", mustQuery(q)); err == nil {
			t.Errorf("%q: want error", q)
		}
	}
}

func TestCampaignComparison(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	mkc := func(name, slug, starts, ends string, archived bool) int64 {
		f.t.Helper()
		var id int64
		var s, e any
		if starts != "" {
			s = starts
		}
		if ends != "" {
			e = ends
		}
		if err := f.pool.QueryRow(context.Background(),
			`INSERT INTO campaigns (user_id, name, slug, starts_at, ends_at, archived) VALUES ($1,$2,$3,$4::timestamptz,$5::timestamptz,$6) RETURNING id`,
			uid, name, slug, s, e, archived).Scan(&id); err != nil {
			f.t.Fatal(err)
		}
		return id
	}
	// Window: Oct 20 - Nov 18 (30d), previous Sep 20 - Oct 19.
	big := mkc("Big", "big", "2026-10-01 00:00+00", "2026-12-31 00:00+00", false)    // live
	small := mkc("Small", "small", "", "", false)                                    // no dates, live
	soon := mkc("Soon", "soon", "2026-11-25 00:00+00", "2026-12-05 00:00+00", false) // scheduled, outside window
	farLater := mkc("Later", "later", "2027-01-01 00:00+00", "2027-02-01 00:00+00", false)
	endedIn := mkc("Ended", "ended", "2026-10-01 00:00+00", "2026-10-25 00:00+00", false) // overlaps window, no clicks
	endedOld := mkc("Old", "old", "2026-01-01 00:00+00", "2026-02-01 00:00+00", false)    // before window, no clicks
	arch := mkc("Archived", "archived", "", "", true)
	_, _, _ = soon, farLater, endedOld

	a := f.lifecycleLink(uid, "bg1", guideA, daysAgo(40, 9), true, 0, nil, big, "mastodon")
	b := f.lifecycleLink(uid, "bg2", guideB, daysAgo(40, 9), true, 0, nil, big, "bluesky")
	b2 := f.lifecycleLink(uid, "bg3", guideA+"?utm_source=x", daysAgo(40, 9), true, 0, nil, big, "bluesky")
	s1 := f.lifecycleLink(uid, "sm1", guideB, daysAgo(40, 9), true, 0, nil, small, "")
	ar := f.lifecycleLink(uid, "ar1", guideB, daysAgo(40, 9), true, 0, nil, arch, "")
	f.clicks(6, click{link: a, campaign: big, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(4, click{link: b, campaign: big, at: daysAgo(3, 10), source: "bluesky"})
	f.clicks(3, click{link: b2, campaign: big, at: daysAgo(3, 10), source: "bluesky"})
	f.clicks(5, click{link: a, campaign: big, at: daysAgo(40, 10), source: "mastodon"})
	f.clicks(2, click{link: s1, campaign: small, at: daysAgo(5, 10)})
	f.clicks(9, click{link: ar, campaign: arch, at: daysAgo(5, 10), bot: true}) // bots not counted
	f.clicks(1, click{link: ar, campaign: arch, at: daysAgo(40, 10)})           // previous only
	_ = endedIn

	other := f.user("b@example.com")
	oc := f.campaign(other, "Theirs", "theirs")
	ol := f.lifecycleLink(other, "oth1", guideA, daysAgo(40, 9), true, 0, nil, oc, "")
	f.clicks(4, click{link: ol, campaign: oc, at: daysAgo(2, 10)})

	env := f.run(uid, "campaign-comparison", "")
	d := env.Data.(CampaignComparisonData)
	var slugs []string
	for _, c := range d.Campaigns {
		slugs = append(slugs, c.Slug)
	}
	// big(13), small(2), then zero-click rows by slug: archived (no dates: not
	// overlapping, no current clicks -> dropped), ended (overlaps), soon is outside.
	if got := strings.Join(slugs, ","); got != "big,small,ended" || d.Total != 3 {
		t.Fatalf("campaigns = %s, want big,small,ended", got)
	}
	r := d.Campaigns[0]
	if r.Status != "live" || r.Clicks != 13 || r.LinksClicked != 3 || r.Links != 3 || r.PreviousClicks == nil || *r.PreviousClicks != 5 ||
		r.StartsOn == nil || *r.StartsOn != "2026-10-01" || r.EndsOn == nil || *r.EndsOn != "2026-12-31" {
		t.Errorf("big = %+v", r)
	}
	// 13 clicks over Oct 20 - Nov 18 (30 active days) = 0.4
	if r.ClicksPerActiveDay == nil || *r.ClicksPerActiveDay != 0.4 {
		t.Errorf("big clicks/day = %v", r.ClicksPerActiveDay)
	}
	// guide A is bg1 + bg3 (9 clicks), guide B is bg2 (4).
	if r.TopContent == nil || r.TopContent.Clicks != 9 || r.TopContent.Title == "" {
		t.Errorf("big top content = %+v", r.TopContent)
	}
	if r.TopSource == nil || r.TopSource.Value != "bluesky" || r.TopSource.Clicks != 7 {
		t.Errorf("big top source = %+v", r.TopSource)
	}
	if s := d.Campaigns[1]; s.Status != "live" || s.Clicks != 2 || s.StartsOn != nil || s.TopSource != nil || *s.ClicksPerActiveDay != 0.1 {
		t.Errorf("small = %+v", s)
	}
	if e := d.Campaigns[2]; e.Status != "ended" || e.Clicks != 0 {
		t.Errorf("ended = %+v", e)
	} else if e.ClicksPerActiveDay == nil || *e.ClicksPerActiveDay != 0 {
		t.Errorf("ended clicks/day = %v", e.ClicksPerActiveDay)
	}
	if h := strings.Join(env.Highlights, " "); !strings.Contains(h, `Top campaign: "Big" with 13 clicks`) {
		t.Errorf("highlights = %v", env.Highlights)
	}

	for _, c := range d.Campaigns {
		if c.Slug == "theirs" {
			t.Errorf("another user's campaign leaked: %+v", c)
		}
	}
	if od := f.run(other, "campaign-comparison", "").Data.(CampaignComparisonData); len(od.Campaigns) != 1 || od.Campaigns[0].Slug != "theirs" {
		t.Errorf("other user's view = %+v", od.Campaigns)
	}

	// include_bots surfaces the archived campaign with its status.
	d = f.run(uid, "campaign-comparison", "include_bots=true").Data.(CampaignComparisonData)
	if d.Campaigns[0].Slug != "big" || d.Campaigns[1].Slug != "archived" || d.Campaigns[1].Status != "archived" || d.Campaigns[1].Clicks != 9 {
		t.Errorf("include_bots = %+v", d.Campaigns)
	}
	// Windows end today, so a scheduled campaign (start after today) never
	// overlaps one and is listed only if it has clicks (see the tz test below).
	d = f.run(uid, "campaign-comparison", "from=2026-11-10&to=2026-11-18&compare=none").Data.(CampaignComparisonData)
	for _, c := range d.Campaigns {
		if c.Slug == "soon" || c.PreviousClicks != nil {
			t.Errorf("unexpected row/previous: %+v", c)
		}
	}
	// Campaign filter and shared filters.
	d = f.run(uid, "campaign-comparison", "campaign=small").Data.(CampaignComparisonData)
	if len(d.Campaigns) != 1 || d.Campaigns[0].Slug != "small" {
		t.Errorf("campaign filter = %+v", d.Campaigns)
	}
	d = f.run(uid, "campaign-comparison", "source=mastodon").Data.(CampaignComparisonData)
	if d.Campaigns[0].Slug != "big" || d.Campaigns[0].Clicks != 6 {
		t.Errorf("source filter = %+v", d.Campaigns[0])
	}
}

func TestCampaignComparison_StatusBoundaryInTZ(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	// testNow is 2026-11-18 20:41Z = 12:41 PST (still Nov 18) and Nov 19 in Tokyo? 20:41Z is 05:41 Nov 19 JST.
	for _, c := range []struct{ slug, starts, ends string }{
		{"starts-19", "2026-11-19 00:00+00", ""}, {"ends-18", "", "2026-11-18 00:00+00"},
	} {
		if _, err := f.pool.Exec(context.Background(),
			`INSERT INTO campaigns (user_id, name, slug, starts_at, ends_at) VALUES ($1,$2,$2,NULLIF($3,'')::timestamptz,NULLIF($4,'')::timestamptz)`,
			uid, c.slug, c.starts, c.ends); err != nil {
			t.Fatal(err)
		}
	}
	var cid int64
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM campaigns WHERE slug = 'starts-19'`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	l := f.lifecycleLink(uid, "s19", "https://example.com/x", daysAgo(5, 9), true, 0, nil, cid, "")
	f.clicks(1, click{link: l, campaign: cid, at: daysAgo(0, 1)}) // Nov 18 01:00Z
	status := func(tz string) map[string]string {
		out := map[string]string{}
		q := "from=2026-11-01&to=2026-11-30"
		if tz != "" {
			q += "&tz=" + tz
		}
		for _, c := range f.run(uid, "campaign-comparison", q).Data.(CampaignComparisonData).Campaigns {
			out[c.Slug] = c.Status
		}
		return out
	}
	la := status("America/Los_Angeles")
	if la["starts-19"] != "scheduled" || la["ends-18"] != "live" {
		t.Errorf("LA: %v", la)
	}
	jp := status("Asia/Tokyo")
	if jp["starts-19"] != "live" || jp["ends-18"] != "ended" {
		t.Errorf("Tokyo: %v", jp)
	}
}

func TestCatalogue_Comparative(t *testing.T) {
	seen := map[string]ReportInfo{}
	for _, r := range NewDefaultRegistry().Catalogue().Reports {
		seen[r.Name] = r
	}
	for name, lim := range map[string]int{"movers": 5, "idle-links": 50, "campaign-comparison": 0} {
		r, ok := seen[name]
		if !ok || r.DefaultLimit != lim {
			t.Errorf("catalogue %s = %+v", name, r)
		}
	}
	if len(seen["movers"].Params) != 2 || len(seen["idle-links"].Params) != 1 || seen["idle-links"].Params[0].Name != "min_age_days" {
		t.Errorf("params = %+v / %+v", seen["movers"].Params, seen["idle-links"].Params)
	}
}

func mustQuery(q string) url.Values {
	v, _ := url.ParseQuery(q)
	return v
}
