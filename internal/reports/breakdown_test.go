package reports

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func breakdownData(t *testing.T, e *Envelope) BreakdownData {
	t.Helper()
	d, ok := e.Data.(BreakdownData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func timeseriesData(t *testing.T, e *Envelope) TimeseriesData {
	t.Helper()
	d, ok := e.Data.(TimeseriesData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func rowMap(d BreakdownData) map[string]int64 {
	m := map[string]int64{}
	for _, r := range d.Rows {
		m[r.Value] = r.Clicks
	}
	return m
}

func TestReportsRegisteredAndCatalogued(t *testing.T) {
	r := NewDefaultRegistry()
	for _, n := range []string{"breakdown", "timeseries"} {
		if _, ok := r.Get(n); !ok {
			t.Errorf("%s not registered", n)
		}
	}
	b, _ := r.Get("breakdown")
	if b.DefaultLimit != 20 || len(b.Params) != 1 || !b.Params[0].Required || b.Params[0].Name != "dimension" {
		t.Errorf("breakdown catalogue = %+v", b)
	}
}

func TestBreakdown_DimensionParam(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	for q, code := range map[string]string{"": "missing_dimension", "dimension=bogus": "invalid_dimension", "dimension=device": "invalid_dimension"} {
		v, _ := url.ParseQuery(q)
		_, err := f.svc.Run(context.Background(), uid, "breakdown", v)
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Code != code {
			t.Errorf("%q: err = %v, want %s", q, err, code)
		}
	}
	for q, code := range map[string]string{"split_by=bogus": "invalid_split_by", "granularity=month": "invalid_granularity", "series_limit=0": "invalid_series_limit"} {
		v, _ := url.ParseQuery(q)
		_, err := f.svc.Run(context.Background(), uid, "timeseries", v)
		var pe *ParamError
		if !errors.As(err, &pe) || pe.Code != code {
			t.Errorf("%q: err = %v, want %s", q, err, code)
		}
	}
}

func TestBreakdown_SourceShareOtherAndPrevious(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	f.clicks(6, click{link: l, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(3, click{link: l, at: daysAgo(3, 10), source: "bluesky"})
	f.clicks(2, click{link: l, at: daysAgo(4, 10), source: "x"})
	f.clicks(1, click{link: l, at: daysAgo(5, 10), source: "reddit"})
	f.clicks(2, click{link: l, at: daysAgo(6, 10)}) // no source
	f.clicks(4, click{link: l, at: daysAgo(3, 11), source: "mastodon", bot: true})
	f.clicks(5, click{link: l, at: daysAgo(40, 10), source: "mastodon"})

	d := breakdownData(t, f.run(uid, "breakdown", "dimension=source&limit=2"))
	if d.Total != 14 || len(d.Rows) != 2 {
		t.Fatalf("total/rows = %d/%d: %+v", d.Total, len(d.Rows), d.Rows)
	}
	if d.Rows[0].Value != "mastodon" || d.Rows[0].Clicks != 6 || d.Rows[0].Share != 0.429 ||
		d.Rows[0].PreviousClicks == nil || *d.Rows[0].PreviousClicks != 5 {
		t.Errorf("row 0 = %+v", d.Rows[0])
	}
	if d.Rows[1].Value != "bluesky" || *d.Rows[1].PreviousClicks != 0 {
		t.Errorf("row 1 = %+v", d.Rows[1])
	}
	if d.Other == nil || d.Other.Clicks != 5 || d.Other.Values != 3 {
		t.Errorf("other = %+v, want 5 clicks over 3 values", d.Other)
	}
	if d.PreviousTotal == nil || *d.PreviousTotal != 5 {
		t.Errorf("previous_total = %v", d.PreviousTotal)
	}

	// Everything fits: no other row; empty source is ~none; no previous with compare=none.
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=source&compare=none"))
	if d.Other != nil || len(d.Rows) != 5 || rowMap(d)[NoneValue] != 2 || d.PreviousTotal != nil || d.Rows[0].PreviousClicks != nil {
		t.Errorf("full = %+v", d)
	}
	var sum float64
	for _, r := range d.Rows {
		sum += r.Share
	}
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("shares sum to %v", sum)
	}

	// Bots included, and a source filter, both apply.
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=source&include_bots=true&compare=none"))
	if rowMap(d)["mastodon"] != 10 {
		t.Errorf("include_bots mastodon = %d", rowMap(d)["mastodon"])
	}
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=source&source=bluesky"))
	if d.Total != 3 || len(d.Rows) != 1 {
		t.Errorf("filtered = %+v", d)
	}
	if env := f.run(uid, "breakdown", "dimension=source"); len(env.Highlights) < 3 || !strings.Contains(env.Highlights[1], "Top source: mastodon") {
		t.Errorf("highlights = %v", env.Highlights)
	}
}

func TestBreakdown_NoClicks(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	env := f.run(uid, "breakdown", "dimension=referer")
	d := breakdownData(t, env)
	if d.Total != 0 || d.Rows == nil || len(d.Rows) != 0 || d.Other != nil || !strings.HasPrefix(env.Highlights[0], "No clicks") {
		t.Errorf("empty = %+v %v", d, env.Highlights)
	}
}

func TestBreakdown_RefererHosts(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	at := daysAgo(2, 10)
	f.clicks(3, click{link: l, at: at, referer: "https://Www.Google.com/search?q=x&utm_source=a"})
	f.clicks(2, click{link: l, at: at, referer: "http://www.google.com:8080/other#frag"})
	f.clicks(2, click{link: l, at: at, referer: "https://user:pw@t.co/abc"})
	f.clicks(1, click{link: l, at: at, referer: "//news.example.org/x"})
	f.clicks(1, click{link: l, at: at, referer: "android-app://com.google.android.gm"})
	f.clicks(1, click{link: l, at: at, referer: "https://[2001:db8::1]:8443/p"})
	f.clicks(2, click{link: l, at: at})                // NULL
	f.clicks(1, click{link: l, at: at, referer: "  "}) // blank
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, clicked_at, is_bot, referer) VALUES ($1, $2, FALSE, '')`, l, at); err != nil {
		t.Fatal(err)
	}
	d := breakdownData(t, f.run(uid, "breakdown", "dimension=referer&compare=none"))
	want := map[string]int64{
		"www.google.com": 5, "t.co": 2, "news.example.org": 1, "com.google.android.gm": 1,
		"[2001:db8::1]": 1, DirectValue: 4,
	}
	if got := rowMap(d); !reflect.DeepEqual(got, want) {
		t.Errorf("referer rows = %v, want %v", got, want)
	}
}

func TestBreakdown_CampaignAndLink(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	c1 := f.campaign(uid, "Guides Nov", "guides-nov")
	a, b := f.link(uid, "aaa", ""), f.link(uid, "bbb", "")
	f.clicks(5, click{link: a, campaign: c1, at: daysAgo(2, 10)})
	f.clicks(3, click{link: b, at: daysAgo(2, 10)})
	f.clicks(1, click{link: a, at: daysAgo(3, 10)})

	d := breakdownData(t, f.run(uid, "breakdown", "dimension=campaign&compare=none"))
	if len(d.Rows) != 2 || d.Rows[0].Value != "guides-nov" || d.Rows[0].Label != "Guides Nov" || d.Rows[0].Clicks != 5 ||
		d.Rows[1].Value != "none" || d.Rows[1].Label != "No campaign" || d.Rows[1].Clicks != 4 {
		t.Errorf("campaign rows = %+v", d.Rows)
	}
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=link&compare=none"))
	if len(d.Rows) != 2 || d.Rows[0].Value != "aaa" || d.Rows[0].Clicks != 6 || d.Rows[1].Value != "bbb" || d.Rows[1].Clicks != 3 {
		t.Errorf("link rows = %+v", d.Rows)
	}
	// A campaign filter narrows the link breakdown.
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=link&campaign=guides-nov&compare=none"))
	if d.Total != 5 || len(d.Rows) != 1 || d.Rows[0].Value != "aaa" {
		t.Errorf("filtered link rows = %+v", d.Rows)
	}
}

func TestBreakdown_ContentGroup(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	env := f.run(uid, "breakdown", "dimension=content_group")
	d := breakdownData(t, env)
	if d.Total != 18 || len(d.Rows) != 2 {
		t.Fatalf("content_group = %+v", d)
	}
	r := d.Rows[0]
	if r.Value != "www.opencircuitsf.com/guides/measuring-current" || r.Label != "Measuring current without blowing the fuse" ||
		r.Clicks != 14 || r.PreviousClicks == nil || *r.PreviousClicks != 8 {
		t.Errorf("row 0 = %+v", r)
	}
	if d.Rows[1].Clicks != 4 || *d.Rows[1].PreviousClicks != 8 || d.PreviousTotal == nil || *d.PreviousTotal != 16 {
		t.Errorf("row 1 = %+v prevTotal %v", d.Rows[1], d.PreviousTotal)
	}
	if !strings.Contains(strings.Join(env.Highlights, " "), "Measuring current without blowing the fuse") {
		t.Errorf("highlights = %v", env.Highlights)
	}
	d = breakdownData(t, f.run(uid, "breakdown", "dimension=content_group&limit=1"))
	if d.Other == nil || d.Other.Clicks != 4 || d.Other.Values != 1 {
		t.Errorf("other = %+v", d.Other)
	}
}

func TestBreakdown_UserScoping(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("a@example.com"), f.user("b@example.com")
	f.clicks(4, click{link: f.link(alice, "a1", ""), at: daysAgo(1, 10), source: "x"})
	f.clicks(9, click{link: f.link(bob, "b1", ""), at: daysAgo(1, 10), source: "y"})
	d := breakdownData(t, f.run(alice, "breakdown", "dimension=source"))
	if d.Total != 4 || len(d.Rows) != 1 {
		t.Errorf("alice sees %+v", d)
	}
}

func TestTimeseries_ZeroFillAndPrevious(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	f.clicks(3, click{link: l, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(1, click{link: l, at: daysAgo(5, 10), source: "bluesky"})
	f.clicks(2, click{link: l, at: daysAgo(8, 10), source: "mastodon"}) // previous window of a 7d range
	env := f.run(uid, "timeseries", "range=7d&tz=UTC")
	d := timeseriesData(t, env)
	if d.Granularity != "day" || len(d.Series) != 1 || d.Series[0].Name != "all" || d.Series[0].Clicks != 4 {
		t.Fatalf("series = %+v", d.Series)
	}
	pts := d.Series[0].Points
	if len(pts) != 7 || pts[0].Date != "2026-11-12" || pts[6].Date != "2026-11-18" {
		t.Fatalf("points = %+v", pts)
	}
	got := []int64{}
	for _, p := range pts {
		got = append(got, p.Clicks)
	}
	if !reflect.DeepEqual(got, []int64{0, 1, 0, 0, 3, 0, 0}) {
		t.Errorf("zero-filled counts = %v", got)
	}
	if len(d.Previous) != 7 || d.Previous[0].Date != "2026-11-05" || d.Previous[5].Clicks != 2 {
		t.Errorf("previous = %+v", d.Previous)
	}
	if len(env.Highlights) < 2 || !strings.Contains(env.Highlights[0], "4 clicks") || !strings.Contains(env.Highlights[1], "Best day: 2026-11-16") {
		t.Errorf("highlights = %v", env.Highlights)
	}
	d = timeseriesData(t, f.run(uid, "timeseries", "range=7d&tz=UTC&compare=none"))
	if d.Previous != nil {
		t.Errorf("previous = %+v, want null", d.Previous)
	}
	// An empty window still has its one zero series.
	d = timeseriesData(t, f.run(f.user("e@example.com"), "timeseries", "range=7d"))
	if len(d.Series) != 1 || d.Series[0].Name != "all" || len(d.Series[0].Points) != 7 || d.Series[0].Clicks != 0 {
		t.Errorf("empty = %+v", d.Series)
	}
}

// Weeks start Monday in tz, including the week that holds the fall-back
// change (2026-11-01 in Los Angeles is 25 hours long).
func TestTimeseries_WeekBucketsAcrossDST(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }
	f.clicks(1, click{link: l, at: at(2026, 10, 26, 8, 0)})   // Mon Oct 26 01:00 PDT
	f.clicks(2, click{link: l, at: at(2026, 11, 2, 7, 30)})   // Sun Nov 1 23:30 PST: still week of Oct 26
	f.clicks(4, click{link: l, at: at(2026, 11, 2, 8, 30)})   // Mon Nov 2 00:30 PST: week of Nov 2
	f.clicks(8, click{link: l, at: at(2026, 11, 8, 20, 0)})   // Sun Nov 8 12:00 PST
	f.clicks(16, click{link: l, at: at(2026, 10, 25, 20, 0)}) // Sun Oct 25: before the window
	env := f.run(uid, "timeseries", "granularity=week&from=2026-10-26&to=2026-11-08&tz=America/Los_Angeles&compare=none")
	d := timeseriesData(t, env)
	pts := d.Series[0].Points
	want := []DayCount{{"2026-10-26", 3}, {"2026-11-02", 12}}
	if !reflect.DeepEqual(pts, want) {
		t.Errorf("weeks = %+v, want %+v", pts, want)
	}
	if !strings.Contains(env.Highlights[1], "Busiest week: starting 2026-11-02") {
		t.Errorf("highlights = %v", env.Highlights)
	}

	// A window starting mid-week buckets on that week's Monday; days are DST-safe too.
	d = timeseriesData(t, f.run(uid, "timeseries", "granularity=week&from=2026-10-28&to=2026-11-03&tz=America/Los_Angeles&compare=none"))
	if got := d.Series[0].Points; len(got) != 2 || got[0].Date != "2026-10-26" || got[0].Clicks != 2 || got[1].Date != "2026-11-02" || got[1].Clicks != 4 {
		t.Errorf("partial weeks = %+v", got)
	}
	d = timeseriesData(t, f.run(uid, "timeseries", "from=2026-10-31&to=2026-11-02&tz=America/Los_Angeles&compare=none"))
	if got := d.Series[0].Points; len(got) != 3 || got[1].Date != "2026-11-01" || got[1].Clicks != 2 || got[2].Clicks != 4 {
		t.Errorf("days across DST = %+v", got)
	}
}

func TestTimeseries_SplitByAndOther(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	f.clicks(5, click{link: l, at: daysAgo(1, 10), source: "mastodon"})
	f.clicks(2, click{link: l, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(4, click{link: l, at: daysAgo(2, 10), source: "bluesky"})
	f.clicks(2, click{link: l, at: daysAgo(3, 10), source: "x"})
	f.clicks(1, click{link: l, at: daysAgo(3, 10), source: "reddit"})
	f.clicks(1, click{link: l, at: daysAgo(1, 10)})
	env := f.run(uid, "timeseries", "range=7d&tz=UTC&split_by=source&series_limit=2&compare=none")
	d := timeseriesData(t, env)
	if d.SplitBy != "source" || len(d.Series) != 3 {
		t.Fatalf("series = %+v", d.Series)
	}
	if d.Series[0].Name != "mastodon" || d.Series[0].Clicks != 7 || d.Series[1].Name != "bluesky" || d.Series[1].Clicks != 4 {
		t.Errorf("top series = %+v", d.Series[:2])
	}
	o := d.Series[2]
	if o.Name != "other" || !o.Other || o.Clicks != 4 || len(o.Points) != 7 || o.Points[3].Clicks != 3 || o.Points[4].Clicks != 0 || o.Points[5].Clicks != 1 {
		t.Errorf("other = %+v", o)
	}
	for _, s := range d.Series {
		if len(s.Points) != 7 {
			t.Errorf("%s not zero-filled: %d points", s.Name, len(s.Points))
		}
	}
	if !strings.Contains(strings.Join(env.Highlights, " "), "Top source: mastodon") {
		t.Errorf("highlights = %v", env.Highlights)
	}
	// previous stays a combined total even when split.
	d = timeseriesData(t, f.run(uid, "timeseries", "range=7d&split_by=source"))
	if len(d.Previous) != 7 {
		t.Errorf("previous = %+v", d.Previous)
	}
}

func TestTimeseries_SplitByContentGroupCampaignAndReferer(t *testing.T) {
	f := newFixture(t)
	uid := seedGuides(f)
	d := timeseriesData(t, f.run(uid, "timeseries", "split_by=content_group&compare=none"))
	if len(d.Series) != 2 || d.Series[0].Label != "Measuring current without blowing the fuse" || d.Series[0].Clicks != 14 || d.Series[1].Clicks != 4 {
		t.Fatalf("content_group series = %+v", d.Series)
	}
	if len(d.Series[0].Points) != 30 {
		t.Errorf("points = %d", len(d.Series[0].Points))
	}
	d = timeseriesData(t, f.run(uid, "timeseries", "split_by=campaign&compare=none&series_limit=1"))
	if len(d.Series) != 2 || d.Series[0].Name != "guides-nov" || d.Series[0].Label != "Guides Nov" || d.Series[0].Clicks != 13 || !d.Series[1].Other || d.Series[1].Clicks != 5 {
		t.Errorf("campaign series = %+v", d.Series)
	}
	f.clicks(2, click{link: f.link(uid, "ref1", ""), at: daysAgo(1, 10), referer: "https://t.co/x"})
	d = timeseriesData(t, f.run(uid, "timeseries", "split_by=referer&compare=none"))
	names := map[string]int64{}
	for _, s := range d.Series {
		names[s.Name] = s.Clicks
	}
	if names["t.co"] != 2 || names[DirectValue] != 18 {
		t.Errorf("referer series = %v", names)
	}
}

func TestBreakdown_BiggestGainHighlight(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	f.clicks(6, click{link: l, at: daysAgo(2, 10), source: "mastodon"})
	f.clicks(3, click{link: l, at: daysAgo(3, 10), source: "bluesky"})
	f.clicks(5, click{link: l, at: daysAgo(40, 10), source: "mastodon"})
	h := strings.Join(f.run(uid, "breakdown", "dimension=source").Highlights, "\n")
	if !strings.Contains(h, "Biggest gain: bluesky, up 3 clicks on the previous 30 days.") {
		t.Errorf("highlights = %q", h)
	}
	h = strings.Join(f.run(uid, "breakdown", "dimension=source&compare=none").Highlights, "\n")
	if strings.Contains(h, "Biggest gain") {
		t.Errorf("gain without previous: %q", h)
	}
	// Same noun, with spaces, in both reports.
	f.clicks(1, click{link: l, at: daysAgo(2, 10)})
	if h := strings.Join(f.run(uid, "breakdown", "dimension=content_group").Highlights, "\n"); !strings.Contains(h, "Top content group:") {
		t.Errorf("breakdown noun: %q", h)
	}
	if h := strings.Join(f.run(uid, "timeseries", "split_by=content_group").Highlights, "\n"); !strings.Contains(h, "Top content group:") || strings.Contains(h, "_") {
		t.Errorf("timeseries noun: %q", h)
	}
}

func TestBreakdown_Placement(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	poster, flyer, none := f.link(uid, "p1", " Poster "), f.link(uid, "f1", "flyer"), f.link(uid, "n1", "")
	f.clicks(4, click{link: poster, at: daysAgo(2, 10)})
	f.clicks(2, click{link: flyer, at: daysAgo(2, 10)})
	f.clicks(1, click{link: none, at: daysAgo(2, 10)})
	d := breakdownData(t, f.run(uid, "breakdown", "dimension=placement&compare=none"))
	if want := map[string]int64{"poster": 4, "flyer": 2, NoneValue: 1}; !reflect.DeepEqual(rowMap(d), want) {
		t.Errorf("placement rows = %v, want %v", rowMap(d), want)
	}
}

func TestBreakdown_RefererRetention(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	f.clicks(3, click{link: l, at: daysAgo(2, 10), referer: "https://t.co/x"})
	f.clicks(2, click{link: l, at: daysAgo(100, 10), referer: "https://t.co/x"})

	// 90d: the current window is inside retention, the previous one is not.
	env := f.run(uid, "breakdown", "dimension=referer&range=90d")
	d := breakdownData(t, env)
	h := strings.Join(env.Highlights, "\n")
	if d.PreviousTotal != nil || len(d.Rows) == 0 || d.Rows[0].PreviousClicks != nil {
		t.Errorf("previous not hidden: %+v", d)
	}
	if strings.Contains(h, "Biggest gain") || !strings.Contains(h, refererPrevUnavailable) || strings.Contains(h, refererCurUndercount) {
		t.Errorf("highlights = %q", h)
	}
	// Other dimensions are unaffected.
	if d := breakdownData(t, f.run(uid, "breakdown", "dimension=source&range=90d")); d.PreviousTotal == nil {
		t.Error("source previous_total hidden")
	}
	// A recent previous window stays available.
	if d := breakdownData(t, f.run(uid, "breakdown", "dimension=referer")); d.PreviousTotal == nil || d.Rows[0].PreviousClicks == nil {
		t.Errorf("30d previous hidden: %+v", d)
	}

	// 365d: the current window itself reaches past the cutoff.
	env = f.run(uid, "breakdown", "dimension=referer&range=365d")
	if h := strings.Join(env.Highlights, "\n"); !strings.Contains(h, refererCurUndercount) || !strings.Contains(h, refererPrevUnavailable) {
		t.Errorf("365d highlights = %q", h)
	}

	// Timeseries split by referer follows the same rules.
	env = f.run(uid, "timeseries", "split_by=referer&range=90d")
	ts := timeseriesData(t, env)
	if ts.Previous != nil || !strings.Contains(strings.Join(env.Highlights, "\n"), refererPrevUnavailable) {
		t.Errorf("timeseries previous = %+v highlights %v", ts.Previous, env.Highlights)
	}
	env = f.run(uid, "timeseries", "split_by=referer&range=365d&compare=none")
	if !strings.Contains(strings.Join(env.Highlights, "\n"), refererCurUndercount) {
		t.Errorf("365d timeseries highlights = %v", env.Highlights)
	}
	if ts := timeseriesData(t, f.run(uid, "timeseries", "split_by=source&range=90d")); ts.Previous == nil {
		t.Error("source previous hidden")
	}
}

func TestTimeseries_WeeklyPreviousAlignsByPosition(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	// Today is Wed 2026-11-18; the 30d window is Tue Oct 20 - Wed Nov 18, so it
	// has five buckets (Oct 19 partial, Oct 26, Nov 2, Nov 9, Nov 16 partial).
	// The previous window is Sep 20 - Oct 19.
	f.clicks(1, click{link: l, at: time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)})  // offset 1: first bucket
	f.clicks(2, click{link: l, at: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)})  // offset 8: second
	f.clicks(4, click{link: l, at: time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)}) // offset 28: last
	d := timeseriesData(t, f.run(uid, "timeseries", "granularity=week&tz=UTC"))
	cur := d.Series[0].Points
	if len(cur) != 5 || cur[0].Date != "2026-10-19" || cur[4].Date != "2026-11-16" {
		t.Fatalf("current = %+v", cur)
	}
	want := []DayCount{{"2026-09-20", 1}, {"2026-09-26", 2}, {"2026-10-03", 0}, {"2026-10-10", 0}, {"2026-10-17", 4}}
	if !reflect.DeepEqual(d.Previous, want) {
		t.Errorf("previous = %+v, want %+v", d.Previous, want)
	}
	if len(d.Previous) != len(cur) {
		t.Fatalf("bucket counts differ: %d vs %d", len(d.Previous), len(cur))
	}
}

func TestTimeseries_SplitByLink(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	a, b := f.link(uid, "aaa", ""), f.link(uid, "bbb", "")
	if _, err := f.pool.Exec(context.Background(), `UPDATE links SET title = 'Alpha' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	f.clicks(3, click{link: a, at: daysAgo(1, 10)})
	f.clicks(5, click{link: b, at: daysAgo(2, 10)})
	f.clicks(1, click{link: a, at: daysAgo(3, 10)})
	d := timeseriesData(t, f.run(uid, "timeseries", "range=7d&tz=UTC&split_by=link&compare=none"))
	if len(d.Series) != 2 || d.Series[0].Name != "bbb" || d.Series[0].Clicks != 5 || d.Series[1].Name != "aaa" || d.Series[1].Label != "Alpha" || d.Series[1].Clicks != 4 {
		t.Fatalf("series = %+v", d.Series)
	}
	if p := d.Series[1].Points; len(p) != 7 || p[3].Clicks != 1 || p[5].Clicks != 3 {
		t.Errorf("points = %+v", p)
	}
}

// Spring forward: 2026-03-08 in Los Angeles is a 23-hour day.
func TestTimeseries_WeekBucketsSpringForwardDST(t *testing.T) {
	f := newFixture(t)
	uid := f.user("a@example.com")
	l := f.link(uid, "k1", "")
	at := func(m time.Month, d, h, min int) time.Time { return time.Date(2026, m, d, h, min, 0, 0, time.UTC) }
	f.clicks(1, click{link: l, at: at(3, 2, 8, 30)})  // Mon Mar 2 00:30 PST
	f.clicks(2, click{link: l, at: at(3, 9, 6, 30)})  // Sun Mar 8 22:30 PST: week of Mar 2
	f.clicks(4, click{link: l, at: at(3, 9, 7, 30)})  // Mon Mar 9 00:30 PDT: week of Mar 9
	f.clicks(8, click{link: l, at: at(3, 16, 6, 59)}) // Sun Mar 15 23:59 PDT: week of Mar 9
	f.clicks(16, click{link: l, at: at(3, 16, 7, 0)}) // Mon Mar 16 00:00 PDT: outside the window
	d := timeseriesData(t, f.run(uid, "timeseries", "granularity=week&from=2026-03-02&to=2026-03-15&tz=America/Los_Angeles&compare=none"))
	want := []DayCount{{"2026-03-02", 3}, {"2026-03-09", 12}}
	if got := d.Series[0].Points; !reflect.DeepEqual(got, want) {
		t.Errorf("weeks = %+v, want %+v", got, want)
	}
	d = timeseriesData(t, f.run(uid, "timeseries", "from=2026-03-07&to=2026-03-09&tz=America/Los_Angeles&compare=none"))
	if got := d.Series[0].Points; len(got) != 3 || got[1].Date != "2026-03-08" || got[1].Clicks != 2 || got[2].Clicks != 4 {
		t.Errorf("days across DST = %+v", got)
	}
}
