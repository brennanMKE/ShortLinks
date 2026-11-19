package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/siteassets"
)

// gradient builds a deterministic, non-flat image so JPEG output is non-trivial.
func gradient(w, h int, alpha uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x + y) % 256), alpha})
		}
	}
	return img
}

func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif inserts an APP1 Exif segment (with a fake GPS string) right after
// the JPEG SOI marker.
func withExif(t *testing.T, jpg []byte) []byte {
	t.Helper()
	payload := append([]byte("Exif\x00\x00"), []byte("GPSLatitude=47.6062;Make=SecretCam")...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}
	seg = append(seg, payload...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// uploadReq builds a PUT /admin/home/hero multipart request. ctype overrides
// the declared part content type; "" omits an image part entirely when img is nil.
func uploadReq(url string, img []byte, filename, ctype string, alt *string) *http.Request {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if img != nil {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", `form-data; name="image"; filename="`+filename+`"`)
		h.Set("Content-Type", ctype)
		pw, _ := mw.CreatePart(h)
		_, _ = pw.Write(img)
	}
	if alt != nil {
		_ = mw.WriteField("alt", *alt)
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPut, url+"/admin/home/hero", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func strp(s string) *string { return &s }

type heroEnv struct {
	t     *testing.T
	pool  *pgxpool.Pool
	srv   *httptest.Server
	admin string
}

func newHeroEnv(t *testing.T) *heroEnv {
	t.Helper()
	pool := settingsTestPool(t)
	clearSiteAssets := func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM site_assets`); err != nil {
			t.Fatalf("clear site_assets: %v", err)
		}
	}
	clearSiteAssets()
	t.Cleanup(clearSiteAssets)

	store := auth.NewStore(pool)
	home := NewHomeHandler(store).WithHero(siteassets.NewStore(pool), audit.New(pool))
	hero := home.Hero()
	requireSession := middleware.RequireSession(store)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/public/home", home)
	mux.HandleFunc("GET /public/hero", hero.Serve)
	mux.Handle("PUT /admin/home/hero", requireAdmin(http.HandlerFunc(hero.Put)))
	mux.Handle("DELETE /admin/home/hero", requireAdmin(http.HandlerFunc(hero.Delete)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")
	return &heroEnv{t: t, pool: pool, srv: srv, admin: "admin-token"}
}

func (e *heroEnv) do(req *http.Request) *http.Response {
	e.t.Helper()
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (e *heroEnv) upload(img []byte, ctype string, alt string) (*http.Response, string) {
	e.t.Helper()
	resp := e.do(withCookie(uploadReq(e.srv.URL, img, "hero.bin", ctype, &alt), e.admin))
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *heroEnv) get(path string, hdr map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp := e.do(req)
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *heroEnv) errMsg(body string) string {
	var m struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &m)
	return m.Error
}

func TestHero_AcceptedFormats(t *testing.T) {
	e := newHeroEnv(t)
	webpBytes, err := os.ReadFile("testdata/hero_1200x400.webp")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		img   []byte
		ctype string
		want  string
		w, h  int
	}{
		{"jpeg", jpegOf(t, gradient(1200, 400, 255)), "image/jpeg", "image/jpeg", 1200, 400},
		{"png opaque becomes jpeg", pngOf(t, gradient(1600, 800, 255)), "image/png", "image/jpeg", 1600, 800},
		{"png transparent stays png", pngOf(t, gradient(1200, 400, 128)), "image/png", "image/png", 1200, 400},
		{"webp becomes jpeg", webpBytes, "image/webp", "image/jpeg", 1200, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := e.upload(c.img, c.ctype, "A wide banner")
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d body=%s", resp.StatusCode, body)
			}
			got, raw := e.get("/public/hero", nil)
			if got.StatusCode != 200 || got.Header.Get("Content-Type") != c.want {
				t.Fatalf("serve: %d %q, want %q", got.StatusCode, got.Header.Get("Content-Type"), c.want)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
			if err != nil || cfg.Width != c.w || cfg.Height != c.h {
				t.Fatalf("stored image cfg=%v err=%v, want %dx%d", cfg, err, c.w, c.h)
			}
		})
	}
}

func TestHero_RejectedFormats(t *testing.T) {
	e := newHeroEnv(t)
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="400"><script>alert(1)</script></svg>`)
	gif := append([]byte("GIF89a"), bytes.Repeat([]byte{0}, 64)...)
	for name, c := range map[string]struct {
		img   []byte
		ctype string
	}{
		"svg declared as png":  {svg, "image/png"},
		"svg declared as svg":  {svg, "image/svg+xml"},
		"gif":                  {gif, "image/gif"},
		"html declared as png": {[]byte("<html><script>1</script></html>"), "image/png"},
		"truncated png":        {pngOf(t, gradient(1200, 400, 255))[:200], "image/png"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := e.upload(c.img, c.ctype, "x")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body=%s)", resp.StatusCode, body)
			}
		})
	}
	if resp, _ := e.get("/public/hero", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("nothing should be stored after rejects; GET = %d", resp.StatusCode)
	}
}

