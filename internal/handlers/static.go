package handlers

import (
	"io/fs"
	"net/http"
	"strings"
)

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
type SPAHandler struct {
	dist    fs.FS
	fileSrv http.Handler
}

// NewSPAHandler constructs an SPAHandler over the embedded dist FS. The provided
// fs.FS must be rooted at the build output so index.html and assets/* live at
// its root (see web.DistFS).
func NewSPAHandler(dist fs.FS) *SPAHandler {
	return &SPAHandler{
		dist:    dist,
		fileSrv: http.FileServerFS(dist),
	}
}

// ServeHTTP implements http.Handler. It serves the requested embedded file when
// it exists, otherwise it serves index.html so the SPA's client-side view
// routing works on a hard refresh of a deep link.
func (h *SPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// fs paths are relative and never start with a slash; the root request maps
	// to index.html, handled below.
	name := strings.TrimPrefix(r.URL.Path, "/")

	if name != "" && h.fileExists(name) {
		h.fileSrv.ServeHTTP(w, r)
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
	_, _ = w.Write(data)
}
