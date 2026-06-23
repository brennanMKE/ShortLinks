# Click Analytics

This document covers click recording and the metrics surface for ShortLinks: what
is captured per redirect, the two backend stats queries, and how the Svelte SPA
renders the data as charts.

---

## Click recording

### When a click is recorded

The redirect handler (`internal/handlers/links.go`) writes a response to the
client first, then fires `Recorder.RecordClick` from a detached goroutine. Click
recording is therefore **best-effort**: a database hiccup can never break or delay
a redirect. The goroutine runs under a 5-second bounded `context.Background()`
(the `recordTimeout` constant in `internal/clicks/recorder.go`), so a slow or
stuck database cannot leak goroutines indefinitely.

### What is captured

Each click maps to one row in the `clicks` table (migration
`migrations/000003_create_clicks.up.sql`):

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Surrogate primary key |
| `link_id` | `BIGINT` | Resolved inside the INSERT via the link's `key`; an unknown key inserts zero rows rather than erroring |
| `clicked_at` | `TIMESTAMPTZ` | Snapshot taken by the handler before the response is written; falls back to `now()` if zero |
| `ip_address` | `INET` | Extracted from `X-Forwarded-For` / `RemoteAddr`; unparseable values are dropped (stored as NULL) |
| `user_agent` | `TEXT` | NULL when empty |
| `referer` | `TEXT` | NULL when empty |
| `utm_source` | `TEXT` | NULL when the parameter was absent |
| `utm_medium` | `TEXT` | NULL when the parameter was absent |
| `utm_campaign` | `TEXT` | NULL when the parameter was absent |
| `utm_term` | `TEXT` | NULL when the parameter was absent |
| `utm_content` | `TEXT` | NULL when the parameter was absent |

The recorder stores empty strings as SQL NULL (via `nullStr` in
`internal/clicks/recorder.go`). This means the analytics `(none)` bucket always
represents genuine absence of a UTM parameter, never an empty string that
happened to be forwarded.

Two indexes support analytics queries:

- `idx_clicks_link_id` on `(link_id)` — aggregate counts per link.
- `idx_clicks_clicked_at` on `(clicked_at)` — time-range queries.

### Go types

```go
// internal/clicks/recorder.go
type Click struct {
    Key         string    // short-link key; resolved to link_id in SQL
    ClickedAt   time.Time // zero → use now()
    IPAddress   string
    UserAgent   string
    Referer     string
    UTMSource   string
    UTMMedium   string
    UTMCampaign string
    UTMTerm     string
    UTMContent  string
}
```

`Recorder` is safe for concurrent use; the underlying `pgxpool.Pool` handles
connection multiplexing. `RecordClick(c Click)` is the fire-and-forget entry
point used on the redirect path; `Record(ctx, c)` is the lower-level method used
in tests.

---

## Stats queries

Both queries live in `internal/clicks/stats.go` and are exposed through
`StatsStore`. `StatsStore` performs no writes and is safe for concurrent use.

Ownership is **not** enforced inside these queries. The link-detail handler
resolves the link `key` scoped to the authenticated user before calling either
method, so a non-owner never reaches these queries with a valid `linkID`.

### `UTMStatsForLink` — per-UTM breakdown

```go
func (s *StatsStore) UTMStatsForLink(ctx context.Context, linkID int64) (UTMStats, error)
```

Returns the aggregate click count plus per-dimension breakdowns for
`utm_source`, `utm_medium`, and `utm_campaign`. Each breakdown is computed by a
shared `breakdown` helper that:

1. Groups clicks by the column value, folding NULL and empty string into the
   single label `(none)` (the exported constant `NoneBucket`) via
   `COALESCE(NULLIF(column, ''), $2)`.
2. Orders results by `count DESC, value ASC` (stable tiebreaker).
3. Limits to the top **20** entries (`breakdownLimit = 20`).

The column name is validated against a fixed allowlist (`allowedDimensions`) before
being interpolated into the query, so this is not an injection vector.

**Output type:**

```go
// internal/clicks/stats.go
type Bucket struct {
    Value string `json:"value"`
    Count int64  `json:"count"`
}

type UTMStats struct {
    ClickCount int64    `json:"click_count"`
    BySource   []Bucket `json:"by_source"`
    ByMedium   []Bucket `json:"by_medium"`
    ByCampaign []Bucket `json:"by_campaign"`
}
```

All three `By*` slices are initialised as empty non-nil slices, so the JSON
representation is always `[]` (never `null`) even for a link with no clicks.
`ClickCount` is fetched with a separate `COUNT(*)` before the dimension breakdowns.

### `ClicksOverTime` — daily UTC buckets

```go
func (s *StatsStore) ClicksOverTime(
    ctx context.Context,
    linkID int64,
    from, to time.Time,
) (TimeseriesResult, error)
```

