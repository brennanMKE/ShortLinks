package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	return string(out)
}

func hasVary(h http.Header) bool { return hasVaryAcceptEncoding(h) }

func runCompress(h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Compress(h).ServeHTTP(rec, req)
	return rec
}

func gzReq(method string) *http.Request {
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	return r
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		lines []string
		want  bool
	}{
		{[]string{""}, false},
		{[]string{"gzip"}, true},
		{[]string{"GZIP"}, true},
		{[]string{"gzip, br"}, true},
		{[]string{"br;q=1, gzip;q=0.5"}, true},
		{[]string{"gzip;q=0"}, false},
		{[]string{"*"}, true},
		{[]string{"identity"}, false},
		{[]string{"br", "gzip"}, true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, l := range c.lines {
			if l != "" {
				r.Header.Add("Accept-Encoding", l)
			}
		}
		if got := AcceptsGzip(r); got != c.want {
			t.Errorf("AcceptsGzip(%q) = %v, want %v", c.lines, got, c.want)
		}
	}
}

func TestCompressGzipsTextTypes(t *testing.T) {
	body := strings.Repeat("hello compressible world\n", 200)
	for _, ct := range []string{"text/html; charset=utf-8", "application/javascript", "application/json", "image/svg+xml", "application/xml"} {
		t.Run(ct, func(t *testing.T) {
			rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", ct)
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				_, _ = io.WriteString(w, body)
			}, gzReq(http.MethodGet))
			if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip", got)
			}
			if !hasVary(rec.Header()) {
				t.Errorf("Vary missing Accept-Encoding: %q", rec.Header().Values("Vary"))
			}
			if cl := rec.Header().Get("Content-Length"); cl != "" {
				t.Errorf("Content-Length = %q, want stripped", cl)
			}
			if got := gunzip(t, rec.Body.Bytes()); got != body {
				t.Errorf("decompressed body mismatch")
			}
		})
	}
}

func TestCompressIdentityWithoutAcceptEncoding(t *testing.T) {
	rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "plain body")
	}, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "plain body" {
		t.Fatalf("got encoding %q body %q", rec.Header().Get("Content-Encoding"), rec.Body.String())
	}
	if !hasVary(rec.Header()) {
		t.Errorf("Vary: Accept-Encoding missing on identity response")
	}
}

func TestCompressSkipsBinaryTypes(t *testing.T) {
	for _, ct := range []string{"image/png", "image/jpeg", "image/webp", "font/woff2"} {
		rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			_, _ = io.WriteString(w, "binarybytes")
		}, gzReq(http.MethodGet))
		if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != "binarybytes" {
			t.Errorf("%s: compressed or altered", ct)
		}
	}
}

func TestCompressSkipsEventStreamAndFlushes(t *testing.T) {
	rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: x\n\n")
		f, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("wrapper is not an http.Flusher")
		}
		f.Flush()
	}, gzReq(http.MethodGet))
	if rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("event-stream was compressed")
	}
	if !rec.Flushed {
		t.Errorf("Flush did not reach the underlying writer")
	}
}

func TestCompressFlushWhileCompressing(t *testing.T) {
	var flushed []byte
	rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"a":`)
		w.(http.Flusher).Flush()
		// Review (#0184): what Flush handed the client must already decode
		// to the first write, or a compressed stream is buffered in gzip.
		flushed = append([]byte(nil), w.(interface{ Unwrap() http.ResponseWriter }).Unwrap().(*httptest.ResponseRecorder).Body.Bytes()...)
		_, _ = io.WriteString(w, `1}`)
	}, gzReq(http.MethodGet))
	if !rec.Flushed {
		t.Errorf("not flushed")
	}
	zr, err := gzip.NewReader(bytes.NewReader(flushed))
	if err != nil {
		t.Fatalf("flushed prefix has no gzip header: %v", err)
	}
	prefix := make([]byte, len(`{"a":`))
	if _, err := io.ReadFull(zr, prefix); err != nil || string(prefix) != `{"a":` {
		t.Errorf("flushed prefix decodes to %q, %v; want the first write", prefix, err)
	}
	if got := gunzip(t, rec.Body.Bytes()); got != `{"a":1}` {
		t.Errorf("body = %q", got)
	}
}

func TestCompressSkipsAlreadyEncoded(t *testing.T) {
	raw := []byte{0x1f, 0x8b, 1, 2, 3}
	rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(raw)
	}, gzReq(http.MethodGet))
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Errorf("pre-encoded body altered")
	}
}

func TestCompressSkipsNoBodyResponses(t *testing.T) {
	cases := []struct {
		name   string
		method string
		code   int
		hdr    map[string]string
	}{
		{"304", http.MethodGet, http.StatusNotModified, nil},
		{"204", http.MethodGet, http.StatusNoContent, nil},
		{"206", http.MethodGet, http.StatusPartialContent, map[string]string{"Content-Range": "bytes 0-1/10"}},
		{"HEAD", http.MethodHead, http.StatusOK, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				for k, v := range c.hdr {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.code)
			}, gzReq(c.method))
			if rec.Header().Get("Content-Encoding") != "" {
				t.Errorf("Content-Encoding set on %s", c.name)
			}
		})
	}
}

func TestCompressWeakensStrongETag(t *testing.T) {
	for in, want := range map[string]string{`"abc"`: `W/"abc"`, `W/"x"`: `W/"x"`} {
		rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("ETag", in)
			_, _ = io.WriteString(w, "body")
		}, gzReq(http.MethodGet))
		if got := rec.Header().Get("ETag"); got != want {
			t.Errorf("ETag %s -> %s, want %s", in, got, want)
		}
	}
}

func TestCompressUnwrap(t *testing.T) {
	runCompress(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(interface{ Unwrap() http.ResponseWriter }); !ok {
			t.Error("wrapper has no Unwrap")
		}
		w.Header().Set("Content-Type", "text/plain")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush: %v", err)
		}
	}, gzReq(http.MethodGet))
}

func TestCompressSkipsRedirectAndHeroPaths(t *testing.T) {
	for _, p := range []string{"/u/abc", "/public/hero"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := runCompress(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, strings.Repeat("x", 500))
		}, req)
		if rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s was compressed", p)
		}
	}
}
