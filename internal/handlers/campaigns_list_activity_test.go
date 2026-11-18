package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// queryCounter is a pgx tracer that counts the statements a pool runs.
type queryCounter struct{ n atomic.Int64 }

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n.Add(1)
	return ctx
}
func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// activityNow is 12:00 on 2026-11-18 in America/Los_Angeles (UTC-8).
var activityNow = time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)

// activityServer serves GET /api/campaigns with a fixed clock. The campaigns
// store runs on its own traced pool so the test can count its queries.
func activityServer(t *testing.T, pool *pgxpool.Pool, defaultTZ string) (*httptest.Server, *queryCounter) {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	qc := &queryCounter{}
	cfg.ConnConfig.Tracer = qc
	tracedPool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("traced pool: %v", err)
	}
	t.Cleanup(tracedPool.Close)

	h := NewCampaignsHandler(campaigns.NewStore(tracedPool), links.NewStore(pool), nil, clicks.NewStatsStore(pool), nil, testBaseURL).WithDefaultTimezone(defaultTZ)
	h.now = func() time.Time { return activityNow }
	mux := http.NewServeMux()
	mux.Handle("GET /api/campaigns", middleware.RequireSession(auth.NewStore(pool))(http.HandlerFunc(h.List)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, qc
}

type activityItem struct {
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	LinkCount   int64   `json:"link_count"`
	TotalClicks int64   `json:"total_clicks"`
	Clicks30d   int64   `json:"clicks_30d"`
	Sparkline   []int64 `json:"sparkline"`
	Status      string  `json:"status"`
}

func getActivity(t *testing.T, srv *httptest.Server, path string) []activityItem {
	t.Helper()
	code, body := getRaw(t, srv, "alice-token", path)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, code, body)
	}
	var out struct {
		Campaigns []activityItem `json:"campaigns"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Campaigns
}

func slugsOf(items []activityItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Slug
	}
	return out
}

// seedCampaignRow inserts a campaign with explicit dates and created_at.
func seedCampaignRow(t *testing.T, pool *pgxpool.Pool, userID int64, name, slug string, starts, ends *time.Time, archived bool, created time.Time) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO campaigns (user_id, name, slug, starts_at, ends_at, archived, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7) RETURNING id`,
		userID, name, slug, starts, ends, archived, created).Scan(&id); err != nil {
		t.Fatalf("seed campaign %q: %v", slug, err)
	}
	return id
}

func day(y int, m time.Month, d int) *time.Time {
	v := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &v
}

// seedActivityClick inserts a click attributed by clicks.campaign_id.
func seedActivityClick(t *testing.T, pool *pgxpool.Pool, linkID, campaignID int64, at time.Time, bot bool) {
	t.Helper()
	execSQL(t, pool, `INSERT INTO clicks (link_id, campaign_id, clicked_at, is_bot) VALUES ($1, $2, $3, $4)`, linkID, campaignID, at, bot)
}

