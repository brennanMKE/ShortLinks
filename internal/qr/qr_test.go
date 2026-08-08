package qr

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	zxqrcode "github.com/makiuchi-d/gozxing/qrcode"
	skipqr "github.com/skip2/go-qrcode"
)

// tryDecodePNG is decodePNG's non-fatal twin: it reports whether the
// independent decoder succeeded rather than failing the test on an error,
// for tests that expect (and want to assert) a decode FAILURE.
func tryDecodePNG(t *testing.T, pngBytes []byte) (string, error) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return "", err
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", err
	}
	result, err := zxqrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		return "", err
	}
	return result.GetText(), nil
}

// ── Independent decode helpers ──────────────────────────────────────────
//
// The acceptance criterion this issue cannot satisfy in this environment is
// "verified by actually scanning a real phone camera" — there is no phone
// here. The honest substitute (per the issue's own instruction) is a
// PROGRAMMATIC decode with a decoder that shares no code with the encoder:
// github.com/makiuchi-d/gozxing is an independent, pure-Go ZXing port,
// entirely unrelated to github.com/skip2/go-qrcode (used only for
// encoding). Every "decodes correctly" test below goes through gozxing, not
// through re-inspecting the bitmap this package itself produced.

// decodePNG decodes PNG bytes with the independent decoder and returns the
// text it read out of the code.
func decodePNG(t *testing.T, pngBytes []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatalf("NewBinaryBitmapFromImage: %v", err)
	}
	result, err := zxqrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("independent decode failed: %v", err)
	}
	return result.GetText()
}

// svgDoc/svgGroup/svgRect parse exactly the shape RenderSVG emits: a root
// <svg> with a background <rect> and one <g> holding the module rects. This
// is deliberately a real XML parse of the SERIALIZED BYTES (encoding/xml),
// not a re-inspection of the bitmap that produced them — decodeSVG below
// proves the actual .svg FILE content round-trips to the same code, which is
// the point of "rasterise the SVG first, then decode" from the issue's
// verification instructions.
type svgDoc struct {
	ViewBox string   `xml:"viewBox,attr"`
	Group   svgGroup `xml:"g"`
	BG      svgRect  `xml:"rect"`
}
type svgGroup struct {
	Fill  string    `xml:"fill,attr"`
	Rects []svgRect `xml:"rect"`
}
type svgRect struct {
	X      float64 `xml:"x,attr"`
	Y      float64 `xml:"y,attr"`
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Fill   string  `xml:"fill,attr"`
}

// wantModuleFill / wantBackgroundFill are the exact fill values RenderSVG is
// expected to emit (qr.go: `fill="#000000"` on the module <g>, `fill="#ffffff"`
// on the background <rect>). decodeSVG asserts BOTH before trusting rect
// geometry as "this is a dark module" — reconstructing the bitmap from
// geometry alone, with no color check, would let a regression that emitted
// WHITE modules on a white background (or any other color swap) pass every
// SVG test here, since the shapes would still be in the right place (review
// finding: "decodeSVG never reads fill").
const (
	wantModuleFill     = "#000000"
	wantBackgroundFill = "#ffffff"
)

