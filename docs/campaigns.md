# Campaigns

This document describes the campaigns feature as it shipped —
[#0098](../issues/0098.md) through [#0107](../issues/0107.md) — consolidated
here by [#0108](../issues/0108.md). For the original design discussion and
decisions-in-progress, see `docs/campaigns-plan.md`; where the two disagree,
this document (and the code) wins — the plan is a historical record, not a
spec.

A campaign is a user-owned grouping of links that promote the same thing
across channels — a product launch emailed, posted to social, and printed on
fliers. Campaigns exist to answer three questions: how many clicks in total
and over time, which channel drove them, and which links belong to the
campaign. What ShortLinks deliberately does **not** try to answer is covered
in ["What this data cannot tell you"](#what-this-data-cannot-tell-you) below.

---

## Data model

### `campaigns` — `migrations/000010_create_campaigns.up.sql`

| Column | Type | Notes |
|---|---|---|
| `id` | `BIGSERIAL` | Primary key |
| `user_id` | `BIGINT` | FK → `users(id)`, not null. Campaigns are scoped per user, matching `links` |
| `name` | `TEXT` | Not null |
| `slug` | `TEXT` | Not null. URL-safe, derived from `name`, **immutable after creation** — renaming a campaign does not change its slug |
| `description` | `TEXT` | Nullable |
| `starts_at` | `TIMESTAMPTZ` | Nullable. Drives the default stats/chart window when set |
| `ends_at` | `TIMESTAMPTZ` | Nullable |
| `archived` | `BOOLEAN` | Default `FALSE`. Reversible — unlike delete |
| `default_utm_source` .. `default_utm_content` | `TEXT` × 5 | Nullable. Prefill values for the create/batch UTM builder; `default_utm_campaign` defaults to the slug at creation and is never re-derived after being cleared |
| `created_at` / `updated_at` | `TIMESTAMPTZ` | Default `now()` |

Constraints: `UNIQUE (user_id, slug)` (slugs are unique **per user**, not
globally — two different users can each own a campaign slugged `summer-fair`)
and `CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at >= starts_at)`,
enforced at the schema level so an inverted window can never reach the
database, not only rejected by the handler. Index: `idx_campaigns_user_id`.

### `links` additions — `migrations/000011_links_campaign_and_utm.up.sql`

| Column | Type | Notes |
|---|---|---|
| `campaign_id` | `BIGINT` | FK → `campaigns(id)`, `ON DELETE SET NULL`, nullable |
| `utm_source` .. `utm_content` | `TEXT` × 5 | Nullable. Discrete copies of what the UTM builder baked into `destination_url` — see `docs/utm.md` |
| `placement` | `TEXT` | Nullable. Free-text operational label (e.g. a poster location), deliberately separate from `utm_content` |

Index: `idx_links_campaign_id` on `(campaign_id) WHERE campaign_id IS NOT NULL`
(partial, since most links have no campaign). Existing rows keep `NULL` in
all seven columns — not backfilled by parsing `destination_url`, since the
parse is ambiguous wherever a link already carried its own `?utm_source=`
from an external source.

### `clicks` additions — `migrations/000012_clicks_campaign_and_bot.up.sql`

| Column | Type | Notes |
|---|---|---|
| `campaign_id` | `BIGINT` | FK → `campaigns(id)`, `ON DELETE SET NULL`, nullable. **Denormalized** — resolved from the link at click time, not joined at query time (see below) |
| `is_bot` | `BOOLEAN` | Default `FALSE`. Classified at record time; see [Bot exclusion](#bot-exclusion-0101) |

Indexes: `idx_clicks_campaign_id` on `(campaign_id) WHERE campaign_id IS NOT
NULL`, and `idx_clicks_campaign_time` on `(campaign_id, clicked_at) WHERE
campaign_id IS NOT NULL` (backs the campaign timeseries query). Existing rows
keep `campaign_id = NULL` and `is_bot = FALSE` — neither is backfilled; see
"Deploy-boundary discontinuities" in `docs/analytics.md`.

For the full table listing (all columns, all tables), see `docs/database.md`.

---

## Campaign membership is a foreign key, not a string match

The plan's central design decision, and the reason `campaigns` exists as its
own table rather than grouping clicks by `utm_campaign` string: a link
created with `utm_campaign=summer-fair` baked into its destination and then
shared as a bare `https://go.sstools.co/u/ab12de` would, under a
string-match design, never record `utm_campaign` on the resulting click at
all unless the short URL itself also carried the query parameter —
`internal/handlers/redirect.go`'s inbound-query capture (see `docs/utm.md`)
only ever sees what's on the *short* URL. Before [#0100](../issues/0100.md)'s
fallback, that made grouping by `clicks.utm_campaign` unusable for the
normal workflow (share a bare short link); after it, `utm_campaign` casing
drift (`Summer-Fair` vs. `summer-fair`) would still silently fragment a
string-keyed rollup. `clicks.campaign_id` sidesteps both problems.

Two decisions that follow from the FK design, both deliberate:

- **A link belongs to at most one campaign**, enforced by the column
  (`links.campaign_id` is a single nullable FK, not a join table). Assigning
  an already-assigned link **moves** it.
- **`clicks.campaign_id` is denormalized onto the click row** at record
  time (`internal/clicks/recorder.go`), not joined from `links` at query
  time. A link can be reassigned to a different campaign, or unassigned,
  after clicks have already been recorded against it; a click resolved via
  a query-time join would silently follow the link's *current* campaign,
  rewriting history every time the link moves. Reassigning or unassigning a
  link never changes the `campaign_id` already stored on its historical
  clicks. Deleting a campaign (`DELETE /api/campaigns/{slug}` — a hard
  delete, not reversible) sets `campaign_id = NULL` on both its links and
  their historical clicks via the two `ON DELETE SET NULL` FKs; it deletes
  no link and no click row. The delete transaction counts the affected
  links first and records that count as `unassigned_links_count` in the
  `campaign.deleted` audit entry's metadata.

**Campaigns are scoped per user**, matching `links`. Ownership is enforced
on every campaign endpoint; slug uniqueness is per user (`UNIQUE (user_id,
slug)`), not global. Cross-user or shared campaigns are out of scope —
revisiting that would mean migrating the unique constraint.

**Slug generation is ASCII-only.** `internal/campaigns/slug.go`'s `Slugify`
lowercases, replaces non-ASCII-alphanumeric runs with `-`, and
`GenerateUniqueSlug` suffixes a collision (`-2`, `-3`, ...) rather than
rejecting it. A name that is entirely non-ASCII (Cyrillic, CJK, emoji)
collapses to `campaign`, `campaign-2`, and so on — acceptable for a
single-admin, English-language instance, but worth knowing since the slug is
user-visible.

---

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/campaigns` | List for the user, with `link_count`/`total_clicks` |
| `POST` | `/api/campaigns` | Create |
| `GET` | `/api/campaigns/{slug}` | Metadata + links + stats (`CampaignSummary`) |
| `PATCH` | `/api/campaigns/{slug}` | Update name, description, dates, archived, defaults |
| `DELETE` | `/api/campaigns/{slug}` | Delete (hard, not reversible; unassigns links/clicks, deletes neither) |
| `GET` | `/api/campaigns/{slug}/stats` | Full rollup (`CampaignRollup`) — stats, timeseries, by-link, series-by-link |
| `GET` | `/api/campaigns/{slug}/links` | Links in the campaign |
| `POST` | `/api/campaigns/{slug}/links` | Assign existing links by key (≤50/request, **not atomic** — resolved independently in order, a failure stops the request without rolling back earlier successes) |
| `DELETE` | `/api/campaigns/{slug}/links/{key}` | Unassign one link |
| `POST` | `/api/campaigns/{slug}/links/batch` | Batch-create new links ([below](#batch-create-0105)) |
| `GET` | `/api/campaigns/{slug}/qr.zip` | Bulk QR download ([below](#qr-codes-0106)) |
| `GET` | `/api/campaigns/{slug}/export.csv` | CSV export ([below](#csv-export-0107)) |

Audit actions, all written **in-band** — see
["Audit convention"](#audit-convention) below: `campaign.created`,
`campaign.updated`, `campaign.deleted`, `campaign.link_assigned`,
`campaign.link_unassigned`.

### Audit convention

Every mutation `internal/campaigns.Store` performs
(`CreateCampaign`/`UpdateCampaign`/`ArchiveCampaign`/`DeleteCampaign`/
`AssignLinkToCampaign`/`UnassignLinkFromCampaign`) writes its audit row via
`audit.Logger.WriteTx`, inside the same transaction as the mutation, rather
than the fire-and-forget `Record` most handlers use elsewhere in the app
(link create/deny/reactivate, credentials, settings, URL filters). This is a
generalization of the pattern `internal/audit/audit.go` originally described
only for the auth ceremonies (registration/login/recovery finish): **any
code that owns the transaction for the mutation it's recording** uses
`WriteTx` so the audit row commits or rolls back atomically with the action;
code whose action has already committed by the time it logs uses `Record`.
See the updated package doc comment in `internal/audit/audit.go`.

---

## Batch create (#0105)

Creating eight links one at a time and typing `utm_campaign=summer-fair`
identically eight times is exactly the failure mode `docs/utm.md` warns
about under "Canonical casing" — batch create is what makes campaigns worth
using. From the campaign detail view: one destination URL, then a row per
channel (source, medium, content, placement, optional per-row title),
prefilled from the campaign's `default_utm_*` values on row 1 only (see
below), each row editable independently.

`POST /api/campaigns/{slug}/links/batch` → `internal/links.Store.CreateLinksBatch`
creates every non-blank row (capped at 50) inside **one transaction** — all
rows commit or none do. Blank rows are skipped
(`skipped_blank_rows` in the response) and reported, not silently created as
empty links.

**Batch create bypasses link deduplication entirely.** Single create
(`POST /api/links`) looks up an existing link by `destination_url` and
reuses it (`CreateOrReactivateLink`); `CreateLinksBatch` never does this —
every row unconditionally inserts, even when its `destination_url` is
byte-identical to another row's or to an existing link's. This is
deliberate, not an oversight: `placement` is never baked into
`destination_url`, so two batch rows differing only in `placement` (the
per-location print case this feature exists to enable) would otherwise
compose to an identical URL and collide onto one shared link via dedup —
turning "two poster locations" into one link that can't distinguish them.
Extending the dedup key or forward-merging onto the matched row (as
`CreateOrReactivateLink` does for single create) were both considered and
rejected: within a batch, a lookup by a wider key could only ever match a
**pre-existing** link, and matching one means either silently moving it into
this campaign or last-write-wins overwriting its metadata — worse than an
unconditional insert for a *create* endpoint. A separate, narrower check
rejects an exact duplicate row (same source + medium + content + placement)
as a likely accidental double-entry, deliberately distinct from dedup.

**Consequence: submitting an identical batch twice yields 2N links, not
N.** Where a repeat `POST /api/links` for the same URL reliably folds onto
the existing row, a repeat batch submission (e.g. a lost response followed
by a retry) creates a second full set. The UI mitigates the common paths —
submit is disabled while the request is in flight, and the form resets on
success — but there is no idempotency key, so a genuine
lost-response-then-retry can still double a batch. Since
[#0106](../issues/0106.md) generates one QR code per link, a doubled batch
means a doubled QR sheet.

**`default_utm_source`/`_medium`/`_content` prefill row 1 of the batch form
only**, not every row — prefilling the same value into every row would make
rows 2..N byte-identical to row 1 (triggering the duplicate-row check before
the user types anything) and would make every untouched row non-blank
(defeating blank-row skipping, so an unedited form would submit N links
instead of zero). `utm_campaign` and `utm_term` are the exception: they're
shared once across the whole batch (one field, not per-row), so prefilling
them has no such collision.

### Print-row convention

Not enforced in code, but the convention this system is built around for
print/flier placements: `utm_medium=print`, `utm_source=flyer`, with
`utm_content` or `placement` naming the physical location (e.g. `"18th &
Texas board"`). Using `placement` rather than `utm_content` for the location
keeps operational metadata (where a poster is stuck up) separate from what
gets forwarded to the destination site's own analytics — they frequently
differ, and collapsing them loses one or the other. See `docs/utm.md` for
the full `placement` vs. `utm_content` reasoning.

---

## QR codes (#0106)

Every link in a campaign's links table (and the link detail view) can be
downloaded as a QR code, individually (SVG or PNG) or in bulk for a whole
campaign (one zip archive, all links, both formats).

### What gets encoded

The QR code always encodes the link's **short URL**
(`https://go.sstools.co/u/{key}`), never its destination URL. Encoding the
destination would let a scan resolve directly, skipping the redirect and
therefore skipping click recording entirely — silently defeating the reason
this feature exists (attributing scans to a specific print placement). See
`internal/qr/qr.go`'s package doc comment and
`TestShortURL_IsNotDestinationURL` / `TestLinksHandler_QRSVG_EncodesShortURLNotDestination`
/ `TestLinksHandler_QRPNG_EncodesShortURLNotDestination`, which decode a
generated code with an independent decoder and assert the result is the
short URL and specifically **not** a (deliberately different) destination
URL.

### Library choice: server-side Go

Generation is entirely server-side, in a new `internal/qr` package, using
`github.com/skip2/go-qrcode` for QR *encoding* only (turning a string into
the ISO/IEC 18004 module matrix). Both output formats — SVG and PNG — are
rendered by `internal/qr` itself from that shared bitmap, not by the
library's own image helpers.

Reasoning:

- The bulk deliverable is one **zip archive per campaign**. Go's standard
  library already has `archive/zip`; doing this client-side would need a
  second dependency (e.g. JSZip) on top of whatever client-side QR encoder
  was chosen. Generating server-side means the zip, the SVG, and the PNG
  all come from **one** dependency.
- SVG must be genuinely vector (acceptance criterion). `skip2/go-qrcode` has
  no SVG output at all, so a renderer had to be written regardless of which
  encoder was picked; owning it lets `internal/qr` guarantee the SVG and PNG
  for the same link agree on module placement and quiet zone, because both
  are drawn from the identical `[][]bool` bitmap.
- The same code path serves the single-link download buttons
  (`GET /api/links/{key}/qr.{svg,png}`) and the bulk archive
  (`GET /api/campaigns/{slug}/qr.zip`) — one encoder, not two that could
  drift apart.

The alternative (a client-side JS library, e.g. `qrcode` + `JSZip`) was
rejected mainly on dependency count: it would need two new npm packages
against Go's one, and would still leave the "generate a zip somewhere"
problem needing a decision anyway. `go list -deps ./cmd/shortlinks` confirms
`gozxing` (the decoder used only in tests, to independently verify what was
encoded) is absent from the production binary.

### Error correction level: Highest (H, ~30%)

`skip2/go-qrcode`'s `Highest` level, not the library's own `Medium` default.
These codes are meant to be printed and stuck up outdoors — a flyer on a
pole, a poster in a shop window — where corners tear, ink fades, and tape or
staples cover part of the code. Level H is standard print/signage guidance
for exactly that exposure.

The size cost that normally discourages picking the highest level doesn't
apply here: the payload is always a short URL under our own domain. At
level H that's QR version 4 (41×41 modules including the quiet zone) for a
typical 6-character generated key, and no worse than version 5 (45×45) even
at `links.key`'s `VARCHAR(12)` maximum length (pinned by
`TestLevel_TypicalShortURLStaysSmall`). Paying for maximum resilience costs
nothing meaningful in size or scannability at this content length.

### PNG size: 20px per module (documented, not arbitrary)

`qr.ModulePixelsPNG = 20` — an **integer pixels-per-module** value, not a
fixed canvas size. Fixing the canvas size instead would force module edges
onto fractional pixel boundaries and blur under raster scaling, which is
the opposite of a print-resolution asset's purpose. Every module renders as
a crisp 20×20px block; the canvas is `(modules across) × 20`.

For the common case (level H, a ~30-byte short URL → 37–41 modules
including the quiet zone), the PNG comes out roughly 740–820px square.
Printed at the conventional density of 300 DPI, that's about
2.5–2.7in / 6.3–6.9cm square — a normal, comfortably hand-scannable flyer
QR size, printed at native resolution with no upscaling softness.

The PNG does **not** embed a DPI/`pHYs` chunk: Go's standard `image/png`
package has no API for it, and most print workflows scale a placed image to
a target physical size at layout time rather than trusting embedded density
metadata. A poster-sized code should use the SVG download instead — it
scales losslessly to any size because it's vector, with no re-render step
and no artifacts (acceptance criterion).

### Quiet zone

`skip2/go-qrcode`'s default (mandatory) 4-module quiet zone is left enabled
(`DisableBorder` is never set) and is part of the bitmap `internal/qr`
reads — both `RenderSVG` and `RenderPNG` draw exactly the modules they're
given and never crop or add their own margin, so the quiet zone is present
in both formats automatically rather than something each renderer has to
remember to add. Pinned by `TestMatrix_QuietZonePreserved` (the bitmap),
`TestRenderPNG_QuietZoneIsBlankWhenCropped` (walks exactly the claimed
quiet-zone ring within the rendered PNG and asserts every pixel there is
opaque white — it iterates the ring's pixels in the full-size image rather
than building a cropped sub-image; the assertion is the same, the mechanism
is not a crop), and
`TestRenderPNG_BlackenedQuietZoneBreaksDecode` (paints that same ring black
instead and shows the independent decoder — which still succeeds on a
merely-cropped, noise-free synthetic image — now fails on the identical QR
modules, proving the margin is functionally load-bearing, not just
cosmetically present).

### Bulk filenames

`qr.Filename(key, placement, ext)` builds each zip entry as
`{key}-{placement-slug}.{ext}`, e.g. `abc123-storefront-window.svg`. The
**key** (globally unique, already validated to a URL-safe alphabet at link
creation) is what guarantees every filename in an archive is unique; the
placement slug exists only to make a stack of printed sheets human-matchable
to locations without opening each file.

The slug is lowercased, ASCII-only, with any run of whitespace, slashes,
quotes, or other punctuation collapsed to a single `-` and capped at 60
characters — e.g. `Main St / 5th Ave` → `main-st-5th-ave`. Three cases are
kept deliberately distinguishable:

| Placement | Slug |
|---|---|
| Not set / whitespace-only | `no-placement` |
| Entirely non-ASCII (e.g. only emoji or CJK — not transliterated) | `placement` |
| Anything else | the lowercased, punctuation-collapsed text |

`no-placement` and `placement` are different tokens on purpose — the first
means "this link never had a placement," the second means "a placement was
set but couldn't be written into an ASCII filename." See `qr.go`'s
`slugifyPlacement` doc comment and `qr_test.go`'s `TestFilename*` table.

### Where the download controls are

- **Links table** (`web/src/views/CampaignDetail.svelte`): a "QR" column
  with both SVG and PNG download links per row (this makes the table eleven
  columns — see the stacked-card layout note in the view's header comment
  for how it degrades below the 900px breakpoint). An earlier round shipped
  a single SVG-only link here after the SVG-and-PNG pair broke #0103's
  measured zero-horizontal-scroll invariant (`scrollWidth == clientWidth`;
  verified at 1024/1280/1440/1920, **not** at every width ≥900px as this doc
  previously claimed — the ~901–944px band still overflows, tracked as
  [#0114](../issues/0114.md) and not yet fixed) — but that measurement was
  taken against thin test content, and re-measuring against realistic
  content (long titles, destinations, and placement names actually reaching
  the cells' `max-width` caps) showed the single-link version *also* broke
  the invariant, so the SVG-only change bought nothing and cost a stated
  deliverable. Both links are restored; `.title-cell`, `.placement-cell`,
  and `.key-cell` (short key) share the width give-back, each capped on
  BOTH its `<th>` and `<td>` — an earlier pass capped `.placement-cell`'s
  `<td>` only, which a `table-layout: auto` column ignores once the
  (uncapped) `<th>`'s own text is wider, making that cap inert. The
  invariant is also verified against a 12-character custom-key alias (the
  documented maximum — generated keys are only 6), not just the generated
  default, since a `scrollWidth == clientWidth` reading alone can read as
  "926 == 926" while sitting a fraction of a pixel below that threshold
  with no real margin; see `CampaignDetail.svelte`'s QR column and
  `.title-cell` comments for the exact, reproduced min-content measurements
  and final cap values. The **Source** and **Medium** columns remain
  uncapped and can still overflow the table with an ordinary value like
  `partner_newsletter` — open as [#0112](../issues/0112.md), reproduced
  live, not yet fixed.

  `.dest-cell` is the exception: rather than sharing the give-back it was cut
  hard to **60px**, a deliberate stub. The column conveys no information at
  any width it can afford — at 150px it rendered `https://www.examp…` and at
  126px `https://www.ex…`, indistinguishable on every row of a campaign whose
  links share a host — so its width is the cheapest in the table and funds the
  columns that do carry signal. [#0113](../issues/0113.md) redesigns what this
  column should display; until then the `<td>`'s `title` attribute is the only
  way to read a full destination on desktop — the only way, since the same
  issue is still open.

  Its `<th>` reads **"URL"**, not "Destination", because "Destination"
  ellipsizes to `DE…` at 60px. A truncated column *header* is a worse defect
  than truncated data — it removes the label telling you what the column is,
  and reads as a bug rather than as a narrow column. "URL" measures 60px ==
  60px, so it renders whole at the existing cap with no geometry change. The
  same reasoning renamed "Short key" → "Key" on `.key-cell`; both `<td>`s keep
  their full `data-label`, so the stacked mobile layout is unaffected.
- **Bulk**: a "Download all QR codes (zip)" button in the links table
  toolbar, linking to `GET /api/campaigns/{slug}/qr.zip`.
- **Link detail view** (`web/src/views/LinkDetail.svelte`): a "QR code" row
  next to "Short URL" with both SVG and PNG download links.

All three are plain `<a href download>` elements, not `fetch`+blob — every
API request in this app is same-origin and cookie-authenticated
(`web/src/lib/api.ts`), so a normal browser navigation to
`/api/links/{key}/qr.svg` already carries the session cookie and the
server's `Content-Disposition: attachment` header drives the native
download, with no client-side JS needed to trigger or name the file.

---

## CSV export (#0107)

`GET /api/campaigns/{slug}/export.csv` returns one row per link currently
assigned to the campaign — short key, short URL, title, destination,
source, medium, content, placement, clicks in window,
`share_of_listed_links_pct`, created date. It calls
`clicks.StatsStore.CampaignRollup` — **the exact same call**
`GET /api/campaigns/{slug}/stats` makes, inside the same `REPEATABLE READ`
transaction — so there is no second aggregation path that could disagree
with the screen.

**`share_of_listed_links_pct` denominates against listed links, not the
campaign total.** It matches the on-screen "Share of listed links" column
in `CampaignDetail.svelte`'s links table exactly, not a share of
`CampaignStats.ClickCount` — clicks from since-unassigned links are counted
in the campaign total but excluded from both the numerator and denominator
of this column, so it can legitimately sum to 100% while being less than
the campaign's overall click count. Any consumer wanting a true
campaign-total share must compute it separately against
`CampaignStats.ClickCount`.

Other decisions, all tested: the filename carries the **inclusive** window
end (matching the on-screen caption), even though `window_to` is exclusive
everywhere else in the API; an empty resolved window (`starts_at ==
ends_at`, so `[X, X)`) is representable, not an error — every assigned link
still gets a row at `0`/`0`, matching the screen; a leading `=`, `+`, `-`,
or `@` in a text field is neutralized against formula injection (numeric
columns are routed around the neutralizer, so numbers stay numbers); the
file is written with `encoding/csv` to a buffer rather than streamed
(deliberate — a clean 500 beats a truncated download); and the export is
bot-excluded by default, matching every other number in the campaign view.

---

## Bot exclusion (#0101)

Comparing channels only means something if the click counts reflect human
interest rather than automated fetches. Posting a short link in Slack,
iMessage, Twitter/X, Discord, or LinkedIn triggers automated unfurl/preview
fetches of the redirect — sometimes several per post — and a printed flier
gets none of that. `internal/clicks/botdetect.go`'s `IsBot` classifies each
click's `User-Agent` case-insensitively against the exported
`BotUserAgentSubstrings` list (`bot`, `crawler`, `spider`, `preview`,
`facebookexternalhit`, `Slackbot`, `Twitterbot`, `Discordbot`, `WhatsApp`,
`TelegramBot`, `LinkedInBot`, `headless`, `curl`, `wget`,
`python-requests`) at record time and stores the result as `clicks.is_bot`
— flagged, **never dropped**, so the data stays inspectable.

A short **allowlist runs first and wins unconditionally**:
`NotBotUserAgentSubstrings` (currently just `cubot`) is checked before the
list above, so a CUBOT-brand phone is classified as human even though its
UA contains the load-bearing `bot` substring. Same asymmetry as everywhere
else in this feature — a misclassified human click is unrecoverable, an
under-caught crawler only leaves documented residual inflation.

An empty or
absent user agent is **not** classified as a bot (missing evidence, not
evidence of automation — see [#0101](../issues/0101.md)'s Fix section for
the asymmetric-harm reasoning).

Every campaign and link stats query excludes `is_bot = TRUE` by default and
returns the excluded count alongside the totals
(`CampaignStats.ExcludedBotCount`, shown in the campaign detail summary
whenever it is non-zero) rather than silently shrinking the number. See
["Deploy-boundary discontinuities"](analytics.md#deploy-boundary-discontinuities)
in `docs/analytics.md` for how this interacts with historical data.

---

## What this data can and cannot honestly compare

This is the part worth reading before drawing a conclusion from the channel
breakdown chart. It is also surfaced directly in the UI, as a note under the
"Channel breakdown" panel on the campaign detail view.

### Social is still inflated after bot filtering

User-agent filtering is a **floor, not a solution**. The bot list matches
crawlers that identify themselves in their `User-Agent` string; some
unfurl/preview bots do not, and those clicks pass through as ordinary
traffic. A social channel's click count after filtering is a better number
than before filtering, not a clean one.

### Print undercounts in raw comparison, but is the cleanest signal per click

A print scan has no `Referer` header (nothing referred it — the person
typed or scanned it directly) and, empirically, little to no bot noise: a
QR code on a physical poster is not fetched by a chat client's link-preview
crawler the way a URL pasted into a chat message is. That makes a print
click closer to "one real human interaction" than any other channel this
app records — but it also means print will look small next to social or
email in a raw side-by-side count, because impression volume (see below)
differs by orders of magnitude between the channels, not because print
performed worse per person who saw it.

This is directly observable in this project's own seeded demo data
(queried against `shortlinks_demo`, not fabricated): a campaign's
non-bot clicks broke down by `utm_source` as `newsletter` 33.9%,
`linkedin` 26.2%, `google` 20.7%, `partner_newsletter` 11.7%, and `flyer`
7.5% — and that one `flyer` source actually represents **two** separate
print placements, at 4.3% and 3.2% of the campaign's non-bot clicks
respectively when broken down by link. In the same window, the excluded
bot counts were 120 (email) and 60 (social) against **zero** for both print
links. That is consistent with the reasoning above, though this sample does
not isolate the unfurl effect cleanly — the paid-search (`google`/`cpc`)
link also recorded zero bots, so "no unfurl crawlers" and "no crawlers of
any kind in this seed" are not distinguishable here. None of that means email or social
"won" — it means more people were exposed to the email and social posts,
which is a fact about reach, not about the poster's effectiveness among
the (much smaller) audience that actually walked past it.

### Impression volume is the missing half of the comparison, and ShortLinks cannot supply it

A social post seen by 2,000 people and a poster seen by 200 are not
comparable on click count alone — the meaningful number is click-through
**per impression**, and **ShortLinks has no way to measure impressions**
for any channel. It does not know how many people saw the social post,
walked past the poster, or received the email. The channel breakdown chart
shows relative click volume, which is a real number worth having, but it
does not by itself say which channel performed better — say so plainly
rather than letting the bar chart imply an answer it cannot give.

---

## What this data cannot tell you

Stated as capability boundaries, not caveats to a feature that mostly does
these things:

- **No unique visitors.** There is no cookie or visitor hash; every click is
  a click. One person scanning the same poster twice counts as two clicks,
  and there is no way to collapse them.
- **No conversions.** ShortLinks sees the redirect, not what happens on the
  destination site afterward. Campaign metrics are reach and interest, not
  outcomes. CSV export (above) exists specifically so those questions can be
  answered against other data, without growing this app to cover them.
- **No geographic breakdown.** `clicks.ip_address` is stored, but there is
  no geo-IP lookup, and adding one has privacy implications (retaining and
  resolving visitor IP-derived location data) that are a separate decision
  from adding the feature.
- **No cross-user or shared campaigns.** Campaigns are scoped to the owning
  user, matching `links`; there is no way for two users to view or manage
  the same campaign.

---

## Known open layout/data issues

Filed, open, and worth knowing about rather than assuming fixed:

- **[#0112](../issues/0112.md)** — the links table's Source and Medium
  columns are uncapped and can overflow the table with an ordinary value
  like `partner_newsletter`; reproduced live.
- **[#0113](../issues/0113.md)** — the Destination column is a deliberate
  60px stub showing `https://www.ex…` on every row; its `<td>`'s `title`
  attribute is currently the only way to read a full destination on
  desktop.
- **[#0114](../issues/0114.md)** — the links table's zero-horizontal-scroll
  invariant is verified at 1024/1280/1440/1920px, **not** at every width
  ≥900px; the ~901–944px band still overflows.
- **[#0111](../issues/0111.md)** — production is missing five `NOT NULL`
  constraints a clean migration replay produces (on `passkey_credentials`
  and `sessions`, unrelated tables to campaigns). [#0110](../issues/0110.md)
  carries a schema guard and a fix **on an unmerged branch**, held pending a
  manual `shortlinks_test` rebuild — it is not on `main`, so nothing in the
  suite enforces it yet. The campaigns migrations (`000010`–`000012`) are unaffected by
  this drift and apply cleanly regardless.

---

## File reference

| Path | Role |
|---|---|
| `migrations/000010_create_campaigns.{up,down}.sql` | The `campaigns` table |
| `migrations/000011_links_campaign_and_utm.{up,down}.sql` | `links.campaign_id` + five discrete UTM columns + `placement` |
| `migrations/000012_clicks_campaign_and_bot.{up,down}.sql` | `clicks.campaign_id` (denormalized) + `is_bot` |
| `internal/campaigns/{doc.go,slug.go,store.go}` | `Store` (CRUD, archive, assign/unassign, all `Campaign`-named methods), slug generation, in-band `WriteTx` audit |
| `internal/handlers/campaigns.go` | All `/api/campaigns...` endpoints |
| `internal/handlers/campaigns_export.go` | CSV export |
| `internal/clicks/botdetect.go` | `IsBot`, `BotUserAgentSubstrings` |
| `internal/clicks/stats.go` | `CampaignStats`, `CampaignSummary`, `CampaignRollup`, `CampaignClicksOverTime`, `CampaignClicksByLink`, `CampaignSeriesByLink` |
| `internal/qr/qr.go` | QR bitmap generation, SVG/PNG rendering, filenames |
| `internal/devstore/devstore.go` | In-memory twins for `scripts/dev.sh` (`STORAGE=json`) |
| `web/src/lib/campaigns.ts` | Pure shaping helpers for the list/detail views, batch rows, CSV/QR URL builders |
| `web/src/views/{CampaignsList.svelte,CampaignDetail.svelte}` | List and detail views |
| `web/src/lib/{CampaignClicksChart.svelte,UTMBarChart.svelte}` | Charts (breakdown chart reused from per-link analytics) |
| `web/src/lib/BatchCreateLinks.svelte` | Batch create panel |
| `docs/campaigns-plan.md` | Original design plan — historical record; where it disagrees with this document, this document and the code win |
| `docs/utm.md` | UTM composition, storage, redirect passthrough, and fallback precedence |
| `docs/analytics.md` | Stats queries, bot exclusion, deploy-boundary discontinuities |
| `docs/database.md` | Full schema reference for every table |
