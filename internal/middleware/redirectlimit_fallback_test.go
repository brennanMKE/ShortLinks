package middleware

import (
	"net/http"
	"testing"
)

// A fallback 302 for an expired key (#0180) is not a 404/410, so it never
// charges the strict miss bucket: 50 late taps from one venue NAT keep working,
// while unknown keys from the same IP still exhaust the bucket.
func TestRedirectLimiter_FallbackRedirectDoesNotChargeMiss(t *testing.T) {
	rl, _, _ := newTestLimiter(120, 20)
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/u/expired" {
			w.Header().Set("Location", "https://app.example/expired?reason=expired")
			w.WriteHeader(http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	for i := 0; i < 50; i++ {
		if got := hit(h, "/u/expired", "", "192.0.2.1:1").Code; got != http.StatusFound {
			t.Fatalf("fallback hit %d = %d, want 302", i, got)
		}
	}
	for i := 0; i < 10; i++ {
		if got := hit(h, "/u/nope", "", "192.0.2.1:1").Code; got != http.StatusNotFound {
			t.Fatalf("miss %d = %d, want 404", i, got)
		}
	}
	if got := hit(h, "/u/nope", "", "192.0.2.1:1").Code; got != http.StatusTooManyRequests {
		t.Fatalf("scanner not throttled after misses: %d", got)
	}
}
