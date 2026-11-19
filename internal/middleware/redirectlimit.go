package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Defaults for the public redirect limiter (#0178).
const (
	DefaultRedirectRatePerMin     = 120
	DefaultRedirectMissRatePerMin = 20
)

// redirectIdleTTL is how long an IP must be silent before its entry is dropped.
// It exceeds the time either bucket needs to refill from empty (a full minute
// at any rate >= burst), so a dropped entry is indistinguishable from a fresh
// one and eviction can never grant extra quota.
const redirectIdleTTL = 2 * time.Minute

// redirectMaxEntries is the hard cap on tracked IPs. At the cap (after a sweep)
// new IPs fail closed with 429 rather than growing memory.
const redirectMaxEntries = 100_000

// bucket is a minimal token bucket with lazy refill.
type bucket struct {
	tokens float64
	burst  float64
	perSec float64
	last   time.Time
}

func newBucket(perMin, burst int, now time.Time) bucket {
	return bucket{tokens: float64(burst), burst: float64(burst), perSec: float64(perMin) / 60, last: now}
}

func (b *bucket) refill(now time.Time) {
	if d := now.Sub(b.last).Seconds(); d > 0 {
		b.tokens += d * b.perSec
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
	}
	b.last = now
}

// wait is the time until one whole token is available (0 if one is).
func (b *bucket) wait() time.Duration {
	if b.tokens >= 1 {
		return 0
	}
	return time.Duration((1 - b.tokens) / b.perSec * float64(time.Second))
}

type redirectEntry struct {
	all, miss bucket
	seen      time.Time
}

// RedirectLimiter protects GET /u/{key} against key scanning with two per-IP
// token buckets: a generous one for all requests, and a strict one charged
// only by 404/410 responses. Once the miss bucket is empty every /u/* request
// from that IP gets 429 + Retry-After until it refills, even for live keys.
// Hits cost only the generous bucket, so a poster scanned by many people
// behind one NAT keeps working. 429 responses consume nothing and are never
// click-recorded (the wrapped handler isn't invoked).
//
// No database work happens here: state is in-memory and per-request cost is
// one mutex-guarded map lookup. Idle entries are swept lazily so memory is
// bounded by the number of IPs seen within redirectIdleTTL.
type RedirectLimiter struct {
	mu         sync.Mutex
	ratePerM   int
	missPerM   int
	missBurst  int
	byIP       map[string]*redirectEntry
	now        func() time.Time
	maxEntries int
	capLogged  bool
}

// NewRedirectLimiter builds a limiter allowing ratePerMin requests per minute
// per IP, of which misses may use missPerMin per minute (burst half of that,
// at least 1). Non-positive values fall back to the defaults.
func NewRedirectLimiter(ratePerMin, missPerMin int) *RedirectLimiter {
	if ratePerMin <= 0 {
		ratePerMin = DefaultRedirectRatePerMin
	}
	if missPerMin <= 0 {
		missPerMin = DefaultRedirectMissRatePerMin
	}
	burst := missPerMin / 2
	if burst < 1 {
		burst = 1
	}
	return &RedirectLimiter{
		ratePerM: ratePerMin, missPerM: missPerMin, missBurst: burst,
		byIP: make(map[string]*redirectEntry), now: time.Now, maxEntries: redirectMaxEntries,
	}
}

// Len reports the number of tracked IPs (for tests).
func (rl *RedirectLimiter) Len() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.byIP)
}

// Sweep drops entries idle longer than redirectIdleTTL. It is O(n) and so runs
// from Run, never on the request path.
func (rl *RedirectLimiter) Sweep() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.sweepLocked(rl.now())
}

// Run sweeps idle entries every 30 seconds until ctx is done.
func (rl *RedirectLimiter) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rl.Sweep()
		}
	}
}

func (rl *RedirectLimiter) sweepLocked(now time.Time) {
	for ip, e := range rl.byIP {
		if now.Sub(e.seen) > redirectIdleTTL {
			delete(rl.byIP, ip)
		}
	}
}

// admit decides whether the request may proceed; if not it returns the
// Retry-After wait.
func (rl *RedirectLimiter) admit(ip string) (ok bool, wait time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	e := rl.byIP[ip]
	if e == nil {
		if len(rl.byIP) >= rl.maxEntries {
			// At the cap, evict one arbitrary entry (O(1) via map iteration)
			// and admit the new IP, so a flood of addresses (e.g. a whole
			// /44) cannot lock new visitors out. Log once per episode.
			for k := range rl.byIP {
				delete(rl.byIP, k)
				break
			}
			if !rl.capLogged {
				rl.capLogged = true
				slog.Warn("redirect limiter at entry cap; evicting entries", "max_entries", rl.maxEntries)
			}
		} else {
			rl.capLogged = false
		}
		e = &redirectEntry{
			all:  newBucket(rl.ratePerM, rl.ratePerM, now),
			miss: newBucket(rl.missPerM, rl.missBurst, now),
		}
		rl.byIP[ip] = e
	}
	e.seen = now
	e.all.refill(now)
	e.miss.refill(now)
	if w := max(e.all.wait(), e.miss.wait()); w > 0 {
		return false, w
	}
	// Reserve both tokens up front (so more than ~burst requests in flight at
	// once from one IP can get a 429 even if they would all be hits) so concurrent misses cannot all pass the
	// check before any is charged; a non-miss response refunds the miss token.
	e.all.tokens--
	e.miss.tokens--
	return true, 0
}

// refundMiss returns the reserved miss token after a response that was not a
// 404/410.
func (rl *RedirectLimiter) refundMiss(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if e := rl.byIP[ip]; e != nil {
		e.miss.tokens++
		if e.miss.tokens > e.miss.burst {
			e.miss.tokens = e.miss.burst
		}
	}
}

// limiterKey buckets IPv6 clients on their /64 (one subscriber typically owns
// a whole /64, so per-address keys would be free to rotate); IPv4 is as is.
func limiterKey(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Middleware wraps the redirect handler. The client IP comes from clientIP
// (last X-Forwarded-For entry when the peer is the loopback proxy, else
// RemoteAddr), keyed by limiterKey.
func (rl *RedirectLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := limiterKey(clientIP(r))
		ok, wait := rl.admit(ip)
		if !ok {
			secs := int((wait + time.Second - 1) / time.Second)
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status != http.StatusNotFound && rec.status != http.StatusGone {
			rl.refundMiss(ip)
		}
	})
}
