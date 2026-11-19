package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCompressSSEStreamsOverRealSocket proves an event stream behind Compress
// reaches the client event by event: the first event is read before the
// handler is allowed to write the second.
func TestCompressSSEStreamsOverRealSocket(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		_ = http.NewResponseController(w).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
	})))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" {
		close(release)
		t.Fatalf("event stream compressed: %q", resp.Header.Get("Content-Encoding"))
	}
	buf := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "data: one\n\n" {
		close(release)
		t.Fatalf("first event = %q, %v", buf, err)
	}
	close(release)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "data: two\n\n" {
		t.Errorf("second event = %q", rest)
	}
}
