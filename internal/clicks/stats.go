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
type UTMStats struct {
	ClickCount int64    `json:"click_count"`
	BySource   []Bucket `json:"by_source"`
	ByMedium   []Bucket `json:"by_medium"`
	ByCampaign []Bucket `json:"by_campaign"`
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

	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM clicks WHERE link_id = $1`, linkID,
	).Scan(&stats.ClickCount); err != nil {
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

	rows, err := s.pool.Query(ctx,
		`SELECT to_char(date_trunc('day', clicked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		        COUNT(*) AS count
		   FROM clicks
		  WHERE link_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3
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
	query := fmt.Sprintf(
		`SELECT COALESCE(NULLIF(%s, ''), $2) AS value, COUNT(*) AS count
		   FROM clicks
		  WHERE link_id = $1
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
