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

### Storage decision — "bake into `destination_url`"

At submit time `buildInput()` passes `composedUrl || destinationUrl.trim()` as
`destination_url` to the API. The backend stores only the single composed URL
string — no separate UTM columns exist in the database or API.

**Consequence for editing:** if a link is later opened in an edit form, the UTM
builder fields cannot be pre-populated, because only the composed URL was saved.
The edit form shows the destination URL with UTM params already present in the
query string. Discrete re-population would require either adding separate DB
columns or parsing the stored URL's query string back into the five fields (which
is lossy when non-UTM params overlap). This is tracked as a known limitation.

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

### What values are recorded

The click record always captures the UTM values from the *short URL's* query
string (the inbound request), not from the stored `destination_url`. This is
consistent: a link author who baked `utm_source=email` into the destination URL
but shared the short link without appending UTM params will see `(none)` in
the source breakdown. To record attribution, UTM params must be present on the
*short URL* at click time.

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
| `web/src/lib/utm.ts` | Pure composition helpers: `composeUtmUrl`, `isUtmEmpty`, `emptyUtmParams`, `UTM_KEYS` |
| `web/src/lib/utm.test.ts` | 25 unit tests for the composition helpers |
| `web/src/views/Dashboard.svelte` | Create-form UTM builder UI, live preview, bake-on-submit wiring |
| `web/src/views/LinkDetail.svelte` | UTM breakdown panel and bar charts |
| `web/src/lib/linkDetail.ts` | `utmDimensions`, `sortBuckets`, `isEmptyStats`, `NONE_BUCKET` |
| `internal/handlers/redirect.go` | `mergeUTM`, `buildClickInfo`, `RedirectHandler.ServeHTTP` |
| `internal/clicks/stats.go` | `UTMStatsForLink`, `breakdown`, `UTMStats`, `Bucket`, `NoneBucket` |
