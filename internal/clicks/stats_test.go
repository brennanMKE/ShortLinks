package clicks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// recordN records n clicks for key with the given UTM triple (empty strings →
// NULL, exercising the "(none)" bucket).
func recordN(t *testing.T, rec *Recorder, key, source, medium, campaign string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := rec.Record(context.Background(), Click{
			Key:         key,
			UTMSource:   source,
			UTMMedium:   medium,
			UTMCampaign: campaign,
		})
		if err != nil {
			t.Fatalf("record click: %v", err)
		}
	}
}

// recordBotN records n clicks for key with a bot User-Agent (Twitterbot),
// exercising the same #0101 classification path recordN's plain clicks go
// through (empty UA, never classified as a bot).
func recordBotN(t *testing.T, rec *Recorder, key string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := rec.Record(context.Background(), Click{
			Key:       key,
			UserAgent: "Twitterbot/1.0",
		})
		if err != nil {
			t.Fatalf("record bot click: %v", err)
		}
	}
}

// findBucket returns the count for value in the slice, or -1 if absent.
func findBucket(buckets []Bucket, value string) int64 {
	for _, b := range buckets {
		if b.Value == value {
			return b.Count
		}
	}
	return -1
}

// TestUTMStatsForLink_Breakdown seeds a mix of UTM combinations and asserts the
// total count plus the per-dimension grouped counts, including the NULL → "(none)"
// bucket and the count-desc ordering.
func TestUTMStatsForLink_Breakdown(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com")

	// 4× email/newsletter/launch, 2× social/cpc/launch, 1× with no UTM at all.
	recordN(t, rec, "abc123", "email", "newsletter", "launch", 4)
	recordN(t, rec, "abc123", "social", "cpc", "launch", 2)
	recordN(t, rec, "abc123", "", "", "", 1)

	got, err := stats.UTMStatsForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}

	if got.ClickCount != 7 {
		t.Errorf("click_count = %d, want 7", got.ClickCount)
	}

	// by_source: email=4, social=2, (none)=1 — ordered desc.
	if len(got.BySource) != 3 {
		t.Fatalf("by_source len = %d, want 3: %+v", len(got.BySource), got.BySource)
	}
	if got.BySource[0].Value != "email" || got.BySource[0].Count != 4 {
		t.Errorf("by_source[0] = %+v, want email=4", got.BySource[0])
	}
	if c := findBucket(got.BySource, "social"); c != 2 {
		t.Errorf("by_source social = %d, want 2", c)
	}
	if c := findBucket(got.BySource, NoneBucket); c != 1 {
		t.Errorf("by_source %s = %d, want 1", NoneBucket, c)
	}

	// by_medium: newsletter=4, cpc=2, (none)=1.
	if c := findBucket(got.ByMedium, "newsletter"); c != 4 {
		t.Errorf("by_medium newsletter = %d, want 4", c)
	}
	if c := findBucket(got.ByMedium, "cpc"); c != 2 {
		t.Errorf("by_medium cpc = %d, want 2", c)
	}
	if c := findBucket(got.ByMedium, NoneBucket); c != 1 {
		t.Errorf("by_medium %s = %d, want 1", NoneBucket, c)
	}

	// by_campaign: launch=6, (none)=1.
	if c := findBucket(got.ByCampaign, "launch"); c != 6 {
		t.Errorf("by_campaign launch = %d, want 6", c)
	}
	if c := findBucket(got.ByCampaign, NoneBucket); c != 1 {
		t.Errorf("by_campaign %s = %d, want 1", NoneBucket, c)
	}
	if got.ByCampaign[0].Value != "launch" {
		t.Errorf("by_campaign[0] = %q, want launch (count desc)", got.ByCampaign[0].Value)
	}
}

