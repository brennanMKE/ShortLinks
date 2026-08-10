package clicks

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
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
	Date  string `json:"date"` // "YYYY-MM-DD"
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
// `AND is_bot = FALSE`, and added no follow-up migration for it, even though
// every campaign query this store's methods will need filters is_bot =
// FALSE. (A `migrations/000013_*` does now exist — #0111's session/passkey
// NOT NULL repair. It is unrelated to this index; nothing has ever extended
// this predicate.)
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

// rowQuerier is the subset of pgx that every campaign-scoped read function in
// this file takes instead of touching s.pool directly. *pgxpool.Pool and
// pgx.Tx both satisfy it, so the SAME query function can run standalone
// against the pool (CampaignClicksOverTime, CampaignClicksByLink,
// CampaignSeriesByLink used alone) or inside a shared transaction
// (CampaignStats, CampaignSummary, CampaignRollup) without two copies of the
// SQL existing.
type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CampaignStats is the campaign-scoped analog of UTMStats: total clicks (bot
// excluded, #0101), the excluded bot count, and per-dimension breakdowns for
// source, medium, content, and referer — the "which channel or placement
// drove clicks" question from docs/campaigns-plan.md. Grouped on
// clicks.campaign_id (#0100) throughout, never a join through links, so a
// click recorded while its link belonged to this campaign stays attributed
// here even after the link is later reassigned or unassigned — the whole
// point of #0100's denormalization. The slices are always non-nil so the
// JSON encodes as [] rather than null.
//
// WindowFrom/WindowTo (#0103 fix 4) are the resolved [from, to) window
// campaignWindow actually computed and every query above filtered on —
// "YYYY-MM-DD", UTC, matching DayBucket.Date's convention. Exposing the
// window the server used (rather than making the frontend re-derive it from
// the campaign's own starts_at/ends_at) is authoritative specifically
// because campaignWindow clamps `to` at today for an in-flight dated
// campaign: a client-side recomputation of the nominal span would drift
// from that clamp and overstate the window (understating any "clicks per
// day" figure derived from it by the same factor — the exact defect this
// field exists to close). Set once, in campaignStatsQuery, from the SAME
// resolved from/to every other field on this struct was computed from, so
// it can never disagree with them.
type CampaignStats struct {
	ClickCount       int64    `json:"click_count"`
	ExcludedBotCount int64    `json:"excluded_bot_count"`
	BySource         []Bucket `json:"by_source"`
	ByMedium         []Bucket `json:"by_medium"`
	ByContent        []Bucket `json:"by_content"`
	ByReferer        []Bucket `json:"by_referer"`
	WindowFrom       string   `json:"window_from"` // "YYYY-MM-DD"
	WindowTo         string   `json:"window_to"`   // "YYYY-MM-DD"
}

// CampaignSummary pairs CampaignStats with the clicks-over-time series for
// the SAME resolved window, both read inside ONE transaction — see
// CampaignRollup's doc comment for why that matters. Backs GET
// /api/campaigns/{slug}, which shows link_count/total_clicks and a chart
// derived from the same underlying clicks.
type CampaignSummary struct {
	Stats      CampaignStats    `json:"stats"`
	Timeseries TimeseriesResult `json:"timeseries"`
}

// CampaignRollup is CampaignSummary plus the per-link breakdown and the
// capped-plus-"Other" per-link series, ALL read inside the same transaction —
// the full payload GET /api/campaigns/{slug}/stats serves in one response.
// See the method doc comment below for why a shared transaction is required
// here specifically (not merely a nice-to-have).
type CampaignRollup struct {
	Stats        CampaignStats    `json:"stats"`
	Timeseries   TimeseriesResult `json:"timeseries"`
	ByLink       []LinkBucket     `json:"by_link"`
	SeriesByLink []LinkSeries     `json:"series_by_link"`
}

// beginCampaignReadTx opens the REPEATABLE READ, read-only transaction every
// multi-query campaign read (CampaignStats, CampaignSummary, CampaignRollup)
// shares, so every read inside it — across however many queries — sees the
// same snapshot. Closed with Rollback by every caller since none of them
// write.
func (s *StatsStore) beginCampaignReadTx(ctx context.Context) (pgx.Tx, error) {
	return s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
}

// CampaignStats returns campaignID's total clicks (bot excluded), the
// excluded bot count, and the four channel breakdowns over the half-open
// [from, to) window. Passing a zero from AND a zero to applies the default
// window campaignWindow documents: the campaign's own starts_at/ends_at when
// both are set (clamped so `to` never exceeds today), otherwise falling back
// to the existing 30-day-ending-today default.
//
// TRANSACTION (#0101's follow-up): every read here — the window lookup, the
// totals query, and all four dimension breakdowns — runs inside ONE
// REPEATABLE READ, read-only transaction (beginCampaignReadTx), so they all
// see the same snapshot regardless of concurrent recording. This is checked
// by TestCampaignStats_BreakdownStaysConsistentWithTotalUnderConcurrentWrites
// as a PROPERTY (by_source sums to click_count under 4 concurrent writers,
// 300 iterations) rather than a statement count or a bare BEGIN tally — per
// #0101's own gotcha, neither of those observes whether the reads are
// actually inside the transaction or what isolation level it runs at. That
// test fails within a handful of iterations if the reads are switched back
// to the pool while still opening (and discarding) the transaction, and
// fails if the isolation level is downgraded to READ COMMITTED.
func (s *StatsStore) CampaignStats(ctx context.Context, campaignID int64, from, to time.Time) (CampaignStats, error) {
	tx, err := s.beginCampaignReadTx(ctx)
	if err != nil {
		return CampaignStats{}, fmt.Errorf("clicks: begin campaign stats tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resolvedFrom, resolvedTo, err := campaignWindow(ctx, tx, campaignID, from, to)
	if err != nil {
		return CampaignStats{}, err
	}
	return campaignStatsQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
}

// CampaignSummary returns CampaignStats plus the clicks-over-time series for
// the identical resolved window, read inside ONE REPEATABLE READ
// transaction so the total and the chart it sits beside can never disagree
// — see CampaignRollup's doc comment for the full argument. Backs GET
// /api/campaigns/{slug}.
func (s *StatsStore) CampaignSummary(ctx context.Context, campaignID int64, from, to time.Time) (CampaignSummary, error) {
	tx, err := s.beginCampaignReadTx(ctx)
	if err != nil {
		return CampaignSummary{}, fmt.Errorf("clicks: begin campaign summary tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resolvedFrom, resolvedTo, err := campaignWindow(ctx, tx, campaignID, from, to)
	if err != nil {
		return CampaignSummary{}, err
	}
	stats, err := campaignStatsQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignSummary{}, err
	}
	ts, err := campaignClicksOverTimeQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignSummary{}, err
	}
	return CampaignSummary{Stats: stats, Timeseries: ts}, nil
}

// CampaignRollup returns the full campaign stats payload — CampaignStats,
// the clicks-over-time series, the per-link breakdown, and the capped
// per-link series — all read inside ONE REPEATABLE READ, read-only
// transaction. Backs GET /api/campaigns/{slug}/stats.
//
// WHY ONE TRANSACTION FOR THE WHOLE PAYLOAD, not just CampaignStats's own
// reads: an earlier version of this code gave CampaignStats its own
// transaction but left CampaignClicksOverTime/CampaignClicksByLink/
// CampaignSeriesByLink reading independently against the pool, each also
// re-resolving the window (four separate campaignWindow calls per request,
// each with its own time.Now()). That fixed breakdown-vs-total consistency
// WITHIN CampaignStats but left total-vs-chart inconsistent ACROSS the
// response: under concurrent recording, `timeseries.days` could sum to a
// different number than `stats.click_count` in the SAME JSON body — #0101's
// "Total clicks: 5" over "No click data yet" defect, reproduced one level
// up, on the exact pair #0104 renders side by side. CampaignRollup resolves
// the window ONCE, inside the transaction, and reads every quarter of the
// payload against that same transaction and that same resolved window, so
// the whole response is internally consistent by construction — not merely
// documented as such.
// TestCampaignRollup_TimeseriesConsistentWithClickCountUnderConcurrentWrites
// pins this under 4 concurrent writers over 300 iterations; it fails if any
// one of the four reads is moved back onto a separate transaction or the
// pool.
func (s *StatsStore) CampaignRollup(ctx context.Context, campaignID int64, from, to time.Time) (CampaignRollup, error) {
	tx, err := s.beginCampaignReadTx(ctx)
	if err != nil {
		return CampaignRollup{}, fmt.Errorf("clicks: begin campaign rollup tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resolvedFrom, resolvedTo, err := campaignWindow(ctx, tx, campaignID, from, to)
	if err != nil {
		return CampaignRollup{}, err
	}
	stats, err := campaignStatsQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignRollup{}, err
	}
	ts, err := campaignClicksOverTimeQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignRollup{}, err
	}
	byLink, err := campaignClicksByLinkQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignRollup{}, err
	}
	seriesByLink, err := campaignSeriesByLinkQuery(ctx, tx, campaignID, resolvedFrom, resolvedTo)
	if err != nil {
		return CampaignRollup{}, err
	}
	return CampaignRollup{Stats: stats, Timeseries: ts, ByLink: byLink, SeriesByLink: seriesByLink}, nil
}

// campaignStatsQuery is CampaignStats's read logic with the window already
// resolved — shared by CampaignStats, CampaignSummary, and CampaignRollup so
// all three compute totals+breakdowns identically regardless of whether q is
// a transaction or the pool.
func campaignStatsQuery(ctx context.Context, q rowQuerier, campaignID int64, from, to time.Time) (CampaignStats, error) {
	stats := CampaignStats{
		BySource:  []Bucket{},
		ByMedium:  []Bucket{},
		ByContent: []Bucket{},
		ByReferer: []Bucket{},
		// The resolved window every query below (and this function's own
		// COUNT) filters on — recorded from the exact from/to this function
		// received, so it can never disagree with the counts alongside it.
		//
		// .UTC() is load-bearing, not decoration. time.Format renders in the
		// value's own Location, and pgx decodes timestamptz into the PROCESS's
		// local zone — so a campaign whose starts_at is UTC-midnight 2026-07-01
		// formats as 2026-06-30 on any server with a negative offset. That
		// silently shifts the window this field documents AND the divisor the
		// UI computes clicks/day from. WindowTo happened to be immune because
		// campaignWindow builds `today` with time.UTC explicitly, but the
		// campaign-supplied branch is exactly the dated in-flight case these
		// fields exist for.
		WindowFrom: from.UTC().Format("2006-01-02"),
		WindowTo:   to.UTC().Format("2006-01-02"),
	}

	if err := q.QueryRow(ctx,
		`SELECT COUNT(*) FILTER (WHERE NOT is_bot),
		        COUNT(*) FILTER (WHERE is_bot)
		   FROM clicks
		  WHERE campaign_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3`,
		campaignID, from, to,
	).Scan(&stats.ClickCount, &stats.ExcludedBotCount); err != nil {
		return CampaignStats{}, fmt.Errorf("clicks: counting campaign clicks: %w", err)
	}

	var err error
	if stats.BySource, err = dimensionBreakdown(ctx, q, "campaign_id", campaignID, "utm_source", from, to); err != nil {
		return CampaignStats{}, err
	}
	if stats.ByMedium, err = dimensionBreakdown(ctx, q, "campaign_id", campaignID, "utm_medium", from, to); err != nil {
		return CampaignStats{}, err
	}
	if stats.ByContent, err = dimensionBreakdown(ctx, q, "campaign_id", campaignID, "utm_content", from, to); err != nil {
		return CampaignStats{}, err
	}
	if stats.ByReferer, err = dimensionBreakdown(ctx, q, "campaign_id", campaignID, "referer", from, to); err != nil {
		return CampaignStats{}, err
	}
	return stats, nil
}

// campaignWindow computes the effective [from, to) window for a
// campaign-scoped stats query, per #0102's acceptance criteria. Takes a
// rowQuerier (pool or tx) rather than always reading s.pool directly, so
// callers that need EVERY read — including the window lookup itself —
// inside one transaction (CampaignStats/CampaignSummary/CampaignRollup) get
// a window resolved against the same snapshot as the clicks it bounds;
// standalone single-query methods (CampaignClicksOverTime,
// CampaignClicksByLink, CampaignSeriesByLink) pass the pool and resolve the
// window once per call, same as before.
//
// When the caller supplies NEITHER bound (from and to both zero — the
// endpoint's optional ?from=/?to= were both absent), the default follows the
// campaign's OWN starts_at/ends_at when BOTH are set on the campaign row:
// from = starts_at, to = ends_at clamped so it never exceeds today (a
// campaign whose ends_at is in the future would otherwise ask for clicks
// that have not happened yet). When starts_at/ends_at are not BOTH set, the
// existing 30-day-ending-today default applies, matching ClicksOverTime.
//
// When the caller supplies AT LEAST ONE bound explicitly, the campaign's
// dates are not consulted at all; the missing side (if any) falls back to
// today / today-30-days, mirroring ClicksOverTime's existing partial-default
// behavior for link stats.
//
// Ownership is NOT enforced here, matching UTMStatsForLink/ClicksOverTime:
// the caller (the campaigns handler) resolves the campaign by slug scoped to
// the authenticated user first, then passes the resolved campaign id, so a
// non-owner never reaches this query.
func campaignWindow(ctx context.Context, q rowQuerier, campaignID int64, from, to time.Time) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	if from.IsZero() && to.IsZero() {
		var startsAt, endsAt *time.Time
		if err := q.QueryRow(ctx,
			`SELECT starts_at, ends_at FROM campaigns WHERE id = $1`, campaignID,
		).Scan(&startsAt, &endsAt); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("clicks: reading campaign window: %w", err)
		}
		if startsAt != nil && endsAt != nil {
			resolvedTo := *endsAt
			if resolvedTo.After(today) {
				resolvedTo = today
			}
			return *startsAt, resolvedTo, nil
		}
		return today.AddDate(0, 0, -defaultTimeseriesDays), today, nil
	}

	if to.IsZero() {
		to = today
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -defaultTimeseriesDays)
	}
	return from, to, nil
}