// A PNG declared as JPEG (and named .jpg) is sniffed from its bytes.
func TestHero_SpoofedContentTypeIsSniffed(t *testing.T) {
	e := newHeroEnv(t)
	resp := e.do(withCookie(uploadReq(e.srv.URL, pngOf(t, gradient(1200, 400, 255)), "x.jpg", "image/jpeg", strp("alt")), e.admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got, _ := e.get("/public/hero", nil); got.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("Content-Type = %q", got.Header.Get("Content-Type"))
	}
	// And the reverse: a JPEG declared as image/png is still a JPEG, not rejected.
	resp = e.do(withCookie(uploadReq(e.srv.URL, jpegOf(t, gradient(1200, 400, 255)), "x.png", "image/png", strp("alt")), e.admin))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("jpeg-as-png status = %d, want 200", resp.StatusCode)
	}
}

func TestHero_SizeAndShapeLimits(t *testing.T) {
	e := newHeroEnv(t)
	cases := []struct {
		name string
		w, h int
		want string
	}{
		{"too small and wrong shape", 900, 600, "Image is 900×600; use at least 1200×400 px and a 2:1 to 4:1 shape"},
		{"too narrow", 1000, 400, "Image is 1000×400; use at least 1200×400 px"},
		{"too large", 4400, 1500, "Image is 4400×1500; use at most 4000×2000 px"},
		{"too square", 1600, 1200, "Image is 1600×1200; use a 2:1 to 4:1 shape"},
		{"too wide", 2000, 400, "Image is 2000×400; use a 2:1 to 4:1 shape"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, body := e.upload(jpegOf(t, gradient(c.w, c.h, 255)), "image/jpeg", "alt")
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if got := e.errMsg(body); got != c.want {
				t.Errorf("message = %q, want %q", got, c.want)
			}
		})
	}
	// Boundaries are accepted: 2:1 and 4:1 at the minimum and maximum widths.
	for _, d := range [][2]int{{1200, 600}, {1600, 400}, {4000, 1000}, {4000, 2000}} {
		if resp, body := e.upload(jpegOf(t, gradient(d[0], d[1], 255)), "image/jpeg", "alt"); resp.StatusCode != 200 {
			t.Errorf("%dx%d status = %d body=%s, want 200", d[0], d[1], resp.StatusCode, body)
		}
	}
}

func TestHero_TooManyBytes(t *testing.T) {
	e := newHeroEnv(t)
	// Valid PNG header followed by 2 MB+ of padding: refused on size alone.
	big := append(pngOf(t, gradient(1200, 400, 255)), bytes.Repeat([]byte{0}, 2<<20)...)
	resp, body := e.upload(big, "image/png", "alt")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(e.errMsg(body), "2 MB") {
		t.Fatalf("status = %d body=%s, want 400 mentioning 2 MB", resp.StatusCode, body)
	}
	// A huge body is cut off by the request cap, not read to the end.
	huge := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 6<<20)...)
	req := withCookie(uploadReq(e.srv.URL, huge, "h.png", "image/png", strp("alt")), e.admin)
	// The server may close the connection mid-upload; that counts as refused.
	if resp, err := e.srv.Client().Do(req); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("huge status = %d, want 400", resp.StatusCode)
		}
	}
}