// decodeSVG parses SVG bytes back into a module bitmap — after verifying the
// fill colors are what RenderSVG is supposed to emit, not just trusting rect
// geometry (see wantModuleFill's doc comment) — rasterizes that bitmap with
// RenderPNG (the same rasterizer the PNG download path uses — reasonable
// here because the thing under test is whether the SVG's own vector content
// reconstructs the right modules, not a second raster implementation), and
// decodes the result with the independent decoder.
func decodeSVG(t *testing.T, svgBytes []byte) string {
	t.Helper()
	var doc svgDoc
	if err := xml.Unmarshal(svgBytes, &doc); err != nil {
		t.Fatalf("xml.Unmarshal(svg): %v", err)
	}
	if doc.BG.Fill != wantBackgroundFill {
		t.Fatalf("background <rect> fill = %q, want %q", doc.BG.Fill, wantBackgroundFill)
	}
	if doc.Group.Fill != wantModuleFill {
		t.Fatalf("module <g> fill = %q, want %q", doc.Group.Fill, wantModuleFill)
	}
	parts := strings.Fields(doc.ViewBox)
	if len(parts) != 4 {
		t.Fatalf("unexpected viewBox %q", doc.ViewBox)
	}
	size, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("viewBox size: %v", err)
	}

	bitmap := make([][]bool, size)
	for y := range bitmap {
		bitmap[y] = make([]bool, size)
	}
	for _, rect := range doc.Group.Rects {
		y := int(rect.Y)
		x0 := int(rect.X)
		x1 := x0 + int(rect.Width)
		if y < 0 || y >= size {
			t.Fatalf("rect y %d out of bitmap bounds", y)
		}
		for x := x0; x < x1; x++ {
			bitmap[y][x] = true
		}
	}

	pngBytes, err := RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG(parsed svg bitmap): %v", err)
	}
	return decodePNG(t, pngBytes)
}

// ── Core round trip: encode → (independently) decode ────────────────────

