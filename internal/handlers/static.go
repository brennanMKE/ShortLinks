package handlers

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// Cache-Control values (#0184). Hashed /assets/* files never change under their
// name, so they are cached for a year; the HTML shell is revalidated on every
// load so a new release is picked up immediately.
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheShell     = "no-cache"
)

// precompressExts are the /assets/* extensions gzipped once at construction.
var precompressExts = map[string]bool{
	".js": true, ".css": true, ".svg": true, ".json": true,
	".map": true, ".txt": true, ".xml": true, ".html": true,
}

// SPAHandler serves the embedded Svelte single-page app. It is mounted on the
// catch-all "GET /" pattern, which is the least-specific route under the Go 1.22
// ServeMux, so the explicit /health, /u/{key}, /api/*, /auth/*, /account/*, and
// /admin/* patterns always win over it.
//
// Behavior:
//   - A request whose path maps to a real embedded file (e.g. the hashed
//     /assets/index-*.js or /assets/index-*.css) is served directly with the
//     correct Content-Type via http.FileServerFS.
//   - Any other path (the root "/", or SPA deep links like /dashboard and
//     /account that have no client-side router) falls back to index.html so a
//     hard refresh on a deep link still loads the app.
//
// Caching and compression (#0184): hashed /assets/* files carry an immutable
// Cache-Control, the HTML shell carries no-cache, and a missing /assets/* file
// is a 404 with no-cache (never the shell, which a chunk cache would keep).
// Compressible assets are gzipped once at construction and served from memory
// with Content-Encoding: gzip to clients that accept it and send no Range;
// middleware.Compress sees the encoding already set and passes them through.
type SPAHandler struct {
	dist    fs.FS
	fileSrv http.Handler
	// gz maps an assets/* fs name to its gzipped bytes.
	gz map[string][]byte
}

// NewSPAHandler constructs an SPAHandler over the embedded dist FS. The provided
// fs.FS must be rooted at the build output so index.html and assets/* live at
// its root (see web.DistFS).
func NewSPAHandler(dist fs.FS) *SPAHandler {
	return &SPAHandler{
		dist:    dist,
		fileSrv: http.FileServerFS(dist),
		gz:      precompressAssets(dist),
	}
}

// precompressAssets gzips every compressible regular file under assets/,
// keeping only those that shrink. A missing assets/ (the committed placeholder
// dist) yields an empty map; a per-file error is logged and the file is served
// uncompressed.
func precompressAssets(dist fs.FS) map[string][]byte {
	out := map[string][]byte{}
	err := fs.WalkDir(dist, "assets", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			log.Printf("spa precompress: %s: %v", name, err)
			return nil
		}
		if !d.Type().IsRegular() || !precompressExts[path.Ext(name)] {
			return nil
		}
		raw, err := fs.ReadFile(dist, name)
		if err != nil {
			log.Printf("spa precompress: read %s: %v", name, err)
			return nil
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if _, err := zw.Write(raw); err != nil {
			log.Printf("spa precompress: gzip %s: %v", name, err)
			return nil
		}
		if err := zw.Close(); err != nil {
			log.Printf("spa precompress: gzip %s: %v", name, err)
			return nil
		}
		if buf.Len() < len(raw) {
			out[name] = buf.Bytes()
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("spa precompress: %v", err)
	}
	return out
}

// ServeHTTP implements http.Handler. It serves the requested embedded file when
// it exists, otherwise it serves index.html so the SPA's client-side view
// routing works on a hard refresh of a deep link.
func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// fs paths are relative and never start with a slash; the root request maps
	// to index.html, handled below.
	name := strings.TrimPrefix(r.URL.Path, "/")

	// An unmatched /api path is a JSON 404, never the HTML shell (#0192). Every
	// real /api route is a more specific mux pattern, so reaching here means no
	// route is mounted (for example /api/reports in dev mode).
	if name == "api" || strings.HasPrefix(name, "api/") {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	if name != "" && h.fileExists(name) {
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", cacheImmutable)
		}
		if gzBody, ok := h.gz[name]; ok {
			w.Header().Add("Vary", "Accept-Encoding")
			if middleware.AcceptsGzip(r) && r.Header.Get("Range") == "" {
				if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Length", strconv.Itoa(len(gzBody)))
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = w.Write(gzBody)
				}
				return
			}
		}
		h.fileSrv.ServeHTTP(w, r)
		return
	}

	// A missing hashed asset is a 404, never the HTML shell, and is marked
	// no-cache so a stale chunk request cannot be pinned (#0184, #0186).
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", cacheShell)
		http.NotFound(w, r)
		return
	}

	// A request for a path that looks like a static asset (it has a file
	// extension, e.g. /favicon.ico) but has no matching embedded file must NOT
	// fall back to the HTML shell. Returning index.html for /favicon.ico made
	// browsers receive an HTML document for an icon URL, which they can cache as
	// a broken/garbage icon. Such paths get a clean 404 instead. SPA deep links
	// (/dashboard, /account — no extension) still fall through to index.html.
	if path.Ext(name) != "" {
		w.Header().Set("Cache-Control", cacheShell)
		http.NotFound(w, r)
		return
	}

	h.serveIndex(w, r)
}

// fileExists reports whether name resolves to a regular file in the embedded
// dist FS. Directories return false so a directory path falls through to the
// index.html SPA fallback rather than rendering a file listing.
func (h *SPAHandler) fileExists(name string) bool {
	info, err := fs.Stat(h.dist, name)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// serveIndex writes dist/index.html as the SPA shell. Used for the root path and
// every unmatched deep link.
func (h *SPAHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(h.dist, "index.html")
	if err != nil {
		http.Error(w, "SPA index.html not found in embedded assets", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheShell)
	_, _ = w.Write(data)
}
