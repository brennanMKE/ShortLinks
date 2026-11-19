package middleware

import (
	"compress/gzip"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
)

// compressibleTypes are the media types Compress gzips (#0184), compared
// after mime.ParseMediaType strips parameters. Everything else passes
// through untouched, deliberately: text/event-stream (the SSE stream on
// /api/events and the MCP SDK's streams on /mcp) must reach the client
// unbuffered, text/csv (the campaign export) flushes as it goes, and image
// and font types are already compressed.
var compressibleTypes = map[string]bool{
	"text/html":                 true,
	"text/css":                  true,
	"text/plain":                true,
	"text/xml":                  true,
	"text/javascript":           true,
	"application/javascript":    true,
	"application/json":          true,
	"application/xml":           true,
	"application/atom+xml":      true,
	"application/rss+xml":       true,
	"application/manifest+json": true,
	"image/svg+xml":             true,
}

var gzipPool = sync.Pool{New: func() any {
	gz, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
	return gz
}}

// AcceptsGzip reports whether the request's Accept-Encoding allows gzip: a
// "gzip" or "*" coding whose q is absent or non-zero.
func AcceptsGzip(r *http.Request) bool {
	for _, line := range r.Header.Values("Accept-Encoding") {
		for _, tok := range strings.Split(line, ",") {
			parts := strings.Split(tok, ";")
			coding := strings.ToLower(strings.TrimSpace(parts[0]))
			if coding != "gzip" && coding != "*" {
				continue
			}
			ok := true
			for _, p := range parts[1:] {
				p = strings.TrimSpace(p)
				if len(p) > 2 && strings.EqualFold(p[:2], "q=") {
					switch strings.TrimSpace(p[2:]) {
					case "0", "0.", "0.0", "0.00", "0.000":
						ok = false
					}
				}
			}
			if ok {
				return true
			}
		}
	}
	return false
}

// Compress gzips text responses when the client accepts gzip (#0184). The
// decision is made once, at WriteHeader, from the status, method and
// response headers. A response that already carries Content-Encoding passes
// through unchanged: that is how SPAHandler's precompressed /assets/* bytes
// reach the client without being compressed twice. text/event-stream is not
// in compressibleTypes, so SSE stays unbuffered; the wrapper still
// implements http.Flusher and Unwrap so streaming handlers and
// http.ResponseController keep working.
func Compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirects (/u/{key}) carry a tiny body and are the hot path;
		// /public/hero serves an already-compressed image. Pass them through.
		if strings.HasPrefix(r.URL.Path, "/u/") || strings.HasPrefix(r.URL.Path, "/public/hero") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, req: r}
		next.ServeHTTP(gw, r)
		if gw.gz != nil {
			_ = gw.gz.Close()
			gw.gz.Reset(io.Discard)
			gzipPool.Put(gw.gz)
		}
	})
}

type gzipWriter struct {
	http.ResponseWriter
	req         *http.Request
	wroteHeader bool
	gz          *gzip.Writer
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.wroteHeader {
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.wroteHeader = true
	h := g.Header()
	compressible := false
	if mt, _, err := mime.ParseMediaType(h.Get("Content-Type")); err == nil {
		compressible = compressibleTypes[strings.ToLower(mt)]
	}
	if compressible && !hasVaryAcceptEncoding(h) {
		h.Add("Vary", "Accept-Encoding")
	}
	if compressible &&
		AcceptsGzip(g.req) &&
		g.req.Method != http.MethodHead &&
		code >= 200 && code != http.StatusNoContent && code != http.StatusPartialContent && code != http.StatusNotModified &&
		h.Get("Content-Encoding") == "" && h.Get("Content-Range") == "" {
		h.Set("Content-Encoding", "gzip")
		h.Del("Content-Length")
		if et := h.Get("ETag"); et != "" && !strings.HasPrefix(et, "W/") {
			h.Set("ETag", "W/"+et)
		}
		g.ResponseWriter.WriteHeader(code)
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(g.ResponseWriter)
		g.gz = gz
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func hasVaryAcceptEncoding(h http.Header) bool {
	for _, v := range h.Values("Vary") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "Accept-Encoding") {
				return true
			}
		}
	}
	return false
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Flush flushes buffered gzip output, then the underlying writer.
func (g *gzipWriter) Flush() {
	if !g.wroteHeader {
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
