package reports

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// budget is the per-report response time the analytics API promises (#0150)
// for a user with 1,000 links and 100,000 clicks in the window.
const budget = 300 * time.Millisecond

// BenchmarkReports runs every report against a seeded database: 1 user, 1,000
// links (20 campaigns, 8 sources, 3 mediums, 120 distinct destinations),
// 100,000 clicks spread over the last 30 days (the default window) and 50,000
// in the 30 days before it (the comparison window). It is skipped unless
// SHORTLINKS_BENCH=1 and TEST_DATABASE_URL are set, because seeding takes a few
// seconds and works in a throwaway schema of the test database:
//
//	SHORTLINKS_BENCH=1 TEST_DATABASE_URL=... go test -run XXX -bench Reports ./internal/reports
//
// A report whose mean time exceeds the budget is logged as OVER BUDGET, and
// fails the benchmark when SHORTLINKS_BENCH_STRICT=1.
func BenchmarkReports(b *testing.B) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if os.Getenv("SHORTLINKS_BENCH") != "1" || dsn == "" {
		b.Skip("set SHORTLINKS_BENCH=1 and TEST_DATABASE_URL to run the report benchmark")
	}
	ctx := context.Background()
	// The benchmark works in its own schema, which holds copies of the four
	// tables, so nothing it writes (rows, sequence values, planner statistics)
	// can reach the tables other packages' tests use. Dropping the schema at
	// the end removes all of it.
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		b.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = benchSchema + ",public"
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatal(err)
	}
	defer admin.Close()
	defer dropBench(ctx, admin)
	createBench(b, admin)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()
	uid := seedBench(b, pool)
	svc := NewService(pool, "America/Los_Angeles")
	svc.BaseURL = "https://go.example.test"

	cases := []struct {
		name, report string
		q            url.Values
	}{
		{"overview", "overview", nil},
		{"top-content", "top-content", nil},
		{"top-content/sort=change", "top-content", url.Values{"sort": {"change"}}},
		{"top-links", "top-links", nil},
		{"breakdown/source", "breakdown", url.Values{"dimension": {"source"}}},
		{"breakdown/referer", "breakdown", url.Values{"dimension": {"referer"}}},
		{"breakdown/link", "breakdown", url.Values{"dimension": {"link"}}},
		{"breakdown/content_group", "breakdown", url.Values{"dimension": {"content_group"}}},
		{"timeseries", "timeseries", nil},
		{"timeseries/split_by=source", "timeseries", url.Values{"split_by": {"source"}, "granularity": {"week"}}},
		{"movers", "movers", nil},
		{"movers/unit=link", "movers", url.Values{"unit": {"link"}}},
		{"idle-links", "idle-links", nil},
		{"campaign-comparison", "campaign-comparison", nil},
		{"campaign-performance", "campaign-performance", url.Values{"slug": {"camp-3"}, "range": {"30d"}}},
		{"link-performance", "link-performance", url.Values{"key": {"bench0042"}}},
		{"overview/range=all", "overview", url.Values{"range": {"all"}}},
		{"overview/source+medium filter", "overview", url.Values{"source": {"s1", "s2"}, "medium": {"m1"}}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			// Warm the plan and the cache once, outside the timing.
			if _, err := svc.Run(ctx, uid, c.report, c.q); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := svc.Run(ctx, uid, c.report, c.q); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			per := b.Elapsed() / time.Duration(b.N)
			b.ReportMetric(float64(per.Microseconds())/1000, "ms/report")
			if per > budget {
				msg := c.name + " took " + per.Round(time.Millisecond).String() + " per run; the budget is " + budget.String()
				if os.Getenv("SHORTLINKS_BENCH_STRICT") == "1" {
					b.Error(msg)
				} else {
					b.Log("OVER BUDGET: " + msg)
				}
			}
		})
	}
}

const benchSchema = "bench_0150"

func dropBench(ctx context.Context, pool *pgxpool.Pool) {
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+benchSchema+` CASCADE`)
}