Returns per-day click counts bucketed by calendar day in UTC. Days with zero
clicks are **omitted** from the result; the frontend fills the gaps (see below).

**Window defaults:**

Both `from` and `to` are optional (callers pass `time.Time{}` to use defaults).
The defaults are computed at query time, not at startup:

```
today  = midnight of the current UTC day
to     = today          (default when to.IsZero())
from   = today − 30d   (default when from.IsZero(), i.e. defaultTimeseriesDays = 30)
```

The window is **half-open: `[from, to)`**. Because `to` defaults to midnight of
today (not midnight of tomorrow), the current UTC day is always excluded. This
prevents a partially-completed day from appearing as a low-count outlier on the
chart.

With defaults applied, the window covers **30 calendar days ending at yesterday**
(UTC). For example, if today is 2026-06-23 UTC:

```
from = 2026-05-24 00:00:00 UTC  (inclusive)
to   = 2026-06-23 00:00:00 UTC  (exclusive)
```

**SQL bucketing:**

```sql
SELECT to_char(date_trunc('day', clicked_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
       COUNT(*) AS count
  FROM clicks
 WHERE link_id = $1
   AND clicked_at >= $2
   AND clicked_at < $3
 GROUP BY day
 ORDER BY day ASC
```

`clicked_at AT TIME ZONE 'UTC'` converts the stored `TIMESTAMPTZ` to UTC before
truncating to day, so a click at 23:59 local time is always bucketed against the
correct UTC calendar date regardless of the database server's local timezone
setting.

Dates are returned as `'YYYY-MM-DD'` strings so the frontend can parse them
without timezone gymnastics.

**Output type:**

```go
// internal/clicks/stats.go
type DayBucket struct {
    Date  string `json:"date"`  // "YYYY-MM-DD"
    Count int64  `json:"count"`
}

type TimeseriesResult struct {
    Days []DayBucket `json:"days"`
}
```

`Days` is initialised as a non-nil empty slice, so the JSON is always `[]`, never
`null`.

---

## SPA consumption

The SPA fetches link detail — including both `utm_stats` and `timeseries` fields —
via `GET /api/links/{key}` and renders them in `web/src/views/LinkDetail.svelte`.

### Data flow

```
GET /api/links/{key}
  → LinkDetail.timeseries  (TimeseriesResult)
  → LinkDetail.utm_stats   (UTMStats)
        │                        │
        ▼                        ▼
  ClicksChart.svelte      UTMBarChart.svelte (×3)
  (charts.ts helpers)     (charts.ts helpers)
```

`LinkDetail.svelte` renders two panels below the link metadata:

1. **"Clicks over time"** — `<ClicksChart timeseries={detail.timeseries} days={30} />`
2. **"UTM breakdown"** — one `<UTMBarChart>` for each of source, medium, and
   campaign, driven by `utmDimensions(detail.utm_stats)` from `linkDetail.ts`.
   The grid uses `auto-fit minmax(14rem, 1fr)` and collapses to a single column
   on narrow screens.

### `web/src/lib/charts.ts` — pure data-shaping helpers

All chart logic is in pure, framework-free TypeScript functions with no DOM or
Svelte dependencies. They are covered by 26 unit tests in
`web/src/lib/charts.test.ts`.

**Timeseries helpers:**

| Function | Purpose |
|---|---|
| `fillDayGaps(days, startDate, endDate)` | Inserts zero-count `DayBucket` entries for every calendar day in `[startDate, endDate]` that the server omitted. Walks UTC dates only, never local time. |
| `toTimeseriesPoints(days)` | Converts `DayBucket[]` to `TimeseriesPoint[]`, adding a short display label (e.g. `"Jun 1"`) for axis rendering. |
| `defaultDateRange(days = 30)` | Returns `[startDate, endDate]` as `"YYYY-MM-DD"` strings (UTC). `endDate` is yesterday UTC; `startDate` is `endDate − (days − 1)`. This matches the backend's `[today−30, today)` window exactly. |
| `toPolylinePoints(points, geo)` | Maps `TimeseriesPoint[]` to an SVG `points` string. X is distributed evenly across `innerW`; a single point is placed at the horizontal centre. Y is scaled linearly with 0 at the bottom. Guards against divide-by-zero (max clamps to 1). |
| `yAxisTicks(max, count = 4)` | Returns up to `count` evenly-spaced tick values from 0 to max (inclusive), always including 0 and max. Returns `[0]` when max ≤ 0. |
| `yCoord(value, max, geo)` | Maps a data value to its SVG y coordinate. Returns the baseline when max ≤ 0. |

**UTM bar helpers:**

