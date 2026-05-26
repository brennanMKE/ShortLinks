package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/cache"
)

// fakeResolver is an in-memory LinkResolver for tests.
type fakeResolver struct {
	links map[string]cache.CachedLink
	err   error
}

func (f *fakeResolver) Resolve(_ context.Context, key string) (cache.CachedLink, bool, error) {
	if f.err != nil {
		return cache.CachedLink{}, false, f.err
	}
	link, ok := f.links[key]
	if !ok || link.Negative {
		return cache.CachedLink{}, false, nil
	}
	return link, true, nil
}

// recordingClicker captures clicks on a buffered channel so the test can
// deterministically observe the asynchronous RecordClick call without sleeping.
type recordingClicker struct {
	clicks chan ClickInfo
}

func newRecordingClicker() *recordingClicker {
	return &recordingClicker{clicks: make(chan ClickInfo, 1)}
}

func (c *recordingClicker) RecordClick(info ClickInfo) {
	c.clicks <- info
}

// await returns the recorded click or fails if none arrives in time.
func (c *recordingClicker) await(t *testing.T) ClickInfo {
	t.Helper()
	select {
	case info := <-c.clicks:
		return info
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for recorded click")
		return ClickInfo{}
	}
}

// expectNoClick fails if a click is recorded within a short window.
func (c *recordingClicker) expectNoClick(t *testing.T) {
	t.Helper()
	select {
	case info := <-c.clicks:
		t.Fatalf("unexpected click recorded: %+v", info)
	case <-time.After(100 * time.Millisecond):
	}
}

// serveMux wires the handler under the real Go 1.22+ pattern route so
// r.PathValue("key") is populated exactly as in production.
func serve(h *RedirectHandler, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("GET /u/{key}", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func newHandler(resolver LinkResolver, recorder ClickRecorder) *RedirectHandler {
	h := NewRedirectHandler(resolver, recorder)
	// Pin time for deterministic expiry checks.
	h.now = func() time.Time { return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC) }
	return h
}

func TestActiveLinkRedirects302(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"abc123": {DestinationURL: "https://example.com/landing", Active: true},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/abc123", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "https://example.com/landing" {
		t.Fatalf("Location = %q, want %q", got, "https://example.com/landing")
	}
	clicker.await(t) // click still recorded for an active link
}

func TestUnknownKeyReturns404(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/missing", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	clicker.expectNoClick(t)
}

func TestNegativeCacheEntryReturns404(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"neg": {Negative: true},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/neg", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	clicker.expectNoClick(t)
}

func TestInactiveLinkReturns404(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"off": {DestinationURL: "https://example.com", Active: false},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/off", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	clicker.expectNoClick(t)
}

func TestExpiredLinkReturns410(t *testing.T) {
	past := time.Date(2026, 5, 25, 11, 0, 0, 0, time.UTC) // before pinned now
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"old": {DestinationURL: "https://example.com", Active: true, ExpiresAt: &past},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/old", nil))

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGone)
	}
	clicker.expectNoClick(t)
}

func TestFutureExpiryStillRedirects(t *testing.T) {
	future := time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC) // after pinned now
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"fut": {DestinationURL: "https://example.com", Active: true, ExpiresAt: &future},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/fut", nil))

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
}

func TestUTMParamsMergedAndRecorded(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"abc123": {DestinationURL: "https://example.com/page", Active: true},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	req := httptest.NewRequest(http.MethodGet,
		"/u/abc123?utm_source=email&utm_medium=newsletter&utm_campaign=launch&utm_term=go&utm_content=hero", nil)
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Referer", "https://referrer.example")
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")

	rec := serve(h, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}

	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := loc.Query()
	wantUTM := map[string]string{
		"utm_source":   "email",
		"utm_medium":   "newsletter",
		"utm_campaign": "launch",
		"utm_term":     "go",
		"utm_content":  "hero",
	}
	for k, want := range wantUTM {
		if got := q.Get(k); got != want {
			t.Errorf("Location %s = %q, want %q", k, got, want)
		}
	}
	if loc.Scheme != "https" || loc.Host != "example.com" || loc.Path != "/page" {
		t.Errorf("Location base changed: %s", loc.String())
	}

	info := clicker.await(t)
	if info.Key != "abc123" {
		t.Errorf("click Key = %q, want abc123", info.Key)
	}
	if info.UTMSource != "email" || info.UTMMedium != "newsletter" || info.UTMCampaign != "launch" ||
		info.UTMTerm != "go" || info.UTMContent != "hero" {
		t.Errorf("click UTM not captured: %+v", info)
	}
	if info.UserAgent != "test-agent" {
		t.Errorf("click UserAgent = %q, want test-agent", info.UserAgent)
	}
	if info.Referer != "https://referrer.example" {
		t.Errorf("click Referer = %q, want https://referrer.example", info.Referer)
	}
	if info.IPAddress != "203.0.113.7" {
		t.Errorf("click IPAddress = %q, want 203.0.113.7 (first XFF hop)", info.IPAddress)
	}
}

func TestDestinationQueryParamsPreservedOnMerge(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"abc123": {DestinationURL: "https://example.com/page?ref=site&utm_source=organic", Active: true},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	req := httptest.NewRequest(http.MethodGet, "/u/abc123?utm_source=email&utm_campaign=launch", nil)
	rec := serve(h, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}

	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := loc.Query()
	if got := q.Get("ref"); got != "site" {
		t.Errorf("pre-existing param ref = %q, want site (must be preserved)", got)
	}
	if got := q.Get("utm_source"); got != "email" {
		t.Errorf("utm_source = %q, want email (inbound must override destination)", got)
	}
	if got := q.Get("utm_campaign"); got != "launch" {
		t.Errorf("utm_campaign = %q, want launch", got)
	}
	clicker.await(t)
}

func TestResolverErrorReturns500(t *testing.T) {
	resolver := &fakeResolver{err: context.DeadlineExceeded}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/abc123", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	clicker.expectNoClick(t)
}

func TestRemoteAddrUsedWhenNoForwardedFor(t *testing.T) {
	resolver := &fakeResolver{links: map[string]cache.CachedLink{
		"abc123": {DestinationURL: "https://example.com", Active: true},
	}}
	clicker := newRecordingClicker()
	h := newHandler(resolver, clicker)

	req := httptest.NewRequest(http.MethodGet, "/u/abc123", nil)
	req.RemoteAddr = "198.51.100.5:54321"
	serve(h, req)

	info := clicker.await(t)
	if info.IPAddress != "198.51.100.5" {
		t.Errorf("click IPAddress = %q, want 198.51.100.5 (port stripped)", info.IPAddress)
	}
}
