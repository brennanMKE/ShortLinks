package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/makiuchi-d/gozxing"
	zxqrcode "github.com/makiuchi-d/gozxing/qrcode"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

// ── Independent decode helpers (see internal/qr/qr_test.go for the same
// pattern and its full rationale: there is no phone in this environment, so
// "verified by scanning" is replaced with a decode via a decoder — gozxing
// — that shares no code with the encoder these handlers use). ──────────────

func decodeQRPNGBody(t *testing.T, body []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(body))
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

type qrSVGDoc struct {
	ViewBox string     `xml:"viewBox,attr"`
	Group   qrSVGGroup `xml:"g"`
	BG      qrSVGRect  `xml:"rect"`
}
type qrSVGGroup struct {
	Fill  string      `xml:"fill,attr"`
	Rects []qrSVGRect `xml:"rect"`
}
type qrSVGRect struct {
	X     float64 `xml:"x,attr"`
	Y     float64 `xml:"y,attr"`
	Width float64 `xml:"width,attr"`
	Fill  string  `xml:"fill,attr"`
}

// wantQRModuleFill / wantQRBackgroundFill mirror internal/qr/qr_test.go's
// wantModuleFill/wantBackgroundFill — see that file's doc comment for why
// decodeQRSVGBody checks these BEFORE trusting rect geometry: reconstructing
// the bitmap from geometry alone would let a regression that emitted white
// modules on a white background pass every SVG test here (review finding).
const (
	wantQRModuleFill     = "#000000"
	wantQRBackgroundFill = "#ffffff"
)

// decodeQRSVGBody parses the SVG bytes RETURNED BY THE HANDLER (not an
// internal bitmap) back into a module grid — after verifying the fill
// colors, not just the geometry (see wantQRModuleFill's doc comment) —
// rasterizes it with the production qr.RenderPNG, and decodes that with the
// independent decoder — proving the actual HTTP response body scans
// correctly, not just that some bitmap somewhere was correct.
func decodeQRSVGBody(t *testing.T, body []byte) string {
	t.Helper()
	var doc qrSVGDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		t.Fatalf("xml.Unmarshal(svg body): %v", err)
	}
	if doc.BG.Fill != wantQRBackgroundFill {
		t.Fatalf("background <rect> fill = %q, want %q", doc.BG.Fill, wantQRBackgroundFill)
	}
	if doc.Group.Fill != wantQRModuleFill {
		t.Fatalf("module <g> fill = %q, want %q", doc.Group.Fill, wantQRModuleFill)
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
		for x := x0; x < x1; x++ {
			bitmap[y][x] = true
		}
	}
	pngBytes, err := qr.RenderPNG(bitmap)
	if err != nil {
		t.Fatalf("RenderPNG(parsed svg body): %v", err)
	}
	return decodeQRPNGBody(t, pngBytes)
}

// ── QRSVG / QRPNG (LinksHandler, per-link) ──────────────────────────────
//
// linksMux (links_test.go) already mounts GET /api/links/{key}/qr.{svg,png}
// alongside the rest of the link routes, so these tests reuse it directly.

func TestLinksHandler_QRSVG_EncodesShortURLNotDestination(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	// A destination deliberately shaped nothing like the short URL (different
	// host, different path) so a decode that accidentally returned the
	// destination instead of the short URL is unambiguously distinguishable
	// from one that returned the short URL.
	seedLink(t, pool, alice, "qrs001", "https://merchant.example.net/landing/page?ref=poster")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/qrs001/qr.svg", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.Contains(cd, "qrs001") {
		t.Errorf("Content-Disposition = %q, want attachment with the key in the filename", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	wantShort := qr.ShortURL("qrs001")
	got := decodeQRSVGBody(t, body)
	if got != wantShort {
		t.Fatalf("decoded %q, want short URL %q", got, wantShort)
	}
	if got == "https://merchant.example.net/landing/page?ref=poster" {
		t.Fatal("decoded value equals the DESTINATION URL, not the short URL — click recording would be bypassed")
	}
}

func TestLinksHandler_QRPNG_EncodesShortURLNotDestination(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "qrp001", "https://merchant.example.net/landing/page?ref=poster")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/qrp001/qr.png", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	wantShort := qr.ShortURL("qrp001")
	got := decodeQRPNGBody(t, body)
	if got != wantShort {
		t.Fatalf("decoded %q, want short URL %q", got, wantShort)
	}
	if got == "https://merchant.example.net/landing/page?ref=poster" {
		t.Fatal("decoded value equals the DESTINATION URL, not the short URL — click recording would be bypassed")
	}
}

