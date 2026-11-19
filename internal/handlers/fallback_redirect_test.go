package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/cache"
)

// fallbackResolver is fakeResolver plus the optional FallbackResolver
// capability (#0180).
type fallbackResolver struct {
	fakeResolver
	fb  map[string]string
	err error
}

func (f *fallbackResolver) FallbackURL(_ context.Context, key string) (string, error) {
	return f.fb[key], f.err
}

func fbHandler(links map[string]cache.CachedLink, fb map[string]string) (*RedirectHandler, *recordingClicker) {
	c := newRecordingClicker()
	return newHandler(&fallbackResolver{fakeResolver: fakeResolver{links: links}, fb: fb}, c), c
}

var past = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func TestRedirectFallback_ExpiredAndInactive(t *testing.T) {
	h, clicker := fbHandler(map[string]cache.CachedLink{
		"old": {DestinationURL: "https://d.example", Active: true, ExpiresAt: &past},
		"off": {DestinationURL: "https://d.example", Active: false},
	}, map[string]string{
		"old": "https://app.example/invite-expired",
		"off": "https://app.example/page?x=1&y=a%20b",
	})

	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/old", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://app.example/invite-expired?reason=expired" {
		t.Fatalf("expired: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = serve(h, httptest.NewRequest(http.MethodGet, "/u/off", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://app.example/page?x=1&y=a%20b&reason=inactive" {
		t.Fatalf("inactive: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	clicker.expectNoClick(t) // fallback hits never record a click
}

func TestRedirectFallback_NoFallbackKeepsTodaysStatuses(t *testing.T) {
	h, _ := fbHandler(map[string]cache.CachedLink{
		"old": {Active: true, ExpiresAt: &past},
		"off": {Active: false},
	}, map[string]string{})
	if c := serve(h, httptest.NewRequest(http.MethodGet, "/u/old", nil)).Code; c != http.StatusGone {
		t.Fatalf("expired without fallback = %d, want 410", c)
	}
	if c := serve(h, httptest.NewRequest(http.MethodGet, "/u/off", nil)).Code; c != http.StatusNotFound {
		t.Fatalf("inactive without fallback = %d, want 404", c)
	}
}

func TestRedirectFallback_DeniedNeverFallsBack(t *testing.T) {
	h, _ := fbHandler(map[string]cache.CachedLink{
		"bad": {Active: false, DeniedReason: 1},
	}, map[string]string{"bad": "https://app.example/x"})
	if c := serve(h, httptest.NewRequest(http.MethodGet, "/u/bad", nil)); c.Code != http.StatusNotFound || c.Header().Get("Location") != "" {
		t.Fatalf("denied = %d %q, want 404 no Location", c.Code, c.Header().Get("Location"))
	}
}

func TestRedirectFallback_UnknownKeyStill404(t *testing.T) {
	h, _ := fbHandler(map[string]cache.CachedLink{}, map[string]string{"nope": "https://app.example/x"})
	if c := serve(h, httptest.NewRequest(http.MethodGet, "/u/nope", nil)).Code; c != http.StatusNotFound {
		t.Fatalf("unknown = %d, want 404", c)
	}
}

func TestRedirectFallback_LookupErrorFallsThrough(t *testing.T) {
	c := newRecordingClicker()
	h := newHandler(&fallbackResolver{
		fakeResolver: fakeResolver{links: map[string]cache.CachedLink{"old": {Active: true, ExpiresAt: &past}}},
		err:          context.DeadlineExceeded,
	}, c)
	if got := serve(h, httptest.NewRequest(http.MethodGet, "/u/old", nil)).Code; got != http.StatusGone {
		t.Fatalf("= %d, want 410", got)
	}
}

func TestRedirectFallback_LiveLinkIgnoresFallback(t *testing.T) {
	h, clicker := fbHandler(map[string]cache.CachedLink{
		"live": {DestinationURL: "https://d.example", Active: true},
	}, map[string]string{"live": "https://app.example/x"})
	rec := serve(h, httptest.NewRequest(http.MethodGet, "/u/live", nil))
	if rec.Header().Get("Location") != "https://d.example" {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	clicker.await(t)
}

func TestWithReason_KeepsQueryAndFragment(t *testing.T) {
	cases := map[string]string{
		"https://a.example/p":                 "https://a.example/p?reason=expired",
		"https://a.example/p?z=1&a=%2F+b":     "https://a.example/p?z=1&a=%2F+b&reason=expired",
		"https://a.example/p#frag":            "https://a.example/p?reason=expired#frag",
		"https://a.example/p?z=1&a=2#f%20rag": "https://a.example/p?z=1&a=2&reason=expired#f%20rag",
	}
	for in, want := range cases {
		got, ok := withReason(in, "expired")
		if !ok || got != want {
			t.Errorf("withReason(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestPointsAtOwnShortLinks(t *testing.T) {
	base := "https://go.example.com"
	loops := []string{
		"https://go.example.com/u/k", "https://go.example.com:443/u/k", "https://go.example.com./u/k",
		"https://GO.Example.COM/u/k", "https://go.example.com/./u/k", "https://go.example.com/x/../u/k",
		"https://go.example.com//u/k", "https://go.example.com/u",
	}
	for _, l := range loops {
		if !pointsAtOwnShortLinks(base, l) {
			t.Errorf("%q should be a self-loop", l)
		}
	}
	for _, ok := range []string{"https://go.example.com/expired", "https://other.example.com/u/k", "https://go.example.com:8443/u/k", "https://go.example.com/user"} {
		if pointsAtOwnShortLinks(base, ok) {
			t.Errorf("%q should be allowed", ok)
		}
	}
	if !pointsAtOwnShortLinks("http://go.example.com", "http://go.example.com:80/u/k") {
		t.Error("http default port 80 not treated as equal")
	}
}
