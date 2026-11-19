package handlers

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestVersionHandlerHeadersAndBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /api/version", NewVersionHandler("abc123"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Build string `json:"build"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Build != "abc123" {
		t.Errorf("build = %q, want abc123", body.Build)
	}
}

const shell = `<html><head><meta name="sl-build" content="%%SL_BUILD%%" /></head><body></body></html>`

func distWith(index string) fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte(index)},
		"assets/app.js": {Data: []byte("console.log('%%SL_BUILD%%')")},
	}
}

func TestStampBuildServedThroughSPAHandler(t *testing.T) {
	h := NewSPAHandler(StampBuild(distWith(shell), `a"b<c`))
	for _, p := range []string{"/", "/some/deep/link"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", p, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<meta name="sl-build" content="a&#34;b&lt;c" />`) {
			t.Errorf("%s: meta tag missing or unescaped in %q", p, body)
		}
		if strings.Contains(body, BuildToken) {
			t.Errorf("%s: token left in body", p)
		}
	}
}

func TestStampBuildLeavesOtherFilesAlone(t *testing.T) {
	dist := StampBuild(distWith(shell), "x1")
	got, err := fs.ReadFile(dist, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), BuildToken) {
		t.Errorf("assets/app.js was rewritten: %q", got)
	}
}

func TestStampBuildOpenAndStatAgree(t *testing.T) {
	dist := StampBuild(distWith(shell), "x1")
	f, err := dist.Open("index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	want, _ := fs.ReadFile(dist, "index.html")
	if info.Size() != int64(len(want)) {
		t.Errorf("Stat size = %d, body = %d", info.Size(), len(want))
	}
}

func TestBuildIDChangesWithVCSRevision(t *testing.T) {
	a := buildID(distWith(shell), "", "rev1/false")
	b := buildID(distWith(shell), "", "rev2/false")
	if a == b {
		t.Errorf("a new revision with an identical bundle must change the id: %q", a)
	}
}

func TestBuildIDOverrideAndHash(t *testing.T) {
	if got := BuildID(distWith(shell), "deadbee"); got != "deadbee" {
		t.Errorf("override: got %q", got)
	}
	a := BuildID(distWith(shell), "")
	b := BuildID(distWith(strings.Replace(shell, "html", "HTML", 1)), "")
	if len(a) != 12 || a == b {
		t.Errorf("hash ids should be 12 chars and differ by content: %q %q", a, b)
	}
	if a != BuildID(distWith(shell), "") {
		t.Errorf("hash id is not stable")
	}
	if got := BuildID(fstest.MapFS{}, ""); got != "unknown" {
		t.Errorf("missing index.html: got %q", got)
	}
}
