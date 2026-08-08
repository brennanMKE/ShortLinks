// Package qr generates QR codes for a link's SHORT URL — never its
// destination — so that a scan resolves through GET /u/{key} and is recorded
// as a click (#0030), exactly like any other visit to the short link. #0106
// exists to let a print placement (a flyer on a specific pole, a poster in a
// specific window) be individually attributable; encoding the destination
// URL instead would skip the redirect entirely and silently defeat that.
//
// # Library choice (server-side Go, documented per #0106/#0108)
//
// Generation happens entirely in Go, using github.com/skip2/go-qrcode
// (MIT-licensed, pure Go, no cgo) for the QR *encoding* only — turning a
// string into the boolean module matrix (ISO/IEC 18004:2006). This package
// does NOT use that library's own PNG/image helpers; it reads the raw
// bitmap (QRCode.Bitmap) and renders both output formats itself, for two
// reasons:
//
//  1. SVG must be genuinely vector (acceptance criterion) — the library has
//     no SVG output at all, so an SVG renderer has to exist somewhere
//     regardless of which encoder is used.
//  2. Owning both renderers from one shared bitmap guarantees the SVG and
//     PNG for the same link agree pixel-for-pixel on module placement and
//     quiet zone, and lets the bulk endpoint (CampaignsHandler.QRZip) build
//     a zip archive with the stdlib's archive/zip — no second, JS-side zip
//     dependency, and no second QR encoder to keep in sync with this one.
//
// The alternative — a client-side JS QR library — was rejected because the
// bulk "one archive per campaign" deliverable needs zip generation
// somewhere, and Go already has archive/zip in the standard library; doing
// the encoding client-side would still require shipping a JS zip library
// (e.g. JSZip) AND a JS QR encoder, two new dependencies against Go's one.
// Generating server-side also means the SAME code path serves the
// single-link download buttons and the bulk archive, rather than two
// independently-maintained encoders that could drift.
package qr

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"regexp"
	"strings"

	skipqr "github.com/skip2/go-qrcode"
)

// ShortURLBase mirrors web/src/lib/links.ts's SHORT_URL_BASE. It is
// deliberately the fixed PRODUCTION display base, not internal/config's
// BaseURL (which is the AUTH-EMAIL base and is localhost in dev — see
// config.Config.BaseURL's doc comment and NoOpMailer). The dashboard always
// shows and copies `https://go.sstools.co/u/{key}` regardless of the
// environment actually serving the request; a QR code that encoded a dev
// localhost URL would scan to something the phone can't reach and would
// silently disagree with the short URL the same page displays right next
// to it. Keep this in sync with links.ts's SHORT_URL_BASE by hand — there
// is no shared source of truth across the Go/TS boundary.
const ShortURLBase = "https://go.sstools.co"

// ShortURL builds the canonical shareable short URL for a key, matching
// links.ts's shortUrl() byte-for-byte (including percent-encoding the key
// defensively, even though generated keys and validated custom aliases are
// already URL-safe).
func ShortURL(key string) string {
	return ShortURLBase + "/u/" + url.PathEscape(key)
}

// Level is the error-correction level used for every generated code:
// skip2/go-qrcode's Highest (QR "H", ~30% of codewords recoverable).
//
// DECIDED DELIBERATELY, not left at the library default (Medium/M, ~15%):
// these codes are printed on paper meant to be stuck outdoors — a flyer on
// a pole or a poster in a shop window — where corners tear, ink fades, and
// tape or staples cover part of the code. H is the level print/signage
// guidance generally recommends for exactly that exposure.
//
// The size cost that normally motivates picking a lower level does not
// apply here: the encoded payload is always a short URL under our own
// domain (`https://go.sstools.co/u/{key}`), and at H that stays at QR
// version 4 (41x41 modules including the quiet zone) for a typical 6-char
// generated key (links.KeyLength), and no worse than version 5 (45x45) even
// at links.key's VARCHAR(12) maximum length — see qr_test.go's
// TestLevel_TypicalShortURLStaysSmall, which pins that upper bound so a
// future change to the key format is forced to re-examine this comment
// rather than silently regress code density. Paying for maximum resilience
// costs nothing meaningful in size or scannability at this content length.
const Level = skipqr.Highest

