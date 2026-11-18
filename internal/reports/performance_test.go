package reports

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func perfCampaignData(t *testing.T, e *Envelope) CampaignPerformanceData {
	t.Helper()
	d, ok := e.Data.(CampaignPerformanceData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func perfLinkData(t *testing.T, e *Envelope) LinkPerformanceData {
	t.Helper()
	d, ok := e.Data.(LinkPerformanceData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

// seedPerf builds a dated campaign (Nov 10-30) with two guides, a duplicate
// mastodon link, a removed link and a link to guide A in a second campaign.
func seedPerf(f *fixture) (uid int64) {
	uid = f.user("a@example.com")
	c1, c2 := f.campaign(uid, "Guides 2026-11", "guides-2026-11"), f.campaign(uid, "Guides Dec", "guides-dec")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE campaigns SET starts_at = '2026-11-10', ends_at = '2026-11-30' WHERE id = $1`, c1); err != nil {
		f.t.Fatal(err)
	}
	created := daysAgo(30, 9)
	m := f.guideLink(uid, "mast84", "Measuring current — Mastodon guide post 84", guideA+"?utm_source=mastodon", "mastodon", c1, created)
	m2 := f.guideLink(uid, "mast84b", "Measuring current — Mastodon guide post 85", guideA, "mastodon", c1, created)
	b := f.guideLink(uid, "blue84", "Measuring current — Bluesky guide post 84", guideA+"/#top", "bluesky", c1, created)
	d := f.guideLink(uid, "mast75", "Deep sleep — Mastodon guide post 75", guideB, "mastodon", c1, created)
	gone := f.guideLink(uid, "gone75", "Deep sleep — Mastodon guide post 74", guideB, "mastodon", 0, created) // removed from the campaign
	other := f.guideLink(uid, "dec84", "Measuring current — Bluesky guide post 90", guideA, "bluesky", c2, created)
	for _, q := range []string{
		`UPDATE links SET placement = 'Mission St board' WHERE key = 'mast84'`,
		`UPDATE links SET placement = ' mission st BOARD ' WHERE key = 'mast84b'`,
	} {
		if _, err := f.pool.Exec(context.Background(), q); err != nil {
			f.t.Fatal(err)
		}
	}
	f.clicks(9, click{link: m, campaign: c1, at: daysAgo(2, 10), source: "mastodon", medium: "social"})
	f.clicks(2, click{link: m2, campaign: c1, at: daysAgo(2, 11), source: "mastodon", medium: "social"})
	f.clicks(5, click{link: b, campaign: c1, at: daysAgo(3, 10), source: "bluesky", medium: "social"})
	f.clicks(4, click{link: d, campaign: c1, at: daysAgo(4, 10), source: "mastodon", medium: "social"})
	f.clicks(3, click{link: gone, campaign: c1, at: daysAgo(4, 11), source: "mastodon", medium: "social"})
	f.clicks(6, click{link: m, campaign: c1, at: daysAgo(12, 10), source: "mastodon", medium: "social"}) // before the campaign
	f.clicks(7, click{link: other, campaign: c2, at: daysAgo(2, 12), source: "bluesky", medium: "social"})
	f.clicks(2, click{link: m, campaign: c1, at: daysAgo(2, 13), source: "mastodon", bot: true})
	return uid
}

func TestCampaignPerformance_DefaultWindowAndMatrix(t *testing.T) {
	f := newFixture(t)
	uid := seedPerf(f)
	env := f.run(uid, "campaign-performance", "slug=guides-2026-11")
	d := perfCampaignData(t, env)

	if env.Window.From != "2026-11-10" || env.Window.To != "2026-11-18" || env.PreviousWindow != nil {
		t.Errorf("window = %+v previous = %+v", env.Window, env.PreviousWindow)
	}
	c := d.Campaign
	if c.Slug != "guides-2026-11" || c.Name != "Guides 2026-11" || c.Status != "live" || c.StartsOn == nil || *c.StartsOn != "2026-11-10" || *c.EndsOn != "2026-11-30" {
		t.Errorf("campaign = %+v", c)
	}
	tt := d.Totals
	if tt.Clicks != 23 || tt.PreviousClicks != nil || tt.Links != 4 || tt.LinksClicked != 4 || tt.PerActiveDay == nil || *tt.PerActiveDay != 2.6 {
		t.Errorf("totals = %+v", tt)
	}
	if d.RemovedLinksClicks != 3 {
		t.Errorf("removed = %d", d.RemovedLinksClicks)
	}
	if len(d.Timeseries) != 9 || sumDays(d.Timeseries) != 23 || d.PreviousTimeseries != nil {
		t.Errorf("timeseries = %v", d.Timeseries)
	}

	m := d.Matrix
	if m.By != "source" || len(m.Columns) != 2 || m.Columns[0] != "mastodon" || m.ColumnTotals["mastodon"] != 18 || m.ColumnTotals["bluesky"] != 5 {
		t.Fatalf("matrix = %+v", m)
	}
	if len(m.Rows) != 2 || m.TotalRows != 2 {
		t.Fatalf("rows = %+v", m.Rows)
	}
	a, b := m.Rows[0], m.Rows[1]
	if a.ContentKey != "www.opencircuitsf.com/guides/measuring-current" || a.Total != 16 {
		t.Errorf("row a = %+v", a)
	}
	if got := a.Cells["mastodon"]; got.Clicks != 11 || got.Key != "" || len(got.Keys) != 2 || got.Keys[0] != "mast84" || got.Keys[1] != "mast84b" {
		t.Errorf("a/mastodon = %+v", got)
	}
	if got := a.Cells["bluesky"]; got.Clicks != 5 || got.Key != "blue84" || got.Keys != nil {
		t.Errorf("a/bluesky = %+v", got)
	}
	if b.Total != 7 || len(b.Cells) != 1 {
		t.Errorf("row b = %+v (bluesky pairing must be absent)", b)
	}
	if _, ok := b.Cells["bluesky"]; ok {
		t.Errorf("missing pairing present: %+v", b.Cells)
	}
	if got := b.Cells["mastodon"]; got.Clicks != 7 || len(got.Keys) != 2 {
		t.Errorf("b/mastodon = %+v", got)
	}

	if len(d.Placements) != 2 || d.Placements[0].Placement != "(none)" || !d.Placements[0].IsNone || d.Placements[0].Clicks != 12 ||
		d.Placements[1].Placement != "Mission St board" || d.Placements[1].Clicks != 11 {
		t.Errorf("placements = %+v", d.Placements)
	}
	h := strings.Join(env.Highlights, " ")
	for _, want := range []string{"Guides 2026-11: 23 clicks", "Best content", "Top source: mastodon", "3 clicks came from links no longer"} {
		if !strings.Contains(h, want) {
			t.Errorf("highlights missing %q: %v", want, env.Highlights)
		}
	}

	// The matrix agrees with the campaign filter on top-content.
	top := contentData(t, f.run(uid, "top-content", "campaign=guides-2026-11&from=2026-11-10"))
	if top.Groups[0].Clicks != a.Total {
		t.Errorf("top-content %d vs matrix %d", top.Groups[0].Clicks, a.Total)
	}
}

func TestCampaignPerformance_ColumnsRangeAndCompare(t *testing.T) {
	f := newFixture(t)
	uid := seedPerf(f)

	d := perfCampaignData(t, f.run(uid, "campaign-performance", "slug=guides-2026-11&columns=medium"))
	if d.Matrix.By != "medium" || len(d.Matrix.Columns) != 1 || d.Matrix.Columns[0] != "social" || d.Matrix.ColumnTotals["social"] != 23 {
		t.Errorf("medium matrix = %+v", d.Matrix)
	}
	d = perfCampaignData(t, f.run(uid, "campaign-performance", "slug=guides-2026-11&columns=placement&limit=1"))
	if d.Matrix.Columns[0] != "~none" || len(d.Matrix.Rows) != 1 || d.Matrix.TotalRows != 2 {
		t.Errorf("placement matrix = %+v", d.Matrix)
	}

	// An explicit range wins over the campaign's dates and gets a previous window.
	env := f.run(uid, "campaign-performance", "slug=guides-2026-11&range=7d")
	d = perfCampaignData(t, env)
	if env.Window.From != "2026-11-12" || env.PreviousWindow == nil || env.PreviousWindow.From != "2026-11-05" {
		t.Errorf("range=7d window = %+v / %+v", env.Window, env.PreviousWindow)
	}
	if d.Totals.Clicks != 23 || d.Totals.PreviousClicks == nil || *d.Totals.PreviousClicks != 6 ||
		len(d.PreviousTimeseries) != len(d.Timeseries) || d.Totals.PerActiveDay == nil || *d.Totals.PerActiveDay != 3.3 {
		t.Errorf("7d totals = %+v", d.Totals)
	}

	// compare=previous over the campaign's own dates: the same-length window before it.
	env = f.run(uid, "campaign-performance", "slug=guides-2026-11&compare=previous")
	d = perfCampaignData(t, env)
	if env.PreviousWindow == nil || env.PreviousWindow.From != "2026-11-01" || env.PreviousWindow.To != "2026-11-09" ||
		len(d.PreviousTimeseries) != 9 || d.Totals.PreviousClicks == nil || *d.Totals.PreviousClicks != 6 {
		t.Errorf("compare=previous: %+v %+v", env.PreviousWindow, d.Totals)
	}
	if !strings.Contains(strings.Join(env.Highlights, " "), "the previous 9 days") {
		t.Errorf("highlights = %v", env.Highlights)
	}
}

func TestCampaignPerformance_OtherColumn(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	c := f.campaign(uid, "Wide", "wide")
	l1 := f.guideLink(uid, "wide1", "Guide — Mastodon guide post 1", guideA, "mastodon", c, daysAgo(5, 9))
	l2 := f.guideLink(uid, "wide2", "Guide — Mastodon guide post 2", guideB, "mastodon", c, daysAgo(5, 9))
	// Eight sources, 8..1 clicks; the three smallest (s6, s7, s8 = 3+2+1) merge into other.
	for i := 1; i <= 8; i++ {
		f.clicks(9-i, click{link: l1, campaign: c, at: daysAgo(1, 10), source: "s" + string(rune('0'+i))})
	}
	f.clicks(1, click{link: l2, campaign: c, at: daysAgo(1, 10), source: "s8"})
	f.clicks(1, click{link: l2, campaign: c, at: daysAgo(1, 10), source: "s7"})

	d := perfCampaignData(t, f.run(uid, "campaign-performance", "slug=wide"))
	m := d.Matrix
	want := []string{"s1", "s2", "s3", "s4", "s5", "other"}
	if len(m.Columns) != 6 {
		t.Fatalf("columns = %v", m.Columns)
	}
	for i, w := range want {
		if m.Columns[i] != w {
			t.Fatalf("columns = %v, want %v", m.Columns, want)
		}
	}
	// s6=3, s7=2+1, s8=1+1 -> other = 8
	if m.ColumnTotals["other"] != 8 || m.ColumnTotals["s6"] != 0 || len(m.ColumnTotals) != 6 {
		t.Errorf("column totals = %v", m.ColumnTotals)
	}
	var sum int64
	for _, r := range m.Rows {
		sum += r.Total
	}
	if sum != d.Totals.Clicks {
		t.Errorf("rows sum %d != clicks %d", sum, d.Totals.Clicks)
	}
	// Guide A's other cell is one link (key), guide B's other cell is its single link too.
	for _, r := range m.Rows {
		if o := r.Cells["other"]; o.Key == "" || o.Keys != nil {
			t.Errorf("other cell of %s = %+v", r.ContentKey, o)
		}
	}
}

func TestCampaignPerformance_NotFoundAndParams(t *testing.T) {
	f := newFixture(t)
	uid := seedPerf(f)
	bob := f.user("b@example.com")
	f.campaign(bob, "Bob", "bobs-camp")

	for _, c := range []struct {
		uid  int64
		slug string
	}{{uid, "nope"}, {uid, "bobs-camp"}, {bob, "guides-2026-11"}} {
		_, err := f.svc.Run(context.Background(), c.uid, "campaign-performance", mustQuery("slug="+c.slug))
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "campaign_not_found" {
			t.Errorf("%d/%s: err = %v", c.uid, c.slug, err)
		}
	}
	for _, q := range []string{"", "slug=guides-2026-11&columns=bogus"} {
		_, err := f.svc.Run(context.Background(), uid, "campaign-performance", mustQuery(q))
		var pe *ParamError
		if !errors.As(err, &pe) {
			t.Errorf("%q: err = %v", q, err)
		}
	}

	// A campaign without dates uses the standard window; an empty one is quiet.
	e := f.run(bob, "campaign-performance", "slug=bobs-camp")
	d := perfCampaignData(t, e)
	if e.Window.From != "2026-10-20" || e.PreviousWindow == nil || d.Totals.Clicks != 0 || d.Totals.PreviousClicks == nil ||
		len(d.Matrix.Rows) != 0 || len(d.Placements) != 0 || !strings.Contains(e.Highlights[0], "no clicks") {
		t.Errorf("empty campaign: %+v %+v", e.Window, d)
	}
}

func TestLinkPerformance(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	f.clicks(3, click{link: f.id("mast84"), at: daysAgo(1, 9), source: "mastodon", referer: "https://mastodon.social/@x/1"})
	f.clicks(2, click{link: f.id("mast84"), at: daysAgo(1, 9), source: "mastodon", content: "post84"})
	f.clicks(1, click{link: f.id("mast84"), at: daysAgo(1, 9), bot: true})
	d := perfLinkData(t, f.run(uid, "link-performance", "key=mast84"))

	if d.Link.Key != "mast84" || d.Link.Label != "Mastodon guide post 84" || d.Link.ContentKey != "www.opencircuitsf.com/guides/measuring-current" ||
		d.Link.CampaignSlug == nil || *d.Link.CampaignSlug != "guides-nov" || !d.Link.Active {
		t.Errorf("link = %+v", d.Link)
	}
	tt := d.Totals
	if tt.Clicks != 14 || tt.PreviousClicks == nil || *tt.PreviousClicks != 6 || tt.AllTime != 20 || tt.LastClickedAt == nil ||
		!tt.LastClickedAt.Equal(daysAgo(1, 9)) {
		t.Errorf("totals = %+v", tt)
	}
	if len(d.Timeseries) != 30 || sumDays(d.Timeseries) != 14 || len(d.PreviousTimeseries) != 30 {
		t.Errorf("series = %d/%d", len(d.Timeseries), len(d.PreviousTimeseries))
	}
	if len(d.BySource) != 1 || d.BySource[0].Value != "mastodon" || d.BySource[0].Clicks != 14 || *d.BySource[0].PreviousClicks != 6 || d.BySource[0].Share != 1 {
		t.Errorf("by_source = %+v", d.BySource)
	}
	if len(d.ByMedium) != 2 || d.ByMedium[0].Value != "social" || d.ByMedium[0].Clicks != 9 || d.ByMedium[1].Value != "~none" {
		t.Errorf("by_medium = %+v", d.ByMedium)
	}
	if len(d.ByContent) != 2 || d.ByContent[0].Value != "~none" || d.ByContent[1].Value != "post84" || d.ByContent[1].Clicks != 2 {
		t.Errorf("by_content = %+v", d.ByContent)
	}
	if len(d.ByReferer) != 2 || d.ByReferer[0].Value != DirectValue || d.ByReferer[1].Value != "mastodon.social" || d.ByReferer[1].Clicks != 3 || d.ByReferer[1].PreviousClicks == nil {
		t.Errorf("by_referer = %+v", d.ByReferer)
	}
	// Sibling: the Bluesky link in another campaign. mast75 is other content.
	if len(d.Siblings) != 1 || d.Siblings[0].Key != "blue84" || d.Siblings[0].Label != "Bluesky guide post 84" || d.Siblings[0].Clicks != 5 ||
		d.Siblings[0].CampaignSlug == nil || *d.Siblings[0].CampaignSlug != "guides-dec" {
		t.Errorf("siblings = %+v", d.Siblings)
	}

	// Filters narrow the window figures but not all-time or last click.
	d = perfLinkData(t, f.run(uid, "link-performance", "key=mast84&content=post84"))
	if d.Totals.Clicks != 2 || d.Totals.AllTime != 20 {
		t.Errorf("filtered totals = %+v", d.Totals)
	}
	d = perfLinkData(t, f.run(uid, "link-performance", "key=mast84&include_bots=true&compare=none"))
	if d.Totals.Clicks != 15 || d.Totals.AllTime != 21 || d.Totals.PreviousClicks != nil || d.PreviousTimeseries != nil || d.BySource[0].PreviousClicks != nil {
		t.Errorf("bots/no compare = %+v", d.Totals)
	}

	// Referrer retention: a 90d window's previous period is past the 90 day detail limit.
	e := f.run(uid, "link-performance", "key=mast84&range=90d")
	d = perfLinkData(t, e)
	if d.ByReferer[0].PreviousClicks != nil || d.BySource[0].PreviousClicks == nil {
		t.Errorf("referer retention: %+v / %+v", d.ByReferer, d.BySource)
	}
	h := strings.Join(e.Highlights, " ")
	if !strings.Contains(h, refererPrevUnavailable) || !strings.Contains(h, "Mastodon guide post 84: ") || !strings.Contains(h, "other link") {
		t.Errorf("highlights = %v", e.Highlights)
	}
}

func TestLinkPerformance_NotFoundAndNeverClicked(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	bob := f.user("b@example.com")
	f.link(bob, "bobs", "")
	quiet := f.link(uid, "quiet", "")
	_ = quiet

	for _, c := range []struct {
		uid int64
		key string
	}{{uid, "nope"}, {uid, "bobs"}, {bob, "mast84"}} {
		_, err := f.svc.Run(context.Background(), c.uid, "link-performance", mustQuery("key="+c.key))
		var nf *NotFoundError
		if !errors.As(err, &nf) || nf.Code != "link_not_found" {
			t.Errorf("%d/%s: err = %v", c.uid, c.key, err)
		}
	}
	if _, err := f.svc.Run(context.Background(), uid, "link-performance", mustQuery("")); err == nil {
		t.Error("missing key: want error")
	}
	e := f.run(uid, "link-performance", "key=quiet")
	d := perfLinkData(t, e)
	if d.Totals.Clicks != 0 || d.Totals.AllTime != 0 || d.Totals.LastClickedAt != nil || d.Siblings == nil || len(d.BySource) != 0 ||
		d.Link.Label != "quiet" || !strings.Contains(strings.Join(e.Highlights, " "), "never been clicked") {
		t.Errorf("quiet link: %+v %v", d, e.Highlights)
	}
}

func TestPerformanceReportsCatalogued(t *testing.T) {
	cat := NewDefaultRegistry().Catalogue()
	seen := map[string]ReportInfo{}
	for _, r := range cat.Reports {
		seen[r.Name] = r
	}
	for name, param := range map[string]string{"campaign-performance": "slug", "link-performance": "key"} {
		r, ok := seen[name]
		if !ok || len(r.Params) == 0 || r.Params[0].Name != param || !r.Params[0].Required {
			t.Errorf("%s catalogue entry = %+v", name, r)
		}
	}
}

// id returns the id of the link with the given key.
func (f *fixture) id(key string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM links WHERE key = $1`, key).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestCampaignPerformance_OneDateFallsBackToStandardWindow(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	c := f.campaign(uid, "Open ended", "open-ended")
	if _, err := f.pool.Exec(context.Background(), `UPDATE campaigns SET starts_at = '2026-11-10' WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	l := f.guideLink(uid, "oe1", "Guide — Mastodon guide post 1", guideA, "mastodon", c, daysAgo(20, 9))
	f.clicks(9, click{link: l, campaign: c, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(4, click{link: l, campaign: c, at: daysAgo(20, 10), source: "mastodon"}) // Oct 29: in window, before the start date

	// The window stays the standard 30 days, with a previous window.
	// Only Nov 10-18 (9 days) are active, so 13 clicks / 9 = 1.4.
	e := f.run(uid, "campaign-performance", "slug=open-ended&campaign=other")
	d := perfCampaignData(t, e)
	if e.Window.From != "2026-10-20" || e.Window.To != "2026-11-18" || e.PreviousWindow == nil {
		t.Errorf("window = %+v / %+v", e.Window, e.PreviousWindow)
	}
	if d.Totals.Clicks != 13 || d.Totals.PreviousClicks == nil || d.Totals.PerActiveDay == nil || *d.Totals.PerActiveDay != 1.4 {
		t.Errorf("totals = %+v", d.Totals)
	}
	if e.Filters.Campaign != "open-ended" {
		t.Errorf("filters.campaign = %q", e.Filters.Campaign)
	}
	// With columns=placement the placement highlight is not repeated.
	e = f.run(uid, "campaign-performance", "slug=open-ended&columns=placement")
	if strings.Count(strings.Join(e.Highlights, " "), "Top placement") != 1 {
		t.Errorf("highlights = %v", e.Highlights)
	}
}
