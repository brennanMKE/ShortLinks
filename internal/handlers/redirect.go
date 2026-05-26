package handlers

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/cache"
)

// utmParams are the five UTM query keys forwarded to the destination and
// recorded with each click, per the PRD's UTM Parameter Passthrough section.
var utmParams = [...]string{
	"utm_source",
	"utm_medium",
	"utm_campaign",
	"utm_term",
	"utm_content",
}

// LinkResolver resolves a short-link key to its cached/DB-backed link data.
//
// Implementations encapsulate the cache→DB lookup described in the PRD's
// Redirect Cache section: check the in-memory cache first, fall back to the
// database on a miss, populate the cache, and cache a negative entry for keys
// that do not exist. The redirect handler depends only on this interface so it
// is fully unit-testable without a database.
//
// The boolean return reports whether a usable entry was found. A negative
// cache entry must be reported as found=false so the handler treats an absent
// key uniformly with a genuine miss. A non-nil error signals an internal
// lookup failure (e.g. DB error) distinct from "not found".
type LinkResolver interface {
	Resolve(ctx context.Context, key string) (cache.CachedLink, bool, error)
}

// ClickInfo carries the request metadata recorded for a single click.
type ClickInfo struct {
	Key         string
	ClickedAt   time.Time
	IPAddress   string
	UserAgent   string
	Referer     string
	UTMSource   string
	UTMMedium   string
	UTMCampaign string
	UTMTerm     string
	UTMContent  string
}

// ClickRecorder persists a click event. RecordClick is invoked from a
// goroutine off the redirect hot path, so implementations must be safe for
// concurrent use and must not assume the request is still in flight.
type ClickRecorder interface {
	RecordClick(info ClickInfo)
}

// RedirectHandler serves GET /u/{key}: it resolves the key, enforces the
// active/expired states, records the click asynchronously, merges inbound UTM
// parameters onto the destination, and issues a 302 redirect.
type RedirectHandler struct {
	resolver LinkResolver
	recorder ClickRecorder
	// now is injectable so expiry checks are deterministic in tests; defaults
	// to time.Now.
	now func() time.Time
}

// NewRedirectHandler constructs a RedirectHandler from its dependencies.
func NewRedirectHandler(resolver LinkResolver, recorder ClickRecorder) *RedirectHandler {
	return &RedirectHandler{
		resolver: resolver,
		recorder: recorder,
		now:      time.Now,
	}
}

// ServeHTTP implements http.Handler, following the PRD's 7-step Redirect
// Behavior flow.
func (h *RedirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.NotFound(w, r)
		return
	}

	// Steps 1–2: resolve via cache→DB (abstracted behind LinkResolver).
	link, found, err := h.resolver.Resolve(r.Context(), key)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	// Step 3: unknown key, negative entry, or inactive link → 404. A denied
	// link (denied_reason > 0) is also inactive and therefore handled here.
	if !found || link.Negative || !link.Active {
		http.NotFound(w, r)
		return
	}

	// Step 4: expired link → 410 Gone.
	if link.ExpiresAt != nil && !link.ExpiresAt.After(h.now()) {
		http.Error(w, "link expired", http.StatusGone)
		return
	}

	// Step 5: record the click asynchronously so it never blocks the redirect.
	// Metadata is snapshotted from the request now (before the goroutine runs)
	// because the request must not be touched once the response is written.
	info := buildClickInfo(key, r, h.now())
	go h.recorder.RecordClick(info)

	// Step 6: merge inbound utm_* params onto the destination URL.
	location := mergeUTM(link.DestinationURL, r.URL.Query())

	// Step 7: 302 Found with the merged Location.
	w.Header().Set("Location", location)
	w.WriteHeader(http.StatusFound)
}

// buildClickInfo snapshots the request metadata recorded for analytics.
func buildClickInfo(key string, r *http.Request, at time.Time) ClickInfo {
	q := r.URL.Query()
	return ClickInfo{
		Key:         key,
		ClickedAt:   at,
		IPAddress:   clientIP(r),
		UserAgent:   r.UserAgent(),
		Referer:     r.Referer(),
		UTMSource:   q.Get("utm_source"),
		UTMMedium:   q.Get("utm_medium"),
		UTMCampaign: q.Get("utm_campaign"),
		UTMTerm:     q.Get("utm_term"),
		UTMContent:  q.Get("utm_content"),
	}
}

// clientIP derives the originating client IP. Behind Apache's reverse proxy the
// real client is in X-Forwarded-For (first hop); otherwise fall back to the
// connection's RemoteAddr with the port stripped.
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

// mergeUTM merges the inbound utm_* query parameters onto the destination URL.
//
// Precedence: the destination's own query parameters are preserved, and inbound
// utm_* values override the destination's value for the same utm_* key (the
// click-time UTM is the campaign context that should win). Only the five known
// utm_* keys are forwarded; any other inbound query parameters are ignored.
//
// If the destination cannot be parsed, it is returned unchanged so a redirect
// still occurs rather than 500-ing on stored data.
func mergeUTM(destination string, inbound url.Values) string {
	dst, err := url.Parse(destination)
	if err != nil {
		return destination
	}

	q := dst.Query()
	for _, k := range utmParams {
		if v := inbound.Get(k); v != "" {
			q.Set(k, v)
		}
	}
	dst.RawQuery = q.Encode()
	return dst.String()
}