| Function | Purpose |
|---|---|
| `toBarRows(buckets)` | Converts `UTMBucket[]` to `BarRow[]`, adding a `pct` field (0–100, percentage of the dimension total). Safe against empty/null input and divide-by-zero (returns `pct: 0` when total is 0). Percentages are computed against the sum of counts in the passed array, so each dimension's bars always fill to 100% of that dimension. |

**SVG geometry:**

`DEFAULT_CHART_GEO` defines the shared coordinate space for the clicks-over-time
chart:

```
viewBox: 0 0 600 180
padLeft: 36, padRight: 12, padTop: 12, padBottom: 32
innerW:  552, innerH: 136
```

The SVG element uses `width="100%"` with a fixed `viewBox`, so it scales
responsively to its container without JavaScript.

### `ClicksChart.svelte`

An inline-SVG area+line chart for the clicks-over-time series.

On each render it:

1. Calls `defaultDateRange(days)` to get the `[startDate, endDate]` window (UTC).
2. Calls `fillDayGaps(timeseries?.days ?? [], startDate, endDate)` to produce a
   dense 30-point array.
3. Converts to `TimeseriesPoint[]` via `toTimeseriesPoints`.
4. Draws horizontal grid lines and y-axis labels for up to 4 ticks, plus
   approximately 5 evenly-spaced x-axis date labels (always including the last
   point).
5. Renders a `<polygon>` area fill and `<polyline>` line, both `aria-hidden`.
   A filled `<circle>` dot is drawn at each non-zero data point.

Accessibility: the SVG carries `role="img"` with `aria-labelledby` pointing to
an inline `<title>` and `<desc>` (e.g. "142 clicks over the last 30 days (2026-05-24
to 2026-06-22)."). A visually-hidden `<table>` below the SVG lists only non-zero
days, so screen readers see the underlying numbers without parsing SVG.

When the window contains no clicks, the SVG is replaced by a plain text
placeholder: "No click data in this period."

All colours come from CSS design tokens (`--accent`, `--accent-subtle`,
`--border`, `--text-faint`), so the chart inherits light/dark mode automatically.

### `UTMBarChart.svelte`

A horizontal proportional bar chart for one UTM dimension. It receives a
`UTMBucket[]` from `LinkDetail` and calls `toBarRows` to add the `pct` field.

Each row renders as:

```
[label]  [====bar track====]  [count]
```

The bar fill width is set via `style="width: {row.pct}%"`. A minimum `min-width:
2px` ensures non-zero rows always show a sliver. The label is capped at `6rem`
with `text-overflow: ellipsis`; `(none)` entries are rendered in italic faint
text.

The visible `<table>` element doubles as the accessible fallback — there is no
hidden screen-reader-only table; the visual row layout is the accessible
representation.

When `buckets` is empty or null, the component renders "No data." in faint text.

---

## Key constants and limits

| Name | Location | Value | Meaning |
|---|---|---|---|
| `recordTimeout` | `internal/clicks/recorder.go` | 5s | Max time a background click INSERT may run |
| `defaultTimeseriesDays` | `internal/clicks/stats.go` | 30 | Look-back window when no range is supplied |
| `NoneBucket` | `internal/clicks/stats.go` | `"(none)"` | Label for NULL/empty UTM values in breakdowns |
| `breakdownLimit` | `internal/clicks/stats.go` | 20 | Top-N rows returned per UTM dimension |

---

## Related files

| Path | Role |
|---|---|
| `migrations/000003_create_clicks.up.sql` | Schema for the `clicks` table and its indexes |
| `internal/clicks/recorder.go` | `Click` type, `Recorder`, `RecordClick` (fire-and-forget), `Record` |
| `internal/clicks/stats.go` | `StatsStore`, `UTMStatsForLink`, `ClicksOverTime`, `Bucket`, `UTMStats`, `DayBucket`, `TimeseriesResult` |
| `internal/clicks/stats_test.go` | DB integration tests for `ClicksOverTime` (BasicBuckets, NoClicks, ZeroDefaults) |
| `internal/handlers/links.go` | Link-detail handler; owns the `statsProvider` interface and populates `linkDetailView.Timeseries` |
| `web/src/lib/charts.ts` | Pure data-shaping helpers and SVG geometry (unit-tested) |
| `web/src/lib/charts.test.ts` | 26 unit tests covering gap-fill, boundary cases, proportion math, NaN guards |
| `web/src/lib/ClicksChart.svelte` | Inline-SVG area+line chart (clicks over time) |
| `web/src/lib/UTMBarChart.svelte` | Inline-SVG proportional bar chart (per UTM dimension) |
| `web/src/views/LinkDetail.svelte` | Renders both chart panels; sources data from `GET /api/links/{key}` |
| `web/src/lib/types.ts` | TypeScript types: `DayBucket`, `TimeseriesResult`, `UTMBucket`, `LinkDetail` |