// CampaignClicksOverTime is the campaign-scoped analog of ClicksOverTime:
// per-day click counts (bot excluded) for campaignID over the half-open
// [from, to) window, bucketed by UTC calendar day. Grouped on
// clicks.campaign_id (#0100), so a since-reassigned or since-unassigned
// link's historical clicks stay attributed to the campaign that was running
// when they happened. Default window resolution follows campaignWindow's
// doc comment. Days with zero clicks are omitted; the result slice is
// always non-nil so the JSON encodes as [] rather than null.
//
// STANDALONE: this method resolves its window and reads clicks as two
// separate pool queries, not inside a transaction — safe when the result is
// the only thing read for a response, but NOT when it will be shown
// alongside CampaignStats/other campaign reads in the same response. Use
// CampaignSummary or CampaignRollup for that — see CampaignRollup's doc
// comment for why a shared transaction is required there.
//
// idx_clicks_campaign_time (migration 000012) carries this query's
// campaign_id + clicked_at range condition — see the package-level doc
// comment above UTMStatsForLink for why its partial predicate is
// deliberately NOT extended to also cover is_bot, and why the resulting
// `Filter: (NOT is_bot)` node in this query's EXPLAIN output is expected,
// not a defect (see TestExplainCampaignClicksOverTime_UsesCampaignTimeIndex).
func (s *StatsStore) CampaignClicksOverTime(ctx context.Context, campaignID int64, from, to time.Time) (TimeseriesResult, error) {
	resolvedFrom, resolvedTo, err := campaignWindow(ctx, s.pool, campaignID, from, to)
	if err != nil {
		return TimeseriesResult{}, err
	}
	return campaignClicksOverTimeQuery(ctx, s.pool, campaignID, resolvedFrom, resolvedTo)
}