// ModulePixelsPNG is the PNG raster's pixel size per QR module — an
// integer, not a target canvas size, precisely because forcing a fixed
// canvas size would make individual module edges fall on fractional pixel
// boundaries and blur under anti-aliasing when raster-scaled, which is the
// opposite of what a print-resolution asset is for. Every module renders
// as a crisp 20x20px solid block; the canvas is (modules across) x 20.
//
// DOCUMENTED SIZE (#0106 acceptance criterion): for the common case (a
// short URL around 30 bytes, Level H → QR version 3-4, 37-41 modules
// including the 4-module quiet zone on each side — see Level's doc
// comment), the resulting PNG is roughly 740-820px square. Printed at the
// conventional print density of 300 DPI (dots per inch — this package does
// not embed a DPI/pHYs chunk in the PNG itself: Go's stdlib image/png has
// no API for it, and most print workflows scale a placed image to a target
// physical size at import/layout time rather than trusting embedded
// density metadata) that is about 2.5-2.7in / 6.3-6.9cm square — a normal,
// comfortably hand-scannable size for a flyer, printed at native resolution
// with no upscaling softness. A poster-sized code should use the SVG
// download instead, which scales losslessly to any size because it is
// vector.
const ModulePixelsPNG = 20

// Matrix encodes content (always a short URL — see ShortURL) at Level and
// returns its module bitmap: bitmap[y][x] is true where a module is dark.
// The bitmap INCLUDES the mandatory 4-module quiet zone on all four sides
// (skip2/go-qrcode's default — DisableBorder is left false), which is what
// makes the quiet-zone acceptance criterion hold automatically for every
// caller of Render{PNG,SVG} rather than something each renderer has to
// remember to add.
func Matrix(content string) ([][]bool, error) {
	code, err := skipqr.New(content, Level)
	if err != nil {
		return nil, fmt.Errorf("qr: encode: %w", err)
	}
	return code.Bitmap(), nil
}

// RenderPNG rasterizes a module bitmap (from Matrix) to a PNG at
// ModulePixelsPNG per module — see its doc comment for the sizing
// rationale. White background, solid black modules, no anti-aliasing (each
// module is an axis-aligned filled rectangle so there is nothing to
// anti-alias). The quiet zone renders as plain white margin because the
// input bitmap already includes it (Matrix's doc comment) — this function
// draws exactly the modules it is given and adds no additional margin of
// its own, so it must never be called with a bitmap that has had its quiet
// zone stripped.
func RenderPNG(bitmap [][]bool) ([]byte, error) {
	size := len(bitmap)
	px := size * ModulePixelsPNG
	img := image.NewGray(image.Rect(0, 0, px, px))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	black := image.NewUniform(color.Black)
	for y, row := range bitmap {
		for x, dark := range row {
			if !dark {
				continue
			}
			r := image.Rect(x*ModulePixelsPNG, y*ModulePixelsPNG, (x+1)*ModulePixelsPNG, (y+1)*ModulePixelsPNG)
			draw.Draw(img, r, black, image.Point{}, draw.Src)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("qr: png encode: %w", err)
	}
	return buf.Bytes(), nil
}

// RenderSVG renders a module bitmap (from Matrix) as a VECTOR SVG — no
// embedded raster of any kind, satisfying the acceptance criterion that the
// SVG scale to poster size without artifacts. The viewBox is in whole
// MODULE units (0..size), so every <rect> has integer coordinates; the
// width/height attributes scale that to ModulePixelsPNG-per-module pixels
// purely as a sensible default DISPLAY size (e.g. previewing the file
// un-scaled) — an SVG consumer is free to render it at any size because the
// viewBox/path data never changes, unlike a raster image. shape-rendering
// "crispEdges" disables anti-aliasing on the rect edges, avoiding the
// hairline gaps some renderers draw between adjacent unit rects. Each
// output row is run-length-encoded into one <rect> per contiguous run of
// dark modules rather than one <rect> per module, keeping the file small
// (a handful of rects per row instead of up to `size`) without changing
// what is drawn. The quiet zone is preserved the same way RenderPNG
// preserves it: it is present in the input bitmap (Matrix's doc comment) as
// a border of false modules, which this function simply never draws a
// <rect> over, leaving the white background rect showing through.
func RenderSVG(bitmap [][]bool) []byte {
	size := len(bitmap)
	px := size * ModulePixelsPNG

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`+"\n", size, size, px, px)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%d" fill="#ffffff"/>`+"\n", size, size)
	b.WriteString(`<g fill="#000000">` + "\n")
	for y, row := range bitmap {
		x := 0
		for x < len(row) {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1"/>`+"\n", start, y, x-start)
		}
	}
	b.WriteString("</g>\n</svg>\n")
	return []byte(b.String())
}

