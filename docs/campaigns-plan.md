# Campaigns Feature Plan

Add campaign grouping to ShortLinks so multiple short links that promote the same
thing can be created together, listed together, and reported on together.

This document is a plan, not a spec. Read the existing code before implementing.
Relevant existing docs: `docs/utm.md`, `docs/analytics.md`, `docs/database.md`,
`docs/links.md`, `docs/frontend.md`. Issue #0069 (cross-link UTM analytics view)
is superseded or absorbed by this work.

## Goal

Brennan runs a promotion across several channels at once: posts on a few social
networks, an email, and printed fliers on neighborhood poster boards. Each
placement gets its own short link. He wants one view that answers:

1. How many clicks did this campaign get in total, and over time?
2. Which channel or placement drove them?
3. What links belong to this campaign, and what does each one point at?

## Critical background: the current attribution gap

Read this before designing anything. It changes the shape of the solution.

Per `docs/utm.md` and `docs/analytics.md`:

- The UTM builder in the create form **bakes** the five UTM params into
  `destination_url` at save time. There are no discrete UTM columns on `links`.
- The redirect handler records UTM values on the `clicks` row from the **inbound
  short URL query string only**, not from the stored destination.
- Therefore a link created with `utm_campaign=summer-fair` baked in, then shared
  as a bare `https://go.sstools.co/u/ab12de`, records `utm_campaign = NULL` on
  every click. It shows as `(none)` in the breakdown.

The practical consequence: **grouping campaign metrics by `clicks.utm_campaign`
will not work** for the normal workflow. It only works if UTM params are appended
to the short URL at share time, which defeats the point of a short link and is
unworkable on a printed flier.

The plan below therefore treats campaign membership as a **first-class database
relationship**, not a string match on UTM values. UTM values stay useful as
labels and as a way to interoperate with Google Analytics on the destination
site, but they are not the join key.

## Design decisions

### 1. Campaign is an owned entity with an explicit foreign key

New `campaigns` table. `links.campaign_id` is a nullable FK. Membership is
explicit and exact. Renaming a campaign does not orphan its metrics. Casing
inconsistency in `utm_campaign` (documented as a known hazard in `docs/utm.md`)
cannot fragment a campaign's numbers.

### 2. Denormalize `campaign_id` onto the `clicks` row

At click-record time, resolve the link's `campaign_id` and store it on the click.

Why not join `clicks -> links -> campaigns` at query time? Two reasons:

- A link can be reassigned to a different campaign, or unassigned. Historical
  clicks should stay attributed to the campaign that was running when they
  happened. A join would silently rewrite history.
- `docs/database.md` notes `clicks.link_id` is nullable and the FK does not
  cascade, so click history outlives deleted links. Campaign rollups should
  survive the same way.

Resolve the campaign inside the existing INSERT (the same statement already
resolves `link_id` from `key`), so the redirect path does not gain an extra
round trip. Keep the recorder fire-and-forget and inside `recordTimeout`.

### 3. Store discrete UTM columns on `links`

Add `utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content` to
`links` alongside the existing baked `destination_url`. Populate them from the
create form's builder at the same time the composed URL is built.

This does three things:

- Lets the campaign view label each link by channel without re-parsing the
  destination URL, which `docs/utm.md` correctly calls lossy.
- Fixes the documented edit-form limitation: the builder can now repopulate.
- Enables the fallback described next.

Keep baking into `destination_url` as well. The destination site's own analytics
depends on it.

### 4. Fall back to the link's stored UTM values when recording a click

When an inbound UTM key is absent from the short URL query, fall back to the
link's stored discrete value for that key before writing the click row. Inbound
values still win, preserving the documented override behavior.

This makes source and medium breakdowns meaningful for the normal case where the
short link is shared bare. Without it, every breakdown in the campaign view is a
single `(none)` bar.

Note this is a behavior change to existing analytics. Historical clicks are not
backfilled. Call it out in `docs/analytics.md` and `docs/utm.md`.

### 5. Bot and preview-crawler filtering

This one matters more than it looks. Comparing social channels against printed
fliers is meaningless if unfurl bots are counted. Posting a short link in Slack,
iMessage, Twitter/X, Discord, or LinkedIn triggers automated fetches of the
redirect, sometimes several per post. A flier gets none of that. Social will look
inflated by a constant that has nothing to do with human interest.

Minimum viable handling:

