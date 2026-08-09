# Click Analytics

This document covers click recording and the metrics surface for ShortLinks:
what is captured per redirect, bot classification, the per-link and
per-campaign stats queries, and how the Svelte SPA renders the data as
charts. For the campaign-specific data model and the honest-comparison
caveats around comparing channels, see `docs/campaigns.md`.

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
`migrations/000003_create_clicks.up.sql`, extended by
`migrations/000012_clicks_campaign_and_bot.up.sql` — #0100):

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Surrogate primary key |
| `link_id` | `BIGINT` | Resolved inside the INSERT via the link's `key`; an unknown key inserts zero rows rather than erroring |
| `clicked_at` | `TIMESTAMPTZ` | Snapshot taken by the handler before the response is written; falls back to `now()` if zero |
| `ip_address` | `INET` | Extracted from `X-Forwarded-For` / `RemoteAddr`; unparseable values are dropped (stored as NULL) |
| `user_agent` | `TEXT` | NULL when empty |
| `referer` | `TEXT` | NULL when empty |
| `utm_source` | `TEXT` | Inbound short-URL value if present, else the link's own stored `utm_source` (#0099), else NULL — see "UTM fallback precedence" below |
| `utm_medium` | `TEXT` | Same precedence as `utm_source` |
| `utm_campaign` | `TEXT` | Same precedence as `utm_source` |
| `utm_term` | `TEXT` | Same precedence as `utm_source` |
| `utm_content` | `TEXT` | Same precedence as `utm_source` |
| `campaign_id` | `BIGINT` | The link's `campaign_id` **at the moment of the click** (#0100), `NULL` if the link had no campaign. `REFERENCES campaigns(id) ON DELETE SET NULL`. See "Campaign attribution" below |
| `is_bot` | `BOOLEAN` | Classified at record time from `user_agent` ([#0101](../issues/0101.md)) — see "Bot classification" below. Column added by #0100; rows recorded before #0101 shipped keep `FALSE` regardless of whether they were automated traffic |

`user_agent`/`referer`/`ip_address` have no fallback: the recorder stores an
empty string as SQL NULL for these three (via `nullStr`/`nullableIP` in
`internal/clicks/recorder.go`), so an absent value is always genuine absence,
never an empty string that happened to be forwarded. The five `utm_*` columns
are different — `nullStr` does not touch them at all; their empty-vs-absent
handling and `(none)`-bucket behavior are governed by the fallback precedence
below instead.

Four indexes support analytics queries:

- `idx_clicks_link_id` on `(link_id)` — aggregate counts per link.
- `idx_clicks_clicked_at` on `(clicked_at)` — time-range queries.
- `idx_clicks_campaign_id` on `(campaign_id) WHERE campaign_id IS NOT NULL` — aggregate counts per campaign.
- `idx_clicks_campaign_time` on `(campaign_id, clicked_at) WHERE campaign_id IS NOT NULL` — campaign-scoped time-range queries (backs [#0102](../issues/0102.md)'s campaign timeseries).

### UTM fallback precedence (#0100)

**The following precedence statement is worded identically in this document
and in `docs/utm.md`'s "What values are recorded" section** — do not let the
two drift; if one changes, change both.

Each of the five `utm_*` columns on a click row resolves independently, in
this exact precedence:

1. **The inbound short-URL query parameter** — if present and non-empty
   (`?utm_source=...` on the `/u/{key}` request), this wins.
2. **Otherwise, the link's own stored discrete UTM value** — what the UTM
   builder baked into `links.utm_source`/`utm_medium`/`utm_campaign`/`utm_term`/`utm_content`
   at create or edit time ([#0099](../issues/0099.md)).
3. **Otherwise, `(none)`** in analytics (`NULL` in the column).

Resolution happens per key, not all-or-nothing: a short URL followed with
only `?utm_source=twitter` records `utm_source = "twitter"` (inbound wins)
while `utm_medium`, `utm_campaign`, `utm_term`, and `utm_content` still fall
back to whatever the link has stored for each, independently. An inbound
empty string (`?utm_source=`) is treated as absent and falls back — never
stored as a literal empty override. Implemented as
`COALESCE(NULLIF($n, ''), l.utm_source)` per column in
`internal/clicks/recorder.go`'s `Record`, inside the same INSERT that
resolves `link_id` and `campaign_id` ([#0100](../issues/0100.md)) — see
"Statement shape" below.

**This is a behavior change to existing per-link analytics, and it is not
retroactive.** Before #0100, a link with `utm_source=newsletter` baked into its
`destination_url` but shared as a bare short URL (no query params on the short
link) recorded `(none)` for every dimension — every breakdown was a single
`(none)` bar. After #0100, the same bare short URL records `utm_source =
"newsletter"` because it now falls back to the link's stored value. Existing
click rows are **not backfilled** with fallback values (see "Campaign
attribution" below for the same non-backfill decision on `campaign_id`), so a
`ClicksOverTime`/UTM-breakdown chart whose date range spans the deploy date
will show a discontinuity — a jump from mostly-`(none)` to mostly-attributed —
at the deploy boundary. That jump is an artifact of when the fallback started
applying, not a real change in traffic or campaign behavior; do not read it as
a trend.

### Campaign attribution (#0100)

`campaign_id` is **denormalized** onto the click row: it is resolved from the
link's `campaign_id` inside the same INSERT that records the click, not joined
from `links`/`campaigns` at query time. This is deliberate — a link can be
reassigned to a different campaign, or unassigned, after clicks have already
been recorded against it. A click resolved via a query-time join would
silently follow the link's *current* campaign, rewriting history every time
the link moves. Because `campaign_id` is captured once, at record time, a
click recorded while a link belonged to Campaign A keeps reporting Campaign A
forever, even after the link is later reassigned to Campaign B or unassigned
entirely.

`campaign_id`'s FK is `ON DELETE SET NULL`, mirroring `links.campaign_id`
(migration 000011) and `clicks.link_id`'s non-cascading FK (migration 000003):
deleting a campaign unassigns its historical clicks' `campaign_id` but never
deletes a click row — click history is designed to outlive the entities it
references.

Existing click rows keep `campaign_id = NULL` after the #0100 migration — they
predate campaigns entirely and are **not backfilled** by guessing from
`utm_campaign` strings (a wrong guess is worse than a known gap, matching
migration 000011's UTM-column decision).

### Bot classification (#0101)

Comparing channels (campaign detail view, `docs/campaigns.md`) only means
something if the click counts reflect human interest rather than automated
fetches. `internal/clicks/botdetect.go`'s `IsBot` classifies each click's
`User-Agent` header, case-insensitively, against the exported
`BotUserAgentSubstrings` list — `bot`, `crawler`, `spider`, `preview`,
`facebookexternalhit`, `Slackbot`, `Twitterbot`, `Discordbot`, `WhatsApp`,
`TelegramBot`, `LinkedInBot`, `headless`, `curl`, `wget`, `python-requests`
— with one exception checked **first**: `NotBotUserAgentSubstrings`
(currently just `cubot`) wins unconditionally, so a CUBOT-brand phone stays
classified as human despite its UA containing the load-bearing `bot`
substring. The result is classified in Go and passed into the same INSERT that
resolves `link_id`/`campaign_id`/the UTM fallback, so classification adds no
extra query on the redirect path.

An **empty or absent User-Agent is NOT classified as a bot** — deliberate:
absence of a UA is missing evidence rather than evidence of automation, and
every crawler on the list self-identifies by default. A bot click is
**flagged, never dropped** — `is_bot = TRUE` still writes the row, so the
data stays inspectable rather than silently vanishing.

**Every stats query in this document excludes `is_bot = TRUE` by default**
(`UTMStatsForLink`'s `ClickCount`, `ClicksOverTime`, every `Campaign*` query
below, and `links.Store`'s `click_count` at every call site) and surfaces the
excluded count alongside the total rather than silently shrinking the
number — `UTMStats` does not currently expose this field, but every
campaign-level struct does, as `ExcludedBotCount` (see "Campaign stats
queries" below).

**User-agent filtering is a floor, not a solution.** Some unfurl/preview
crawlers do not identify themselves in their `User-Agent` string, so a
social channel's click count after filtering is a better number than before
filtering, not a clean one. See `docs/campaigns.md`'s "What this data can
and cannot honestly compare" for the fuller discussion of what this means
for comparing channels.

### Statement shape

Both the UTM fallback and the campaign resolution fit inside the **same
single INSERT...SELECT** the recorder already used to resolve `link_id` — the
redirect path gains no additional query. The `WHERE l.key = $1` scalar lookup
that resolves `link_id` also supplies `l.campaign_id`, `l.utm_source`, etc. for
the `SELECT` list in one round trip.

### Go types

```go
// internal/clicks/recorder.go
type Click struct {
    Key         string    // short-link key; resolved to link_id (and campaign_id) in SQL
    ClickedAt   time.Time // zero → use now()
    IPAddress   string
    UserAgent   string
    Referer     string
    // UTMSource..UTMContent are the INBOUND values only (see "UTM fallback
    // precedence" above) — Record resolves the stored value itself when one
    // of these is "".
    UTMSource   string
    UTMMedium   string
    UTMCampaign string
    UTMTerm     string
    UTMContent  string
}
```

`Click` has no `CampaignID` field — it is never supplied by the caller and is
always resolved from the link inside `Record`'s SQL, so it cannot drift from
whatever campaign the link actually belonged to at record time.

`Recorder` is safe for concurrent use; the underlying `pgxpool.Pool` handles
connection multiplexing. `RecordClick(c Click)` is the fire-and-forget entry
point used on the redirect path; `Record(ctx, c)` is the lower-level method used
in tests.

---

## Deploy-boundary discontinuities

Three numbers change meaning at the moment the campaigns migrations
(`000010`–`000012`) deploy, none of them retroactively. A chart or export
whose date range spans the deploy date will show a step at the boundary in
each case — that step is an artifact of when the new logic started applying,
not a real change in traffic, and should not be read as a trend:

1. **UTM fallback (#0100).** Before the deploy, a link with UTM values baked
   into its `destination_url` but shared as a bare short URL recorded
   `(none)` for every dimension on every click. After, the same bare short
   URL falls back to the link's stored values (see "UTM fallback
   precedence" above). Historical click rows are not rewritten, so a
   per-link or per-campaign breakdown spanning the deploy date jumps from
   mostly-`(none)` to mostly-attributed right at the boundary.
2. **Bot exclusion (#0101).** `is_bot` did not exist before migration
   `000012`, and nothing populated it until #0101 shipped — every click row
   recorded before then keeps `is_bot = FALSE` regardless of whether it was
   actually automated traffic. Historical bot traffic is therefore
   **understated**: a chart spanning the deploy shows a step down in total
   click count (or up, depending on which side excludes more) that reflects
   when filtering started, not a change in how much bot traffic actually
   occurred.
3. **`click_count`'s meaning on links and campaigns.** Before #0101,
   `links.Store`'s `click_count` (surfaced on the dashboard list, the link
   detail response, and the duplicate/reactivate response from
   `POST /api/links`) was a raw `COUNT(*)` over every recorded click. As of
   #0101 it excludes `is_bot = TRUE` at every call site, matching
   `UTMStatsForLink`'s `ClickCount` and every campaign-level count. A link
   with bot clicks reports a **lower** `click_count` after the deploy than
   it would have before — not because clicks were lost, but because the
   field's definition changed from "every recorded click" to "every
   non-bot recorded click."

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

## Campaign stats queries (#0102)

Four campaign-scoped queries extend `internal/clicks/stats.go`'s
`StatsStore`, all grouping on **`clicks.campaign_id`** — never on
`utm_campaign` strings, which is the point of the FK (see
`docs/campaigns.md`'s "Campaign membership is a foreign key" for why):

```go
func (s *StatsStore) CampaignStats(ctx, campaignID int64, from, to time.Time) (CampaignStats, error)
func (s *StatsStore) CampaignClicksOverTime(ctx, campaignID int64, from, to time.Time) (TimeseriesResult, error)
func (s *StatsStore) CampaignClicksByLink(ctx, campaignID int64, from, to time.Time) ([]LinkBucket, error)
func (s *StatsStore) CampaignSeriesByLink(ctx, campaignID int64, from, to time.Time) ([]LinkSeries, error)
```

`CampaignStats` returns the bot-excluded total click count, the excluded bot
count, and breakdowns for source, medium, content, and referer (reusing the
same `breakdown` helper — renamed `dimensionBreakdown` internally — and its
column allowlist as the per-link query above, so there is one validated
interpolation site rather than two that could drift). `CampaignSeriesByLink`
caps the per-link series at **6 named links plus one "Other" fold** — the
cap lives in the query layer so the frontend cannot forget it or pick a
different limit than what the backend enforces.

**These four methods are not the public interface.** `campaignStatsProvider`
exposes exactly two **composite** methods, each of which reads its entire
payload inside one `REPEATABLE READ`, read-only transaction:

```go
func (s *StatsStore) CampaignSummary(ctx, campaignID int64, from, to time.Time) (CampaignSummary, error)
func (s *StatsStore) CampaignRollup(ctx, campaignID int64, from, to time.Time) (CampaignRollup, error)
```

`CampaignSummary` (stats + timeseries) backs `GET /api/campaigns/{slug}`;
`CampaignRollup` (+ by-link + series-by-link) backs
`GET /api/campaigns/{slug}/stats` and the CSV export
(`docs/campaigns.md`'s "CSV export" section). Assembling a response from the
four standalone methods directly — each against its own snapshot — is
possible but production-dead: doing so under concurrent writes lets a
response's timeseries and total disagree, because each fragment would be
read at a different instant. The four standalone methods exist for tests and
carry `STANDALONE:` warnings in their doc comments for exactly this reason.

**Default window:** the campaign's own `starts_at`–`ends_at` when both are
set, clamped so `to` never exceeds today (`campaignWindow` in
`internal/clicks/stats.go`); otherwise the existing 30-day default shared
with the per-link queries above. `CampaignStats.WindowFrom`/`WindowTo` report
the window actually used, so the frontend (and the CSV export's filename)
label and divide by the server's resolved window rather than re-deriving it
client-side.

**Two numbers on two time scales, in the same payload — by design, not a
bug.** `GET /api/campaigns/{slug}` returns `total_clicks` (all-time,
membership-independent of the window) alongside `stats.click_count`
(windowed). A campaign with clicks only outside the current window
legitimately shows a non-zero `total_clicks` next to a zero
`stats.click_count`. This divergence is intentionally pinned by a named test
(`TestCampaignsGet_TotalClicksIsAllTimeStatsClickCountIsWindowed`) rather
than "fixed" by making the two agree.

**Bot exclusion generalizes the same trap #0101 found on links**: a campaign
whose every click is a bot click must still appear in `GET /api/campaigns`'s
list, and `campaigns.Store`'s `link_count`/`total_clicks` use the same
correlated-subquery bot-exclusion spelling as `links.Store`, specifically to
avoid a `LEFT JOIN ... ON is_bot = FALSE` collapsing the row out of the
result via `GROUP BY`.

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
| `defaultTimeseriesDays` | `internal/clicks/stats.go` | 30 | Look-back window when no range is supplied (per-link and campaign-without-dates) |
| `NoneBucket` | `internal/clicks/stats.go` | `"(none)"` | Label for NULL/empty UTM values in breakdowns |
| `breakdownLimit` | `internal/clicks/stats.go` | 20 | Top-N rows returned per UTM dimension, per-link and per-campaign alike |
| `BotUserAgentSubstrings` | `internal/clicks/botdetect.go` | 15 substrings | Case-insensitive bot classification list — see "Bot classification" above |
| series cap (`CampaignSeriesByLink`) | `internal/clicks/stats.go` | 6 + "Other" | Per-link series shown on the campaign clicks-over-time chart |

---

## Related files

| Path | Role |
|---|---|
| `migrations/000003_create_clicks.up.sql` | Original schema for the `clicks` table and its first two indexes |
| `migrations/000012_clicks_campaign_and_bot.{up,down}.sql` | Adds `campaign_id`/`is_bot` and `idx_clicks_campaign_id`/`idx_clicks_campaign_time` (#0100) |
| `internal/clicks/recorder.go` | `Click` type, `Recorder`, `RecordClick` (fire-and-forget), `Record` (the #0100 UTM fallback + campaign_id resolution + #0101 bot classification) |
| `internal/clicks/botdetect.go` | `IsBot`, `BotUserAgentSubstrings` (#0101) |
| `internal/clicks/stats.go` | `StatsStore`; per-link `UTMStatsForLink`/`ClicksOverTime`; campaign `CampaignStats`/`CampaignSummary`/`CampaignRollup`/`CampaignClicksOverTime`/`CampaignClicksByLink`/`CampaignSeriesByLink` (#0102); `Bucket`, `UTMStats`, `DayBucket`, `TimeseriesResult`, `LinkBucket`, `LinkSeries` |
| `internal/clicks/stats_test.go` | DB integration tests for `ClicksOverTime` (BasicBuckets, NoClicks, ZeroDefaults) and the campaign queries |
| `internal/handlers/links.go` | Link-detail handler; owns the `statsProvider` interface and populates `linkDetailView.Timeseries` |
| `internal/handlers/campaigns.go` | Campaign endpoints, including `GET /api/campaigns/{slug}/stats` |
| `web/src/lib/charts.ts` | Pure data-shaping helpers and SVG geometry (unit-tested) |
| `web/src/lib/charts.test.ts` | 26 unit tests covering gap-fill, boundary cases, proportion math, NaN guards |
| `web/src/lib/ClicksChart.svelte` | Inline-SVG area+line chart (per-link clicks over time) |
| `web/src/lib/CampaignClicksChart.svelte` | Inline-SVG area+line chart, combined/per-link toggle (#0104) |
| `web/src/lib/UTMBarChart.svelte` | Inline-SVG proportional bar chart (per UTM dimension), reused for both per-link and per-campaign breakdowns |
| `web/src/views/LinkDetail.svelte` | Renders both per-link chart panels; sources data from `GET /api/links/{key}` |
| `web/src/views/CampaignDetail.svelte` | Renders the campaign summary, charts, and links table; sources data from `GET /api/campaigns/{slug}` and `.../stats` |
| `web/src/lib/types.ts` | TypeScript types: `DayBucket`, `TimeseriesResult`, `UTMBucket`, `LinkDetail`, `CampaignStats`, `LinkBucket`, `LinkSeries` |