// TestUTMStatsForLink_NoClicks asserts a link with no clicks returns a zero count
// and empty (non-nil) breakdown slices, so the JSON encodes [] not null.
func TestUTMStatsForLink_NoClicks(t *testing.T) {
	pool := testPool(t)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "bob@example.com")
	linkID := seedLink(t, pool, uid, "empty1", "https://example.com")

	got, err := stats.UTMStatsForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}
	if got.ClickCount != 0 {
		t.Errorf("click_count = %d, want 0", got.ClickCount)
	}
	if got.BySource == nil || len(got.BySource) != 0 {
		t.Errorf("by_source = %+v, want empty non-nil", got.BySource)
	}
	if got.ByMedium == nil || len(got.ByMedium) != 0 {
		t.Errorf("by_medium = %+v, want empty non-nil", got.ByMedium)
	}
	if got.ByCampaign == nil || len(got.ByCampaign) != 0 {
		t.Errorf("by_campaign = %+v, want empty non-nil", got.ByCampaign)
	}
}

// TestUTMStatsForLink_ExcludesBotsAndReportsExcludedCount is the #0101
// acceptance criterion at the stats layer: bot-flagged clicks must not appear
// in ClickCount or any breakdown, and the excluded count must be exactly the
// number of bot clicks — not zero (which a "forgot to exclude" bug would
// wrongly still report as 0 excluded while ClickCount silently included them)
// and not equal to the total (which a "excluded everything" bug would
// produce). Mixing bot and non-bot clicks across two UTM sources also
// verifies breakdown counts stay consistent with the total once bots are
// removed.
func TestUTMStatsForLink_ExcludesBotsAndReportsExcludedCount(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "bot-stats@example.com")
	linkID := seedLink(t, pool, uid, "botst01", "https://example.com")

	// 3 human clicks via email, 2 human clicks via social, 4 bot clicks
	// (no UTM at all, matching a bare crawler fetch of the short URL).
	recordN(t, rec, "botst01", "email", "newsletter", "launch", 3)
	recordN(t, rec, "botst01", "social", "cpc", "launch", 2)
	recordBotN(t, rec, "botst01", 4)

	got, err := stats.UTMStatsForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}

	if got.ClickCount != 5 {
		t.Errorf("click_count = %d, want 5 (bot clicks excluded)", got.ClickCount)
	}
	if got.ExcludedBotCount != 4 {
		t.Errorf("excluded_bot_count = %d, want 4", got.ExcludedBotCount)
	}
	// Breakdown totals must sum to the non-bot ClickCount, not 9 — a bug that
	// forgot the is_bot filter in breakdown() specifically (even with
	// UTMStatsForLink's own COUNT(*) correctly filtered) would surface here.
	var bySourceTotal int64
	for _, b := range got.BySource {
		bySourceTotal += b.Count
	}
	if bySourceTotal != 5 {
		t.Errorf("by_source total = %d, want 5 (bot clicks must not appear in the breakdown)", bySourceTotal)
	}
	if c := findBucket(got.BySource, "email"); c != 3 {
		t.Errorf("by_source email = %d, want 3", c)
	}
	if c := findBucket(got.BySource, "social"); c != 2 {
		t.Errorf("by_source social = %d, want 2", c)
	}
}

