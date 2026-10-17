package middleware

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// APITokenResolver authenticates a plaintext API token ("slk_…") and records
// its last use. *auth.Store satisfies it via ResolveAPIToken.
type APITokenResolver interface {
	ResolveAPIToken(ctx context.Context, token, ip string, now time.Time) (auth.APITokenUser, error)
}

// APITokenLimits configures the two rate limits the API-token path enforces.
type APITokenLimits struct {
	// PerToken bounds the request rate of one valid token, keyed by token id —
	// not IP — so an agent behind a shared NAT is not throttled by its
	// neighbours and a token spread across many IPs is still bounded.
	PerToken *RateLimiter
	// FailuresPerIP bounds how many INVALID token attempts one IP may make,
	// slowing online guessing. Once exhausted, the IP gets 429 before any
	// database lookup.
	FailuresPerIP *RateLimiter
}

// DefaultAPITokenLimits returns the production limits: 600 requests/minute per
// token (burst 60), and 20 failed token attempts/minute per IP (burst 20).
func DefaultAPITokenLimits() APITokenLimits {
	return APITokenLimits{
		PerToken:      NewRateLimiter(rate.Every(time.Minute/600), 60),
		FailuresPerIP: NewRateLimiter(rate.Every(time.Minute/20), 20),
	}
}

// RequireSessionOrAPIToken guards the /api/* data routes, the surface scripts
// and agents use. It accepts everything RequireSession accepts (session cookie
// or session bearer) and, additionally, an API token in
// "Authorization: Bearer slk_…". An API token is honoured ONLY in that header —
// never a cookie or query parameter — so it cannot leak through URLs, Referer
// headers, or access logs, and cookie-based CSRF does not apply to it.
//
// For an API token it:
//   - rejects with 429 + Retry-After when the IP has too many recent failed
//     attempts, before touching the database;
//   - resolves it (401 unauthenticated for unknown/revoked/expired/inactive
//     owner — the same body as every other auth failure);
//   - applies the per-token rate limit (429 + Retry-After);
//   - refuses non-safe methods for a read-only token (403 api_token_read_only);
//   - attaches an AuthUser with IsAdmin=false and the token identity, and
//     marks the context for audit attribution (audit.WithAPIToken).
func RequireSessionOrAPIToken(sessions SessionResolver, tokens APITokenResolver, limits APITokenLimits) func(http.Handler) http.Handler {
	sessionGuard := RequireSession(sessions)
	return func(next http.Handler) http.Handler {
		viaSession := sessionGuard(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if !auth.IsAPIToken(token) {
				viaSession.ServeHTTP(w, r)
				return
			}

			ip := clientIP(r)
			if limits.FailuresPerIP != nil && limits.FailuresPerIP.limiterFor(ip).Tokens() < 1 {
				writeRateLimited(w, limits.FailuresPerIP)
				return
			}

			u, err := tokens.ResolveAPIToken(r.Context(), token, ip, nowFunc())
			if err != nil {
				if errors.Is(err, auth.ErrAPITokenInvalid) {
					if limits.FailuresPerIP != nil {
						limits.FailuresPerIP.limiterFor(ip).Allow()
					}
					writeUnauthenticated(w)
					return
				}
				writeJSONError(w, http.StatusInternalServerError, "internal_error")
				return
			}

			if limits.PerToken != nil && !limits.PerToken.limiterFor(strconv.FormatInt(u.TokenID, 10)).Allow() {
				writeRateLimited(w, limits.PerToken)
				return
			}

			if u.ReadOnly && !isSafeMethod(r.Method) {
				writeJSONError(w, http.StatusForbidden, "api_token_read_only")
				return
			}

			authUser := &AuthUser{
				ID:           u.ID,
				Email:        u.Email,
				IsAdmin:      false, // tokens never carry admin authority
				APITokenID:   u.TokenID,
				APITokenName: u.TokenName,
				ReadOnly:     u.ReadOnly,
			}
			ctx := withUser(r.Context(), authUser)
			ctx = audit.WithAPIToken(ctx, audit.APITokenRef{ID: u.TokenID, Name: u.TokenName})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireCookieSession guards the routes that must only ever be driven by a
// person in the browser: minting and revoking API tokens. It refuses any
// request carrying an Authorization header — an API token, or a bearer
// session from a native client — with 403 cookie_session_required, then
// applies RequireSession, which is left with only the HttpOnly,
// SameSite=Strict session cookie. A leaked token therefore can never be used
// to mint more tokens or to keep itself alive after being revoked.
func RequireCookieSession(sessions SessionResolver) func(http.Handler) http.Handler {
	sessionGuard := RequireSession(sessions)
	return func(next http.Handler) http.Handler {
		guarded := sessionGuard(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				writeJSONError(w, http.StatusForbidden, "cookie_session_required")
				return
			}
			guarded.ServeHTTP(w, r)
		})
	}
}

// isSafeMethod reports whether the method is read-only per RFC 9110.
func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// writeRateLimited writes the 429 rate_limit_exceeded body with a Retry-After
// derived from the limiter's refill rate.
func writeRateLimited(w http.ResponseWriter, rl *RateLimiter) {
	w.Header().Set("Retry-After", rl.retryAfterSeconds())
	writeJSONError(w, http.StatusTooManyRequests, "rate_limit_exceeded")
}