func TestMatrixRenderPNG_DecodesToExactShortURL(t *testing.T) {
	want := ShortURL("abc123")
	bitmap, err := Matrix(want)
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	pngBytes, err := RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	got := decodePNG(t, pngBytes)
	if got != want {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

func TestMatrixRenderSVG_DecodesToExactShortURL(t *testing.T) {
	want := ShortURL("def456")
	bitmap, err := Matrix(want)
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	svgBytes := RenderSVG(bitmap)
	got := decodeSVG(t, svgBytes)
	if got != want {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

// TestShortURL_IsNotDestinationURL is a mutation guard for the acceptance
// criterion "the encoded URL is the short URL, not the destination": it
// encodes a SHORT URL that is deliberately structurally different from a
// plausible destination URL (different host, different path shape) and
// decodes it back, asserting the result is byte-identical to the short URL
// and explicitly NOT equal to the destination. A future change that swaps
// ShortURL(key) for a link's DestinationURL at a call site would still pass
// a weaker "decodes to SOME url" test; this one fails specifically because
// the decoded text stops matching either string it's compared against in
// the way the test expects.
func TestShortURL_IsNotDestinationURL(t *testing.T) {
	key := "campaign7"
	short := ShortURL(key)
	destination := "https://example.com/really/long/landing/page?utm_source=flyer"

	if short == destination {
		t.Fatalf("test setup invalid: short and destination must differ")
	}

	bitmap, err := Matrix(short)
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	pngBytes, err := RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	got := decodePNG(t, pngBytes)
	if got != short {
		t.Fatalf("decoded %q, want short URL %q", got, short)
	}
	if got == destination {
		t.Fatalf("decoded value unexpectedly equals the destination URL")
	}
}

func TestShortURL_Format(t *testing.T) {
	if got, want := ShortURL("abc123"), "https://go.sstools.co/u/abc123"; got != want {
		t.Fatalf("ShortURL = %q, want %q", got, want)
	}
	// Defensive percent-encoding, mirroring links.ts's shortUrl().
	if got, want := ShortURL("a b"), "https://go.sstools.co/u/a%20b"; got != want {
		t.Fatalf("ShortURL(\"a b\") = %q, want %q", got, want)
	}
}

// ── Error correction level ───────────────────────────────────────────────

func TestLevel_IsHighest(t *testing.T) {
	if Level != skipqr.Highest {
		t.Fatalf("Level = %v, want skipqr.Highest — see qr.go's doc comment for why", Level)
	}
}

// TestLevel_TypicalShortURLStaysSmall pins the size claim in Level's doc
// comment: at Highest, a short URL built from a key at links.key's
// VARCHAR(12) maximum length — the worst case, well beyond the typical
// 6-char generated key (links.KeyLength) — stays at QR version 5 or
// smaller (45x45 modules including the quiet zone). If the key format ever
// grows materially longer than that column bound allows today, this test
// starts failing and forces a re-reading of that comment's size-cost
// reasoning rather than a silent density regression.
func TestLevel_TypicalShortURLStaysSmall(t *testing.T) {
	// 12 chars: links.key's VARCHAR(12) maximum, i.e. the longest key this
	// package could ever be asked to encode.
	bitmap, err := Matrix(ShortURL("abcdEFGH1234"))
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	const maxModules = 45 // QR version 5 including the 4-module quiet zone.
	if len(bitmap) > maxModules {
		t.Fatalf("bitmap is %dx%d modules, want at most %dx%d", len(bitmap), len(bitmap), maxModules, maxModules)
	}
}

// ── Quiet zone ────────────────────────────────────────────────────────────

// TestMatrix_QuietZonePreserved asserts the mandatory 4-module white quiet
// zone (ISO/IEC 18004:2006) is present on all four edges of the bitmap
// Matrix returns: every module in the outer 4 rows/columns is false (light),
// while the bitmap is NOT entirely blank (i.e. this isn't trivially true
// because nothing was encoded). A regression that flipped
// skip2/go-qrcode's DisableBorder to true would fail this test.
func TestMatrix_QuietZonePreserved(t *testing.T) {
	bitmap, err := Matrix(ShortURL("qz1"))
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	size := len(bitmap)
	const quietZone = 4

	anyDark := false
	for y, row := range bitmap {
		for x, dark := range row {
			if !dark {
				continue
			}
			anyDark = true
			if y < quietZone || y >= size-quietZone || x < quietZone || x >= size-quietZone {
				t.Fatalf("dark module at (%d,%d) falls inside the required %d-module quiet zone (bitmap size %d)", x, y, quietZone, size)
			}
		}
	}
	if !anyDark {
		t.Fatal("bitmap has no dark modules at all — test is not exercising anything")
	}
}

// quietZoneModules is the mandatory ISO/IEC 18004 quiet zone width, in
// modules, that Matrix's bitmap includes on all four sides (see Matrix's
// doc comment) and the two tests below independently verify at the RASTER
// level.
const quietZoneModules = 4

// TestRenderPNG_QuietZoneIsBlankWhenCropped decodes the PNG RenderPNG
// produces back to an image.Image, CROPS it down to exactly the claimed
// quiet-zone border (everything in the outer quietZoneModules*ModulePixelsPNG
// ring, excluding the actual QR modules), and asserts every pixel in that
// cropped region is pure white.
//
// This replaces an earlier version of this test that claimed to crop but
// actually just re-decoded the uncropped image unchanged (review finding) —
// which meant nothing distinguished "the margin is genuinely blank" from
// "the margin doesn't exist at all"; both would have passed. This version
// performs a REAL crop and inspects pixels directly, which is also the
// reason it does not merely re-run the independent decoder: that decoder
// turns out to succeed even when the crop is fed back to it (a clean,
// noise-free synthetic image needs no margin to be found), so a decode
// pass/fail here would not actually pin anything either — see
// TestRenderPNG_BlackenedQuietZoneBreaksDecode below for the test that
// exercises the decoder meaningfully.
func TestRenderPNG_QuietZoneIsBlankWhenCropped(t *testing.T) {
	bitmap, err := Matrix(ShortURL("qz2"))
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	pngBytes, err := RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}

	size := len(bitmap)
	qzPixels := quietZoneModules * ModulePixelsPNG
	bounds := img.Bounds()
	wantSide := size * ModulePixelsPNG
	if bounds.Dx() != wantSide || bounds.Dy() != wantSide {
		t.Fatalf("PNG is %dx%d, want %dx%d", bounds.Dx(), bounds.Dy(), wantSide, wantSide)
	}

	checked := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			inQuietZone := x < bounds.Min.X+qzPixels || x >= bounds.Max.X-qzPixels ||
				y < bounds.Min.Y+qzPixels || y >= bounds.Max.Y-qzPixels
			if !inQuietZone {
				continue
			}
			checked++
			r, g, b, a := img.At(x, y).RGBA()
			if r != 0xffff || g != 0xffff || b != 0xffff || a != 0xffff {
				t.Fatalf("quiet zone pixel (%d,%d) is not opaque white: rgba=(%d,%d,%d,%d)", x, y, r, g, b, a)
			}
		}
	}
	if checked == 0 {
		t.Fatal("cropped no pixels — test is not exercising anything")
	}
}

// TestRenderPNG_BlackenedQuietZoneBreaksDecode proves the white quiet zone
// RenderPNG draws is FUNCTIONALLY load-bearing, not merely cosmetically
// present: it takes the exact same rendered modules, paints the quiet-zone
// ring black instead of leaving it white (simulating a code printed with no
// gap next to other dark content — the real-world failure mode the quiet
// zone exists to prevent), and shows the independent decoder that
// previously succeeded now fails on the identical QR modules. This is the
// meaningful complement to the pixel-level check above, which only proves
// the margin is blank as rendered, not that a scanner actually needs it.
func TestRenderPNG_BlackenedQuietZoneBreaksDecode(t *testing.T) {
	want := ShortURL("qz4")
	bitmap, err := Matrix(want)
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	pngBytes, err := RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG: %v", err)
	}

	// Sanity: decodes fine as actually rendered (white quiet zone intact).
	if got := decodePNG(t, pngBytes); got != want {
		t.Fatalf("baseline decode = %q, want %q", got, want)
	}

	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	bounds := img.Bounds()
	adversarial := image.NewGray(bounds)
	draw.Draw(adversarial, bounds, image.NewUniform(color.Black), image.Point{}, draw.Src)

	size := len(bitmap)
	qzPixels := quietZoneModules * ModulePixelsPNG
	inner := image.Rect(bounds.Min.X+qzPixels, bounds.Min.Y+qzPixels, bounds.Max.X-qzPixels, bounds.Max.Y-qzPixels)
	if inner.Dx() != (size-2*quietZoneModules)*ModulePixelsPNG {
		t.Fatalf("test setup: unexpected inner rect %v for size %d", inner, size)
	}
	draw.Draw(adversarial, inner, img, inner.Min, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, adversarial); err != nil {
		t.Fatalf("png.Encode(adversarial): %v", err)
	}

	got, err := tryDecodePNG(t, buf.Bytes())
	if err == nil {
		t.Fatalf("expected decode to FAIL with the quiet zone blackened, but it succeeded and returned %q", got)
	}
}