func TestCampaignsList_ClicksSparklineStatusAndZeroFill(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	bob := seedUser(t, pool, "bob@example.com")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	busy := seedCampaignRow(t, pool, alice, "Busy", "busy", nil, nil, false, base)
	quiet := seedCampaignRow(t, pool, alice, "Quiet", "quiet", nil, nil, false, base.Add(time.Hour))
	bobC := seedCampaignRow(t, pool, bob, "Bobs", "bobs", nil, nil, false, base)
	link := seedLinkWithCampaign(t, pool, alice, "act0001", "https://example.com/a", &busy)
	bobLink := seedLinkWithCampaign(t, pool, bob, "act0002", "https://example.com/b", &bobC)

	la, _ := time.LoadLocation("America/Los_Angeles")
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, la) }
	// Window in LA: Oct 20 .. Nov 18.
	seedActivityClick(t, pool, link, busy, at(2026, 11, 18, 11), false)   // today -> last
	seedActivityClick(t, pool, link, busy, at(2026, 11, 18, 0), false)    // today, local midnight
	seedActivityClick(t, pool, link, busy, at(2026, 10, 20, 0), false)    // first day, local midnight -> index 0
	seedActivityClick(t, pool, link, busy, at(2026, 10, 19, 23), false)   // day before the window
	seedActivityClick(t, pool, link, busy, at(2026, 11, 19, 0), false)    // tomorrow (future)
	seedActivityClick(t, pool, link, busy, at(2026, 11, 10, 12), true)    // bot
	seedActivityClick(t, pool, link, busy, at(2026, 11, 10, 12), false)   // mid window
	seedActivityClick(t, pool, bobLink, bobC, at(2026, 11, 18, 9), false) // another user's

	srv, _ := activityServer(t, pool, "America/Los_Angeles")
	items := getActivity(t, srv, "/api/campaigns")
	if got := slugsOf(items); !reflect.DeepEqual(got, []string{"quiet", "busy"}) {
		t.Fatalf("default order = %v, want created order [quiet busy]", got)
	}
	q, b := items[0], items[1]
	_ = quiet
	if len(q.Sparkline) != 30 || len(b.Sparkline) != 30 {
		t.Fatalf("sparkline lengths = %d, %d, want 30", len(q.Sparkline), len(b.Sparkline))
	}
	if q.Clicks30d != 0 || q.Status != "live" {
		t.Errorf("quiet = %d clicks, status %q", q.Clicks30d, q.Status)
	}
	for i, n := range q.Sparkline {
		if n != 0 {
			t.Errorf("quiet sparkline[%d] = %d, want zero-fill", i, n)
		}
	}
	want := make([]int64, 30)
	want[0], want[21], want[29] = 1, 1, 2
	if !reflect.DeepEqual(b.Sparkline, want) {
		t.Errorf("busy sparkline = %v, want %v", b.Sparkline, want)
	}
	if b.Clicks30d != 4 {
		t.Errorf("busy clicks_30d = %d, want 4", b.Clicks30d)
	}
	var sum int64
	for _, n := range b.Sparkline {
		sum += n
	}
	if sum != b.Clicks30d {
		t.Errorf("sparkline sums to %d, clicks_30d = %d", sum, b.Clicks30d)
	}
	// Existing fields unchanged: total_clicks stays all-time, bot-excluded
	// (6 non-bot clicks including the out-of-window and future ones).
	if b.TotalClicks != 6 || b.LinkCount != 1 {
		t.Errorf("busy total_clicks/link_count = %d/%d, want 6/1", b.TotalClicks, b.LinkCount)
	}

	// tz changes the bucketing: in UTC the local-midnight click on Nov 18 is
	// still Nov 18, but the Oct 19 23:00 LA click (Oct 20 06:00 UTC) now falls
	// on the first day, and the Nov 19 00:00 LA one (08:00 UTC) is Nov 19.
	utc := getActivity(t, srv, "/api/campaigns?tz=UTC")
	if utc[1].Sparkline[0] != 2 {
		t.Errorf("tz=UTC first bucket = %d, want 2", utc[1].Sparkline[0])
	}
}

func TestCampaignsList_StatusAtDateBoundaries(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Today in LA is 2026-11-18 (it is 2026-11-18 20:00 UTC).
	seedCampaignRow(t, pool, alice, "Starts today", "starts-today", day(2026, 11, 18), nil, false, base)
	seedCampaignRow(t, pool, alice, "Starts tomorrow", "starts-tomorrow", day(2026, 11, 19), nil, false, base)
	seedCampaignRow(t, pool, alice, "Ends today", "ends-today", nil, day(2026, 11, 18), false, base)
	seedCampaignRow(t, pool, alice, "Ended yesterday", "ended-yesterday", nil, day(2026, 11, 17), false, base)
	seedCampaignRow(t, pool, alice, "Archived", "archived", nil, day(2026, 11, 17), true, base)
	seedCampaignRow(t, pool, alice, "Undated", "undated", nil, nil, false, base)

	srv, _ := activityServer(t, pool, "America/Los_Angeles")
	got := map[string]string{}
	for _, it := range getActivity(t, srv, "/api/campaigns") {
		got[it.Slug] = it.Status
	}
	want := map[string]string{
		"starts-today": "live", "starts-tomorrow": "scheduled", "ends-today": "live",
		"ended-yesterday": "ended", "archived": "archived", "undated": "live",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statuses = %v, want %v", got, want)
	}

	// Just past local midnight the day has already rolled over in Tokyo
	// (2026-11-19 05:00 there), so "starts tomorrow" is live and "ends today" ended.
	tok := map[string]string{}
	for _, it := range getActivity(t, srv, "/api/campaigns?tz=Asia/Tokyo") {
		tok[it.Slug] = it.Status
	}
	if tok["starts-tomorrow"] != "live" || tok["ends-today"] != "ended" {
		t.Errorf("tz=Asia/Tokyo statuses = %v", tok)
	}
}

