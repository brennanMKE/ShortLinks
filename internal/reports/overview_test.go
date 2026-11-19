package reports

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testNow is the pinned "now" for the DB-backed tests: 12:41 PST on Nov 18.
var testNow = time.Date(2026, 11, 18, 20, 41, 7, 0, time.UTC)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}
	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`TRUNCATE clicks, links, campaigns, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
}

func newFixture(t *testing.T) *fixture {
	pool := testPool(t)
	svc := NewService(pool, "")
	svc.Now = func() time.Time { return testNow }
	return &fixture{t: t, pool: pool, svc: svc}
}

func (f *fixture) user(email string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at) VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email).Scan(&id); err != nil {
		f.t.Fatalf("seed user: %v", err)
	}
	return id
}

func (f *fixture) link(userID int64, key, placement string) int64 {
	f.t.Helper()
	var id int64
	var pl *string
	if placement != "" {
		pl = &placement
	}
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, placement)
		 VALUES ($1, $2, 'https://example.com/', TRUE, 0, now(), $3) RETURNING id`, userID, key, pl).Scan(&id); err != nil {
		f.t.Fatalf("seed link: %v", err)
	}
	return id
}

func (f *fixture) campaign(userID int64, name, slug string) int64 {
	f.t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO campaigns (user_id, name, slug) VALUES ($1, $2, $3) RETURNING id`, userID, name, slug).Scan(&id); err != nil {
		f.t.Fatalf("seed campaign: %v", err)
	}
	return id
}

// click describes one seeded click; empty strings store NULL.
type click struct {
	link           int64
	campaign       int64 // 0 = none
	at             time.Time
	bot            bool
	source, medium string
	content        string
	referer        string
	ip             string // non-empty stores a 1-byte ip_hash
}

func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (f *fixture) clicks(n int, c click) {
	f.t.Helper()
	var camp *int64
	if c.campaign != 0 {
		camp = &c.campaign
	}
	var ip []byte
	if c.ip != "" {
		ip = []byte(c.ip)
	}
	for i := 0; i < n; i++ {
		if _, err := f.pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, campaign_id, clicked_at, is_bot, utm_source, utm_medium, utm_content, ip_hash, referer)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			c.link, camp, c.at, c.bot, nz(c.source), nz(c.medium), nz(c.content), ip, nz(c.referer)); err != nil {
			f.t.Fatalf("seed click: %v", err)
		}
	}
}

func (f *fixture) overview(userID int64, query string) *Envelope {
	f.t.Helper()
	v, _ := url.ParseQuery(query)
	env, err := f.svc.Run(context.Background(), userID, "overview", v)
	if err != nil {
		f.t.Fatalf("Run(%q): %v", query, err)
	}
	return env
}

func data(t *testing.T, e *Envelope) OverviewData {
	t.Helper()
	d, ok := e.Data.(OverviewData)
	if !ok {
		t.Fatalf("data is %T", e.Data)
	}
	return d
}

func daysAgo(n int, hour int) time.Time {
	return time.Date(2026, 11, 18-n, hour, 0, 0, 0, time.UTC)
}

func TestOverview_UserScoping(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice@example.com"), f.user("bob@example.com")
	la, lb := f.link(alice, "aaa", ""), f.link(bob, "bbb", "")
	f.clicks(3, click{link: la, at: daysAgo(1, 10), source: "mastodon"})
	f.clicks(7, click{link: lb, at: daysAgo(1, 10), source: "bluesky"})

	d := data(t, f.overview(alice, ""))
	if d.Totals.Clicks != 3 || d.Totals.LinksClicked != 1 || d.Totals.LinksTotal != 1 {
		t.Errorf("alice totals = %+v", d.Totals)
	}
	for _, fv := range d.Facets.Source {
		if fv.Value == "bluesky" {
			t.Error("bob's source leaked into alice's facets")
		}
	}
	// A campaign slug owned by bob must not match for alice.
	cb := f.campaign(bob, "Bob C", "bobc")
	f.clicks(2, click{link: lb, campaign: cb, at: daysAgo(1, 11)})
	d = data(t, f.overview(alice, "campaign=bobc"))
	if d.Totals.Clicks != 0 {
		t.Errorf("alice saw %d clicks through bob's campaign slug", d.Totals.Clicks)
	}
	for _, c := range data(t, f.overview(alice, "")).Campaigns {
		if c.Slug != nil {
			t.Errorf("alice's campaigns include %v", *c.Slug)
		}
	}
}

func TestOverview_BotsExcludedByDefault(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	f.clicks(5, click{link: l, at: daysAgo(2, 9)})
	f.clicks(3, click{link: l, at: daysAgo(2, 9), bot: true})

	d := data(t, f.overview(u, ""))
	if d.Totals.Clicks != 5 || d.Totals.ExcludedBots != 3 {
		t.Errorf("default totals = %+v", d.Totals)
	}
	d = data(t, f.overview(u, "include_bots=true"))
	if d.Totals.Clicks != 8 || d.Totals.ExcludedBots != 0 {
		t.Errorf("include_bots totals = %+v", d.Totals)
	}
	env := f.overview(u, "include_bots=true")
	if !env.Filters.IncludeBots {
		t.Error("filters.include_bots not echoed")
	}
}

func TestOverview_TodayIncludedAndPartial(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	f.clicks(4, click{link: l, at: testNow.Add(-time.Hour)})

	env := f.overview(u, "")
	d := data(t, env)
	if !env.Window.PartialLastDay || env.Window.To != "2026-11-18" || env.Window.From != "2026-10-20" || env.Window.TZ != "UTC" {
		t.Errorf("window = %+v", env.Window)
	}
	if env.PreviousWindow == nil || env.PreviousWindow.From != "2026-09-20" || env.PreviousWindow.To != "2026-10-19" {
		t.Errorf("previous_window = %+v", env.PreviousWindow)
	}
	if len(d.Timeseries) != 30 {
		t.Fatalf("timeseries has %d days", len(d.Timeseries))
	}
	last := d.Timeseries[29]
	if last.Date != "2026-11-18" || last.Clicks != 4 || d.Totals.Clicks != 4 {
		t.Errorf("last day = %+v, totals %+v", last, d.Totals)
	}
	if d.BestDay == nil || d.BestDay.Date != "2026-11-18" || d.BestDay.Clicks != 4 {
		t.Errorf("best_day = %+v", d.BestDay)
	}
	if env.GeneratedAt != testNow.Truncate(time.Second) {
		t.Errorf("generated_at = %v", env.GeneratedAt)
	}
}

func TestOverview_PreviousPeriod(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	f.clicks(6, click{link: l, at: daysAgo(5, 12)})  // current
	f.clicks(4, click{link: l, at: daysAgo(35, 12)}) // previous (Oct 14)... see window below
	f.clicks(9, click{link: l, at: daysAgo(70, 12)}) // before previous

	env := f.overview(u, "range=30d")
	d := data(t, env)
	if d.Totals.Clicks != 6 {
		t.Errorf("current = %d", d.Totals.Clicks)
	}
	if d.Previous == nil || d.Previous.Clicks != 4 {
		t.Fatalf("previous = %+v", d.Previous)
	}
	if len(d.PreviousTimeseries) != 30 || d.PreviousTimeseries[0].Date != "2026-09-20" {
		t.Errorf("previous_timeseries = %d days from %v", len(d.PreviousTimeseries), d.PreviousTimeseries[0])
	}
	if !contains(env.Highlights, "6 clicks between 2026-10-20 and 2026-11-18, up 50% on the previous 30 days.") {
		t.Errorf("highlights = %q", env.Highlights)
	}

	env = f.overview(u, "compare=none")
	d = data(t, env)
	if d.Previous != nil || env.PreviousWindow != nil || len(d.PreviousTimeseries) != 0 || d.PreviousTimeseries == nil {
		t.Errorf("compare=none: previous=%+v window=%+v series=%v", d.Previous, env.PreviousWindow, d.PreviousTimeseries)
	}

	env = f.overview(u, "range=all")
	d = data(t, env)
	if d.Totals.Clicks != 19 || d.Previous != nil || env.PreviousWindow != nil || env.Window.From != "2026-09-09" {
		t.Errorf("range=all: totals=%+v from=%s", d.Totals, env.Window.From)
	}
}

func TestOverview_FacetsIgnoreOwnDimension(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	at := daysAgo(1, 10)
	f.clicks(5, click{link: l, at: at, source: "mastodon", medium: "social"})
	f.clicks(3, click{link: l, at: at, source: "bluesky", medium: "social"})
	f.clicks(2, click{link: l, at: at, source: "email", medium: "newsletter"})
	f.clicks(1, click{link: l, at: at}) // no UTM at all

	d := data(t, f.overview(u, "source=mastodon"))
	if d.Totals.Clicks != 5 {
		t.Errorf("clicks = %d, want 5", d.Totals.Clicks)
	}
	// Source facet ignores the source filter: all sources visible.
	want := []FacetValue{{"mastodon", 5}, {"bluesky", 3}, {"email", 2}, {NoneValue, 1}}
	if !reflect.DeepEqual(d.Facets.Source, want) {
		t.Errorf("source facet = %+v, want %+v", d.Facets.Source, want)
	}
	// Medium facet applies the source filter: newsletter (only with email) is absent.
	if !reflect.DeepEqual(d.Facets.Medium, []FacetValue{{"social", 5}}) {
		t.Errorf("medium facet = %+v", d.Facets.Medium)
	}
	if d.Facets.Term == nil || d.Facets.Placement == nil || d.Facets.UTMCampaign == nil || d.Facets.Content == nil {
		t.Error("empty facets must be [] not null")
	}

	// ~none and OR within a dimension.
	d = data(t, f.overview(u, "source=~none,email"))
	if d.Totals.Clicks != 3 {
		t.Errorf("~none,email clicks = %d, want 3", d.Totals.Clicks)
	}
	// AND across dimensions.
	d = data(t, f.overview(u, "source=mastodon,email&medium=newsletter"))
	if d.Totals.Clicks != 2 {
		t.Errorf("AND clicks = %d, want 2", d.Totals.Clicks)
	}
}

func TestOverview_PlacementFilterAndFacet(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	poster, flyer, none := f.link(u, "p", " Poster A "), f.link(u, "f", "flyer"), f.link(u, "n", "")
	at := daysAgo(1, 10)
	f.clicks(4, click{link: poster, at: at})
	f.clicks(2, click{link: flyer, at: at})
	f.clicks(1, click{link: none, at: at})

	d := data(t, f.overview(u, "placement=POSTER%20a"))
	if d.Totals.Clicks != 4 {
		t.Errorf("placement filter clicks = %d", d.Totals.Clicks)
	}
	want := []FacetValue{{"poster a", 4}, {"flyer", 2}, {NoneValue, 1}}
	if !reflect.DeepEqual(d.Facets.Placement, want) {
		t.Errorf("placement facet = %+v, want %+v", d.Facets.Placement, want)
	}
}

func TestOverview_CampaignsIgnoreCampaignFilter(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	c1, c2 := f.campaign(u, "Guides", "guides"), f.campaign(u, "Posters", "posters")
	f.campaign(u, "Empty", "empty")
	at := daysAgo(1, 10)
	f.clicks(5, click{link: l, campaign: c1, at: at, source: "mastodon"})
	f.clicks(3, click{link: l, campaign: c2, at: at, source: "mastodon"})
	f.clicks(2, click{link: l, at: at, source: "mastodon"})
	f.clicks(9, click{link: l, campaign: c2, at: at, source: "other"})

	d := data(t, f.overview(u, "campaign=guides"))
	if d.Totals.Clicks != 5 {
		t.Errorf("totals with campaign filter = %d, want 5", d.Totals.Clicks)
	}
	slug := func(r CampaignRow) string {
		if r.Slug == nil {
			return "<nil>"
		}
		return *r.Slug
	}
	// Campaign filter ignored; sorted by clicks; zero-click campaign omitted.
	var got []string
	for _, c := range d.Campaigns {
		got = append(got, slug(c))
	}
	if !reflect.DeepEqual(got, []string{"posters", "guides", "<nil>"}) {
		t.Errorf("campaigns = %v", got)
	}
	if d.Campaigns[0].Clicks != 12 || d.Campaigns[0].Name != "Posters" || d.Campaigns[2].Name != "No campaign" || d.Campaigns[2].Clicks != 2 {
		t.Errorf("campaign rows = %+v", d.Campaigns)
	}

	// UTM filters DO apply to the campaigns list.
	d = data(t, f.overview(u, "campaign=guides&source=mastodon"))
	got = nil
	for _, c := range d.Campaigns {
		got = append(got, slug(c))
	}
	if !reflect.DeepEqual(got, []string{"guides", "posters", "<nil>"}) {
		t.Errorf("campaigns with source filter = %v", got)
	}

	// campaign=none restricts totals to unattributed clicks.
	d = data(t, f.overview(u, "campaign=none"))
	if d.Totals.Clicks != 2 {
		t.Errorf("campaign=none clicks = %d", d.Totals.Clicks)
	}
}

func TestOverview_VisitorDays(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	// Day 1: ip "x" twice, ip "y" once. Day 2: ip "x" again. A NULL hash never counts.
	f.clicks(2, click{link: l, at: daysAgo(3, 10), ip: "x"})
	f.clicks(1, click{link: l, at: daysAgo(3, 11), ip: "y"})
	f.clicks(1, click{link: l, at: daysAgo(2, 10), ip: "x"})
	f.clicks(5, click{link: l, at: daysAgo(2, 10)})
	f.clicks(2, click{link: l, at: daysAgo(40, 10), ip: "z"}) // previous window of 30d

	env := f.overview(u, "range=30d")
	d := data(t, env)
	if d.Totals.Clicks != 9 || d.Totals.VisitorDays != 3 {
		t.Errorf("totals = %+v, want 9 clicks / 3 visitor_days", d.Totals)
	}
	if d.Previous == nil || d.Previous.VisitorDays == nil || *d.Previous.VisitorDays != 1 {
		t.Errorf("previous = %+v", d.Previous)
	}

	// 90d + previous 90d reaches past the 90-day retention: null, and said so.
	env = f.overview(u, "range=90d")
	d = data(t, env)
	if d.Previous == nil || d.Previous.VisitorDays != nil {
		t.Fatalf("previous.visitor_days should be null: %+v", d.Previous)
	}
	found := false
	for _, h := range env.Highlights {
		if len(h) > 12 && h[:12] == "Visitor-days" {
			found = true
		}
	}
	if !found {
		t.Errorf("highlights do not mention visitor-days: %q", env.Highlights)
	}
}

func TestOverview_TimeZoneBucketing(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	// 03:00 UTC on the 17th is 19:00 PST on the 16th.
	f.clicks(2, click{link: l, at: time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)})

	has := func(env *Envelope, date string) int64 {
		for _, d := range data(t, env).Timeseries {
			if d.Date == date {
				return d.Clicks
			}
		}
		return -1
	}
	if got := has(f.overview(u, "range=7d"), "2026-11-17"); got != 2 {
		t.Errorf("UTC bucket 17th = %d", got)
	}
	env := f.overview(u, "range=7d&tz=America/Los_Angeles")
	if has(env, "2026-11-16") != 2 || has(env, "2026-11-17") != 0 {
		t.Errorf("LA buckets wrong: %+v", data(t, env).Timeseries)
	}
	if env.Window.TZ != "America/Los_Angeles" {
		t.Errorf("tz = %s", env.Window.TZ)
	}

	// DEFAULT_TIMEZONE applies when the request has no tz.
	f.svc.DefaultTZ = "America/Los_Angeles"
	if env := f.overview(u, "range=7d"); env.Window.TZ != "America/Los_Angeles" || has(env, "2026-11-16") != 2 {
		t.Errorf("default tz not applied: %+v", env.Window)
	}
}

func TestOverview_CustomWindowAndLimit(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	f.clicks(2, click{link: l, at: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})    // first instant of window
	f.clicks(3, click{link: l, at: time.Date(2026, 10, 10, 23, 59, 0, 0, time.UTC)}) // last minute of window
	f.clicks(7, click{link: l, at: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)})   // just outside
	for _, s := range []string{"a", "b", "c"} {
		f.clicks(1, click{link: l, at: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), source: s})
	}

	env := f.overview(u, "from=2026-10-01&to=2026-10-10&limit=2")
	d := data(t, env)
	if d.Totals.Clicks != 8 || env.Window.PartialLastDay || len(d.Timeseries) != 10 {
		t.Errorf("totals = %+v partial=%v days=%d", d.Totals, env.Window.PartialLastDay, len(d.Timeseries))
	}
	if len(d.Facets.Source) != 2 {
		t.Errorf("limit=2 gave %d source values", len(d.Facets.Source))
	}
	if env.PreviousWindow == nil || env.PreviousWindow.From != "2026-09-21" {
		t.Errorf("previous_window = %+v", env.PreviousWindow)
	}
}

func TestOverview_EmptyAccount(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	env := f.overview(u, "")
	d := data(t, env)
	if d.Totals != (Totals{}) || d.BestDay != nil || len(d.Campaigns) != 0 || d.Campaigns == nil {
		t.Errorf("empty data = %+v", d)
	}
	if len(env.Highlights) != 1 || env.Highlights[0] != "No clicks between 2026-10-20 and 2026-11-18." {
		t.Errorf("highlights = %q", env.Highlights)
	}
}

func TestOverview_HighlightsCapped(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	c := f.campaign(u, "Guides", "guides")
	f.clicks(5, click{link: l, campaign: c, at: daysAgo(2, 9), source: "mastodon", content: "post"})
	f.clicks(2, click{link: l, at: daysAgo(2, 9), bot: true})
	f.clicks(1, click{link: l, at: daysAgo(100, 9)})
	env := f.overview(u, "range=90d")
	if n := len(env.Highlights); n < 1 || n > 5 {
		t.Errorf("highlights count = %d: %q", n, env.Highlights)
	}
	if !contains(env.Highlights, "Top campaign: Guides (5).") || !contains(env.Highlights, "Top source: mastodon (5).") {
		t.Errorf("highlights = %q", env.Highlights)
	}
}

func TestService_Errors(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	if _, err := f.svc.Run(context.Background(), u, "nope", url.Values{}); !errors.Is(err, ErrReportNotFound) {
		t.Errorf("unknown report err = %v", err)
	}
	_, err := f.svc.Run(context.Background(), u, "overview", url.Values{"range": {"2w"}})
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Code != "invalid_range" {
		t.Errorf("bad range err = %v", err)
	}
	_, err = f.svc.Run(context.Background(), u, "overview", url.Values{"limit": {"1000"}})
	if !errors.As(err, &pe) || pe.Code != "invalid_limit" {
		t.Errorf("bad limit err = %v", err)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestService_ReportControlledWindows proves a report's Windows hook replaces
// the standard windows, that the envelope reports them, and that
// Params.RangeExplicit lets the hook defer to a caller-supplied range.
func TestService_ReportControlledWindows(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	l := f.link(u, "aaa", "")
	f.clicks(2, click{link: l, at: time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)})
	f.clicks(5, click{link: l, at: daysAgo(1, 10)})

	f.svc.Registry.Register(Report{
		Name: "stub", Description: "stub",
		Windows: func(ctx context.Context, req *Request) (ResolvedWindows, error) {
			if req.Params.RangeExplicit {
				return req.Windows, nil
			}
			p := req.Params
			p.Compare = CompareNone
			p.From, p.To = "2026-10-01", "2026-10-05"
			return ResolveWindows(p, req.Now, nil), nil
		},
		Run: func(ctx context.Context, req *Request) (Output, error) {
			sc := req.Params.ScopedQuery(req.UserID, req.Windows.Current, ScopeOpts{})
			var n int64
			if err := req.Q.QueryRow(ctx, `SELECT count(*)`+sc.SQL, sc.Args...).Scan(&n); err != nil {
				return Output{}, err
			}
			return Output{Data: n}, nil
		},
	})
	run := func(q string) *Envelope {
		v, _ := url.ParseQuery(q)
		env, err := f.svc.Run(context.Background(), u, "stub", v)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	env := run("")
	if env.Window.From != "2026-10-01" || env.Window.To != "2026-10-05" || env.Window.PartialLastDay || env.PreviousWindow != nil || env.Data.(int64) != 2 {
		t.Errorf("hook windows not used: %+v prev=%+v data=%v", env.Window, env.PreviousWindow, env.Data)
	}
	env = run("range=7d")
	if env.Window.From != "2026-11-12" || env.Data.(int64) != 5 || env.PreviousWindow == nil {
		t.Errorf("explicit range not honoured: %+v data=%v", env.Window, env.Data)
	}
}

func TestService_FutureFromRejected(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	_, err := f.svc.Run(context.Background(), u, "overview", url.Values{"from": {"2026-11-19"}})
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Code != "invalid_date" || pe.Field != "from" {
		t.Errorf("err = %v", err)
	}
	env := f.overview(u, "from=2026-11-10&to=2027-01-01")
	if env.Window.To != "2026-11-18" || !env.Window.PartialLastDay {
		t.Errorf("future to not clamped: %+v", env.Window)
	}
}

// TestOverview_FiltersWithComparison checks the filtered figures that the
// one-pass overview derives from flags rather than separate queries. The
// expected values were worked out by hand from the seeded rows, applying the
// rule that a facet ignores its own dimension's filter, the campaign list
// ignores the campaign filter, and everything else applies every filter.
func TestOverview_FiltersWithComparison(t *testing.T) {
	f := newFixture(t)
	u := f.user("eq@example.com")
	l1, l2, l3 := f.link(u, "l1", ""), f.link(u, "l2", ""), f.link(u, "l3", "")
	spring := f.campaign(u, "Spring", "spring")

	// Current window (7d: Nov 12 - Nov 18).
	f.clicks(4, click{link: l1, campaign: spring, at: daysAgo(1, 10), source: "x"})
	f.clicks(2, click{link: l1, campaign: spring, at: daysAgo(1, 10), source: "x", bot: true})
	f.clicks(3, click{link: l2, at: daysAgo(2, 10), source: "y"})
	f.clicks(1, click{link: l2, at: daysAgo(2, 11), source: "y", bot: true})
	f.clicks(1, click{link: l1, campaign: spring, at: daysAgo(3, 9), source: "y"})
	f.clicks(2, click{link: l3, at: daysAgo(3, 9), source: "z"})
	// Previous window (Nov 5 - Nov 11).
	f.clicks(3, click{link: l1, campaign: spring, at: daysAgo(8, 10), source: "x"})
	f.clicks(2, click{link: l2, at: daysAgo(9, 10), source: "y"})
	f.clicks(1, click{link: l2, at: daysAgo(9, 10), source: "y", bot: true})
	f.clicks(5, click{link: l3, at: daysAgo(10, 10), source: "z"})

	day := func(ds []DayCount, date string) int64 {
		for _, d := range ds {
			if d.Date == date {
				return d.Clicks
			}
		}
		t.Fatalf("no day %s in %v", date, ds)
		return 0
	}
	facet := func(fs []FacetValue) string {
		out := ""
		for _, v := range fs {
			out += fmt.Sprintf("%s:%d ", v.Value, v.Clicks)
		}
		return out
	}
	camps := func(cs []CampaignRow) string {
		out := ""
		for _, c := range cs {
			s := "none"
			if c.Slug != nil {
				s = *c.Slug
			}
			out += fmt.Sprintf("%s:%d ", s, c.Clicks)
		}
		return out
	}
	type want struct {
		clicks, bots, linksClicked, prev int64
		sources, campaigns               string
	}
	for _, c := range []struct {
		query string
		want  want
	}{
		// 4+3+1+2 humans; bots 2+1; l1, l2, l3 clicked; previous 3+2+5.
		{"range=7d", want{10, 3, 3, 10, "x:4 y:4 z:2 ", "spring:5 none:5 "}},
		// source=y: humans 3+1; the one source-y bot; l2 and l1; previous 2.
		// The source facet ignores source=y; campaigns apply it.
		{"range=7d&source=y", want{4, 1, 2, 2, "x:4 y:4 z:2 ", "none:3 spring:1 "}},
		// campaign=spring: humans 4+1; the two spring bots; l1 only; previous 3.
		// Campaigns ignore the campaign filter, so "none" (3+2) is listed;
		// the source facet applies it.
		{"range=7d&campaign=spring", want{5, 2, 1, 3, "x:4 y:1 ", "spring:5 none:5 "}},
		// Both: only the source-y spring click; no bots; previous (source x) 0.
		{"range=7d&campaign=spring&source=y", want{1, 0, 1, 0, "x:4 y:1 ", "none:3 spring:1 "}},
	} {
		d := data(t, f.overview(u, c.query))
		if d.Totals.Clicks != c.want.clicks || d.Totals.ExcludedBots != c.want.bots || d.Totals.LinksClicked != c.want.linksClicked {
			t.Errorf("%q totals = %+v; want clicks %d, bots %d, links_clicked %d", c.query, d.Totals, c.want.clicks, c.want.bots, c.want.linksClicked)
		}
		if d.Previous == nil || d.Previous.Clicks != c.want.prev {
			t.Errorf("%q previous = %+v; want %d", c.query, d.Previous, c.want.prev)
		}
		if got := facet(d.Facets.Source); got != c.want.sources {
			t.Errorf("%q source facet = %q; want %q", c.query, got, c.want.sources)
		}
		if got := camps(d.Campaigns); got != c.want.campaigns {
			t.Errorf("%q campaigns = %q; want %q", c.query, got, c.want.campaigns)
		}
	}

	// previous_timeseries and the current series under a filter.
	d := data(t, f.overview(u, "range=7d&source=y"))
	if got := day(d.PreviousTimeseries, "2026-11-09"); got != 2 {
		t.Errorf("source=y previous 11-09 = %d; want 2 (the bot is excluded)", got)
	}
	if got := day(d.PreviousTimeseries, "2026-11-08"); got != 0 {
		t.Errorf("source=y previous 11-08 = %d; want 0 (that day's clicks are source x)", got)
	}
	if got, other := day(d.Timeseries, "2026-11-16"), day(d.Timeseries, "2026-11-17"); got != 3 || other != 0 {
		t.Errorf("source=y current 11-16 = %d, 11-17 = %d; want 3, 0", got, other)
	}
	if d.BestDay == nil || d.BestDay.Date != "2026-11-16" || d.BestDay.Clicks != 3 {
		t.Errorf("source=y best day = %+v; want 2026-11-16 with 3", d.BestDay)
	}

	// A tie at the limit boundary: x and y both have 4, so limit=1 keeps the
	// one that sorts first, x; the campaign list is not limited.
	d = data(t, f.overview(u, "range=7d&limit=1"))
	if got := facet(d.Facets.Source); got != "x:4 " {
		t.Errorf("limit=1 source facet = %q; want %q", got, "x:4 ")
	}
	if got := camps(d.Campaigns); got != "spring:5 none:5 " {
		t.Errorf("limit=1 campaigns = %q; want both", got)
	}
}