// ── SVG is genuinely vector ──────────────────────────────────────────────

// TestRenderSVG_NoEmbeddedRaster is a direct check of the acceptance
// criterion "SVG output is vector (no embedded raster)": it inspects the
// serialized bytes for any of the ways a raster image could be smuggled
// into an SVG (an <image> element, or a data: URI, which is how a base64
// PNG/JPEG would appear inline) and fails if either is present. A
// hypothetical future "just embed a PNG in the SVG for simplicity" change
// would be caught here even though the file would still open and even still
// decode.
func TestRenderSVG_NoEmbeddedRaster(t *testing.T) {
	bitmap, err := Matrix(ShortURL("vec1"))
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	svgBytes := RenderSVG(bitmap)
	svg := string(svgBytes)

	if strings.Contains(svg, "<image") {
		t.Fatal("SVG contains an <image> element — not vector-only")
	}
	if strings.Contains(svg, "data:image") {
		t.Fatal("SVG contains a data:image URI — an embedded raster, not vector-only")
	}
	if !strings.Contains(svg, "<rect") {
		t.Fatal("SVG contains no <rect> elements at all — nothing was drawn")
	}
	if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "viewBox") {
		t.Fatal("SVG is missing a root <svg> element with a viewBox")
	}
}

// TestRenderSVG_ScalesWithoutChangingContent asserts the SAME viewBox (and
// therefore the same module geometry) is emitted regardless of the nominal
// pixel width/height attribute — i.e. scaling for print is a matter of the
// SVG CONSUMER choosing a display size, not this package re-rendering at a
// different resolution. Vector output that scaled by re-quantizing modules
// at a different resolution would not "scale to poster size without
// artifacts" as the acceptance criterion requires.
func TestRenderSVG_ScalesWithoutChangingContent(t *testing.T) {
	bitmap, err := Matrix(ShortURL("scale1"))
	if err != nil {
		t.Fatalf("Matrix: %v", err)
	}
	svg := string(RenderSVG(bitmap))
	size := len(bitmap)
	wantViewBox := "viewBox=\"0 0 " + strconv.Itoa(size) + " " + strconv.Itoa(size) + "\""
	if !strings.Contains(svg, wantViewBox) {
		t.Fatalf("SVG missing expected %q", wantViewBox)
	}
}

