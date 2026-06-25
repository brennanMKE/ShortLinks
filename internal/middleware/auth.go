package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// bearerPrefix is the RFC 6750 Authorization scheme (matched case-insensitively).
const bearerPrefix = "bearer "

// sessionToken extracts the raw session token from the request, preferring an
// "Authorization: Bearer <token>" header (used by native/API clients, e.g. the
// iPhone app — #0077) over the shortlinks_session cookie (used by the browser
// SPA). The two transports carry the SAME opaque token; this only changes how
// it's read. A present-but-malformed Authorization header (wrong scheme, empty
// token) falls through to the cookie so the browser SPA is never affected.
// Returns "" when neither source carries a token.
func sessionToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > len(bearerPrefix) &&
		strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
		if tok := strings.TrimSpace(h[len(bearerPrefix):]); tok != "" {
			return tok
		}
	}
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// AuthUser is the authenticated principal attached to a request's context by
// RequireSession. Handlers retrieve it with UserFromContext. It carries exactly
// the fields downstream code needs for authorization and auditing — the account
// id, email, and admin flag — and nothing session-specific, so it can be passed
// around freely.
//
// AuthUser lives in the middleware package (not auth) so any handler package can
// import it to read the context without risking an import cycle with auth.
type AuthUser struct {
	ID      int64
	Email   string
	IsAdmin bool
}

// SessionResolver validates a raw session cookie value and returns the
// authenticated user, applying the sliding-window bump as a side effect. It is
// the single dependency RequireSession needs from the data layer.
//
// *auth.Store satisfies this interface via its ResolveSession method. Taking an
// interface (rather than a concrete *auth.Store) keeps the guard testable with a
// fake and documents the exact contract: a successful call must have already
// extended the session's expiry and updated last_seen_at.
type SessionResolver interface {
	ResolveSession(ctx context.Context, token string, now time.Time) (auth.SessionUser, error)
}

// contextKey is an unexported type for context keys defined in this package, so
// keys here never collide with keys from other packages sharing a context.
type contextKey int

// userContextKey is the key under which the authenticated AuthUser is stored.
const userContextKey contextKey = iota

// nowFunc returns the current time. It is a package var so tests can pin time;
// production code uses the real clock.
var nowFunc = time.Now

// UserFromContext returns the authenticated user attached by RequireSession, and
// a bool reporting whether one was present. Handlers behind RequireSession can
// rely on ok being true; handlers reachable without the guard should check it.
func UserFromContext(ctx context.Context) (*AuthUser, bool) {
	u, ok := ctx.Value(userContextKey).(*AuthUser)
	return u, ok
}

// withUser returns a copy of ctx carrying the authenticated user.
func withUser(ctx context.Context, u *AuthUser) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

// RequireSession returns middleware that guards protected routes. On each
// request it reads the session token — from an "Authorization: Bearer <token>"
// header (native/API clients, #0077) or the shortlinks_session cookie (the
// browser SPA), see sessionToken — validates it against the database, and—on
// success—attaches the authenticated AuthUser to the request context before
// calling next. The validation also applies the 30-day sliding window (bumps
// last_seen_at and extends expires_at) per the PRD's Session Security rules.
//
// It writes a 401 JSON response and stops the chain when:
//   - no session token is present (neither header nor cookie),
//   - the token is unknown or the session has expired (the expired row is
//     reaped best-effort by the resolver), or
//   - the owning account has been deactivated (active = false).
//
// The resolver argument is typically an *auth.Store.
func RequireSession(resolver SessionResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := sessionToken(r)
			if token == "" {
				writeUnauthenticated(w)
				return
			}

			u, err := resolver.ResolveSession(r.Context(), token, nowFunc())
			if err != nil {
				// Unknown/expired token and deactivated account both deny
				// access. Per the PRD a deactivated user "can't use the API",
				// so a live cookie for an inactive account is rejected with the
				// same 401 as an invalid session — no information is leaked
				// about which case occurred.
				if errors.Is(err, auth.ErrSessionInvalid) || errors.Is(err, auth.ErrAccountInactive) {
					writeUnauthenticated(w)
					return
				}
				// Anything else is an unexpected backend failure.
				writeJSONError(w, http.StatusInternalServerError, "internal_error")
				return
			}

			authUser := &AuthUser{ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin}
			next.ServeHTTP(w, r.WithContext(withUser(r.Context(), authUser)))
		})
	}
}

// RequireAdmin returns middleware that allows only admin users through. It must
// run after RequireSession (it reads the user the guard attached): if no
// authenticated user is present it answers 401, and if the user is not an admin
// it answers 403. Admin-only routes wrap their handlers with
// RequireSession then RequireAdmin.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			// No session was established (RequireSession not applied or it
			// failed). Treat as unauthenticated rather than forbidden.
			writeUnauthenticated(w)
			return
		}
		if !u.IsAdmin {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeUnauthenticated writes the canonical 401 JSON body used for every
// authentication failure.
func writeUnauthenticated(w http.ResponseWriter) {
	writeJSONError(w, http.StatusUnauthorized, "unauthenticated")
}

// writeJSONError writes a minimal {"error":"..."} body with the given status.
// The body is a fixed, escape-free token, so it is emitted directly to avoid an
// encoding dependency in the hot path.
func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}`))
}
