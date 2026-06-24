package handlers

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
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
		"index.html":       "<!doctype html><title>Short Links</title><div id=\"app\"></div>",
		"favicon.png":      pngMagic + "fake-favicon-body",
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