- Add `is_bot BOOLEAN NOT NULL DEFAULT FALSE` to `clicks`, classified at record
  time from the user agent against a maintained substring list (bot, crawler,
  spider, preview, facebookexternalhit, Slackbot, Twitterbot, Discordbot,
  WhatsApp, TelegramBot, LinkedInBot, headless, curl, wget, python-requests).
- Exclude `is_bot = TRUE` from campaign and link stats by default.
- Surface the excluded count in the campaign view so the number is visible rather
  than silently dropped.

Do not attempt IP-based bot detection or unique-visitor deduplication in this
phase. See "Explicitly out of scope".

## Data model

Next migration number is `000010` (highest existing is `000009`). Follow the
`NNNNNN_description.{up,down}.sql` convention in `migrations/`, with a down
migration that exactly reverses each up.

### `000010_create_campaigns`

```
campaigns
  id            BIGSERIAL PRIMARY KEY
  user_id       BIGINT NOT NULL REFERENCES users(id)
  name          TEXT NOT NULL
  slug          TEXT NOT NULL          -- URL-safe, used in API paths
  description   TEXT
  starts_at     TIMESTAMPTZ            -- nullable; drives default chart window
  ends_at       TIMESTAMPTZ            -- nullable
  archived      BOOLEAN NOT NULL DEFAULT FALSE
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()

  UNIQUE (user_id, slug)
INDEX idx_campaigns_user_id ON (user_id)
```

Scope campaigns per user, matching how `links` is owned. Slug uniqueness is per
user, not global.

### `000011_links_campaign_and_utm`

```
ALTER TABLE links
  ADD COLUMN campaign_id  BIGINT REFERENCES campaigns(id) ON DELETE SET NULL,
  ADD COLUMN utm_source   TEXT,
  ADD COLUMN utm_medium   TEXT,
  ADD COLUMN utm_campaign TEXT,
  ADD COLUMN utm_term     TEXT,
  ADD COLUMN utm_content  TEXT,
  ADD COLUMN placement    TEXT;   -- free-text label, e.g. "18th & Texas board"

CREATE INDEX idx_links_campaign_id ON links(campaign_id) WHERE campaign_id IS NOT NULL;
```

`ON DELETE SET NULL` so deleting a campaign never deletes links.

`placement` is deliberately separate from `utm_content`. A physical poster
location is operational metadata that Brennan cares about; `utm_content` is what
gets sent to the destination site's analytics. They often differ.

### `000012_clicks_campaign_and_bot`

```
ALTER TABLE clicks
  ADD COLUMN campaign_id BIGINT REFERENCES campaigns(id) ON DELETE SET NULL,
  ADD COLUMN is_bot      BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_clicks_campaign_id ON clicks(campaign_id) WHERE campaign_id IS NOT NULL;
CREATE INDEX idx_clicks_campaign_time ON clicks(campaign_id, clicked_at) WHERE campaign_id IS NOT NULL;
```

The composite index carries the campaign timeseries query. Verify with `EXPLAIN`
rather than assuming.

Existing rows get `campaign_id = NULL`. That is correct: they predate campaigns.
Do not backfill by guessing from `utm_campaign` strings.

## Backend

### New package `internal/campaigns`

Mirror the structure of `internal/links`:

- `Store` with `Create`, `Update`, `Archive`, `Delete`, `ListForUser`,
  `GetBySlug`, `AssignLink`, `UnassignLink`.
- Slug generation and validation. Lowercase, hyphens, collision-suffixed against
  the per-user unique constraint.
- Ownership checks live here or in the handler, matching whatever `internal/links`
  already does. Follow the existing pattern rather than inventing a new one.

### Extend `internal/clicks/stats.go`

Add campaign-scoped queries. Reuse the existing `breakdown` helper and its
`allowedDimensions` allowlist rather than writing new interpolation logic.

```go
func (s *StatsStore) CampaignStats(ctx, campaignID int64, from, to time.Time) (CampaignStats, error)
func (s *StatsStore) CampaignClicksOverTime(ctx, campaignID int64, from, to time.Time) (TimeseriesResult, error)
func (s *StatsStore) CampaignClicksByLink(ctx, campaignID int64, from, to time.Time) ([]LinkBucket, error)
func (s *StatsStore) CampaignSeriesByLink(ctx, campaignID int64, from, to time.Time) ([]LinkSeries, error)
```

