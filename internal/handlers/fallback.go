package handlers

import (
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/filters"
)

// checkFallbackURL validates a fallback URL (#0180) the way a destination is
// validated: an absolute http(s) URL, run through the URL filter, and not
// pointing at this instance's own /u/ path (a fallback that is itself a short
// link could loop). It writes the error response and returns false on refusal.
// An empty raw value is valid (it clears the fallback) and writes nothing.
//
// Unlike PATCH destination_url (#0109), this runs on every create AND every
// edit that carries the field, so an edit cannot sneak a denied URL past the
// filter. A nil rules provider skips only the filter step.
func checkFallbackURL(w http.ResponseWriter, r *http.Request, rules ruleProvider, baseURL, field, raw string) bool {
	if raw == "" {
		return true
	}
	if !validDestinationURL(raw) {
		writeError(w, http.StatusBadRequest, field+" must be a valid absolute http(s) URL")
		return false
	}
	if pointsAtOwnShortLinks(baseURL, raw) {
		writeError(w, http.StatusBadRequest, field+" must not point at this instance's /u/ short links")
		return false
	}
	if rules != nil {
		rs, err := rules.Rules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return false
		}
		if code, _, matched := filters.Evaluate(rs, raw); matched {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "fallback_denied",
				"field":  field,
				"reason": code,
				"label":  filters.ReasonLabel(code),
			})
			return false
		}
	}
	return true
}

// pointsAtOwnShortLinks reports whether raw is on the same host as baseURL and
// under its /u/ path. Hosts compare case-insensitively without a trailing dot,
// default ports (443 https, 80 http) equal an absent port, and the path is
// cleaned first so /./u/k and /x/../u/k are caught.
func pointsAtOwnShortLinks(baseURL, raw string) bool {
	b, err := url.Parse(baseURL)
	if err != nil || b.Host == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if canonHost(b.Hostname()) != canonHost(u.Hostname()) || effectivePort(b) != effectivePort(u) {
		return false
	}
	p := path.Clean("/" + u.Path)
	return p == "/u" || strings.HasPrefix(p, "/u/")
}

func canonHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return "443"
}
