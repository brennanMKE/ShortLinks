package reports

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// guideLink seeds a link with a title, destination, utm_source and campaign.
func (f *fixture) guideLink(userID int64, key, title, dest, source string, campaign int64, created time.Time) int64 {
	f.t.Helper()
	var camp *int64
	if campaign != 0 {
		camp = &campaign
	}
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, title, active, denied_reason, created_at, utm_source, utm_medium, campaign_id)
		 VALUES ($1, $2, $3, $4, TRUE, 0, $5, $6, 'social', $7) RETURNING id`,
		userID, key, dest, title, created, nz(source), camp).Scan(&id); err != nil {
		f.t.Fatalf("seed guide link: %v", err)
	}
	return id
}

func (f *fixture) run(userID int64, report, query string) *Envelope {
	f.t.Helper()
	v, _ := url.ParseQuery(query)
	env, err := f.svc.Run(context.Background(), userID, report, v)
	if err != nil {
		f.t.Fatalf("Run(%s, %q): %v", report, query, err)
	}
	return env
}

func contentData(t *testing.T, e *Envelope) TopContentData {
	t.Helper()
	d, ok := e.Data.(TopContentData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func linksData(t *testing.T, e *Envelope) TopLinksData {
	t.Helper()
	d, ok := e.Data.(TopLinksData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

const guideA = "https://www.opencircuitsf.com/guides/measuring-current"
const guideB = "https://www.opencircuitsf.com/guides/deep-sleep"

// seedGuides: guide A is two links (mastodon in campaign c1, bluesky in c2,
// with different utm/fragment/slash forms of the URL), guide B is one link.
func seedGuides(f *fixture) (uid int64) {
	uid = f.user("a@example.com")
	c1, c2 := f.campaign(uid, "Guides Nov", "guides-nov"), f.campaign(uid, "Guides Dec", "guides-dec")
	created := daysAgo(20, 9)
	m := f.guideLink(uid, "mast84", "Measuring current without blowing the fuse — Mastodon guide post 84", guideA+"?utm_source=mastodon&utm_medium=social", "mastodon", c1, created)
	b := f.guideLink(uid, "blue84", "Measuring current without blowing the fuse — Bluesky guide post 84", strings.ToUpper(guideA[:12])+guideA[12:]+"/#top", "bluesky", c2, created.Add(time.Hour))
	d := f.guideLink(uid, "mast75", "Deep sleep: make a battery last months — Mastodon guide post 75", guideB, "mastodon", c1, created.Add(2*time.Hour))
	// Current window (30d): A = 9 + 5, B = 4. Previous: A = 6 + 2, B = 8.
	f.clicks(9, click{link: m, campaign: c1, at: daysAgo(2, 10), source: "mastodon", medium: "social"})
	f.clicks(5, click{link: b, campaign: c2, at: daysAgo(3, 10), source: "bluesky", medium: "social"})
	f.clicks(4, click{link: d, campaign: c1, at: daysAgo(4, 10), source: "mastodon", medium: "social"})
	f.clicks(6, click{link: m, campaign: c1, at: daysAgo(40, 10), source: "mastodon", medium: "social"})
	f.clicks(2, click{link: b, campaign: c2, at: daysAgo(41, 10), source: "bluesky", medium: "social"})
	f.clicks(8, click{link: d, campaign: c1, at: daysAgo(42, 10), source: "mastodon", medium: "social"})
	return uid
}

func TestTopContent_GroupsAcrossCampaigns(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	env := f.run(uid, "top-content", "")
	d := contentData(t, env)
	if d.TotalGroups != 2 || len(d.Groups) != 2 {
		t.Fatalf("groups = %d/%d, want 2", len(d.Groups), d.TotalGroups)
	}
	g := d.Groups[0]
	if g.ContentKey != "www.opencircuitsf.com/guides/measuring-current" || g.Title != "Measuring current without blowing the fuse" ||
		g.Path != "/guides/measuring-current" || g.Clicks != 14 {
		t.Errorf("group 0 = %+v", g)
	}
	if g.PreviousClicks == nil || *g.PreviousClicks != 8 || g.ChangePct == nil || *g.ChangePct != 75 {
		t.Errorf("previous/change = %v / %v, want 8 / 75", g.PreviousClicks, g.ChangePct)
	}
	if len(g.Links) != 2 || g.Links[0].Key != "mast84" || g.Links[0].Label != "Mastodon guide post 84" || g.Links[0].Clicks != 9 ||
		g.Links[0].CampaignSlug == nil || *g.Links[0].CampaignSlug != "guides-nov" ||
		g.Links[1].Label != "Bluesky guide post 84" || *g.Links[1].CampaignSlug != "guides-dec" {
		t.Errorf("links = %+v", g.Links)
	}
	if len(g.BySource) != 2 || g.BySource[0] != (SourceCount{"mastodon", 9}) || g.BySource[1] != (SourceCount{"bluesky", 5}) {
		t.Errorf("by_source = %+v", g.BySource)
	}
	if d.Groups[1].Title != "Deep sleep: make a battery last months" || d.Groups[1].Clicks != 4 {
		t.Errorf("group 1 = %+v", d.Groups[1])
	}
	// Highlights name the top content and the biggest source share (9/14 = 64%).
	h := strings.Join(env.Highlights, " ")
	if !strings.Contains(h, "Measuring current without blowing the fuse") || !strings.Contains(h, "mastodon drove the biggest share") || !strings.Contains(h, "64%") {
		t.Errorf("highlights = %v", env.Highlights)
	}
}

func TestTopContent_FiltersApplied(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	d := contentData(t, f.run(uid, "top-content", "source=bluesky"))
	if len(d.Groups) != 1 || d.Groups[0].Clicks != 5 || len(d.Groups[0].Links) != 1 || d.Groups[0].Links[0].Key != "blue84" {
		t.Fatalf("source filter: %+v", d.Groups)
	}
	if d.Groups[0].PreviousClicks == nil || *d.Groups[0].PreviousClicks != 2 {
		t.Errorf("previous under filter = %v, want 2", d.Groups[0].PreviousClicks)
	}
	d = contentData(t, f.run(uid, "top-content", "campaign=guides-nov"))
	if len(d.Groups) != 2 || d.Groups[0].Clicks != 9 {
		t.Errorf("campaign filter: %+v", d.Groups)
	}
}

func TestTopContent_NoPreviousWindow(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	d := contentData(t, f.run(uid, "top-content", "compare=none"))
	if d.Groups[0].PreviousClicks != nil || d.Groups[0].ChangePct != nil {
		t.Errorf("previous = %v change = %v, want nil", d.Groups[0].PreviousClicks, d.Groups[0].ChangePct)
	}
}

func TestTopContent_SortChangeAndLimit(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	// A grew by 6, B shrank by 4; clicks order is also A, B. Flip with change:
	// make B grow most by adding clicks.
	var id int64
	_ = f.pool.QueryRow(context.Background(), `SELECT id FROM links WHERE key = 'mast75'`).Scan(&id)
	f.clicks(30, click{link: id, at: daysAgo(5, 10), source: "mastodon"})
	d := contentData(t, f.run(uid, "top-content", "sort=change"))
	if d.Groups[0].Title != "Deep sleep: make a battery last months" {
		t.Errorf("sort=change first = %q", d.Groups[0].Title)
	}
	d = contentData(t, f.run(uid, "top-content", "limit=1"))
	if len(d.Groups) != 1 || d.TotalGroups != 2 {
		t.Errorf("limit: %d groups, total %d", len(d.Groups), d.TotalGroups)
	}
}

func TestTopContent_NewestIncludesUnclicked(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	f.guideLink(uid, "new1", "Fresh guide — Mastodon guide post 90", "https://www.opencircuitsf.com/guides/fresh/", "mastodon", 0, daysAgo(1, 9))
	if d := contentData(t, f.run(uid, "top-content", "")); len(d.Groups) != 2 {
		t.Errorf("clicks sort lists a group with no clicks: %d groups", len(d.Groups))
	}
	d := contentData(t, f.run(uid, "top-content", "sort=newest"))
	if len(d.Groups) != 3 || d.Groups[0].Title != "Fresh guide" || d.Groups[0].Clicks != 0 {
		t.Fatalf("newest: %+v", d.Groups)
	}
	if d.Groups[1].Title != "Deep sleep: make a battery last months" {
		t.Errorf("newest second = %q", d.Groups[1].Title)
	}
}

func TestTopContent_UserScopingAndErrors(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	bob := f.user("bob@example.com")
	if d := contentData(t, f.run(bob, "top-content", "")); len(d.Groups) != 0 {
		t.Errorf("bob sees %d groups", len(d.Groups))
	}
	env := f.run(bob, "top-content", "")
	if len(env.Highlights) != 1 || !strings.HasPrefix(env.Highlights[0], "No content") {
		t.Errorf("empty highlights = %v", env.Highlights)
	}
	_, err := f.svc.Run(context.Background(), uid, "top-content", url.Values{"sort": {"bogus"}})
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Errorf("bad sort err = %v", err)
	}
}

func TestTopLinks(t *testing.T) {
	f := newFixture(t)
	f.svc.BaseURL = "https://s.example"
	uid := seedGuides(f)
	env := f.run(uid, "top-links", "")
	d := linksData(t, env)
	if d.TotalLinks != 3 || len(d.Links) != 3 {
		t.Fatalf("links = %d/%d", len(d.Links), d.TotalLinks)
	}
	l := d.Links[0]
	if l.Key != "mast84" || l.Clicks != 9 || l.ShortURL != "https://s.example/u/mast84" || l.Label != "Mastodon guide post 84" ||
		l.ContentKey != "www.opencircuitsf.com/guides/measuring-current" || l.ContentTitle != "Measuring current without blowing the fuse" ||
		l.UTMSource != "mastodon" || l.UTMMedium != "social" || l.CampaignSlug == nil || *l.CampaignSlug != "guides-nov" || !l.Active {
		t.Errorf("top link = %+v", l)
	}
	if l.PreviousClicks == nil || *l.PreviousClicks != 6 || l.ChangePct == nil || *l.ChangePct != 50 {
		t.Errorf("change = %v / %v", l.PreviousClicks, l.ChangePct)
	}
	if d.Links[1].Key != "blue84" || d.Links[2].Key != "mast75" {
		t.Errorf("order = %s %s", d.Links[1].Key, d.Links[2].Key)
	}
	h := strings.Join(env.Highlights, " ")
	if !strings.Contains(h, "mast84") || !strings.Contains(h, "mastodon drove the biggest share") {
		t.Errorf("highlights = %v", env.Highlights)
	}

	// Filters, limit, newest and no-compare.
	if d := linksData(t, f.run(uid, "top-links", "source=bluesky")); len(d.Links) != 1 || d.Links[0].Key != "blue84" {
		t.Errorf("source filter: %+v", d.Links)
	}
	if d := linksData(t, f.run(uid, "top-links", "limit=2")); len(d.Links) != 2 || d.TotalLinks != 3 {
		t.Errorf("limit: %d / %d", len(d.Links), d.TotalLinks)
	}
	f.guideLink(uid, "new1", "Fresh", "https://www.opencircuitsf.com/guides/fresh", "", 0, daysAgo(1, 9))
	d = linksData(t, f.run(uid, "top-links", "sort=newest&compare=none"))
	if len(d.Links) != 4 || d.Links[0].Key != "new1" || d.Links[0].Clicks != 0 || d.Links[0].PreviousClicks != nil {
		t.Errorf("newest: %+v", d.Links[0])
	}
	if d.Links[0].ContentTitle != "Fresh" {
		t.Errorf("content title = %q", d.Links[0].ContentTitle)
	}
}

func TestCatalogue_TopReports(t *testing.T) {
	c := NewDefaultRegistry().Catalogue()
	seen := map[string]ReportInfo{}
	for _, r := range c.Reports {
		seen[r.Name] = r
	}
	for name, lim := range map[string]int{"top-content": 10, "top-links": 20} {
		r, ok := seen[name]
		if !ok || r.DefaultLimit != lim || len(r.Params) != 1 || r.Params[0].Name != "sort" {
			t.Errorf("catalogue %s = %+v", name, r)
		}
	}
}

func TestTopReports_Truncated(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	for _, rep := range []string{"top-content", "top-links"} {
		env := f.run(uid, rep, "")
		if contentTruncated(env) {
			t.Errorf("%s truncated at the default cap", rep)
		}
	}
	old := maxLinkRows
	maxLinkRows = 2
	t.Cleanup(func() { maxLinkRows = old })
	for _, rep := range []string{"top-content", "top-links"} {
		env := f.run(uid, rep, "")
		if !contentTruncated(env) {
			t.Errorf("%s not flagged truncated at cap 2", rep)
		}
		if h := strings.Join(env.Highlights, " "); !strings.Contains(h, "may be incomplete") {
			t.Errorf("%s highlights = %v", rep, env.Highlights)
		}
	}
}

func contentTruncated(e *Envelope) bool {
	switch d := e.Data.(type) {
	case TopContentData:
		return d.Truncated
	case TopLinksData:
		return d.Truncated
	}
	return false
}

func TestLabelIndependentOfClicks(t *testing.T) {
	f := newFixture(t)
	uid := f.user("l@example.com")
	// No utm_source on the link: label comes from the title alone.
	f.guideLink(uid, "k1", "Guide — Mastodon post 1", "https://x.com/g", "", 0, daysAgo(5, 9))
	id := int64(0)
	_ = f.pool.QueryRow(context.Background(), `SELECT id FROM links WHERE key='k1'`).Scan(&id)
	f.clicks(2, click{link: id, at: daysAgo(1, 9), source: "zzz"})
	d := linksData(t, f.run(uid, "top-links", ""))
	if d.Links[0].Label != "Mastodon post 1" || d.Links[0].ContentTitle != "Guide" {
		t.Errorf("row = %+v", d.Links[0])
	}
}