func TestHero_AltRequiredAndBounded(t *testing.T) {
	e := newHeroEnv(t)
	img := jpegOf(t, gradient(1200, 400, 255))
	for name, alt := range map[string]string{
		"empty":        "",
		"blank":        "   \n",
		"too long":     strings.Repeat("a", 201),
		"control char": "bad\x00alt",
		"newline":      "two\nlines",
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := e.upload(img, "image/jpeg", alt)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", resp.StatusCode, body)
			}
		})
	}
	// Field missing altogether.
	resp := e.do(withCookie(uploadReq(e.srv.URL, img, "h.jpg", "image/jpeg", nil), e.admin))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing alt status = %d, want 400", resp.StatusCode)
	}
	// Missing image.
	resp = e.do(withCookie(uploadReq(e.srv.URL, nil, "", "", strp("alt")), e.admin))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing image status = %d, want 400", resp.StatusCode)
	}
	// Exactly 200 characters is fine (counted in characters, not bytes).
	if resp, body := e.upload(img, "image/jpeg", strings.Repeat("é", 200)); resp.StatusCode != 200 {
		t.Errorf("200-char alt status = %d body=%s", resp.StatusCode, body)
	}
}

func TestHero_ExifStripped(t *testing.T) {
	e := newHeroEnv(t)
	src := withExif(t, jpegOf(t, gradient(1200, 400, 255)))
	if !bytes.Contains(src, []byte("GPSLatitude")) {
		t.Fatal("fixture should contain EXIF")
	}
	if resp, body := e.upload(src, "image/jpeg", "alt"); resp.StatusCode != 200 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	// Read the bytes straight from the database, not through the handler.
	var stored []byte
	if err := e.pool.QueryRow(context.Background(), `SELECT bytes FROM site_assets WHERE name='hero'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"Exif", "GPSLatitude", "SecretCam"} {
		if bytes.Contains(stored, []byte(needle)) {
			t.Errorf("stored image still contains %q", needle)
		}
	}
	// No APP1 (0xFFE1) marker segment before the first frame.
	for i := 2; i+4 < len(stored) && stored[i] == 0xFF; {
		if stored[i+1] == 0xE1 {
			t.Fatal("stored JPEG has an APP1 segment")
		}
		if stored[i+1] == 0xC0 || stored[i+1] == 0xC2 || stored[i+1] == 0xDA {
			break
		}
		i += 2 + int(stored[i+2])<<8 + int(stored[i+3])
	}
	if _, err := jpeg.Decode(bytes.NewReader(stored)); err != nil {
		t.Errorf("stored bytes do not decode: %v", err)
	}
}

func TestHero_ReuploadReplacesAndChangesETag(t *testing.T) {
	e := newHeroEnv(t)
	if resp, body := e.upload(jpegOf(t, gradient(1200, 400, 255)), "image/jpeg", "first"); resp.StatusCode != 200 {
		t.Fatalf("first: %d %s", resp.StatusCode, body)
	}
	r1, _ := e.get("/public/hero", nil)
	etag1 := r1.Header.Get("ETag")
	if resp, body := e.upload(jpegOf(t, gradient(1600, 600, 255)), "image/jpeg", "second"); resp.StatusCode != 200 {
		t.Fatalf("second: %d %s", resp.StatusCode, body)
	}
	r2, _ := e.get("/public/hero", nil)
	if etag2 := r2.Header.Get("ETag"); etag2 == "" || etag2 == etag1 {
		t.Errorf("ETag %q should differ from %q", etag2, etag1)
	}
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM site_assets`).Scan(&n)
	if n != 1 {
		t.Errorf("site_assets rows = %d, want 1", n)
	}
	_, body := e.get("/api/public/home", nil)
	var home struct {
		Hero *struct {
			URL    string `json:"url"`
			Alt    string `json:"alt"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
		} `json:"hero"`
	}
	if err := json.Unmarshal(body, &home); err != nil || home.Hero == nil {
		t.Fatalf("home body %s err=%v", body, err)
	}
	if home.Hero.Alt != "second" || home.Hero.Width != 1600 || home.Hero.Height != 600 {
		t.Errorf("hero = %+v", home.Hero)
	}
	want := "/public/hero?v=" + strings.Trim(r2.Header.Get("ETag"), `"`)[:16]
	if home.Hero.URL != want {
		t.Errorf("hero url = %q, want %q", home.Hero.URL, want)
	}
}

func TestHero_PublicHomeHeroNullWhenUnset(t *testing.T) {
	e := newHeroEnv(t)
	_, body := e.get("/api/public/home", nil)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if string(m["hero"]) != "null" {
		t.Errorf("hero = %s, want null", m["hero"])
	}
}

func TestHero_Delete(t *testing.T) {
	e := newHeroEnv(t)
	del := func() *http.Response {
		req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/admin/home/hero", nil)
		return e.do(withCookie(req, e.admin))
	}
	if resp := del(); resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete with nothing set = %d, want 404", resp.StatusCode)
	}
	if resp, body := e.upload(jpegOf(t, gradient(1200, 400, 255)), "image/jpeg", "alt"); resp.StatusCode != 200 {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	if resp := del(); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", resp.StatusCode)
	}
	if resp, _ := e.get("/public/hero", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", resp.StatusCode)
	}
	_, body := e.get("/api/public/home", nil)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(body, &m)
	if string(m["hero"]) != "null" {
		t.Errorf("hero after delete = %s, want null", m["hero"])
	}
}

func TestHero_AdminOnly(t *testing.T) {
	e := newHeroEnv(t)
	img := jpegOf(t, gradient(1200, 400, 255))
	var userID int64
	if err := e.pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at) VALUES ('user@example.com', FALSE, TRUE, now()) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	seedSession(t, e.pool, userID, "user-token")

	put := func(mod func(*http.Request)) int {
		req := uploadReq(e.srv.URL, img, "h.jpg", "image/jpeg", strp("alt"))
		mod(req)
		return e.do(req).StatusCode
	}
	del := func(mod func(*http.Request)) int {
		req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/admin/home/hero", nil)
		mod(req)
		return e.do(req).StatusCode
	}
	for name, c := range map[string]struct {
		mod  func(*http.Request)
		want int
	}{
		"anonymous": {func(*http.Request) {}, http.StatusUnauthorized},
		"non-admin": {func(r *http.Request) { withCookie(r, "user-token") }, http.StatusForbidden},
		"api token": {func(r *http.Request) { r.Header.Set("Authorization", "Bearer slk_0123456789abcdef0123456789abcdef") }, http.StatusForbidden},
		"token+admin": {func(r *http.Request) {
			withCookie(r, "admin-token")
			r.Header.Set("Authorization", "Bearer slk_0123456789abcdef0123456789abcdef")
		}, http.StatusForbidden},
	} {
		if got := put(c.mod); got != c.want {
			t.Errorf("PUT %s = %d, want %d", name, got, c.want)
		}
		if got := del(c.mod); got != c.want {
			t.Errorf("DELETE %s = %d, want %d", name, got, c.want)
		}
	}
	if resp, _ := e.get("/public/hero", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a refused upload stored something: GET = %d", resp.StatusCode)
	}
}

