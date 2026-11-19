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
| `ip_hash` | `BYTEA` | 32-byte keyed daily HMAC-SHA256 of the visitor IP (extracted from `X-Forwarded-For` / `RemoteAddr`), computed by the `Recorder`; **the raw IP is never stored** (#0121). NULL when the IP is empty/unparseable, and for every click recorded before `000015` — see "Visitor privacy and retention" below |
| `user_agent` | `TEXT` | NULL when empty. Set to NULL after 90 days (#0121) |
| `referer` | `TEXT` | NULL when empty |
| `utm_source` | `TEXT` | Inbound short-URL value if present, else the link's own stored `utm_source` (#0099), else NULL — see "UTM fallback precedence" below |
| `utm_medium` | `TEXT` | Same precedence as `utm_source` |
| `utm_campaign` | `TEXT` | Same precedence as `utm_source` |
| `utm_term` | `TEXT` | Same precedence as `utm_source` |
| `utm_content` | `TEXT` | Same precedence as `utm_source` |
| `campaign_id` | `BIGINT` | The link's `campaign_id` **at the moment of the click** (#0100), `NULL` if the link had no campaign. `REFERENCES campaigns(id) ON DELETE SET NULL`. See "Campaign attribution" below |
| `is_bot` | `BOOLEAN` | Classified at record time from `user_agent` ([#0101](../issues/0101.md)) — see "Bot classification" below. Column added by #0100; rows recorded before #0101 shipped keep `FALSE` regardless of whether they were automated traffic |

`user_agent`/`referer` have no fallback: the recorder stores an
empty string as SQL NULL for these two (via `nullStr` in
`internal/clicks/recorder.go`), so an absent value is always genuine absence,
never an empty string that happened to be forwarded. The five `utm_*` columns
are different — `nullStr` does not touch them at all; their empty-vs-absent
handling and `(none)`-bucket behavior are governed by the fallback precedence
below instead.

### Visitor privacy and retention (#0121)

Nothing in the stats, CSV export, or API reads per-click rows, so the visitor
IP is only ever used to tell repeat clicks apart within a day. It is stored as
a one-way hash, and click detail expires.

- **Hash.** `Recorder.Record` calls `IPHasher.HashAt(ip, clickedAt)`
  (`internal/clicks/iphash.go`). The key is
  `HMAC-SHA256(SESSION_SECRET, "shortlinks click-ip key v1")`, so no new
  variable is needed and the key is independent of cookie signing. The daily
  salt is `HMAC-SHA256(key, "shortlinks click-ip salt v1\x00" + <UTC date of the click>)`
  and the stored value is `HMAC-SHA256(salt, canonical IP)`. The same IP hashes
  the same within one UTC day and differently on every other day, so hashes
  cannot be linked across days. IPv4-mapped IPv6 is hashed as IPv4 and zones
  are dropped; IPv6 is hashed at full address. Rotating `SESSION_SECRET`
  changes the key, which is harmless for the same reason.
- **Fail closed.** A nil hasher or an unparseable IP stores NULL, never the raw
  value.
- **Retention.** `Sweeper` (`internal/clicks/retention.go`, started from
  `servePostgres`) runs at startup and then hourly. It sets `ip_hash` and
  `user_agent` to NULL on clicks older than `DetailRetention` (90 days). The
  **row is kept** as an anonymous event, so `links.click_count`, campaign
  totals, charts and breakdowns are unchanged and no daily-aggregate table is
  needed. `referer` and `utm_*` are kept: they are stats dimensions and
  describe the referring page, not the visitor. `is_bot` is classified at
  record time, so stripping `user_agent` loses nothing the stats use.
- **Legacy data.** Migration `000015` drops `ip_address` and adds `ip_hash`;
  existing rows get NULL because SQL cannot reproduce the keyed hash and an
  unsalted hash of an IPv4 address is reversible. The raw IPs are gone for good.
- **Not covered.** Apache's access log on the host still records visitor IPs
  (rotate it with logrotate), and `audit_log` / `api_tokens.last_used_ip` hold
  the signed-in operator's IP, not visitors'.

Five indexes support analytics queries:

- `idx_clicks_link_id` on `(link_id)` — aggregate counts per link.
- `idx_clicks_clicked_at` on `(clicked_at)` — time-range queries.
- `idx_clicks_campaign_id` on `(campaign_id) WHERE campaign_id IS NOT NULL` — aggregate counts per campaign.
- `idx_clicks_link_time` on `(link_id, clicked_at)` — per-link time probes (idle-links, last click; migration `000019`, [#0150](../issues/0150.md)).
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
`CampaignRollup` (+ by-link + series-by-link + by-placement,
`campaignClicksByPlacementQuery`, #0122) backs
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

## Reports (#0139)

`internal/reports` serves user-wide analytics: every report adds up clicks
across **all of the caller's links** (`JOIN links l ON l.id = k.link_id WHERE
l.user_id = $1`; the user comes from the session or API token, never from the
query). The routes are behind `requireAPI`, so a browser session, a full token
and a read-only token all work (both routes are `GET`). They are not mounted
in `STORAGE=json` dev mode.

- `GET /api/reports` returns the catalogue: `{"reports": [{name, description,
  path, params}], "common_params": [...]}`. Each report lists only its *extra*
  parameters; `common_params` applies to all of them.
- `GET /api/reports/{name}` runs one report. An unknown name is `404
  {"error":"report_not_found"}`; a bad parameter is `400` with
  `{"error": "<code>", "field": "<param>", "message": "..."}` where the code
  is `invalid_range`, `invalid_tz`, `invalid_date`, `invalid_compare`,
  `invalid_include_bots` or `invalid_limit`. A report about one campaign or
  link answers `404 {"error":"campaign_not_found"}` or
  `{"error":"link_not_found"}` when the slug or key does not exist or is not the
  caller's (a `*reports.NotFoundError`, below).

### Parameters (shared by every report)

| Param | Values | Default |
|---|---|---|
| `range` | `7d`, `30d`, `90d`, `365d`, `all` | `30d` |
| `from`, `to` | `YYYY-MM-DD`, inclusive days in `tz`; overrides `range`. `to` defaults to today and needs `from`; a `to` in the future is clamped to today, but a `from` in the future (in `tz`) is `400 invalid_date` | none |
| `tz` | IANA zone, e.g. `America/Los_Angeles` | `DEFAULT_TIMEZONE`, else `UTC` |
| `compare` | `previous` (same-length window immediately before), `none` | `previous` (no previous window for `range=all`) |
| `campaign` | campaign slug, or `none` for clicks with no campaign | all |
| `source`, `medium`, `utm_campaign`, `content`, `term`, `placement` | repeated parameters (`content=a&content=b`, each value taken whole, commas allowed) or one comma-separated parameter (`content=a,b`; write `\,` for a literal comma); `~none` matches empty/NULL | all |
| `include_bots` | `true` / `false` | `false` |
| `limit` | 1-500 | report-specific (`default_limit` in the catalogue) |

Filters are **OR within a dimension, AND across dimensions**. `source`,
`medium`, `utm_campaign`, `content` and `term` match the click's own `utm_*`
value (trimmed, case-sensitive). `placement` is stored on the link, not the
click, so it matches `links.placement`, trimmed and lower-cased the same way
the campaign placement rollup groups it. `campaign` uses `clicks.campaign_id`
(attribution at click time), like the campaign rollups.

**Days and windows.** Days are bucketed with `date_trunc('day', clicked_at AT
TIME ZONE tz)`. A window is whole civil days in `tz`, `[midnight of from,
midnight after to)`, computed through the calendar rather than 24-hour steps,
so a daylight-saving day of 23 or 25 hours still starts and ends at local
midnight. The window **includes today as a partial day** and says so
(`window.partial_last_day: true`). The previous window has the same number of
days and ends the day before the current one starts.

### Response envelope

```json
{
  "report": "overview",
  "window": {"from": "2026-10-20", "to": "2026-11-18", "tz": "America/Los_Angeles", "partial_last_day": true},
  "previous_window": {"from": "2026-09-20", "to": "2026-10-19"},
  "filters": {"campaign": "guides-2026-11", "source": ["mastodon", "bluesky"]},
  "generated_at": "2026-11-18T20:41:07Z",
  "highlights": ["1,284 clicks between 2026-10-20 and 2026-11-18, up 18% on the previous 30 days."],
  "data": {}
}
```

`previous_window` is `null` for `compare=none` and `range=all`. `filters`
echoes only the filters that were applied. `highlights` is 1-5 plain-English
sentences computed in Go from `data`, enough to answer simple questions
without reading it. All queries of one request run in one `REPEATABLE READ`
read-only transaction, so the numbers agree with each other.

### `overview`

No extra parameters. `limit` (default 50) caps the values in each facet.

```json
"data": {
  "totals":   {"clicks": 1284, "visitor_days": 911, "excluded_bots": 64, "links_clicked": 37, "links_total": 58},
  "previous": {"clicks": 1088, "visitor_days": 813},
  "timeseries": [{"date": "2026-10-20", "clicks": 31}],
  "previous_timeseries": [{"date": "2026-09-20", "clicks": 22}],
  "best_day": {"date": "2026-11-10", "clicks": 96},
  "campaigns": [{"slug": "guides-2026-11", "name": "Guides 2026-11", "clicks": 902}, {"slug": null, "name": "No campaign", "clicks": 71}],
  "facets": {"source": [{"value": "mastodon", "clicks": 498}], "medium": [], "utm_campaign": [], "content": [], "term": [], "placement": []}
}
```

- `timeseries` has one entry per day of the window (zero-filled);
  `previous_timeseries` the same for the previous window (`[]` when none).
  `previous` and `best_day` are `null` when there is no previous window or no
  clicks.
- `totals.excluded_bots` counts bot clicks left out (0 with
  `include_bots=true`). `links_total` is every link the user owns;
  `links_clicked` those with a counted click in the window.
- `campaigns` lists every campaign with at least one counted click, sorted by
  clicks. It applies the UTM filters but **not** the `campaign` filter, so a
  focus-chip row does not collapse to the selected campaign. Clicks with no
  campaign appear as `{"slug": null, "name": "No campaign"}`.
- Each facet applies every filter **except its own dimension's**, so a
  dropdown never offers a value that yields zero results. Empty values appear
  as `~none`, which can be passed straight back as a filter.
- `visitor_days` is `COUNT(DISTINCT (ip_hash, day))` over clicks with an
  `ip_hash`. Because click detail is stripped after 90 days (see
  [Visitor privacy and retention](#visitor-privacy-and-retention-0121)),
  `previous.visitor_days` is `null` when the previous window starts before
  that cutoff, and `highlights` says so; a current window that starts before
  it gets a highlight that the figure undercounts.

### Content grouping (#0140)

One piece of content is often shared as several links (one per platform or
campaign). `internal/reports/content.go` groups them in Go with pure functions:

- `ContentKey(destination)`: the destination with the scheme, every `utm_*`
  parameter (any case), the fragment and any trailing slash removed and the
  host lower-cased, e.g. `www.opencircuitsf.com/guides/measuring-current`.
  Default ports (`:443` on https, `:80` on http) are dropped. Other query
  parameters are kept, sorted by name, since they can select different
  content. An opaque URL such as `mailto:` is returned trimmed and unchanged. Two links share a group if and only if their keys match.
  `ContentPath` gives the display path (`/guides/measuring-current`, `/` for
  the root).
- `LinkLabel(title, utmSource)` cuts the title at its last ` — `, ` – `, ` | `
  or ` - ` when the **whole tail** is channel tokens, matched as whole words,
  case-insensitively: a platform word (mastodon, bluesky, instagram, facebook,
  threads, x, twitter, linkedin, newsletter, email, poster, flyer, qr), the
  link's own `utm_source`, a channel word (guide, post, posts, flyer, poster,
  qr, newsletter, link, story, reel, thread, issue, edition), a number, `#`,
  `No.` or a month name; at least one must be a platform/source or channel
  word. So "- Poster QR 3" and "— November newsletter" are cut but "- Threads
  in Go" and "- Email marketing for makers" are not. (This is stricter than the
  original issue text, which cut on any tail that started with a platform word
  or contained post/flyer/...; that mis-cut real titles.) It returns the head
  and the tail, the link's `label`, and uses only the link's own fields, so
  labels and titles never depend on the window or filters.
- `ContentTitle(members, destination)`: the most common non-empty head across a
  group's links (ties go to the lexically smaller head). With no head left it
  uses the last path segment, dashes and underscores turned into spaces, then
  the host.

Grouping runs on per-link counts: SQL aggregates counted clicks per link and
click `utm_source` (current and previous window, at most 20,000 rows each,
busiest first; when a cap is hit `data.truncated` is `true` and a highlight
says counts may be incomplete), then Go groups by key. Previous-window counts are summed per
group and per link; `previous_clicks` and `change_pct` are `null` when there is
no previous window, and `change_pct` is also `null` when the previous count is
zero. `change_pct` is rounded to one decimal.

### `top-content`

Extra parameter `sort`: `clicks` (default), `change` (clicks minus previous
clicks, largest first) or `newest` (newest link in the group first). `limit`
defaults to 10 and caps `groups`; `total_groups` counts all of them. A group
appears when at least one member had a counted click in the window, or with
`sort=newest`, which lists every link's group (zero-click ones too; the click
filters still apply to the counts, not to which links are listed).

```json
"data": {"groups": [{
  "content_key": "www.opencircuitsf.com/guides/measuring-current",
  "title": "Measuring current without blowing the fuse",
  "path": "/guides/measuring-current",
  "clicks": 164, "previous_clicks": 150, "change_pct": 9.3,
  "by_source": [{"value": "mastodon", "clicks": 90}, {"value": "bluesky", "clicks": 52}, {"value": "~none", "clicks": 22}],
  "links": [{"key": "HaQRwV", "label": "Mastodon guide post 84", "utm_source": "mastodon", "utm_medium": "social", "campaign_slug": "guides-2026-11", "clicks": 90}]
}], "total_groups": 23}
```

`by_source` uses the clicks' own `utm_source`. `highlights` names the top
content, the source with the biggest share of its clicks across all its links and how many links it
is shared as.

### `top-links`

Extra parameter `sort` as above; `limit` defaults to 20, `total_links` counts
every matching link. Each row has `key`, `short_url` (built from `BASE_URL`),
`title`, `label`, `content_key`, `content_title`, `destination`,
`campaign_slug`, `utm_source`/`utm_medium`/`utm_campaign`/`utm_content`/
`utm_term` and `placement` (the link's own fields), `active`, `created_at`,
`clicks`, `previous_clicks` and `change_pct`. `highlights` names the top link
and the source with the biggest share of clicks across all matching links (not
only the listed rows).

### `breakdown`

Extra parameter `dimension` (required; `missing_dimension` / `invalid_dimension`
otherwise): `source`, `medium`, `utm_campaign`, `content`, `term`, `referer`,
`placement`, `campaign`, `link` or `content_group`. `limit` defaults to 20; the
values beyond it are summed into `other` (`null` when everything fits). All
filters apply, including one on the dimension itself.

```json
"data": {"dimension": "source", "total": 1284, "previous_total": 1088,
  "rows": [{"value": "mastodon", "clicks": 498, "share": 0.388, "previous_clicks": 410}],
  "other": {"clicks": 12, "values": 4}}
```

- `share` is clicks over `total` (all counted clicks, including `other`),
  rounded to three decimals. `previous_clicks` and `previous_total` are `null`
  without a previous window. Rows sort by clicks, then value.
- Empty values are `~none` for the UTM dimensions and placement (usable as a
  filter), `none` for `campaign` (a campaign slug otherwise; `label` is the
  campaign name) and `(direct)` for `referer`.
- `referer` is the referrer **hostname**, lower-cased: scheme, userinfo, port,
  path and query are dropped, so `https://www.google.com/search?q=x` and
  `http://www.google.com:8080/` both count as `www.google.com` (`www.` is not
  stripped). Empty or missing is `(direct)`. A value with no parseable host is
  reported as is. `clicks.referer` is cleared after 90 days (see
  [Visitor privacy and retention](#visitor-privacy-and-retention-0121)), so older
  clicks read as `(direct)`.
- `link` values are link keys (`label` is the title); `content_group` values
  are #0140 content keys (`label` is the content title), grouped in Go from the
  per-link counts, so `truncated` and the 20,000-link cap apply as for
  `top-content`.

### `timeseries`

Extra parameters `granularity` (`day` default, or `week`), `split_by` (any
`breakdown` dimension) and `series_limit` (default 5, 1-50).

```json
"data": {"granularity": "day", "split_by": "source",
  "series": [{"name": "mastodon", "clicks": 498, "points": [{"date": "2026-10-20", "clicks": 12}]}],
  "previous": [{"date": "2026-09-20", "clicks": 9}]}
```

- Without `split_by` there is one series named `all`. With it, series are
  ranked by clicks and those past `series_limit` are summed into one
  `{"name": "other", "other": true}` series. Series that come from a campaign,
  link or content group carry a `label`.
- Every series has one point per bucket of the window (zero-filled).
- Weeks start **Monday in `tz`**; a point's date is that Monday. The first and
  last weeks of a window are partial (only the window's days count), so a
  window starting on a Wednesday has a first point dated the Monday before.
  Bucketing is calendar arithmetic on civil dates, so a daylight-saving week
  is no different.
- `previous` is the previous window's combined total with **exactly as many
  buckets as the current series, aligned by position**: previous bucket i
  covers the same day offsets within the previous window as current bucket i
  covers within the current window, and its `date` is the first day of that
  span (so with weekly buckets a partial first week lines up with an equally
  partial previous bucket, not a calendar week). `null` when there is no
  previous window.
- Referrers are cleared after 90 days. For `dimension=referer` or
  `split_by=referer`, a previous window that starts before that cutoff gives
  `previous_clicks`, `previous_total` and `previous` as `null` (and a
  highlight says so); a current window that starts before it gets a highlight
  that older clicks show as `(direct)`.

### `movers`

Extra parameters `unit` (`content` default, or `link`) and `min_clicks`
(default 5, 0 or more); `limit` is per list (default 5).

```json
"data": {"unit": "content", "min_clicks": 5, "compared": true,
  "rising": [{"title": "Deep sleep guide", "content_key": "…", "clicks": 212, "previous_clicks": 150, "change": 62, "change_pct": 41.3}],
  "falling": [], "new": [], "gone_quiet": []}
```

- `rising`: clicks up on a previous period that had some; `falling`: down but
  still above zero; `new`: no previous clicks, now at least `min_clicks`;
  `gone_quiet`: at least `min_clicks` before, zero now. `min_clicks` applies to
  the larger of the two periods (for new and gone quiet, the only non-zero one).
- Rising and falling are ranked by absolute change, then by percentage; new by
  current clicks; gone quiet by previous clicks. `change_pct` is null for `new`
  (no previous clicks to divide by) and -100 for `gone_quiet`.
- `unit=content` groups by `ContentKey` using the same loading as
  `top-content`, so a group's clicks, previous clicks and title are identical to
  that report's. `unit=link` rows also carry `key` and `short_url`.
- Needs a previous window: with `compare=none` or `range=all`, `compared` is
  false and every list is empty.
- `highlights` names the top riser ("\"Deep sleep guide\" is up 41% (150 to 212
  clicks)."), the top faller, and the top new and gone-quiet item.

### `idle-links`

Extra parameter `min_age_days` (default 7, 0-3650); `limit` defaults to 50.

```json
"data": {"min_age_days": 7, "total_idle": 9, "links": [{"key": "…", "short_url": "…", "title": "…", "label": "…",
  "destination": "…", "content_key": "…", "campaign_slug": null, "utm_source": "", "utm_medium": "", "placement": "",
  "created_at": "…", "age_days": 41, "last_clicked_at": null}]}
```

- Considers links that are active (`active` and not denied), not expired
  (`expires_at` null or in the future) and created at least `min_age_days` **calendar days** ago in `tz`
  (created on or before today minus `min_age_days`; `0` includes today's links),
  that have **no counted click in the window**. Counted means bots excluded
  unless `include_bots=true`.
- `last_clicked_at` is the newest counted click **all-time**, not just in the
  window; null when the link has never been clicked.
- Sorted oldest first. `total_idle` counts every match; `limit` only trims the
  list.
- **Filters restrict which links are considered, using the link's own fields**:
  `campaign` is the link's current campaign (`none` for unassigned), and
  `source`, `medium`, `utm_campaign`, `content`, `term` and `placement` match
  the link's stored UTM and placement values (OR within a dimension, AND across,
  `~none` for empty). They are not applied to individual clicks: a link idle in
  the window is idle whatever its clicks' UTM values were.
- `highlights`: "9 links have had no clicks in 30 days (from to to)." and the
  oldest idle link.

### `campaign-comparison`

No extra parameters; `limit` is optional (unlimited by default).

```json
"data": {"total_campaigns": 3, "campaigns": [{"slug": "…", "name": "…", "status": "live",
  "starts_on": "2026-10-01", "ends_on": null, "links": 3, "links_clicked": 3, "clicks": 13,
  "previous_clicks": 5, "change_pct": 160, "clicks_per_active_day": 0.4,
  "top_content": {"title": "…", "clicks": 9}, "top_source": {"value": "bluesky", "clicks": 7}}]}
```

- Lists every campaign that has at least one counted click in the window, or
  whose date range overlaps it. A campaign with no dates overlaps nothing, so
  it appears only through clicks. Sorted by clicks, then slug.
- Clicks use the campaign recorded on each click (not the link's current one).
  `links` is the number of links currently in the campaign; `links_clicked` the
  distinct links clicked in the window. `top_content` groups by `ContentKey`
  within the campaign; `top_source` is its biggest non-empty `utm_source`.
- `clicks_per_active_day` is clicks divided by the days of the window the
  campaign's date range covers (a campaign with no dates covers the whole
  window); null when it covers none.
- `campaign` limits the report to one campaign (`none` yields nothing); the UTM,
  placement and bot filters apply to the counted clicks.
- `status` comes from `reports.CampaignStatus` (below). `highlights` names the
  top campaign and its best content.

#### Campaign status

`CampaignStatus(startsAt, endsAt *time.Time, archived bool, now time.Time, loc *time.Location) string`
is pure and exported for reuse (e.g. the campaigns list). `starts_at` and
`ends_at` are date-only values stored as UTC midnight, so their date is read
in UTC; "today" is `now` in `loc` (the request's `tz`), and both bounds are
inclusive.

| Status | Rule |
|---|---|
| `archived` | The archived flag is set; it wins over the dates. |
| `scheduled` | `starts_at` is set and is after today. |
| `ended` | `ends_at` is set and is before today. |
| `live` | Everything else, **including a campaign with no dates**. |

### `campaign-performance`

One campaign in a call. Extra parameters: `slug` (required; `missing_slug` 400
when absent, `404 campaign_not_found` when unknown or not the caller's) and
`columns=source|medium|placement` (default `source`). `limit` caps matrix rows
(default 50).

```json
"data": {"campaign": {"slug": "guides-2026-11", "name": "Guides 2026-11", "status": "live", "starts_on": "2026-11-01", "ends_on": "2026-11-30"},
  "totals": {"clicks": 902, "previous_clicks": null, "per_active_day": 50.1, "links": 42, "links_clicked": 24},
  "matrix": {"by": "source", "columns": ["mastodon", "bluesky"], "total_rows": 12,
    "rows": [{"content_key": "…", "title": "…", "total": 179,
      "cells": {"mastodon": {"clicks": 118, "key": "HaQRwV"}, "bluesky": {"clicks": 61, "keys": ["riCY47", "Xk29aB"]}}}],
    "column_totals": {"mastodon": 498, "bluesky": 261}},
  "timeseries": [{"date": "2026-11-14", "clicks": 64}], "previous_timeseries": null,
  "placements": [{"placement": "Mission St board", "clicks": 14}, {"placement": "(none)", "is_none": true, "clicks": 3}],
  "removed_links_clicks": 7}
```

- **Window.** When the campaign has both dates and the caller sent no `range`,
  `from` or `to`, the window is `starts_on` to `ends_on` (inclusive civil days
  in `tz`, end clamped to today). `ends_on` is an inclusive last day and today
  so far is included. `GET /api/campaigns/{slug}/stats` (`campaignWindow` in
  package `clicks`) currently excludes the last day and today, so its totals can
  be lower than this report's; the gap is tracked in #0119. Otherwise (explicit range, or a campaign missing a
  date) the standard window applies. Over the campaign's own dates there is no
  previous window unless `compare=previous` is sent explicitly (the days before
  a campaign began are rarely a useful baseline); then it is the same-length
  window before it. `totals.previous_clicks` and `previous_timeseries` are null
  without a previous window; `previous_timeseries[i]` lines up with
  `timeseries[i]`.
- **Scope.** Clicks are the ones recorded under the campaign
  (`clicks.campaign_id`), so history survives a link moving. The common
  `campaign` filter is ignored; the UTM, placement and bot filters apply.
- **Matrix.** Rows are content groups (`ContentKey`, as `top-content`) by total
  clicks; cells are the group's clicks per column value (`~none` for an empty
  value). A cell has `key` when exactly one link had clicks there and `keys`
  (most clicks first) when several did. A pairing with no clicks is **absent**
  from `cells`, never zero. More than six columns keep the top five (by
  clicks) and add `other`, summing the rest per row. `column_totals` covers all
  rows, even those `limit` cut (`total_rows` is the uncut count). Each row's
  `total` sums its cells and all rows sum to `totals.clicks`. Matrix column
  names are normalized (lower-cased and trimmed for `columns=placement`),
  whereas `placements` shows the most common spelling. `filters.campaign` in the
  envelope is the `slug`.
- `removed_links_clicks` counts clicks attributed to the campaign whose link is
  no longer in it; they are included in `totals`, the matrix and the series.
  `links` is current membership; `links_clicked` the member links clicked.
  `placements` are normalized as in the campaign stats (case and space folded,
  most common spelling shown).

### `link-performance`

One link. Extra parameter: `key` (required; `missing_key` 400, `404
link_not_found` when unknown or not the caller's). `limit` caps each breakdown
(default 10).

```json
"data": {"link": {"key": "HaQRwV", "short_url": "…", "title": "…", "label": "Mastodon guide post 84", "content_key": "…",
    "destination_url": "…", "campaign_slug": "guides-2026-11", "active": true},
  "totals": {"clicks": 90, "previous_clicks": 74, "all_time": 141, "last_clicked_at": "2026-11-18T18:42:00Z"},
  "timeseries": [{"date": "2026-11-14", "clicks": 6}], "previous_timeseries": [],
  "by_source": [], "by_medium": [], "by_content": [], "by_referer": [],
  "siblings": [{"key": "riCY47", "label": "Bluesky guide post 83", "campaign_slug": "guides-2026-12", "active": true, "clicks": 52}]}
```

- `link.label` is the tail `LinkLabel` cuts off the title (the key when there
  is none). `all_time` and `last_clicked_at` ignore the window and the UTM
  filters (bots still follow `include_bots`); `last_clicked_at` is null for a
  link never clicked.
- `by_*` rows are `breakdown` rows (`value`, `clicks`, `share`,
  `previous_clicks`) for this link, empty values as `~none`, referrers as host
  or `(direct)`. As in `breakdown`, referrer detail is cleared after 90 days:
  `by_referer[].previous_clicks` is null when the previous window reaches past
  that, and a highlight says so.
- `siblings` are the caller's *other* links with the same `ContentKey`, in any
  campaign, with their clicks in the window (zero included), most first. Their
  click counts follow the caller's filters, including `campaign=`, and the list
  is capped like `loadLinks`, with `truncated` set when the cap is hit.
- The existing `GET /api/links/{key}` and `GET /api/campaigns/{slug}/stats`
  are unchanged; these reports are additive.

### Adding a report

Register a `reports.Report` in `NewDefaultRegistry`:

- `Name`, `Description`, `Params []ParamSpec` (the report's *extra* parameters;
  `Required: true` marks one the caller must send) and `DefaultLimit` (what
  `p.LimitOr` falls back to, shown in the catalogue as `default_limit`; the
  common `limit` parameter is not repeated in `Params`).
- `Run(ctx, *Request) (Output{Highlights, Data}, error)`. `Request` holds the
  parsed `Params`, the resolved `Windows`, `Raw` (the full query), `Now`, `UserID`
  and `Q`, the shared REPEATABLE READ transaction. `Service.Run` adds the
  envelope. Extra parameters are read with `req.IntParam(name, def, min, max)`
  (`invalid_<name>`), `req.EnumParam(name, def, allowed...)` (`invalid_<name>`)
  and `req.RequiredString(name)` (`missing_<name>`); each returns a
  `*ParamError`, which the handler turns into a 400. A report about a thing the
  caller may not have returns `&NotFoundError{Code, Message}`, which the handler
  turns into `404 {"error": Code}`; it may be returned from `Run` or `Windows`.
- `Windows` (optional) overrides the standard windows, which `Service.Run`
  passes in as `req.Windows`. Return the windows the report should use and the
  envelope reports them. `req.Params.RangeExplicit` is true when the caller
  sent `range`, `from` or `to`, so a campaign report can default to the
  campaign's own dates only when it is false.

Build queries with `p.ScopedQuery(userID, window, ScopeOpts{Joins, Skip, Where})`.
It returns `Scope{SQL, Args, Next}`: ` FROM clicks k JOIN links l ... WHERE
l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3 AND (<filters>)`,
the arguments for `$1..$4` (user, window start, window end, time zone) plus the
filters', and the next free placeholder number. Append `GROUP BY` etc. to `SQL`
and your own arguments after `Args`, numbered from `Next`. Use `DayExpr` to
bucket by day, `Skip` (`SkipCampaign`, `SkipBots`, a dimension name) to leave a
filter out, and `DimensionExpr(dim)` to group by a dimension. `p.Predicate` is
the lower-level piece underneath.

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
| `DetailRetention` | `internal/clicks/retention.go` | 90 days | Age after which a click's `ip_hash` and `user_agent` are set to NULL (#0121) |
| `sweepInterval` | `internal/clicks/retention.go` | 1h | How often the retention sweep runs (also once at startup) |
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
| `migrations/000015_clicks_ip_hash.{up,down}.sql` | Replaces `ip_address INET` with `ip_hash BYTEA` (#0121); existing rows become NULL |
| `internal/reports/` | User-wide reports: parameter model, windows, filter predicate, registry, `overview` (#0139) |
| `internal/clicks/iphash.go` | `IPHasher`: keyed daily-salted IP hash (#0121) |
| `internal/clicks/retention.go` | `Sweeper`, `DetailRetention`: strips `ip_hash`/`user_agent` after 90 days (#0121) |
| `internal/clicks/recorder.go` | `Click` type, `Recorder`, `RecordClick` (fire-and-forget), `Record` (the #0100 UTM fallback + campaign_id resolution + #0101 bot classification) |
| `internal/clicks/botdetect.go` | `IsBot`, `BotUserAgentSubstrings` (#0101) |
| `internal/clicks/stats.go` | `StatsStore`; per-link `UTMStatsForLink`/`ClicksOverTime`; campaign `CampaignStats`/`CampaignSummary`/`CampaignRollup`/`CampaignClicksOverTime`/`CampaignClicksByLink`/`CampaignSeriesByLink` (#0102); `Bucket`, `UTMStats`, `DayBucket`, `TimeseriesResult`, `LinkBucket`, `PlacementBucket`, `LinkSeries` |
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
| `web/src/lib/types.ts` | TypeScript types: `DayBucket`, `TimeseriesResult`, `UTMBucket`, `LinkDetail`, `CampaignStats`, `LinkBucket`, `PlacementBucket`, `LinkSeries` |
