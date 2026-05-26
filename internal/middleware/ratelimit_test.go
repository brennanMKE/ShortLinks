package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// okHandler is the downstream handler the limiter wraps; it records that it ran
// and returns 200 so tests can distinguish a passed request (200) from a
// throttled one (429).
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// do issues one request through h with the given X-Forwarded-For and RemoteAddr
// (either may be empty) and returns the recorded response.
func do(h http.Handler, xff, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/login/start", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRateLimiterBurstThenReject confirms that with burst 2, the first two
// requests from one IP pass (200) and the third is rejected (429) with the
// canonical JSON body.
func TestRateLimiterBurstThenReject(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Minute), 2)
	h := rl.Middleware(okHandler())

	const ip = "203.0.113.7"
	if got := do(h, ip, "").Code; got != http.StatusOK {
		t.Fatalf("request 1: status = %d, want 200", got)
	}
	if got := do(h, ip, "").Code; got != http.StatusOK {
		t.Fatalf("request 2: status = %d, want 200", got)
	}

	rec := do(h, ip, "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("request 3: status = %d, want 429", rec.Code)
	}
	if body := rec.Body.String(); body != `{"error":"rate_limit_exceeded"}` {
		t.Fatalf("request 3: body = %q, want rate_limit_exceeded JSON", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("request 3: Content-Type = %q, want JSON", ct)
	}
}

// TestRateLimiterPerIPIsolation confirms each IP has its own bucket: exhausting
// IP-A must not throttle IP-B.
func TestRateLimiterPerIPIsolation(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Minute), 1)
	h := rl.Middleware(okHandler())

	const ipA = "198.51.100.1"
	const ipB = "198.51.100.2"

	// Spend IP-A's single token, then confirm IP-A is throttled.
	if got := do(h, ipA, "").Code; got != http.StatusOK {
		t.Fatalf("IP-A request 1: status = %d, want 200", got)
	}
	if got := do(h, ipA, "").Code; got != http.StatusTooManyRequests {
		t.Fatalf("IP-A request 2: status = %d, want 429", got)
	}

	// IP-B has its own bucket and must still be allowed.
	if got := do(h, ipB, "").Code; got != http.StatusOK {
		t.Fatalf("IP-B request 1: status = %d, want 200 (IP-A exhaustion leaked)", got)
	}
}

// TestRateLimiterUsesXForwardedFor confirms the limiter buckets by the
// X-Forwarded-For client (set by Apache) when present: two requests with the
// same XFF but different RemoteAddr share a bucket, so the second (burst 1) is
// throttled.
func TestRateLimiterUsesXForwardedFor(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Minute), 1)
	h := rl.Middleware(okHandler())

	const client = "192.0.2.55"
	if got := do(h, client, "10.0.0.1:1111").Code; got != http.StatusOK {
		t.Fatalf("XFF request 1: status = %d, want 200", got)
	}
	// Different RemoteAddr, same XFF client → same bucket → throttled.
	if got := do(h, client, "10.0.0.2:2222").Code; got != http.StatusTooManyRequests {
		t.Fatalf("XFF request 2: status = %d, want 429 (XFF not honored for bucketing)", got)
	}

	// A first-hop list "client, proxy1, proxy2" buckets on the leftmost entry.
	if got := do(h, client+", 10.0.0.9", "10.0.0.3:3333").Code; got != http.StatusTooManyRequests {
		t.Fatalf("XFF list request: status = %d, want 429 (leftmost XFF entry not used)", got)
	}
}

// TestRateLimiterFallsBackToRemoteAddr confirms that with no X-Forwarded-For,
// the limiter buckets by RemoteAddr (host portion, port stripped): two requests
// from the same host but different ports share a bucket.
func TestRateLimiterFallsBackToRemoteAddr(t *testing.T) {
	rl := NewRateLimiter(rate.Every(time.Minute), 1)
	h := rl.Middleware(okHandler())

	if got := do(h, "", "203.0.113.42:5000").Code; got != http.StatusOK {
		t.Fatalf("RemoteAddr request 1: status = %d, want 200", got)
	}
	// Same host, different port → same bucket → throttled.
	if got := do(h, "", "203.0.113.42:6000").Code; got != http.StatusTooManyRequests {
		t.Fatalf("RemoteAddr request 2: status = %d, want 429 (port not stripped?)", got)
	}
	// A different host gets its own bucket.
	if got := do(h, "", "203.0.113.43:5000").Code; got != http.StatusOK {
		t.Fatalf("RemoteAddr different host: status = %d, want 200", got)
	}
}

// TestRateLimiterReplenishes confirms tokens refill over time: with a fast
// refill (every 5ms) and burst 1, the second immediate request is throttled but
// a request after the refill interval is allowed again. Uses a short, generous
// interval to stay non-flaky.
func TestRateLimiterReplenishes(t *testing.T) {
	rl := NewRateLimiter(rate.Every(5*time.Millisecond), 1)
	h := rl.Middleware(okHandler())

	const ip = "203.0.113.99"
	if got := do(h, ip, "").Code; got != http.StatusOK {
		t.Fatalf("replenish request 1: status = %d, want 200", got)
	}
	if got := do(h, ip, "").Code; got != http.StatusTooManyRequests {
		t.Fatalf("replenish request 2 (immediate): status = %d, want 429", got)
	}

	// Wait comfortably past the refill interval, then expect a token again.
	time.Sleep(50 * time.Millisecond)
	if got := do(h, ip, "").Code; got != http.StatusOK {
		t.Fatalf("replenish request 3 (after refill): status = %d, want 200", got)
	}
}