func TestHero_ServeHeadersAnd304(t *testing.T) {
	e := newHeroEnv(t)
	if resp, body := e.upload(jpegOf(t, gradient(1200, 400, 255)), "image/jpeg", "alt"); resp.StatusCode != 200 {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	resp, body := e.get("/public/hero", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h := resp.Header
	if h.Get("Content-Type") != "image/jpeg" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", h.Get("X-Content-Type-Options"))
	}
	if cc := h.Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("Cache-Control (no v) = %q", cc)
	}
	etag := h.Get("ETag")
	if len(etag) != 66 || etag[0] != '"' { // quote + 64 hex + quote
		t.Errorf("ETag = %q, want quoted sha256", etag)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
	if len(body) == 0 || h.Get("Content-Length") == "" {
		t.Errorf("empty body or Content-Length")
	}

	// The versioned URL from /api/public/home is immutable; a stale version is not.
	cur, _ := e.get("/public/hero?v="+strings.Trim(etag, `"`)[:16], nil)
	if cc := cur.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=31536000") {
		t.Errorf("Cache-Control (current v) = %q, want immutable", cc)
	}
	stale, _ := e.get("/public/hero?v=0000000000000000", nil)
	if cc := stale.Header.Get("Cache-Control"); strings.Contains(cc, "immutable") {
		t.Errorf("stale v must not be immutable, got %q", cc)
	}

	// Conditional requests.
	for _, inm := range []string{etag, "W/" + etag, `"nope", ` + etag, "*"} {
		r304, b := e.get("/public/hero", map[string]string{"If-None-Match": inm})
		if r304.StatusCode != http.StatusNotModified || len(b) != 0 {
			t.Errorf("If-None-Match %q: status = %d len=%d, want 304 empty", inm, r304.StatusCode, len(b))
		}
		if r304.Header.Get("ETag") != etag {
			t.Errorf("304 ETag = %q", r304.Header.Get("ETag"))
		}
	}
	if r, _ := e.get("/public/hero", map[string]string{"If-None-Match": `"other"`}); r.StatusCode != 200 {
		t.Errorf("non-matching If-None-Match = %d, want 200", r.StatusCode)
	}
}

