package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"
)

// RateLimiter enforces a per-IP request rate using a token-bucket limiter per
// client IP. It is intended for the public, abuse-prone auth endpoints
// (registration, login, recovery), where a single source should not be able to
// hammer the WebAuthn/email machinery.
//
// The limiter is constructed once per protected route with NewRateLimiter and
// installed via Middleware. Each distinct client IP gets its own
// *rate.Limiter created lazily on first sight and reused thereafter; the map is
// guarded by a mutex so the middleware is safe for concurrent use.
//
// Memory note: limiters are never evicted. For this single-instance service
// behind Apache, the set of distinct client IPs is bounded enough that the
// simplicity is worth it; see issues/0020.md Gotchas for the tradeoff.
type RateLimiter struct {
	mu    sync.Mutex
	rate  rate.Limit
	burst int
	byIP  map[string]*rate.Limiter
}

// NewRateLimiter returns a RateLimiter that allows requests at sustained rate r
// with bucket capacity burst, applied independently per client IP. Setting
// burst to the per-window allowance lets a freshly seen IP spend its whole
// allotment immediately, then refill at rate r.
//
// Examples matching the PRD's Phase 2 auth limits:
//
//	NewRateLimiter(rate.Every(time.Hour/3), 3)    // 3 requests / hour / IP
//	NewRateLimiter(rate.Every(time.Minute/10), 10) // 10 requests / minute / IP
func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	return &RateLimiter{
		rate:  r,
		burst: burst,
		byIP:  make(map[string]*rate.Limiter),
	}
}

// limiterFor returns the *rate.Limiter for the given IP, creating it on first
// sight. The lock is held only for the map lookup/insert.
func (rl *RateLimiter) limiterFor(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	lim, ok := rl.byIP[ip]
	if !ok {
		lim = rate.NewLimiter(rl.rate, rl.burst)
		rl.byIP[ip] = lim
	}
	return lim
}

// Middleware returns an http middleware that rejects requests from a client IP
// once that IP exceeds the configured rate, responding 429 Too Many Requests
// with a {"error":"rate_limit_exceeded"} JSON body. Requests within the limit
// are passed to next unchanged.
//
// The client IP is taken from the X-Forwarded-For header (set by the Apache
// reverse proxy in production) with a fallback to RemoteAddr, matching the
// extraction used by the redirect handler so all components bucket clients
// identically.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.limiterFor(ip).Allow() {
			writeJSONError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP derives the originating client IP for rate-limiting buckets. Behind
// Apache's reverse proxy the real client is the first hop in X-Forwarded-For;
// otherwise fall back to the connection's RemoteAddr with the port stripped.
// This mirrors the redirect handler's IP extraction so a single client is
// bucketed consistently across the service.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For is a comma-separated list; the first entry is the
		// original client.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
