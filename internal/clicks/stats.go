package clicks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultTimeseriesDays is the default look-back window used by ClicksOverTime
// when the caller does not supply an explicit range. 30 days covers a typical
// campaign window and keeps the result set small.
const defaultTimeseriesDays = 30

// NoneBucket is the label used for clicks whose UTM dimension is NULL or empty.
// The aggregation collapses both NULL and the empty string into this single
// bucket so "no value" is reported consistently regardless of how the click was
// recorded.
const NoneBucket = "(none)"

// breakdownLimit caps how many distinct values are returned per UTM dimension
// (top-N by count), per #0030's acceptance criteria.
const breakdownLimit = 20

// Bucket is one row of a UTM breakdown: a distinct dimension value and how many
// clicks carried it. Value is NoneBucket for clicks with no value in that
// dimension.
type Bucket struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// UTMStats is the per-link UTM analytics surface: the total click count plus a
// breakdown of clicks by utm_source, utm_medium, and utm_campaign. Each
// breakdown is ordered by count descending and limited to the top breakdownLimit
// values. The slices are always non-nil (empty for a link with no clicks) so the
// JSON encodes as [] rather than null.
//
// ClickCount and every breakdown exclude clicks with is_bot = TRUE (#0101):
// automated fetches (link-preview bots, crawlers, curl/wget/scripts) are real
// rows in the clicks table — recording never drops them — but they are not
// human interest and would otherwise inflate channels like social, which get
// far more automated preview-fetching than, say, a printed flier.
// ExcludedBotCount is that same exclusion made visible rather than silent: it
// is the count of bot-flagged clicks left out of ClickCount and the
// breakdowns below, computed here in the stats layer in the same query as
// ClickCount so callers never need a second query to reconstruct it.
type UTMStats struct {
	ClickCount       int64    `json:"click_count"`
	ExcludedBotCount int64    `json:"excluded_bot_count"`
	BySource         []Bucket `json:"by_source"`
	ByMedium         []Bucket `json:"by_medium"`
	ByCampaign       []Bucket `json:"by_campaign"`
}

// DayBucket is one row of the clicks-over-time series: a calendar date (UTC,
// truncated to day) and how many clicks occurred on that day. The date is
// encoded as an RFC 3339 / ISO 8601 date-only string ("2026-06-01") so the
// frontend can parse it cheaply without timezone gymnastics.
type DayBucket struct {
	Date  string `json:"date"`  // "YYYY-MM-DD"
	Count int64  `json:"count"`
}

// TimeseriesResult is the response shape for the clicks-over-time query.
// Days is always non-nil so the JSON encodes as [] rather than null. The slice
// covers only days with at least one click within the requested window; days
// with zero clicks are omitted (the frontend fills gaps for a smooth chart).
type TimeseriesResult struct {
	Days []DayBucket `json:"days"`
}

// StatsStore reads aggregate click analytics from the clicks table. It performs
// no writes and is safe for concurrent use.
type StatsStore struct {
	pool *pgxpool.Pool
}

// NewStatsStore constructs a StatsStore over the shared connection pool.
func NewStatsStore(pool *pgxpool.Pool) *StatsStore {
	return &StatsStore{pool: pool}
}

// Note for #0102 (campaign-scoped stats): idx_clicks_campaign_time
// (migration 000012) is `(campaign_id, clicked_at) WHERE campaign_id IS NOT
// NULL`. #0101 deliberately did NOT extend that predicate to
// `AND is_bot = FALSE`, and did not add a migration 000013, even though
// every campaign query this store's methods will need filters is_bot =
// FALSE.
//
// The invariant this rests on, reproduced across every distribution tested
// (30 campaigns x 5,000 clicks/campaign at 10% bot; 150k rows at 10% bot;
// 500k rows) via EXPLAIN (ANALYZE, BUFFERS) with enable_seqscan off and
// unrelated indexes removed to isolate the comparison: extending the
// predicate to `AND is_bot = FALSE` makes idx_clicks_campaign_time
// UNUSABLE for any query that doesn't also filter is_bot = FALSE — its
// predicate no longer implies the query's WHERE clause. Concretely, this
// issue's own ExcludedBotCount query (is_bot = TRUE) and any "total
// including bots" query fall back to the unfiltered idx_clicks_campaign_id
// (or worse), every time, in every distribution measured. That fallback,
// not any specific number, is the reason the predicate is left alone: a
// partial predicate that makes an index unusable for one of the two queries
// a single stats call needs (the excluded count sits right next to the
// filtered total in this file's own UTMStatsForLink) is a strictly worse
// index than accepting a residual `Filter: (NOT is_bot)` on the unchanged
// one — regardless of by how much.
//
// The MAGNITUDE of that fallback's cost is real but does not generalize —
// don't requote it as a fixed ratio. It ranged from roughly 1.04x cost /
// 2.3x buffers to 22x cost / 83x buffers / 12x heap blocks across the
// distributions above, driven by how many heap pages a campaign's rows
// happen to span relative to the query's time window, not by anything
// about the index design itself. The residual filter's own cost on the
// UNCHANGED index was consistently small but not reliably zero or
// one-directional either — in at least one measured distribution the
// "extended predicate" case came out slightly more expensive even for the
// is_bot = FALSE query it was meant to help. Re-measure before citing a
// number; the fallback itself is the durable finding here, not any figure
// attached to it. If #0102 sees a Filter node in its own EXPLAIN output for
// a campaign-scoped query, that is this decision working as intended, not a
// defect to fix by touching the index.

