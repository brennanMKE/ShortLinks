# UTM / Campaign Parameters

This document covers the full UTM workflow in ShortLinks: authoring parameters
with the create-form builder, how they survive the redirect, and how they surface
in per-link analytics.

---

## Overview

UTM parameters (`utm_source`, `utm_medium`, `utm_campaign`, `utm_term`,
`utm_content`) travel through the system in three stages:

1. **Authoring** — the create form's UTM builder composes the five fields onto
   the destination URL before the link is saved.
2. **Passthrough** — when a visitor follows the short link, any UTM parameters
   on the *short URL* are merged onto the destination URL and the values are
   captured with the click record.
3. **Analytics** — the captured values are aggregated into per-dimension
   breakdowns (`utm_source`, `utm_medium`, `utm_campaign`) visible in the link
   detail view.

---

## Stage 1 — Authoring (the UTM builder)

### Where it lives

`web/src/views/Dashboard.svelte` — the "Campaign / UTM parameters" collapsible
section on the link-create form (implemented in #0048).

### The five fields

| Field label | Parameter key | Notes |
|---|---|---|
| Source | `utm_source` | Required for meaningful attribution (e.g. `email`, `twitter`) |
| Medium | `utm_medium` | Channel type (e.g. `newsletter`, `cpc`, `social`) |
| Campaign | `utm_campaign` | Campaign name (e.g. `launch-2026`) |
| Term | `utm_term` | Keyword for paid search; optional for other channels |
| Content | `utm_content` | Distinguishes creatives/variants; optional |

All five fields are optional. Leaving a field blank omits that parameter
entirely — no stray empty query params are produced.

### Composition logic (`web/src/lib/utm.ts`)

All composition is handled by pure, DOM-free functions so they are unit-testable
without a browser. The public surface is:

| Export | Purpose |
|---|---|
| `UTM_KEYS` | Ordered tuple of the five parameter keys |
| `emptyUtmParams()` | Returns a fresh `UtmParams` object with all fields set to `''` |
| `isUtmEmpty(params)` | `true` when every field is blank or whitespace-only |
| `composeUtmUrl(base, params)` | Returns the composed destination URL |

#### `composeUtmUrl` rules (exact behaviour from the source)

- If `base` is empty or whitespace-only, returns `''` — nothing to compose.
- If all UTM fields are empty/whitespace, returns `base` unchanged — no `?` is
  appended to a clean URL.
- Otherwise, non-empty field values are trimmed of surrounding whitespace and
  merged onto `base`:
  - **Happy path (valid absolute URL):** uses `new URL(base)` and
    `URLSearchParams.set()`, which URL-encodes values correctly (spaces, `&`,
    `=`, `#`, non-ASCII, etc.) and replaces any existing same-named key without
    duplicating it.
  - **Fallback (relative or not-yet-valid URL):** `new URL()` throws; the
    function falls back to `appendQueryFallback`, which splits on `?`, parses
    the existing query into a `Map`, merges UTM entries (replacing duplicates),
    then reassembles via `URLSearchParams` for correct encoding. The fallback
    keeps the live preview useful while the user is still typing a URL.

Key invariant: an existing `utm_source` in the base URL is **replaced** by the
builder's value; an existing non-UTM parameter (e.g. `ref=homepage`) is always
preserved.

The 25 unit tests in `web/src/lib/utm.test.ts` cover: empty/whitespace no-ops,
single-param and all-five append, empty-field dropping, existing-query merge,
duplicate-key replacement, special-character/unicode encoding, whitespace
trimming, relative-URL fallback, partially-typed URL fallback, and
fallback duplicate-key replacement.

### Live preview

`Dashboard.svelte` wires a Svelte 5 `$derived` reactive value:

```
const composedUrl = $derived(composeUtmUrl(destinationUrl, utmParams));
```

The composed URL is rendered in real time under the UTM fields as a "Destination
preview", so the author can verify the final URL before saving.

### Storage decision — "bake into `destination_url`" AND store discretely (#0099)

At submit time `buildInput()` passes `composedUrl || destinationUrl.trim()` as
`destination_url` to the API — **this part is unchanged**. The destination
site's own analytics depends on those params arriving, so the composed URL is
still what gets stored and served.

As of [#0099](../issues/0099.md), the request ALSO carries the five UTM
values (plus an optional `placement` label) as discrete fields, and the
backend stores them in five new nullable `links` columns
(`utm_source`, `utm_medium`, `utm_campaign`, `utm_term`, `utm_content`) plus
`placement`, alongside the unchanged `destination_url`. The two are expected
to agree — whatever `composeUtmUrl` bakes into the URL is exactly what is
sent as the discrete fields — and an integration test asserts this by parsing
the stored URL and comparing it field-by-field against the stored columns.

`placement` is deliberately **not** `utm_content`: a physical poster location
("18th & Texas board") is operational metadata, not something forwarded to
the destination site's analytics. They frequently differ.

Existing rows created before this migration keep `NULL` in all seven new
columns — they are **not** backfilled by parsing `destination_url`, since the
parse is ambiguous wherever a link already carried its own `?utm_source=`
from an external source.

**Edit re-population (closes the limitation this section used to document):**
`GET /api/links/{key}` now returns the five UTM fields, `placement`, and
`campaign_id`/`campaign_name`/`campaign_slug` (when assigned) on every link,
so the edit form CAN pre-populate the UTM builder — see
`utmParamsFromLink` in `web/src/lib/utm.ts` and its use in
`web/src/views/LinkDetail.svelte`'s "Edit" action. A link with all-NULL
columns (created before #0099, or one that simply never had UTM params) maps
to `emptyUtmParams()` — the edit form opens with empty fields rather than
erroring, exactly as it would for a link that legitimately has none. Saving
an edit PATCHes `destination_url` (the builder's re-composed URL) together
with the five discrete fields and `placement`.

**Clearing a field on edit deletes it from the composed URL, not just the
column.** `composeUtmUrl`'s edit-path input is the link's ALREADY-baked
`destination_url` (e.g. `?utm_source=email&utm_medium=newsletter&...`), so
"blank" and "absent" are not the same thing there the way they are on a fresh
create. If the author clears `utm_source` in the builder and
`composeUtmUrl` merely skipped re-setting it (the original #0048
behavior — never delete, only add/replace), the recomposed URL would still
ship the stale `utm_source=email` while the PATCH sends `utm_source: ""`,
which the store writes as `NULL`. The column and the baked URL would then
permanently disagree: exactly the "campaign view labels the link by the
wrong channel" failure this issue exists to remove. `composeUtmUrl` therefore
RECONCILES all five keys against `params` on every call — set when non-blank,
**delete when blank** — for both the URL-object path and the
relative/invalid-URL fallback. This is one function for both create and
edit; there is no separate "recompose" variant. See the
"clearing a previously-baked key" tests in `utm.test.ts`.

### Campaign membership (#0098, #0099)

A link may optionally belong to one campaign (`links.campaign_id`, nullable,
`ON DELETE SET NULL` — deleting a campaign never deletes its links, only
unassigns them). Selecting a campaign in the create form's new "Assign to
campaign" dropdown **prefills** the five UTM builder fields from the
campaign's `default_utm_*` values (`utmParamsFromCampaignDefaults` in
`web/src/lib/utm.ts`) — every prefilled value stays exactly as editable as if
the author had typed it themselves; the campaign supplies a starting point,
never a lock. Membership itself is set via `POST /api/links`'s optional
`campaign_id`/`campaign_slug` fields, or afterward via the dedicated
`POST`/`DELETE /api/campaigns/{slug}/links...` endpoints — it is NOT
patchable through `PATCH /api/links/{key}`, which only ever touches
title/destination/UTM/placement/expiry. Assigning a link already in another
campaign **moves** it (a link belongs to at most one campaign, enforced by
the column). Assigning multiple links in one `POST` is capped at 50 keys per
request and is **not atomic across keys** — each is resolved and assigned
independently in the order given, so a key that fails ownership stops the
request without rolling back keys already assigned earlier in the same call.
See [#0099](../issues/0099.md) for the full endpoint list.

**Duplicate/reactivate creates forward-merge, never clear.** When a `POST
/api/links` on the generated-key path matches an existing link
(`duplicate: true`), any campaign_id/UTM/placement THIS request supplies is
written onto that existing row rather than discarded — otherwise picking a
campaign on the create form and happening to hit a URL you already
shortened would silently not apply it. This is a **forward merge, not an
overwrite**: a field the request leaves blank is never cleared, so a bare
`POST {"destination_url": "..."}` re-submission cannot wipe out a campaign
assignment or UTM values an earlier create already set on that row. See
`internal/links/store.go`'s `applyRequestedMetadataTx`.

This did **not**, by itself, solve #0105's batch-creation problem. Two batch
rows differing only in `placement` still compose to the identical
`destination_url` (`placement` is never baked into the URL), so the dedup
lookup still matched them to the same row — row 2 never became a second
link. Forward-merge only changed what happened to that one shared row:
before it, row 2's placement was silently ignored; after it, row 2's
placement silently overwrote row 1's.

**#0105 fixes this by bypassing dedup entirely for batch creation**, rather
than extending the dedup key or baking something new into the URL.
`POST /api/campaigns/{slug}/links/batch` creates every non-blank row through
`links.Store.CreateLinksBatch`, a separate method that never calls
`CreateOrReactivateLink` or looks up an existing row by `destination_url` at
all — every row unconditionally inserts, even when its `destination_url` is
byte-identical to another row's. Two rows differing only in `placement`
therefore become two links, each with its own generated key. The whole
batch is one transaction (all rows commit, or none do), and a separate,
narrower client- and server-side check rejects an EXACT duplicate row (same
source + medium + content + **and** placement) as a likely accidental
double-entry — deliberately not the same check as dedup, and deliberately
including placement so it cannot reject the placement-only case the fix
exists to allow. See `links.Store.CreateLinksBatch`'s doc comment
(`internal/links/store.go`) for the full decision and
`internal/handlers/campaigns.go`'s `BatchCreateLinks` for the request-level
validation (row cap, blank-row skip, URL-filter check, duplicate-row
rejection), all of which run BEFORE any row is inserted.

**Bypassing dedup also means a double-submit is not deduplicated — a
contract change from single create.** Submitting the identical batch twice
(e.g. a lost response followed by a retry) creates 2N links, not N, where a
repeat `POST /api/links` for the same URL reliably folds onto the existing
row via `CreateOrReactivateLink`. The batch UI mitigates the common paths
(the submit button disables while the request is in flight, and the form
resets on success), leaving only the genuine lost-response-then-retry
window — an accepted trade, not an oversight, but worth stating since
[#0106](../issues/0106.md) builds QR sheets on these links and a doubled
batch means doubled sheets.

**`default_utm_source`/`_medium`/`_content` prefill ROW 1 of the batch form
only, not every row.** Like the single-create form, the batch form prefills
per-channel fields from the campaign's own `default_utm_*` values — but
where single-create has one set of UTM fields to prefill, the batch form has
N rows, and prefilling the same source/medium/content into all of them would
make rows 2..N byte-identical to row 1 (an immediate, spurious "duplicate
row" error under the batch's own duplicate-row check, before the user has
typed anything) and would make every untouched row non-blank (defeating
blank-row skipping — an unedited form would submit N identical links instead
of zero). `lib/campaigns.ts`'s `initialBatchRows` therefore prefills only
the first row; every row added afterward via "+ Add row" starts genuinely
blank, exactly as before this fix. `utm_campaign`/`utm_term` remain the
exception described above: they are shared across the whole batch (one
field each, not per-row), so prefilling them once is unambiguous and does
not have this collision.

**A `NULL` `default_utm_campaign` prefills empty, never the campaign's
slug.** The column can be cleared via `PATCH /api/campaigns/{slug}` and is
never re-derived afterward (`campaigns.CampaignUpdate`'s convention). Once a
user has deliberately cleared it, silently resurrecting a value from the
slug would undo that — and the slug is immutable while the campaign's name
is not, so it can already have drifted from what the campaign is now called.
`utmParamsFromCampaignDefaults` does not even accept a slug as input, so
there is no fallback path to reach for. See "a NULL default_utm_campaign
... does NOT fall back to the campaign slug" in `utm.test.ts`.

**Print/flier rows** have a suggested (not enforced) UTM convention —
`utm_medium=print`, `utm_source=flyer`, with `utm_content` or `placement`
naming the physical location — documented in full in `docs/campaigns.md`'s
"Print-row convention" section, alongside the rest of the batch-create and
campaign data model.

---

## Stage 2 — Redirect passthrough

### Handler: `internal/handlers/redirect.go`

`RedirectHandler.ServeHTTP` follows a seven-step flow. Steps 5–7 are relevant
to UTM:

**Step 5 — capture click metadata (including UTM)**

Before the response is written, `buildClickInfo` snapshots the inbound request's
query string and reads the five UTM keys via `url.Values.Get`:

```go
UTMSource:   q.Get("utm_source"),
UTMMedium:   q.Get("utm_medium"),
UTMCampaign: q.Get("utm_campaign"),
UTMTerm:     q.Get("utm_term"),
UTMContent:  q.Get("utm_content"),
```

This `ClickInfo` is dispatched to `RecordClick` in a goroutine so it never
blocks the redirect.

**Step 6 — merge inbound UTM onto the destination**

```go
location := mergeUTM(link.DestinationURL, r.URL.Query())
```

`mergeUTM` parses `destination` with `url.Parse`, then calls `q.Set(k, v)` for
each of the five UTM keys that appear (non-empty) in the inbound query. Only the
five known UTM keys are forwarded — any other inbound query parameters are
ignored. If the destination cannot be parsed, it is returned unchanged rather
than returning a 500.

**Step 7 — 302 redirect**

The merged location is written as the `Location` header and the handler returns
`302 Found`.

### Precedence when both stored URL and inbound URL carry UTM params

If the stored `destination_url` already contains `utm_source=email` (baked in
at create time) and the short link is followed with `?utm_source=twitter` on
the short URL, the inbound value **wins** — `mergeUTM` calls `q.Set` which
overwrites. This lets a single short link be reused across campaigns by
overriding parameters at click time.

This is the same "inbound wins" rule the #0100 click-recording fallback below
uses, but the two are separate mechanisms operating on separate data: this
section is about what URL the *visitor's browser* is redirected to
(`mergeUTM` over the composed `destination_url`); the next section is about
what gets written to the `clicks` table (`Record`'s fallback over the link's
discrete `utm_*` columns). #0099 keeps the composed URL and the discrete
columns in agreement, so in practice the two rarely disagree — but they are
computed independently, by different code, against different inputs.

### What values are recorded (#0100 fallback precedence)

**The following precedence statement is worded identically in this document
and in `docs/analytics.md`'s "UTM fallback precedence" section** — do not
let the two drift; if one changes, change both.

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
resolves `link_id` and `campaign_id` ([#0100](../issues/0100.md)). See
`docs/analytics.md`'s "UTM fallback precedence" section for the
`campaign_id` denormalization that shipped alongside this.

**Before #0100**, a link author who baked `utm_source=email` into the
destination URL but shared the short link without appending UTM params to the
*short* URL saw `(none)` in the source breakdown for every one of that link's
clicks — attribution required UTM params on the short URL itself at click
time, and the link's own stored value was never consulted. **As of #0100**,
that same bare short URL now records `utm_source = "email"`, because the
recorder falls back to the link's stored value when the inbound query omits
it.

**This is a behavior change to existing analytics, and it is NOT
retroactive.** Historical click rows recorded before the #0100 migration keep
whatever `(none)`/inbound-only values they already had — they are not
rewritten with fallback values. A per-link UTM breakdown or clicks-over-time
chart whose date range spans the deploy date will show a discontinuity right
at the boundary (a jump from mostly-`(none)` to mostly-attributed for a link
that was always shared the same way). That jump is an artifact of when the
fallback started applying, not a real change in traffic or campaign
performance — do not read it as a trend.

---

## Stage 3 — Analytics

### Backend: `internal/clicks/stats.go`

`UTMStatsForLink` returns a `UTMStats` struct:

```go
type UTMStats struct {
    ClickCount int64    `json:"click_count"`
    BySource   []Bucket `json:"by_source"`
    ByMedium   []Bucket `json:"by_medium"`
    ByCampaign []Bucket `json:"by_campaign"`
}
```

Each `Bucket` is `{ value string, count int64 }`. The `breakdown` helper groups
clicks by one column, applying `COALESCE(NULLIF(dimension, ''), $2)` so both
`NULL` and the empty string fold into the `"(none)"` sentinel (`NoneBucket`).
Results are ordered by count descending with value ascending as a tiebreaker,
and capped at `breakdownLimit = 20` distinct values.

`utm_term` and `utm_content` are captured per click but are **not** surfaced in
the aggregated `UTMStats` response — only `utm_source`, `utm_medium`, and
`utm_campaign` are broken down. `utm_term` and `utm_content` are stored in the
`clicks` table and available for future queries.

### Frontend: `web/src/views/LinkDetail.svelte`

The link detail view renders a "UTM breakdown" panel when the link has at least
one click (`isEmptyStats` returns `false`). It calls `utmDimensions` from
`web/src/lib/linkDetail.ts`, which prepares three labelled, pre-sorted
`UTMDimension` objects:

| Dimension key | Label |
|---|---|
| `source` | Source |
| `medium` | Medium |
| `campaign` | Campaign |

Each dimension is rendered by `UTMBarChart.svelte` as a horizontal bar chart.
Buckets whose value matches the `"(none)"` sentinel (`NONE_BUCKET` constant in
`linkDetail.ts`) are rendered with muted styling to distinguish "no UTM value
recorded" rows from real campaign values.

### Cross-reference

For click timeseries (clicks over time per link), see `ClicksOverTime` in
`internal/clicks/stats.go` and the corresponding chart in `LinkDetail.svelte`.
If a cross-link UTM analytics view is added (issue #0069), this section will
link to `docs/analytics.md`.

---

## Practical guidance for consistent campaign tagging

### Canonical casing

`utm_source` and `utm_medium` values are stored and grouped exactly as
provided — `Email` and `email` appear as two distinct buckets. Pick a
convention (lowercase recommended) and apply it across all links.

### Bake vs. override

- **Bake at create time** when a short link is dedicated to one campaign and
  will always carry the same attribution. The UTM builder is designed for this:
  the composed URL is stored so the redirect works with no UTM params on the
  short URL.
- **Override at share time** when a single short link is reused across campaigns.
  Append the UTM params to the short URL when distributing it; these inbound
  values override whatever was baked into the destination.

Both approaches can coexist: if the stored destination has `utm_medium=email`
baked in and the short link is shared with `?utm_medium=sms` appended, the
click records `utm_medium=sms` and the browser lands on the destination URL
with `utm_medium=sms`.

### When `(none)` appears in analytics

A `"(none)"` bucket means clicks arrived at the short URL with no value (or an
empty value) for that dimension. Common causes:

- The link was shared without UTM params on the short URL and nothing was baked
  into the destination URL.
- The link was accessed directly (bookmark, typed URL, etc.).
- Only some UTM params were baked; the rest recorded as `(none)`.

### Recommended minimum fields

For meaningful source/medium attribution, always populate at least
`utm_source` and `utm_medium`. `utm_campaign` is strongly recommended when
running multiple concurrent campaigns. `utm_term` and `utm_content` are
optional and are most useful for paid search and A/B creative testing.

---

## File reference

| Path | Role |
|---|---|
| `web/src/lib/utm.ts` | Pure composition helpers: `composeUtmUrl`, `isUtmEmpty`, `emptyUtmParams`, `UTM_KEYS`, and (#0099) `utmParamsFromLink`, `utmParamsFromCampaignDefaults` |
| `web/src/lib/utm.test.ts` | Unit tests for the composition + (#0099) repopulation/prefill helpers |
| `web/src/views/Dashboard.svelte` | Create-form UTM builder UI, live preview, bake-on-submit wiring, and (#0099) the campaign-selection dropdown + placement field |
| `web/src/views/LinkDetail.svelte` | UTM breakdown panel/bar charts, and (#0099) the "Edit" action that repopulates the UTM builder from the stored columns |
| `web/src/lib/linkDetail.ts` | `utmDimensions`, `sortBuckets`, `isEmptyStats`, `NONE_BUCKET` |
| `internal/handlers/redirect.go` | `mergeUTM`, `buildClickInfo`, `RedirectHandler.ServeHTTP` |
| `internal/clicks/recorder.go` | (#0100) `Record`'s per-key UTM fallback (`COALESCE(NULLIF($n, ''), l.utm_source)`) and `campaign_id` denormalization, both inside the single click INSERT |
| `internal/clicks/stats.go` | `UTMStatsForLink`, `breakdown`, `UTMStats`, `Bucket`, `NoneBucket` |
| `internal/links/store.go` | (#0099) `Link`/`NewLink`/`LinkUpdate`'s discrete `campaign_id`/`utm_*`/`placement` fields, `GetLink`'s campaign LEFT JOIN, `ListLinksForCampaign` |
| `internal/campaigns/store.go` | (#0099) `AssignLinkToCampaign`, `UnassignLinkFromCampaign`, `GetCampaignByID` |
| `internal/handlers/campaigns.go` | (#0099) `GET`/`POST /api/campaigns/{slug}/links`, `DELETE /api/campaigns/{slug}/links/{key}` |
| `migrations/000011_links_campaign_and_utm.{up,down}.sql` | The seven new `links` columns and `idx_links_campaign_id` |
| `migrations/000012_clicks_campaign_and_bot.{up,down}.sql` | (#0100) `clicks.campaign_id`/`is_bot` and `idx_clicks_campaign_id`/`idx_clicks_campaign_time` |
