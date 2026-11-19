// Package openapi embeds the hand-written OpenAPI 3.1 description of the REST
// API (#0148) and serves it at GET /api/openapi.yaml.
//
// The YAML lives here, not in docs/, because go:embed cannot reach outside the
// package directory; docs/ link to this file. A test in cmd/shortlinks keeps
// its paths and methods in step with the routes buildMux registers.
package openapi

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

//go:embed openapi.yaml
var spec []byte

// Spec returns the embedded OpenAPI document. The slice must not be modified.
func Spec() []byte { return spec }

// Handler serves the spec as application/yaml. It needs no authentication:
// the document holds no secrets.
func Handler() http.Handler {
	length := strconv.Itoa(len(spec))
	sum := sha256.Sum256(spec)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Cache-Control", "no-cache")
		if etagMatches(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", "application/yaml")
		h.Set("Content-Length", length)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(spec)
		}
	})
}

// etagMatches reports whether an If-None-Match header lists etag (weak
// comparison, as RFC 9110 requires for If-None-Match) or is "*".
func etagMatches(header, etag string) bool {
	for _, c := range strings.Split(header, ",") {
		c = strings.TrimPrefix(strings.TrimSpace(c), "W/")
		if c == "*" || c == etag {
			return true
		}
	}
	return false
}
