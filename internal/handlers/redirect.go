package handlers

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
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

// FallbackResolver is an optional capability of a LinkResolver (#0180): it
// returns the effective fallback URL for a key (the link's own, else its
// campaign's default, "" when none or when the link was denied by the URL
// filter). The redirect handler asks it only for expired or deactivated links.
type FallbackResolver interface {
	FallbackURL(ctx context.Context, key string) (string, error)
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

// clickSink is the behavior of the DB-backed click recorder
// (*clicks.Recorder). Depending on it through an interface keeps the adapter
// testable and avoids a hard handler→concrete coupling.
type clickSink interface {
	RecordClick(c clicks.Click)
}

// NewClickRecorder adapts a *clicks.Recorder (which speaks clicks.Click) to the
// handler's ClickRecorder interface (which speaks ClickInfo). The redirect
// handler depends only on ClickRecorder, so the clicks package never needs to
// import handlers; this thin adapter bridges the two value shapes at the wiring
// boundary.
func NewClickRecorder(sink clickSink) ClickRecorder {
	return clickRecorderAdapter{sink: sink}
}

type clickRecorderAdapter struct {
	sink clickSink
}

func (a clickRecorderAdapter) RecordClick(info ClickInfo) {
	a.sink.RecordClick(clicks.Click{
		Key:         info.Key,
		ClickedAt:   info.ClickedAt,
		IPAddress:   info.IPAddress,
		UserAgent:   info.UserAgent,
		Referer:     info.Referer,
		UTMSource:   info.UTMSource,
		UTMMedium:   info.UTMMedium,
		UTMCampaign: info.UTMCampaign,
		UTMTerm:     info.UTMTerm,
		UTMContent:  info.UTMContent,
	})
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

	// Step 3: unknown key or negative entry → 404. A key that does not exist
	// never falls back.
	if !found || link.Negative {
		http.NotFound(w, r)
		return
	}

	// A link denied by the URL filter never redirects anywhere (#0180).
	if link.DeniedReason != 0 {
		http.NotFound(w, r)
		return
	}

	// Steps 3-4: deactivated → 404, expired → 410, unless a fallback URL is
	// configured (#0180), in which case 302 to it with ?reason=. Inactive wins
	// when a link is both. Fallback hits record no click.
	if !link.Active {
		if h.fallback(w, r, key, "inactive") {
			return
		}
		http.NotFound(w, r)
		return
	}
	if link.ExpiresAt != nil && !link.ExpiresAt.After(h.now()) {
		if h.fallback(w, r, key, "expired") {
			return
		}
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

// fallback writes a 302 to the key's fallback URL with reason appended and
// reports whether it did. Any lookup error or unusable URL falls through to the
// plain 404/410. No click is recorded: a fallback hit is not a click on the
// link, so it can never reach a report. The 302 is not a 404/410, so the
// redirect rate limiter does not charge the strict miss bucket for it (#0178).
func (h *RedirectHandler) fallback(w http.ResponseWriter, r *http.Request, key, reason string) bool {
	fr, ok := h.resolver.(FallbackResolver)
	if !ok {
		return false
	}
	raw, err := fr.FallbackURL(r.Context(), key)
	if err != nil || raw == "" {
		return false
	}
	loc, ok := withReason(raw, reason)
	if !ok {
		return false
	}
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
	return true
}

// withReason appends reason=<reason> to raw's query, leaving the existing raw
// query bytes untouched (no re-encoding or reordering) and keeping any
// fragment. An existing reason parameter is kept as is; the new one follows it.
func withReason(raw, reason string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	if u.RawQuery == "" {
		u.RawQuery = "reason=" + reason
	} else {
		u.RawQuery += "&reason=" + reason
	}
	return u.String(), true
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

// clientIP derives the originating client IP. Behind Apache's reverse proxy on
// loopback the real client is the LAST X-Forwarded-For entry (the one Apache
// appended; earlier entries are client-supplied); otherwise fall back to the
// connection's RemoteAddr with the port stripped.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if p := net.ParseIP(host); p != nil {
		host = p.String() // normalise so ::ffff:a.b.c.d and a.b.c.d share a key
	}
	// Only the loopback reverse proxy (Apache) is trusted to supply
	// X-Forwarded-For; a direct client could send anything.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if p := net.ParseIP(host); p != nil && p.IsLoopback() {
			// Apache's mod_proxy APPENDS the address it saw, so leading entries
			// are client-controlled and only the last is trustworthy (#0178).
			last := xff
			if i := strings.LastIndexByte(xff, ','); i >= 0 {
				last = xff[i+1:]
			}
			if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
				return ip.String()
			}
		}
	}
	return host
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
