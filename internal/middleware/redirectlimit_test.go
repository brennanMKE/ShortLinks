package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// redirectStub answers 302 for key "live", 410 for "gone", else 404, and
// counts invocations (a 429 must not reach it, so no click gets recorded).
func redirectStub(calls *int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		switch r.URL.Path {
		case "/u/live":
			w.Header().Set("Location", "https://example.com")
			w.WriteHeader(http.StatusFound)
		case "/u/gone":
			http.Error(w, "gone", http.StatusGone)
		default:
			http.NotFound(w, r)
		}
	})
}

func hit(h http.Handler, path, xff, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newTestLimiter(rate, miss int) (*RedirectLimiter, *clock, *int) {
	rl := NewRedirectLimiter(rate, miss)
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	rl.now = c.now
	return rl, c, new(int)
}

func TestRedirectLimiter_HitsPass(t *testing.T) {
	rl, _, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 120; i++ {
		if got := hit(h, "/u/live", "", "192.0.2.1:1").Code; got != http.StatusFound {
			t.Fatalf("hit %d = %d, want 302", i, got)
		}
	}
	rec := hit(h, "/u/live", "", "192.0.2.1:1")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("hit 121 = %d retry-after=%q, want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestRedirectLimiter_MissesExhaustStrictBucket(t *testing.T) {
	rl, c, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 10; i++ { // burst = 10
		path := "/u/nope"
		if i%2 == 1 {
			path = "/u/gone" // 410 counts as a miss too
		}
		if got := hit(h, path, "", "192.0.2.1:1").Code; got != http.StatusNotFound && got != http.StatusGone {
			t.Fatalf("miss %d = %d", i, got)
		}
	}
	before := *calls
	rec := hit(h, "/u/live", "", "192.0.2.1:1") // even a live key is refused
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("live key after misses = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "3" { // 20/min = 1 token per 3s
		t.Fatalf("Retry-After = %q, want 3", got)
	}
	if *calls != before {
		t.Fatal("429 reached the redirect handler (would record a click)")
	}
	if got := hit(h, "/u/live", "", "192.0.2.2:1").Code; got != http.StatusFound {
		t.Fatalf("other IP = %d, want 302", got)
	}
	c.t = c.t.Add(3 * time.Second)
	if got := hit(h, "/u/live", "", "192.0.2.1:1").Code; got != http.StatusFound {
		t.Fatalf("after refill = %d, want 302", got)
	}
}

func TestRedirectLimiter_IdleEntriesEvicted(t *testing.T) {
	rl, c, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 50; i++ {
		hit(h, "/u/nope", "", fmt.Sprintf("192.0.2.%d:1", i))
	}
	if got := rl.Len(); got != 50 {
		t.Fatalf("Len = %d, want 50", got)
	}
	c.t = c.t.Add(redirectIdleTTL + time.Minute)
	hit(h, "/u/live", "", "203.0.113.9:1")
	rl.Sweep()
	if got := rl.Len(); got != 1 {
		t.Fatalf("Len after idle = %d, want 1", got)
	}
}

// Behind Apache on loopback RemoteAddr is always 127.0.0.1; the client is the
// last X-Forwarded-For entry, and spoofed leading entries must not matter.
func TestRedirectLimiter_UsesForwardedClientIP(t *testing.T) {
	rl, _, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 10; i++ {
		hit(h, "/u/nope", fmt.Sprintf("10.9.9.%d, 203.0.113.50", i), "127.0.0.1:5000")
	}
	if got := hit(h, "/u/live", "1.2.3.4, 203.0.113.50", "127.0.0.1:5001").Code; got != http.StatusTooManyRequests {
		t.Fatalf("scanner behind proxy = %d, want 429 (bucketed by forwarded IP, spoof ignored)", got)
	}
	if got := hit(h, "/u/live", "203.0.113.51", "127.0.0.1:5002").Code; got != http.StatusFound {
		t.Fatalf("other client behind same proxy = %d, want 302", got)
	}
}

func TestRedirectLimiter_ConcurrentMissesBoundedByBurst(t *testing.T) {
	rl, _, _ := newTestLimiter(120, 20)
	var mu sync.Mutex
	passed := 0
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		passed++
		mu.Unlock()
		http.NotFound(w, r)
	}))
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hit(h, "/u/nope", "", "192.0.2.1:1")
		}()
	}
	wg.Wait()
	if passed > 10 {
		t.Fatalf("%d concurrent misses admitted, want <= burst 10", passed)
	}
}

func TestRedirectLimiter_HitRefundsMissToken(t *testing.T) {
	rl, _, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 50; i++ { // far more hits than the miss burst
		if got := hit(h, "/u/live", "", "192.0.2.1:1").Code; got != http.StatusFound {
			t.Fatalf("hit %d = %d, want 302", i, got)
		}
	}
}

func TestRedirectLimiter_IPv6SharesSlash64(t *testing.T) {
	rl, _, calls := newTestLimiter(120, 20)
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 10; i++ {
		hit(h, "/u/nope", "", fmt.Sprintf("[2001:db8:1:2::%x]:1", i+1))
	}
	if got := hit(h, "/u/live", "", "[2001:db8:1:2:ffff::9]:1").Code; got != http.StatusTooManyRequests {
		t.Fatalf("same /64 = %d, want 429", got)
	}
	if got := hit(h, "/u/live", "", "[2001:db8:1:3::1]:1").Code; got != http.StatusFound {
		t.Fatalf("other /64 = %d, want 302", got)
	}
}

func TestRedirectLimiter_EntryCapEvicts(t *testing.T) {
	rl, c, calls := newTestLimiter(120, 20)
	rl.maxEntries = 5
	h := rl.Middleware(redirectStub(calls))
	for i := 0; i < 5; i++ {
		if got := hit(h, "/u/live", "", fmt.Sprintf("192.0.2.%d:1", i)).Code; got != http.StatusFound {
			t.Fatalf("ip %d = %d", i, got)
		}
	}
	for i := 0; i < 20; i++ { // many new IPs at the cap are all admitted
		if got := hit(h, "/u/live", "", fmt.Sprintf("198.51.100.%d:1", i)).Code; got != http.StatusFound {
			t.Fatalf("new IP %d at cap = %d, want 302", i, got)
		}
		if got := rl.Len(); got > 5 {
			t.Fatalf("Len = %d, want <= cap 5", got)
		}
	}
	_ = c
}