// TestLinksHandler_QR_OwnershipEnforced asserts both QR routes 404 for a key
// that exists but belongs to another user — the same indistinguishable-404
// contract Get uses, so a QR download route cannot be used to fingerprint
// another user's key.
func TestLinksHandler_QR_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, bob, "bobkey", "https://bob.example.com")

	for _, path := range []string{"/api/links/bobkey/qr.svg", "/api/links/bobkey/qr.png"} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestLinksHandler_QR_NonexistentKey404(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/doesnotexist/qr.svg", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLinksHandler_QR_Unauthenticated401(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	for _, path := range []string{"/api/links/anykey/qr.svg", "/api/links/anykey/qr.png"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("request %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, resp.StatusCode)
		}
	}
}

// TestLinksHandler_QRFilenames_ReflectPlacementAndMissingPlacement asserts
// the Content-Disposition filename embeds the slugified placement (AC:
// filenames unambiguous/filesystem-safe including spaces/slashes/
// punctuation) and that a link with NO placement still exports with a
// sensible name (the other explicit AC).
func TestLinksHandler_QRFilenames_ReflectPlacementAndMissingPlacement(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "noplace", "https://example.com/a")
	seedLinkWithPlacement(t, pool, alice, "hasplace", "https://example.com/b", `Main St / 5th Ave "Window"`)

	cases := []struct {
		key      string
		wantSubs string
	}{
		{"noplace", "noplace-no-placement.svg"},
		{"hasplace", "hasplace-main-st-5th-ave-window.svg"},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/"+tc.key+"/qr.svg", nil)
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		cd := resp.Header.Get("Content-Disposition")
		if !strings.Contains(cd, tc.wantSubs) {
			t.Errorf("key %s: Content-Disposition = %q, want it to contain %q", tc.key, cd, tc.wantSubs)
		}
	}
}

// ── QRZip (CampaignsHandler, bulk) ──────────────────────────────────────
//
// campaignsMux (campaigns_test.go) already mounts GET
// /api/campaigns/{slug}/qr.zip alongside the rest of the campaign routes,
// so these tests reuse it directly.

// seedLinkWithPlacement inserts an active link with a placement set, unassigned
// to any campaign.
func seedLinkWithPlacement(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest, placement string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, placement)
		 VALUES ($1, $2, $3, TRUE, 0, now(), $4) RETURNING id`,
		userID, key, dest, placement,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

func TestCampaignsHandler_QRZip_ContainsOneSVGAndPNGPerLink(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Zip Test"}`)

	campaignID := c.ID
	seedLinkWithCampaignAndPlacement(t, pool, alice, campaignID, "zipa", "https://a.example.com", "Storefront Window")
	seedLinkWithCampaignAndPlacement(t, pool, alice, campaignID, "zipb", "https://b.example.com", "")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/qr.zip", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		names[f.Name] = f
	}

	wantNames := []string{
		"zipa-storefront-window.svg", "zipa-storefront-window.png",
		"zipb-no-placement.svg", "zipb-no-placement.png",
	}
	if len(names) != len(wantNames) {
		t.Fatalf("zip has %d entries, want %d: %v", len(names), len(wantNames), names)
	}
	for _, name := range wantNames {
		f, ok := names[name]
		if !ok {
			t.Errorf("zip missing entry %q; got %v", name, names)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		var decoded string
		if strings.HasSuffix(name, ".svg") {
			decoded = decodeQRSVGBody(t, content)
		} else {
			decoded = decodeQRPNGBody(t, content)
		}

		wantKey := "zipa"
		if strings.HasPrefix(name, "zipb") {
			wantKey = "zipb"
		}
		wantShort := qr.ShortURL(wantKey)
		if decoded != wantShort {
			t.Errorf("%s decoded to %q, want %q", name, decoded, wantShort)
		}
	}
}

func TestCampaignsHandler_QRZip_EmptyCampaignProducesEmptyValidZip(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Empty Zip"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/qr.zip", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip.NewReader (must still be a valid archive): %v", err)
	}
	if len(zr.File) != 0 {
		t.Errorf("zip has %d entries, want 0", len(zr.File))
	}
}

func TestCampaignsHandler_QRZip_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+bobCampaign.Slug+"/qr.zip", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestCampaignsHandler_QRZip_Unauthenticated401(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/campaigns/anything/qr.zip")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// seedLinkWithCampaignAndPlacement inserts an active link assigned to
// campaignID with a placement set (possibly "").
func seedLinkWithCampaignAndPlacement(t *testing.T, pool *pgxpool.Pool, userID, campaignID int64, key, dest, placement string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, campaign_id, placement)
		 VALUES ($1, $2, $3, TRUE, 0, now(), $4, $5) RETURNING id`,
		userID, key, dest, campaignID, placement,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}