// UTMStatsForLink returns the UTM breakdown for the given link id: total clicks
// plus per-dimension counts grouped by utm_source, utm_medium, and utm_campaign.
// NULL and empty values fold into the NoneBucket label. Each dimension is ordered
// by count descending (value ascending as a stable tiebreaker) and limited to the
// top breakdownLimit entries. A link with no clicks returns a zero count and
// empty (non-nil) breakdown slices.
//
// Ownership is NOT enforced here — the caller (the link-detail handler) resolves
// the link by key scoped to the authenticated user first, then passes the
// resolved link id, so a non-owner never reaches this query.
func (s *StatsStore) UTMStatsForLink(ctx context.Context, linkID int64) (UTMStats, error) {
	stats := UTMStats{
		BySource:   []Bucket{},
		ByMedium:   []Bucket{},
		ByCampaign: []Bucket{},
	}

	// A single query computes both the human (non-bot) total and the excluded
	// bot count via FILTER, rather than two separate COUNT(*) queries or —
	// worse — making the caller derive the excluded count itself from
	// unrelated totals.
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE NOT is_bot),
		        COUNT(*) FILTER (WHERE is_bot)
		   FROM clicks WHERE link_id = $1`, linkID,
	).Scan(&stats.ClickCount, &stats.ExcludedBotCount); err != nil {
		return UTMStats{}, fmt.Errorf("clicks: counting clicks: %w", err)
	}

	var err error
	if stats.BySource, err = s.breakdown(ctx, linkID, "utm_source"); err != nil {
		return UTMStats{}, err
	}
	if stats.ByMedium, err = s.breakdown(ctx, linkID, "utm_medium"); err != nil {
		return UTMStats{}, err
	}
	if stats.ByCampaign, err = s.breakdown(ctx, linkID, "utm_campaign"); err != nil {
		return UTMStats{}, err
	}
	return stats, nil
}

// ClicksOverTime returns the per-day click counts for the given link over the
// [from, to) UTC date range, bucketed by calendar day (UTC). Callers that pass
// a zero from receive the last defaultTimeseriesDays days ending at midnight
// of the current UTC day; a zero to defaults to midnight of the current UTC
// day. Days with zero clicks are omitted — the frontend fills gaps. The result
// slice is always non-nil so the JSON encodes as [] rather than null.
//
// Ownership is NOT enforced here — the caller (the link-detail handler) has
// already resolved the link by key scoped to the authenticated user.
func (s *StatsStore) ClicksOverTime(ctx context.Context, linkID int64, from, to time.Time) (TimeseriesResult, error) {
	now := time.Now().UTC()
	// Snap to midnight UTC.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if to.IsZero() {
		to = today
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -defaultTimeseriesDays)
	}

	// is_bot = FALSE (#0101): the day-by-day trend is a "link stat" same as
	// UTMStatsForLink's totals, so it excludes bot-flagged clicks for the same
	// reason — automated preview fetches would otherwise show up as human
	// traffic spikes with no corresponding interest behind them.
	rows, err := s.pool.Query(ctx,
		`SELECT to_char(date_trunc('day', clicked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		        COUNT(*) AS count
		   FROM clicks
		  WHERE link_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3
		    AND is_bot = FALSE
		  GROUP BY day
		  ORDER BY day ASC`,
		linkID, from, to,
	)
	if err != nil {
		return TimeseriesResult{}, fmt.Errorf("clicks: querying timeseries: %w", err)
	}
	defer rows.Close()

	out := []DayBucket{}
	for rows.Next() {
		var b DayBucket
		if err := rows.Scan(&b.Date, &b.Count); err != nil {
			return TimeseriesResult{}, fmt.Errorf("clicks: scanning timeseries row: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return TimeseriesResult{}, fmt.Errorf("clicks: iterating timeseries rows: %w", err)
	}
	return TimeseriesResult{Days: out}, nil
}

// allowedDimensions is the fixed set of columns breakdown may group by. The
// column name is interpolated into SQL, so it must be validated against this set
// to keep the query injection-safe even though all callers pass constants.
var allowedDimensions = map[string]bool{
	"utm_source":   true,
	"utm_medium":   true,
	"utm_campaign": true,
}

// breakdown groups the link's clicks by one UTM dimension, folding NULL/empty
// into NoneBucket, ordered by count desc then value asc, limited to the top N.
// The dimension column name is validated against allowedDimensions before being
// interpolated, so this is not an injection vector.
func (s *StatsStore) breakdown(ctx context.Context, linkID int64, dimension string) ([]Bucket, error) {
	if !allowedDimensions[dimension] {
		return nil, fmt.Errorf("clicks: unsupported UTM dimension %q", dimension)
	}

	// COALESCE NULL → '', then NULLIF '' → NULL, then COALESCE → NoneBucket folds
	// both NULL and empty-string values into the single "(none)" bucket.
	// is_bot = FALSE (#0101) excludes automated-fetch clicks from every
	// dimension's breakdown, matching UTMStatsForLink's ClickCount so the
	// per-dimension counts always sum consistently with the total.
	query := fmt.Sprintf(
		`SELECT COALESCE(NULLIF(%s, ''), $2) AS value, COUNT(*) AS count
		   FROM clicks
		  WHERE link_id = $1
		    AND is_bot = FALSE
		  GROUP BY value
		  ORDER BY count DESC, value ASC
		  LIMIT %d`, dimension, breakdownLimit)

	rows, err := s.pool.Query(ctx, query, linkID, NoneBucket)
	if err != nil {
		return nil, fmt.Errorf("clicks: querying %s breakdown: %w", dimension, err)
	}
	defer rows.Close()

	out := []Bucket{}
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Value, &b.Count); err != nil {
			return nil, fmt.Errorf("clicks: scanning %s bucket: %w", dimension, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clicks: iterating %s rows: %w", dimension, err)
	}
	return out, nil
}