// campaignClicksOverTimeQuery is CampaignClicksOverTime's read logic with the
// window already resolved — shared with CampaignSummary/CampaignRollup so
// both compute the series identically regardless of whether q is a
// transaction or the pool.
func campaignClicksOverTimeQuery(ctx context.Context, q rowQuerier, campaignID int64, from, to time.Time) (TimeseriesResult, error) {
	rows, err := q.Query(ctx,
		`SELECT to_char(date_trunc('day', clicked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		        COUNT(*) AS count
		   FROM clicks
		  WHERE campaign_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3
		    AND is_bot = FALSE
		  GROUP BY day
		  ORDER BY day ASC`,
		campaignID, from, to,
	)
	if err != nil {
		return TimeseriesResult{}, fmt.Errorf("clicks: querying campaign timeseries: %w", err)
	}
	defer rows.Close()

	out := []DayBucket{}
	for rows.Next() {
		var b DayBucket
		if err := rows.Scan(&b.Date, &b.Count); err != nil {
			return TimeseriesResult{}, fmt.Errorf("clicks: scanning campaign timeseries row: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return TimeseriesResult{}, fmt.Errorf("clicks: iterating campaign timeseries rows: %w", err)
	}
	return TimeseriesResult{Days: out}, nil
}

// LinkBucket is one row of the per-link click breakdown for a campaign: a
// link's identity (id/key/title) paired with its click count (bot excluded)
// within the requested window. LinkID/Key/Title are nil/empty only when the
// click's link_id itself is NULL. No current code path produces that:
// clicks.link_id is a plain `REFERENCES links(id)` (NO ACTION, migration
// 000003) and links are soft-deleted (active = FALSE), never hard-deleted,
// so every existing click's link_id resolves to a real row today. The
// handling below is defensive rather than reachable — it matches the
// column's actual (if currently unexercised) nullability, so a future
// hard-delete path, or a manually null'd row, degrades to its own row
// instead of an error or a silently dropped click.
type LinkBucket struct {
	LinkID *int64 `json:"link_id"`
	Key    string `json:"key"`
	Title  string `json:"title"`
	Count  int64  `json:"count"`
}

// CampaignClicksByLink returns campaignID's clicks (bot excluded) broken
// down by link over the half-open [from, to) window, ordered by count desc
// then link_id asc. Default window resolution follows campaignWindow's doc
// comment.
//
// STANDALONE: like CampaignClicksOverTime, this reads the pool directly, not
// inside a transaction. Use CampaignRollup instead when the result will be
// shown alongside CampaignStats/the timeseries in the same response.
//
// Grouped on clicks.campaign_id AND clicks.link_id — the click's OWN
// recorded values (#0100) — never on links.campaign_id, so a link
// reassigned or unassigned after the click was recorded still appears under
// THIS campaign for its historical clicks (asserted directly in
// TestCampaignClicksByLink_SinceUnassignedLinkStaysAttributed). The LEFT
// JOIN to links below is enrichment only — it supplies key/title for
// display and carries no predicate — so it cannot make a row disappear the
// way a FILTERING join would (the #0101 LEFT JOIN...ON-vs-WHERE trap this
// issue's own acceptance criteria call out generalizing to any campaign
// aggregate).
//
// Unlike CampaignSeriesByLink, this is a table, not a multi-series chart, so
// it is deliberately NOT capped: a campaign with many links returns one row
// per link (or per NULL link_id) that received at least one non-bot click in
// the window. The result slice is always non-nil so the JSON encodes as []
// rather than null.
func (s *StatsStore) CampaignClicksByLink(ctx context.Context, campaignID int64, from, to time.Time) ([]LinkBucket, error) {
	resolvedFrom, resolvedTo, err := campaignWindow(ctx, s.pool, campaignID, from, to)
	if err != nil {
		return nil, err
	}
	return campaignClicksByLinkQuery(ctx, s.pool, campaignID, resolvedFrom, resolvedTo)
}

// campaignClicksByLinkQuery is CampaignClicksByLink's read logic with the
// window already resolved — shared with CampaignRollup.
func campaignClicksByLinkQuery(ctx context.Context, q rowQuerier, campaignID int64, from, to time.Time) ([]LinkBucket, error) {
	rows, err := q.Query(ctx,
		`SELECT k.link_id, COALESCE(l.key, ''), COALESCE(l.title, ''), COUNT(*) AS bucket_count
		   FROM clicks k
		   LEFT JOIN links l ON l.id = k.link_id
		  WHERE k.campaign_id = $1
		    AND k.clicked_at >= $2
		    AND k.clicked_at < $3
		    AND k.is_bot = FALSE
		  GROUP BY k.link_id, l.key, l.title
		  ORDER BY bucket_count DESC, k.link_id ASC`,
		campaignID, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("clicks: querying campaign clicks by link: %w", err)
	}
	defer rows.Close()

	out := []LinkBucket{}
	for rows.Next() {
		var b LinkBucket
		if err := rows.Scan(&b.LinkID, &b.Key, &b.Title, &b.Count); err != nil {
			return nil, fmt.Errorf("clicks: scanning campaign link bucket: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clicks: iterating campaign link rows: %w", err)
	}
	return out, nil
}

// seriesByLinkCap is the maximum number of individually-named per-link
// series CampaignSeriesByLink returns; the remainder are folded into one
// synthetic "Other" series (LinkSeries.IsOther). Chosen from the issue's
// stated 5-8 range. The cap lives here, in the query layer, specifically so
// the frontend (#0104) cannot forget to apply it — a campaign with 30
// fliers would otherwise render as unreadable spaghetti.
const seriesByLinkCap = 6

// seriesOtherLabel is the display label CampaignSeriesByLink assigns to the
// synthetic "Other" series' Title when folding series beyond
// seriesByLinkCap. LinkID is nil and IsOther is true on that row, so the
// frontend can identify it structurally rather than string-matching a label.
const seriesOtherLabel = "Other"

// LinkSeries is one named series in the campaign's per-link clicks-over-time
// chart (#0104): a link's identity paired with its own day-bucketed series,
// mirroring TimeseriesResult/DayBucket's shape and the half-open-window/
// UTC-day/YYYY-MM-DD conventions used everywhere else in this file. LinkID
// is nil and IsOther is true for the single synthetic series that folds
// every link beyond seriesByLinkCap (plus any click whose own link_id is
// NULL — see LinkBucket's doc comment) — Days is always non-nil, per this
// file's slices-never-null convention.
type LinkSeries struct {
	LinkID  *int64      `json:"link_id"`
	Key     string      `json:"key"`
	Title   string      `json:"title"`
	IsOther bool        `json:"is_other"`
	Days    []DayBucket `json:"days"`
}

// CampaignSeriesByLink returns campaignID's clicks (bot excluded) as one
// day-bucketed series per link over the half-open [from, to) window, ranked
// by total clicks in the window (desc, link_id asc tiebreak), capped at the
// top seriesByLinkCap links with every remaining click — including clicks
// whose own link_id is NULL — folded into one synthetic "Other" series
// (LinkSeries.IsOther). "Other" is appended ONLY when there is a remainder
// to fold; a campaign with seriesByLinkCap or fewer distinct, identifiable
// links returns no "Other" row at all. Default window resolution follows
// campaignWindow's doc comment.
//
// STANDALONE: like CampaignClicksOverTime, this reads the pool directly, not
// inside a transaction. Use CampaignRollup instead when the result will be
// shown alongside CampaignStats/the timeseries in the same response.
//
// RECONCILIATION: every row this method returns is built by partitioning
// the SAME single query result (grouped by clicks.campaign_id, clicks.link_id,
// and UTC day) into "named" and "folded" halves in Go — no click is queried
// twice and none is dropped, so summing every series' Days always equals
// the campaign's overall bot-excluded ClickCount for the identical window
// (asserted directly by TestCampaignSeriesByLink_CapsAndOtherReconciles).
// Grouped on clicks.campaign_id/clicks.link_id (#0100), never a join through
// links, for the same since-unassigned reason CampaignClicksByLink documents.
func (s *StatsStore) CampaignSeriesByLink(ctx context.Context, campaignID int64, from, to time.Time) ([]LinkSeries, error) {
	resolvedFrom, resolvedTo, err := campaignWindow(ctx, s.pool, campaignID, from, to)
	if err != nil {
		return nil, err
	}
	return campaignSeriesByLinkQuery(ctx, s.pool, campaignID, resolvedFrom, resolvedTo)
}

// campaignSeriesByLinkQuery is CampaignSeriesByLink's read logic with the
// window already resolved — shared with CampaignRollup so the series-by-link
// chart and the rest of the payload can be read from the same transaction.
func campaignSeriesByLinkQuery(ctx context.Context, q rowQuerier, campaignID int64, from, to time.Time) ([]LinkSeries, error) {
	rows, err := q.Query(ctx,
		`SELECT link_id,
		        to_char(date_trunc('day', clicked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		        COUNT(*) AS count
		   FROM clicks
		  WHERE campaign_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3
		    AND is_bot = FALSE
		  GROUP BY link_id, day
		  ORDER BY link_id ASC NULLS LAST, day ASC`,
		campaignID, from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("clicks: querying campaign series by link: %w", err)
	}
	defer rows.Close()

	type dayCount struct {
		linkID *int64
		day    string
		count  int64
	}
	var raw []dayCount
	totals := map[int64]int64{}
	for rows.Next() {
		var dc dayCount
		if err := rows.Scan(&dc.linkID, &dc.day, &dc.count); err != nil {
			return nil, fmt.Errorf("clicks: scanning campaign series row: %w", err)
		}
		raw = append(raw, dc)
		if dc.linkID != nil {
			totals[*dc.linkID] += dc.count
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("clicks: iterating campaign series rows: %w", err)
	}

	// Rank distinct, identifiable link ids by total desc, id asc tiebreak —
	// deterministic regardless of map iteration order.
	ids := make([]int64, 0, len(totals))
	for id := range totals {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if totals[ids[i]] != totals[ids[j]] {
			return totals[ids[i]] > totals[ids[j]]
		}
		return ids[i] < ids[j]
	})

	namedCount := seriesByLinkCap
	if len(ids) < namedCount {
		namedCount = len(ids)
	}
	named := make(map[int64]bool, namedCount)
	for _, id := range ids[:namedCount] {
		named[id] = true
	}

	// Look up key/title only for the named ids — the rest fold into "Other"
	// and never need a display name.
	names := map[int64]struct{ key, title string }{}
	if namedCount > 0 {
		namedIDs := ids[:namedCount]
		lrows, err := q.Query(ctx, `SELECT id, key, COALESCE(title, '') FROM links WHERE id = ANY($1)`, namedIDs)
		if err != nil {
			return nil, fmt.Errorf("clicks: querying link names for series: %w", err)
		}
		defer lrows.Close()
		for lrows.Next() {
			var id int64
			var key, title string
			if err := lrows.Scan(&id, &key, &title); err != nil {
				return nil, fmt.Errorf("clicks: scanning link name: %w", err)
			}
			names[id] = struct{ key, title string }{key, title}
		}
		if err := lrows.Err(); err != nil {
			return nil, fmt.Errorf("clicks: iterating link names: %w", err)
		}
	}

	// Partition the raw rows: named ids keep their own per-day buckets
	// as-is; everything else (unnamed ids beyond the cap, plus any row whose
	// link_id is NULL) folds into a single per-day "Other" accumulator.
	namedDays := make(map[int64][]DayBucket, namedCount)
	otherByDay := map[string]int64{}
	for _, dc := range raw {
		if dc.linkID != nil && named[*dc.linkID] {
			namedDays[*dc.linkID] = append(namedDays[*dc.linkID], DayBucket{Date: dc.day, Count: dc.count})
			continue
		}
		otherByDay[dc.day] += dc.count
	}

	out := make([]LinkSeries, 0, namedCount+1)
	for _, id := range ids[:namedCount] {
		id := id
		n := names[id]
		out = append(out, LinkSeries{
			LinkID: &id,
			Key:    n.key,
			Title:  n.title,
			Days:   namedDays[id],
		})
	}
	if len(otherByDay) > 0 {
		days := make([]string, 0, len(otherByDay))
		for d := range otherByDay {
			days = append(days, d)
		}
		sort.Strings(days)
		otherDays := make([]DayBucket, 0, len(days))
		for _, d := range days {
			otherDays = append(otherDays, DayBucket{Date: d, Count: otherByDay[d]})
		}
		out = append(out, LinkSeries{
			Title:   seriesOtherLabel,
			IsOther: true,
			Days:    otherDays,
		})
	}
	return out, nil
}

// allowedDimensions is the fixed set of columns breakdown may group by. The
// column name is interpolated into SQL, so it must be validated against this set
// to keep the query injection-safe even though all callers pass constants.
// utm_content and referer (#0102) extend the original three UTM keys for
// CampaignStats's channel breakdowns; UTMStatsForLink never requests either,
// but the allowlist has no reason to be narrower than "columns breakdown
// knows how to fold into NoneBucket".
var allowedDimensions = map[string]bool{
	"utm_source":   true,
	"utm_medium":   true,
	"utm_campaign": true,
	"utm_content":  true,
	"referer":      true,
}

// breakdownFilterColumns is the fixed set of columns dimensionBreakdown may
// filter its WHERE clause by. Like allowedDimensions, this string is
// interpolated into SQL and validated first — even though today only two
// constants (link_id, campaign_id) ever reach here from breakdown/
// campaignStatsQuery, this keeps the same injection-safety discipline as the
// dimension allowlist rather than trusting the caller.
var breakdownFilterColumns = map[string]bool{
	"link_id":     true,
	"campaign_id": true,
}

// breakdown groups the link's clicks by one UTM dimension, folding NULL/empty
// into NoneBucket, ordered by count desc then value asc, limited to the top N,
// over the link's entire history (no time window). The dimension column name
// is validated against allowedDimensions before being interpolated, so this
// is not an injection vector.
func (s *StatsStore) breakdown(ctx context.Context, linkID int64, dimension string) ([]Bucket, error) {
	return dimensionBreakdown(ctx, s.pool, "link_id", linkID, dimension, time.Time{}, time.Time{})
}

// dimensionBreakdown is the single query builder behind breakdown
// (per-link, unwindowed, against the pool) and campaignStatsQuery
// (per-campaign, windowed, against whatever rowQuerier it is given — the
// pool or a shared transaction). It groups clicks matching
// filterColumn = filterID by one UTM/referer dimension, folding NULL/empty
// into NoneBucket, ordered by count desc then value asc, limited to the top
// breakdownLimit. filterColumn and dimension are both interpolated into SQL
// and validated against their respective allowlists first — the whole point
// of centralizing this in one function is that there is exactly one place
// performing that validation before interpolation, not two
// independently-written ones that could drift.
func dimensionBreakdown(ctx context.Context, q rowQuerier, filterColumn string, filterID int64, dimension string, from, to time.Time) ([]Bucket, error) {
	if !breakdownFilterColumns[filterColumn] {
		return nil, fmt.Errorf("clicks: unsupported filter column %q", filterColumn)
	}
	if !allowedDimensions[dimension] {
		return nil, fmt.Errorf("clicks: unsupported UTM dimension %q", dimension)
	}

	// COALESCE NULL → '', then NULLIF '' → NULL, then COALESCE → NoneBucket folds
	// both NULL and empty-string values into the single "(none)" bucket.
	// is_bot = FALSE (#0101) excludes automated-fetch clicks from every
	// dimension's breakdown, matching ClickCount so the per-dimension counts
	// always sum consistently with the total.
	query := fmt.Sprintf(
		`SELECT COALESCE(NULLIF(%s, ''), $2) AS value, COUNT(*) AS count
		   FROM clicks
		  WHERE %s = $1
		    AND is_bot = FALSE`, dimension, filterColumn)
	args := []any{filterID, NoneBucket}
	next := 3
	if !from.IsZero() {
		query += fmt.Sprintf(" AND clicked_at >= $%d", next)
		args = append(args, from)
		next++
	}
	if !to.IsZero() {
		query += fmt.Sprintf(" AND clicked_at < $%d", next)
		args = append(args, to)
	}
	query += fmt.Sprintf(" GROUP BY value ORDER BY count DESC, value ASC LIMIT %d", breakdownLimit)

	rows, err := q.Query(ctx, query, args...)
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