`CampaignStats` returns total clicks, bot-excluded count, and per-dimension
breakdowns for source, medium, content, and referer. Keep the `(none)` sentinel
and the 20-row `breakdownLimit` for consistency with the per-link view.

`CampaignSeriesByLink` powers a stacked or multi-line chart. Cap the number of
series returned (5 to 8) and fold the remainder into an "Other" series, otherwise
a campaign with 30 fliers renders as spaghetti.

Preserve the existing conventions: non-nil empty slices so JSON is `[]` and not
`null`, half-open `[from, to)` window, UTC day bucketing via
`date_trunc('day', clicked_at AT TIME ZONE 'UTC')`, `YYYY-MM-DD` string dates.

Default window: campaign `starts_at` to `ends_at` when both are set, clamped to
today. Otherwise fall back to the existing 30-day default. A campaign chart that
ignores the campaign's own dates is the wrong default.

### API endpoints

Follow existing `/api/` conventions for auth guard, error shape, and the SSE
broker.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/campaigns` | List campaigns for the user, with link count and total clicks |
| `POST` | `/api/campaigns` | Create |
| `GET` | `/api/campaigns/{slug}` | Metadata, links, and stats in one payload |
| `PATCH` | `/api/campaigns/{slug}` | Update name, description, dates, archived |
| `DELETE` | `/api/campaigns/{slug}` | Delete campaign, unassign links, keep click history |
| `GET` | `/api/campaigns/{slug}/links` | Links in the campaign |
| `POST` | `/api/campaigns/{slug}/links` | Assign existing links by key |
| `DELETE` | `/api/campaigns/{slug}/links/{key}` | Unassign one link |
| `GET` | `/api/campaigns/{slug}/stats` | Stats with optional `from`, `to` |
| `GET` | `/api/campaigns/{slug}/export.csv` | Per-link rollup as CSV |

Extend `POST /api/links` to accept an optional `campaign_id` or `campaign_slug`
plus the discrete UTM fields and `placement`. Extend the link detail response to
include campaign name and slug.

The single-payload `GET /api/campaigns/{slug}` mirrors how `GET /api/links/{key}`
already returns detail plus `utm_stats` plus `timeseries`. Keep that shape so the
frontend patterns carry over.

Mirror the existing audit log actions: `campaign.created`, `campaign.updated`,
`campaign.deleted`, `campaign.link_assigned`, `campaign.link_unassigned`.

## Frontend

Svelte 5, matching the patterns in `docs/frontend.md`. Reuse the existing pure
helpers in `web/src/lib/charts.ts` and their test discipline. Any new data
shaping goes in a pure, DOM-free module with unit tests, same as `utm.ts` and
`charts.ts`.

### Campaigns list view

Table or card list: name, date range, link count, total clicks, sparkline.
Archived campaigns collapsed or filtered out by default.

### Campaign detail view

Sections, top to bottom:

1. **Header.** Name, description, date range, edit and archive controls.
2. **Summary.** Total clicks in window, clicks per day average, active link
   count, bots excluded count.
3. **Clicks over time.** Extend `ClicksChart.svelte` to accept multiple series,
   or add `CampaignClicksChart.svelte` if the single-series component is cleaner
   left alone. Toggle between combined total and per-link breakdown.
4. **Channel breakdown.** Reuse `UTMBarChart.svelte` for source, medium, and
   content. Keep the muted `(none)` styling.
5. **Links table.** Every link in the campaign: short key, title, destination,
   source, medium, placement, clicks in window, share of campaign total, created
   date. This is the "list all links associated with the campaign" requirement.
   Each row links to the existing link detail view. Sortable by clicks.
6. **Copy and export.** Copy all short URLs, download the per-link CSV.

Keep charts as inline SVG with CSS design tokens, consistent with the existing
components. Do not introduce a charting library for this.

### Multi-link create flow

A campaign is usually a set of links created at once. Add a batch mode on the
campaign detail page: one destination URL, then a row per channel where each row
supplies source, medium, content, and optional placement. Generate one short link
per row, all assigned to the campaign, with the campaign's `utm_campaign` value
prefilled and locked.

This is the feature that makes the whole thing worth using. Creating eight links
one at a time through the existing form and remembering to type
`utm_campaign=summer-fair` identically eight times is exactly the failure mode
`docs/utm.md` warns about under "Canonical casing".

### Print and QR placements

For fliers, each poster location should get its own link so scans are
attributable to a location. Generate a QR code per link in the campaign view,
downloadable as SVG or PNG at print resolution. A small Go QR library on the
backend or a client-side one is fine; pick one and note the choice in the docs.

Suggested convention to document, not enforce in code: `utm_medium=print`,
`utm_source=flyer`, `utm_content` or `placement` set to the location.

## Explicitly out of scope

State these in the docs so expectations are set:

- **Unique visitors.** There is no cookie or visitor hash. Every click is a
  click. One person scanning a poster twice counts twice.
- **Conversions.** ShortLinks sees the redirect, not what happens on the
  destination site. Campaign metrics are reach and interest, not outcomes. If
  conversion matters, the baked UTM params are what carry attribution into the
  destination site's own analytics.
- **Geographic breakdown.** `ip_address` is stored but no geo-IP lookup exists.
  Adding one has privacy implications worth deciding on separately.
- **Cross-user or shared campaigns.**

## Comparing social to print, honestly

Worth a short section in `docs/campaigns.md`, because the numbers will mislead
otherwise:

- Social clicks are inflated by unfurl bots even after user-agent filtering,
  since some crawlers do not identify themselves.
- Print scans have no referer and no bot noise, so they undercount relative to
  social in raw comparison but are cleaner signal per click.
- Impression volume differs by orders of magnitude. A social post seen by 2000
  people and a poster seen by 200 are not comparable on click count alone.
  Click-through per impression is the meaningful comparison and ShortLinks cannot
  measure impressions. Say so plainly in the UI or docs rather than implying the
  bar chart settles the question.

## Suggested phasing

Each phase should end green: builds, tests pass, `./scripts/dev.sh` works.

**Phase 1: data model and membership.** Migrations 10 through 12. `internal/campaigns`
store. Campaign CRUD endpoints. Assign and unassign. Discrete UTM columns on
`links` populated on create. No stats yet. Campaigns list view and a minimal
detail view showing just the links table. This alone delivers the "list all links
in the campaign" requirement.

**Phase 2: recording changes.** Campaign ID and bot flag on the click row. UTM
fallback to the link's stored values. Update `docs/analytics.md` and `docs/utm.md`
to describe the new precedence: inbound query, then link's stored UTM, then
`(none)`.

**Phase 3: campaign stats.** `CampaignStats`, timeseries, per-link breakdown.
Stats endpoints. Charts on the campaign detail view.

**Phase 4: authoring ergonomics.** Batch multi-link create. QR codes. CSV export.

## Testing

Match the existing bar, which is high: 25 unit tests on `utm.ts`, 26 on
`charts.ts`, DB integration tests on `stats.go`.

- Unit tests on slug generation, collision handling, and any new pure frontend
  helpers.
- DB integration tests for the campaign stats queries: empty campaign, single
  link, multiple links, clicks outside the window, clicks on a since-unassigned
  link (must remain attributed), bot exclusion, campaigns with more series than
  the display cap.
- Handler tests for ownership: user A cannot read, assign into, or delete user
  B's campaign, and cannot assign user B's link into their own campaign.
- Redirect handler tests for the new UTM fallback precedence, including the case
  where inbound supplies some keys and the link supplies others.
- Confirm the redirect path did not get slower. Click recording stays
  fire-and-forget within `recordTimeout`.

## Documentation

- New `docs/campaigns.md`, linked from `docs/README.md` under "Data & backend".
- Update `docs/database.md` with the three new tables and columns.
- Update `docs/analytics.md` with the campaign queries and the bot exclusion.
- Update `docs/utm.md` with the discrete columns, the new fallback precedence,
  and removal of the "cannot repopulate the edit form" limitation.
- Update the README feature list.
- Note in `DEPLOYMENT.md` that this deploy requires `migrate up`.

## Open questions for Brennan

1. Should a link be allowed in more than one campaign? The plan assumes no, which
   keeps rollups unambiguous. A join table would allow it at the cost of double
   counting in totals.
2. Should `utm_campaign` on member links be forced to the campaign slug, or just
   defaulted and editable? Forcing keeps destination-side analytics aligned with
   ShortLinks-side grouping. Defaulting is more flexible and more error prone.
3. Is per-user campaign scoping right, or should campaigns eventually be shared
   across users on the instance? Worth deciding now since it affects the unique
   constraint.
