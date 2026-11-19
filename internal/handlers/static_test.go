package handlers

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// distFS builds a minimal in-memory FS that mirrors the embedded web/dist
// layout used by SPAHandler in production.
func distFS(files map[string]string) fs.FS {
	m := fstest.MapFS{}
	for name, content := range files {
		m[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func serveSPA(fsys fs.FS, path string) *httptest.ResponseRecorder {
	h := NewSPAHandler(fsys)
	mux := http.NewServeMux()
	mux.Handle("GET /", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestSPAFaviconServedFromDist confirms that /favicon.png is served as
// image/png (from the embedded dist root) and NOT intercepted by the SPA
// catch-all that returns index.html.  This is the core acceptance criterion
// for issue #0061.
func TestSPAFaviconServedFromDist(t *testing.T) {
	pngMagic := "\x89PNG\r\n\x1a\n" // first 8 bytes of any PNG file
	fsys := distFS(map[string]string{
		"index.html":           "<!doctype html><title>Short Links</title><div id=\"app\"></div>",
		"favicon.png":          pngMagic + "fake-favicon-body",
		"apple-touch-icon.png": pngMagic + "fake-apple-touch-icon-body",
	})

	rec := serveSPA(fsys, "/favicon.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /favicon.png: status = %d, want 200", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}

	body := rec.Body.String()
	if !strings.HasPrefix(body, pngMagic) {
		preview := body
		if len(preview) > 16 {
			preview = preview[:16]
		}
		t.Errorf("body does not start with PNG magic bytes; got %q", preview)
	}
	if strings.Contains(body, "<!doctype html>") {
		t.Error("GET /favicon.png returned index.html — static file not being served; check SPAHandler.fileExists")
	}
}

// TestSPAAppleTouchIconServedFromDist is the same check for the 180×180 iOS
// bookmark icon also added in #0061.
func TestSPAAppleTouchIconServedFromDist(t *testing.T) {
	pngMagic := "\x89PNG\r\n\x1a\n"
	fsys := distFS(map[string]string{
		"index.html":           "<!doctype html><title>Short Links</title><div id=\"app\"></div>",
		"apple-touch-icon.png": pngMagic + "fake-apple-touch-icon-body",
	})

	rec := serveSPA(fsys, "/apple-touch-icon.png")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /apple-touch-icon.png: status = %d, want 200", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}

	body := rec.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		t.Error("GET /apple-touch-icon.png returned index.html — static file not being served")
	}
}

// TestSPAMissingAssetReturns404 confirms a request for a path that looks like a
// static asset (has a file extension) but has no embedded file returns 404 —
// NOT the HTML shell. /favicon.ico is the motivating case: the site ships
// PNG-only favicons (no favicon.ico), and serving index.html for /favicon.ico
// let browsers cache an HTML document as a broken icon.
func TestSPAMissingAssetReturns404(t *testing.T) {
	fsys := distFS(map[string]string{
		"index.html":  "<!doctype html><title>Short Links</title><div id=\"app\"></div>",
		"favicon.png": "\x89PNG\r\n\x1a\nfake",
	})

	for _, p := range []string{"/favicon.ico", "/missing.png", "/nope.css"} {
		rec := serveSPA(fsys, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<!doctype html>") {
			t.Errorf("GET %s returned index.html — missing asset must 404, not serve the SPA shell", p)
		}
	}
}

// TestSPAUnknownPathFallsBackToIndex confirms SPA deep-link fallback still
// works: a path that has no matching file in the dist FS returns index.html.
func TestSPAUnknownPathFallsBackToIndex(t *testing.T) {
	fsys := distFS(map[string]string{
		"index.html": "<!doctype html><title>Short Links</title><div id=\"app\"></div>",
	})

	rec := serveSPA(fsys, "/dashboard")

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /dashboard: status = %d, want 200", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("SPA fallback did not return index.html for unknown deep-link path")
	}
}

// #0184: cache headers and precompressed assets.

func serveSPAReq(fsys fs.FS, req *http.Request) *httptest.ResponseRecorder {
	h := NewSPAHandler(fsys)
	mux := http.NewServeMux()
	mux.Handle("GET /", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

var compressibleJS = strings.Repeat("console.log('x');\n", 500)

func assetFS() fs.FS {
	return distFS(map[string]string{
		"index.html":          "<!doctype html><title>Short Links</title>",
		"assets/index-abc.js": compressibleJS,
		"favicon.png":         "\x89PNG\r\n\x1a\nxxxx",
	})
}

func TestSPAHashedAssetCacheImmutable(t *testing.T) {
	for _, ae := range []string{"", "gzip"} {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
		if ae != "" {
			req.Header.Set("Accept-Encoding", ae)
		}
		rec := serveSPAReq(assetFS(), req)
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("AE=%q: Cache-Control = %q", ae, got)
		}
	}
}

func TestSPAShellCacheNoCache(t *testing.T) {
	for _, p := range []string{"/", "/campaigns/x", "/no-such-page"} {
		rec := serveSPA(assetFS(), p)
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, got)
		}
	}
}

func TestSPAMissingAssetIs404NoCacheNotShell(t *testing.T) {
	for _, p := range []string{"/assets/missing-abc.js", "/assets/chunk", "/assets/"} {
		rec := serveSPA(assetFS(), p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", p, got)
		}
		if strings.Contains(rec.Body.String(), "<!doctype") {
			t.Errorf("%s: served the HTML shell", p)
		}
	}
}

func TestSPARootFileHasNoImmutable(t *testing.T) {
	rec := serveSPA(assetFS(), "/favicon.png")
	if strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("favicon got immutable Cache-Control")
	}
}

func TestSPAPrecompressedAssetGzip(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveSPAReq(assetFS(), req)
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("Content-Type = %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
		t.Errorf("Content-Length = %q, body %d", cl, rec.Body.Len())
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Errorf("Vary missing Accept-Encoding")
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(zr)
	if string(out) != compressibleJS {
		t.Errorf("decompressed body mismatch")
	}
}

func TestSPAPrecompressedAssetHead(t *testing.T) {
	req := httptest.NewRequest(http.MethodHead, "/assets/index-abc.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveSPAReq(assetFS(), req)
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD returned a body")
	}
}

func TestSPAPrecompressedAssetIdentity(t *testing.T) {
	rec := serveSPA(assetFS(), "/assets/index-abc.js")
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != compressibleJS {
		t.Errorf("identity response altered: encoding %q", rec.Header().Get("Content-Encoding"))
	}
}

func TestSPAPrecompressedAssetRangeFallsBack(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-9")
	rec := serveSPAReq(assetFS(), req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != compressibleJS[:10] {
		t.Errorf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestSPANoAssetsDirStillConstructs(t *testing.T) {
	rec := serveSPA(distFS(map[string]string{"index.html": "<!doctype html>"}), "/")
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}
}

// #0186: route-level code splitting emits many hashed chunks under /assets/.
// Every one must be precompressed and immutable like the entry, and a chunk
// from a previous release (absent from this build) must be a 404, never the
// shell, so a stale tab's failed import() is recognised and recovers.
func TestSPALazyChunksPrecompressedImmutableAndStaleChunk404(t *testing.T) {
	fsys := distFS(map[string]string{
		"index.html":                "<!doctype html><title>Short Links</title>",
		"assets/index-new1.js":      compressibleJS,
		"assets/Overview-new2.js":   compressibleJS + "a",
		"assets/Admin-new3.js":      compressibleJS + "b",
		"assets/LinksList-new4.css": strings.Repeat(".x{color:red}\n", 400),
	})
	for _, p := range []string{"/assets/index-new1.js", "/assets/Overview-new2.js", "/assets/Admin-new3.js", "/assets/LinksList-new4.css"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := serveSPAReq(fsys, req)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Errorf("%s: status %d, Content-Encoding %q", p, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("%s: Cache-Control = %q", p, got)
		}
	}

	// A chunk the previous release had and this one does not.
	req := httptest.NewRequest(http.MethodGet, "/assets/Overview-old9.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serveSPAReq(fsys, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("stale chunk: status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("stale chunk: Cache-Control = %q, want no-cache", got)
	}
	if strings.Contains(rec.Body.String(), "<!doctype") {
		t.Errorf("stale chunk: served the HTML shell")
	}
}

// An unmatched /api path is a JSON 404 with no-store, never the shell (#0192);
// a known path with the wrong method is the mux's 405; the shell and assets
// are unchanged.
func TestSPAUnknownAPIPathIsJSON404(t *testing.T) {
	fsys := distFS(map[string]string{"index.html": "<html>shell</html>"})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/known", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("GET /", NewSPAHandler(fsys))

	do := func(method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}

	for _, target := range []string{"/api/nope", "/api/nope/deeper", "/api", "/api/"} {
		rec := do(http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type = %q", target, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", target, cc)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) || strings.Contains(rec.Body.String(), "shell") {
			t.Errorf("%s: body = %q", target, rec.Body.String())
		}
	}

	if rec := do(http.MethodPost, "/api/known"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: status = %d, want 405", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/known"); rec.Code != http.StatusOK {
		t.Errorf("known path: status = %d", rec.Code)
	}
	if rec := do(http.MethodGet, "/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "shell") {
		t.Errorf("shell changed: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/apiary"); rec.Code != http.StatusOK {
		t.Errorf("/apiary should still be shell, got %d", rec.Code)
	}
}