// ── Filename scheme ───────────────────────────────────────────────────────

func TestFilename(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		placement string
		ext       string
		want      string
	}{
		{"simple placement", "abc123", "Storefront Window", "svg", "abc123-storefront-window.svg"},
		{"no placement at all", "abc123", "", "png", "abc123-no-placement.png"},
		{"whitespace-only placement counts as none", "abc123", "   ", "svg", "abc123-no-placement.svg"},
		{"slash in placement", "abc123", "Main St / 5th Ave", "png", "abc123-main-st-5th-ave.png"},
		{"quotes in placement", "abc123", `Bob's "Diner" window`, "svg", "abc123-bob-s-diner-window.svg"},
		{"already-safe placement is lowercased", "abc123", "downtown-cafe", "png", "abc123-downtown-cafe.png"},
		{
			"mixed ASCII and non-ASCII keeps the ASCII",
			"abc123", "Café Window", "svg", "abc123-caf-window.svg",
		},
		{
			"entirely non-ASCII placement falls back to a distinct token",
			"abc123", "咖啡馆", "png", "abc123-placement.png",
		},
		{
			"entirely emoji placement falls back to the same distinct token",
			"abc123", "🎉🎊", "svg", "abc123-placement.svg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Filename(tc.key, tc.placement, tc.ext)
			if got != tc.want {
				t.Fatalf("Filename(%q, %q, %q) = %q, want %q", tc.key, tc.placement, tc.ext, got, tc.want)
			}
		})
	}
}

// TestFilename_NoPlacementDistinctFromUnrepresentablePlacement guards the
// two fallback tokens ("no-placement" vs "placement") staying genuinely
// different — a reviewer scanning a directory of a hundred filenames should
// be able to tell "this link never had a placement set" apart from "this
// link had one, but it couldn't be written in a filename". A mutation that
// collapsed both branches to the same string would pass every case in
// TestFilename individually if they were checked in isolation, but not this
// direct comparison.
func TestFilename_NoPlacementDistinctFromUnrepresentablePlacement(t *testing.T) {
	none := Filename("k", "", "svg")
	unrepresentable := Filename("k", "咖啡", "svg")
	if none == unrepresentable {
		t.Fatalf("both fallback filenames are %q — the two cases must be distinguishable", none)
	}
}

func TestFilename_LongPlacementIsTruncated(t *testing.T) {
	long := strings.Repeat("a very long placement name ", 10)
	got := Filename("k", long, "png")
	if len(got) > len("k-")+maxPlacementSlugLen+len(".png")+1 {
		t.Fatalf("Filename produced an unbounded-length name: %d bytes: %q", len(got), got)
	}
}

// TestFilename_UniqueAcrossPlacementCollision documents that uniqueness in a
// bulk archive comes from the KEY, not the placement slug: two different
// links whose placements slugify to the identical token (e.g. differ only
// in case/punctuation the slug strips) still produce different filenames
// because their keys differ.
func TestFilename_UniqueAcrossPlacementCollision(t *testing.T) {
	a := Filename("keyA", "Storefront Window!!!", "svg")
	b := Filename("keyB", "storefront window", "svg")
	if a == b {
		t.Fatalf("expected different filenames for different keys, got %q for both", a)
	}
}