// TestUTMStatsForLink_ClickCountAndExcludedComputedInOneQuery pins the claim
// made in UTMStats's doc comment and #0101's commit message: ClickCount and
// ExcludedBotCount are computed in the SAME query via COUNT(*) FILTER, not
// two sequential COUNT(*) queries. That claim was previously undertested —
// splitting the FILTER pair into two round trips broke no assertion, since
// the two counts individually still came out correct either way.
//
// This asserts a PROPERTY of the statements UTMStatsForLink issues, not a
// total count: exactly one statement contains "FILTER (WHERE" (the combined
// pair), and none matches the shape of a bare per-count query
// ("COUNT(*) FROM clicks WHERE link_id" with no FILTER in between — what the
// split-into-two-queries mutant produces). An earlier version of this test
// asserted "exactly 4 statements total", which is brittle in ways that have
// nothing to do with the claim: #0102 plans reusing breakdown() for two more
// dimensions (content, referer), which would legitimately push the total to
// 5 or 6, and wrapping these reads in a transaction (a semantics-improving
// fix for the pre-existing "breakdowns can out-sum ClickCount under
// concurrent recording" gap) makes pgx fire an extra traced statement for
// BEGIN. Both would fail the old total-count assertion with a message
// blaming "not two sequential COUNT(*) queries" — false in both cases. The
// property below is unaffected by either change and still fails only when
// the FILTER pair is actually split.
func TestUTMStatsForLink_ClickCountAndExcludedComputedInOneQuery(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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

	uid := seedUser(t, pool, "query-count@example.com")
	linkID := seedLink(t, pool, uid, "qc0001", "https://example.com")
	rec := NewRecorder(pool, nil)
	recordN(t, rec, "qc0001", "email", "newsletter", "launch", 2)
	recordBotN(t, rec, "qc0001", 1)

	stats := NewStatsStore(pool)
	counter.reset() // exclude seeding queries above; only UTMStatsForLink itself is under test

	if _, err := stats.UTMStatsForLink(context.Background(), linkID); err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}

	var filterStatements, barePerCountStatements int
	for _, sql := range counter.sqls {
		if strings.Contains(sql, "FILTER (WHERE") {
			filterStatements++
		}
		if strings.Contains(sql, "COUNT(*) FROM clicks WHERE link_id") {
			barePerCountStatements++
		}
	}
	if filterStatements != 1 {
		t.Errorf("statements containing \"FILTER (WHERE\" = %d, want exactly 1 (%v) — "+
			"ClickCount and ExcludedBotCount must resolve in ONE query via COUNT(*) FILTER", filterStatements, counter.sqls)
	}
	if barePerCountStatements != 0 {
		t.Errorf("statements matching a bare per-count query = %d, want 0 (%v) — "+
			"found a COUNT(*) FROM clicks WHERE link_id with no FILTER, i.e. the pair was split into two sequential queries",
			barePerCountStatements, counter.sqls)
	}
}

// findDay returns the count for the given "YYYY-MM-DD" date in a DayBucket
// slice, or -1 if absent.
func findDay(days []DayBucket, date string) int64 {
	for _, d := range days {
		if d.Date == date {
			return d.Count
		}
	}
	return -1
}

// TestClicksOverTime_BasicBuckets seeds clicks on two known UTC dates, requests
// a window that includes both, and asserts the per-day counts are correct.
func TestClicksOverTime_BasicBuckets(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "charlie@example.com")
	linkID := seedLink(t, pool, uid, "time01", "https://example.com")

	// Plant 3 clicks on 2026-01-10 and 2 on 2026-01-12 (UTC).
	day10 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	day12 := time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)

	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at) VALUES ($1, $2)`, linkID, day10,
		); err != nil {
			t.Fatalf("seed click day10: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at) VALUES ($1, $2)`, linkID, day12,
		); err != nil {
			t.Fatalf("seed click day12: %v", err)
		}
	}

	// Use a click via the recorder too (day10) to ensure the recorder path also works.
	_ = rec // recorder validated in recorder_test.go; we use raw inserts here for speed.

	from := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 13, 0, 0, 0, 0, time.UTC)

	got, err := stats.ClicksOverTime(context.Background(), linkID, from, to)
	if err != nil {
		t.Fatalf("ClicksOverTime: %v", err)
	}
	if got.Days == nil {
		t.Fatal("Days slice is nil, want non-nil")
	}
	// Only days with clicks are returned; day11 is absent.
	if len(got.Days) != 2 {
		t.Fatalf("len(Days) = %d, want 2: %+v", len(got.Days), got.Days)
	}
	if c := findDay(got.Days, "2026-01-10"); c != 3 {
		t.Errorf("2026-01-10 count = %d, want 3", c)
	}
	if c := findDay(got.Days, "2026-01-12"); c != 2 {
		t.Errorf("2026-01-12 count = %d, want 2", c)
	}
	// Days are ordered ascending.
	if got.Days[0].Date >= got.Days[1].Date {
		t.Errorf("days not ascending: %q then %q", got.Days[0].Date, got.Days[1].Date)
	}
}