func TestCampaignsList_DefaultTimezoneAndInvalidParams(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedCampaignRow(t, pool, alice, "Starts tomorrow", "st", day(2026, 11, 19), nil, false, time.Now())

	// DEFAULT_TIMEZONE applies when tz is absent; tz overrides it.
	srv, _ := activityServer(t, pool, "Asia/Tokyo")
	if s := getActivity(t, srv, "/api/campaigns")[0].Status; s != "live" {
		t.Errorf("default tz Tokyo status = %q, want live", s)
	}
	if s := getActivity(t, srv, "/api/campaigns?tz=America/Los_Angeles")[0].Status; s != "scheduled" {
		t.Errorf("tz override status = %q, want scheduled", s)
	}
	for _, p := range []string{"/api/campaigns?tz=Nope/Zone", "/api/campaigns?sort=clicks"} {
		if code, _ := getRaw(t, srv, "alice-token", p); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", p, code)
		}
	}
}

func TestCampaignsList_SortOptions(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// created order (newest first): d, c, b, a.
	a := seedCampaignRow(t, pool, alice, "Alpha", "a", nil, nil, false, base)
	b := seedCampaignRow(t, pool, alice, "bravo", "b", nil, nil, false, base.Add(1*time.Hour))
	c := seedCampaignRow(t, pool, alice, "Charlie", "c", nil, nil, false, base.Add(2*time.Hour))
	d := seedCampaignRow(t, pool, alice, "Delta", "d", nil, nil, false, base.Add(3*time.Hour))
	link := seedLinkWithCampaign(t, pool, alice, "srt0001", "https://example.com/s", nil)
	recent := activityNow.Add(-time.Hour)
	// a: 3 clicks, b: 1, c: 3, d: 0. Ties (a, c) fall back to newest first.
	for i := 0; i < 3; i++ {
		seedActivityClick(t, pool, link, a, recent, false)
		seedActivityClick(t, pool, link, c, recent, false)
	}
	seedActivityClick(t, pool, link, b, recent, false)
	// Old, out-of-window clicks must not lift d.
	seedActivityClick(t, pool, link, d, activityNow.AddDate(0, 0, -60), false)

	srv, _ := activityServer(t, pool, "UTC")
	for path, want := range map[string][]string{
		"/api/campaigns":               {"d", "c", "b", "a"},
		"/api/campaigns?sort=created":  {"d", "c", "b", "a"},
		"/api/campaigns?sort=activity": {"c", "a", "b", "d"},
		"/api/campaigns?sort=name":     {"a", "b", "c", "d"},
	} {
		if got := slugsOf(getActivity(t, srv, path)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s order = %v, want %v", path, got, want)
		}
	}
}

// TestCampaignsList_QueryCountDoesNotGrow proves the list costs the same
// number of SQL statements for 1 campaign as for 12.
func TestCampaignsList_QueryCountDoesNotGrow(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	srv, qc := activityServer(t, pool, "UTC")
	link := seedLinkWithCampaign(t, pool, alice, "cnt0001", "https://example.com/q", nil)

	count := func() int64 {
		before := qc.n.Load()
		getActivity(t, srv, "/api/campaigns")
		return qc.n.Load() - before
	}
	add := func(from, to int) {
		for i := from; i < to; i++ {
			id := seedCampaignRow(t, pool, alice, fmt.Sprintf("C%d", i), fmt.Sprintf("c%d", i), nil, nil, false, time.Now())
			seedActivityClick(t, pool, link, id, activityNow.Add(-time.Hour), false)
		}
	}
	add(0, 1)
	one := count()
	add(1, 12)
	twelve := count()
	if one != twelve {
		t.Errorf("queries for 1 campaign = %d, for 12 = %d; must not grow", one, twelve)
	}
	if one != 2 {
		t.Errorf("queries per list request = %d, want 2 (campaigns+counts, grouped daily clicks)", one)
	}
}