func TestHero_AuditEntries(t *testing.T) {
	e := newHeroEnv(t)
	if resp, body := e.upload(jpegOf(t, gradient(1200, 400, 255)), "image/jpeg", "alt text"); resp.StatusCode != 200 {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+"/admin/home/hero", nil)
	e.do(withCookie(req, e.admin))

	rows, err := e.pool.Query(context.Background(),
		`SELECT action, target_type, metadata::text FROM audit_log WHERE action LIKE 'home.hero.%' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actions []string
	for rows.Next() {
		var a, tt, meta string
		if err := rows.Scan(&a, &tt, &meta); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
		if tt != "site_asset" {
			t.Errorf("target_type = %q", tt)
		}
		for _, k := range []string{`"sha256"`, `"width"`, `"height"`} {
			if !strings.Contains(meta, k) {
				t.Errorf("%s metadata %s missing %s", a, meta, k)
			}
		}
		if a == audit.ActionHomeHeroUpdated && !strings.Contains(meta, `"size"`) {
			t.Errorf("updated metadata %s missing size", meta)
		}
		if strings.Contains(meta, "\\x") || len(meta) > 1000 {
			t.Errorf("metadata looks like it carries bytes: %.80s", meta)
		}
	}
	if len(actions) != 2 || actions[0] != "home.hero.updated" || actions[1] != "home.hero.removed" {
		t.Errorf("audit actions = %v", actions)
	}
}

func TestHero_MultipleAltFieldsRejected(t *testing.T) {
	e := newHeroEnv(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("alt", "one")
	_ = mw.WriteField("alt", "two")
	pw, _ := mw.CreateFormFile("image", "h.jpg")
	_, _ = pw.Write(jpegOf(t, gradient(1200, 400, 255)))
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/admin/home/hero", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if resp := e.do(withCookie(req, e.admin)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHero_NotMultipartIs400(t *testing.T) {
	e := newHeroEnv(t)
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+"/admin/home/hero", strings.NewReader("raw"))
	req.Header.Set("Content-Type", "image/png")
	if resp := e.do(withCookie(req, e.admin)); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSniffHeroFormat(t *testing.T) {
	if sniffHeroFormat([]byte("<svg")) != fmtUnknown || sniffHeroFormat(nil) != fmtUnknown {
		t.Error("svg/empty must be unknown")
	}
	if sniffHeroFormat([]byte("RIFF\x00\x00\x00\x00WAVE")) != fmtUnknown {
		t.Error("RIFF WAVE is not WebP")
	}
}