// createBench makes the schema with empty copies of the tables the reports
// read, with their indexes (so a migration's index is measured too) but no
// foreign keys. Ids are supplied explicitly by the seed.
func createBench(b testing.TB, pool *pgxpool.Pool) {
	b.Helper()
	ctx := context.Background()
	dropBench(ctx, pool)
	stmts := []string{`CREATE SCHEMA ` + benchSchema}
	for _, t := range []string{"users", "campaigns", "links", "clicks"} {
		stmts = append(stmts, `CREATE TABLE `+benchSchema+`.`+t+` (LIKE public.`+t+` INCLUDING DEFAULTS INCLUDING INDEXES)`)
	}
	for _, q := range stmts {
		if _, err := pool.Exec(ctx, q); err != nil {
			b.Fatalf("create bench schema: %v\n%s", err, q)
		}
	}
}

// seedBench loads the benchmark data with set-based SQL and returns the user.
func seedBench(b testing.TB, pool *pgxpool.Pool) int64 {
	b.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			b.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	const uid int64 = 1
	exec(`INSERT INTO users (id, email, is_admin, active, created_at) VALUES ($1, 'bench@example.invalid', FALSE, TRUE, now())`, uid)
	exec(`INSERT INTO campaigns (id, user_id, name, slug) SELECT g, $1, 'Camp ' || g, 'camp-' || g FROM generate_series(1, 20) g`, uid)
	// Links: 1,000, every 5th without a campaign, created over the last 90
	// days, 120 distinct destinations so content grouping has work to do.
	exec(`INSERT INTO links (id, user_id, key, destination_url, title, active, denied_reason, created_at, campaign_id, utm_source, utm_medium, utm_content, placement)
		SELECT g, $1, 'bench' || lpad(g::text, 4, '0'), 'https://example.com/page/' || (g % 120), 'Link ' || g, TRUE, 0,
		       now() - ((g % 90) || ' days')::interval,
		       CASE WHEN g % 5 = 0 THEN NULL ELSE (1 + g % 20) END,
		       's' || (g % 8), 'm' || (g % 3), 'c' || (g % 40), CASE WHEN g % 7 = 0 THEN 'poster' END
		FROM generate_series(1, 1000) g`, uid)
	// Clicks: 100,000 in the last 30 days and 50,000 in the 30 before, spread
	// over links, sources, referers and times; 5% bots; 20,000 distinct
	// visitors. A hash of the series value stands in for randomness so the
	// data is the same on every run.
	exec(`INSERT INTO clicks (id, link_id, clicked_at, user_agent, referer, utm_source, utm_medium, utm_campaign, utm_content, campaign_id, is_bot, ip_hash)
		SELECT g, l.id, now() - (((g * 7919 % (30 * 86400)) + CASE WHEN g % 3 = 2 THEN 30 * 86400 ELSE 0 END) || ' seconds')::interval,
		       'bench', CASE g % 6 WHEN 0 THEN NULL WHEN 1 THEN 'https://t.co/' || (g % 3000) WHEN 2 THEN 'https://www.facebook.com/p/' || (g % 3000) WHEN 3 THEN 'https://news.ycombinator.com/item?id=' || (g % 3000) WHEN 4 THEN 'https://www.google.com/search?q=' || (g % 3000) ELSE 'https://example.org/a/' || (g % 3000) END,
		       's' || (g % 8), 'm' || (g % 3), 'u' || (g % 11), 'c' || (g % 40),
		       l.campaign_id, g % 20 = 0, decode(md5((g % 20000)::text), 'hex')
		FROM generate_series(1, 150000) g
		JOIN links l ON l.id = 1 + (g * 31 % 1000)
		WHERE g > 0`)
	exec(`ANALYZE clicks`)
	exec(`ANALYZE links`)
	exec(`ANALYZE campaigns`)
	var cur int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM clicks WHERE clicked_at > now() - interval '30 days'`).Scan(&cur)
	b.Logf("seeded 1 user, 1000 links, %d clicks in the last 30 days, 150000 total", cur)
	return uid
}