// nonSlugRun matches any run of characters OUTSIDE [a-z0-9] once a
// placement has been lowercased — see slugifyPlacement.
var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// maxPlacementSlugLen bounds the placement portion of a bulk-download
// filename. Placements are free text (up to whatever the links table
// allows); an unbounded slug would make filenames unwieldy in a stack of a
// hundred printed sheets, which is precisely the "match a sheet to a
// location at a glance" use case this filename scheme exists for.
const maxPlacementSlugLen = 60

// slugifyPlacement converts an arbitrary placement string into a lowercase,
// filesystem-safe, ASCII token for Filename. It is intentionally lossy
// rather than transliterating: the key prefix Filename always adds already
// guarantees every filename in a bulk archive is unique (keys are globally
// unique — see links.Store), so this function's only job is to be a
// SAFE, non-empty, human-recognizable hint, not a lossless encoding of the
// placement text.
//
// Three cases, each with a distinct, deliberately different token so a
// reviewer scanning a directory listing can tell them apart:
//   - No placement at all (empty/whitespace-only) → "no-placement".
//   - A placement that IS entirely non-ASCII/punctuation (e.g. only emoji,
//     or only CJK, which this function does not transliterate) → "placement"
//     (distinct from "no-placement": a location name WAS given, it just
//     could not be preserved in an ASCII filename).
//   - Anything else → the lowercased placement with every run of
//     whitespace, slashes, quotes, and other punctuation collapsed to a
//     single '-', leading/trailing '-' trimmed, capped at
//     maxPlacementSlugLen. Examples: "Main St / 5th Ave" →
//     "main-st-5th-ave"; `Bob's "Diner" window` → "bob-s-diner-window".
func slugifyPlacement(placement string) string {
	trimmed := strings.TrimSpace(placement)
	if trimmed == "" {
		return "no-placement"
	}
	lower := strings.ToLower(trimmed)
	slug := nonSlugRun.ReplaceAllString(lower, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > maxPlacementSlugLen {
		slug = strings.Trim(slug[:maxPlacementSlugLen], "-")
	}
	if slug == "" {
		return "placement"
	}
	return slug
}

// Filename builds the bulk-download archive entry name for one link's QR
// code: "{key}-{placement-slug}.{ext}", e.g. "abc123-storefront-window.svg"
// or "abc123-no-placement.png". key is used verbatim (link keys are already
// validated to a URL/filesystem-safe alphabet at creation — see
// links.Store) and alone guarantees uniqueness within an archive; the
// placement slug (slugifyPlacement) exists only to make a stack of printed
// sheets matchable to locations without opening each file — see this
// package's doc comment and #0106.
func Filename(key, placement, ext string) string {
	return key + "-" + slugifyPlacement(placement) + "." + ext
}
