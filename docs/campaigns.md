# Campaigns

This document is a placeholder — the full campaigns workflow (creation,
link membership, batch create, stats/charts) is scattered across
[#0098](../issues/0098.md)–[#0105](../issues/0105.md) and is consolidated
here by [#0108](../issues/0108.md). Only the QR code feature ([#0106](../issues/0106.md))
is documented below, since that issue's acceptance criteria explicitly call
for the library/error-correction/DPI decisions to be written down somewhere
durable rather than left implicit in code.

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
problem needing a decision anyway.

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
`TestRenderPNG_QuietZoneIsBlankWhenCropped` (crops the rendered PNG down to
exactly the claimed quiet-zone ring and asserts every pixel there is opaque
white), and `TestRenderPNG_BlackenedQuietZoneBreaksDecode` (paints that same
ring black instead and shows the independent decoder — which still succeeds
on a merely-cropped, noise-free synthetic image — now fails on the
identical QR modules, proving the margin is functionally load-bearing, not
just cosmetically present).

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
  previously claimed — see [#0114](../issues/0114.md) for the ~901–944px band
  where the table still overflows) — but that measurement was taken against thin test
  content, and re-measuring against realistic content (long titles,
  destinations, and placement names actually reaching the cells'
  `max-width` caps) showed the single-link version *also* broke the
  invariant, so the SVG-only change bought nothing and cost a stated
  deliverable. Both links are restored; `.title-cell`, `.placement-cell`, and
  `.key-cell` (short key) share the width give-back, each capped on BOTH its
  `<th>` and `<td>` — an earlier pass capped
  `.placement-cell`'s `<td>` only, which a `table-layout: auto` column
  ignores once the (uncapped) `<th>`'s own text is wider, making that cap
  inert. The invariant is also verified against a 12-character custom-key
  alias (the documented maximum — generated keys are only 6), not just the
  generated default, since a `scrollWidth == clientWidth` reading alone can
  read as "926 == 926" while sitting a fraction of a pixel below that
  threshold with no real margin; see `CampaignDetail.svelte`'s QR column
  and `.title-cell` comments for the exact, reproduced min-content
  measurements and final cap values.

  `.dest-cell` is the exception: rather than sharing the give-back it was cut
  hard to **60px**, a deliberate stub. The column conveys no information at
  any width it can afford — at 150px it rendered `https://www.examp…` and at
  126px `https://www.ex…`, indistinguishable on every row of a campaign whose
  links share a host — so its width is the cheapest in the table and funds the
  columns that do carry signal. [#0113](../issues/0113.md) redesigns what this
  column should display; until then the `<td>`'s `title` attribute is the only
  way to read a full destination on desktop, which is itself part of #0113.

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