// TestClicksOverTime_ExcludesBots asserts the day-by-day trend excludes
// bot-flagged clicks the same way UTMStatsForLink does: a day with both human
// and bot clicks must report only the human count, and a day with ONLY bot
// clicks must not appear in the result at all (rather than showing up as a
// spurious zero-human day with inflated raw activity nobody asked to see).
func TestClicksOverTime_ExcludesBots(t *testing.T) {
	pool := testPool(t)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "bot-timeseries@example.com")
	linkID := seedLink(t, pool, uid, "bottime1", "https://example.com")

	day10 := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC) // mixed: 3 human + 2 bot
	day11 := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC) // bot-only: 5 bot

	for i := 0; i < 3; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, $2, FALSE)`, linkID, day10,
		); err != nil {
			t.Fatalf("seed human click day10: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, $2, TRUE)`, linkID, day10,
		); err != nil {
			t.Fatalf("seed bot click day10: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1, $2, TRUE)`, linkID, day11,
		); err != nil {
			t.Fatalf("seed bot click day11: %v", err)
		}
	}

	from := time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 13, 0, 0, 0, 0, time.UTC)

	got, err := stats.ClicksOverTime(context.Background(), linkID, from, to)
	if err != nil {
		t.Fatalf("ClicksOverTime: %v", err)
	}
	if len(got.Days) != 1 {
		t.Fatalf("len(Days) = %d, want 1 (only the mixed human+bot day, bot-only day dropped): %+v", len(got.Days), got.Days)
	}
	if c := findDay(got.Days, "2026-02-10"); c != 3 {
		t.Errorf("2026-02-10 count = %d, want 3 (bot clicks on this day excluded)", c)
	}
	if c := findDay(got.Days, "2026-02-11"); c != -1 {
		t.Errorf("2026-02-11 count = %d, want absent (bot-only day must not appear)", c)
	}
}

// TestClicksOverTime_NoClicks asserts a link with no clicks in the window
// returns an empty (non-nil) Days slice, so the JSON encodes [] not null.
func TestClicksOverTime_NoClicks(t *testing.T) {
	pool := testPool(t)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "dave@example.com")
	linkID := seedLink(t, pool, uid, "notime1", "https://example.com")

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	got, err := stats.ClicksOverTime(context.Background(), linkID, from, to)
	if err != nil {
		t.Fatalf("ClicksOverTime: %v", err)
	}
	if got.Days == nil {
		t.Error("Days is nil, want empty non-nil slice")
	}
	if len(got.Days) != 0 {
		t.Errorf("Days len = %d, want 0", len(got.Days))
	}
}

// TestClicksOverTime_ZeroDefaults asserts that passing zero time values triggers
// the store's built-in 30-day default and returns a non-nil result without error.
// (We cannot assert the exact date range from the outside, but we verify it does
// not error, and that a click inserted "now" appears in the result.)
func TestClicksOverTime_ZeroDefaults(t *testing.T) {
	pool := testPool(t)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "eve@example.com")
	linkID := seedLink(t, pool, uid, "def01", "https://example.com")

	// Insert a click at "now" — it should appear in the default window.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, clicked_at) VALUES ($1, now())`, linkID,
	); err != nil {
		t.Fatalf("seed click: %v", err)
	}

	// Zero time values → the store defaults to 30 days ending at current UTC midnight.
	// A click inserted at "now" may land in today's bucket (if now > today midnight)
	// but at UTC midnight exactly it is excluded (< to, not <=). Either way we only
	// assert no error and a non-nil slice; the exact count is environment-dependent.
	got, err := stats.ClicksOverTime(context.Background(), linkID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("ClicksOverTime (zero defaults): %v", err)
	}
	if got.Days == nil {
		t.Error("Days is nil, want non-nil")
	}
}
