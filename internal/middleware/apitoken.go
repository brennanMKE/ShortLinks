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
	return RequireGuardOrAPIToken(RequireSession(sessions), tokens, limits)
}

// RequireGuardOrAPIToken is RequireSessionOrAPIToken over any session guard, so
// the Portiko sign-in (#0132) can supply its own while the API-token path stays
// the same code.
func RequireGuardOrAPIToken(sessionGuard func(http.Handler) http.Handler, tokens APITokenResolver, limits APITokenLimits) func(http.Handler) http.Handler {
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

			// An in-process MCP dispatch (#0149) was already counted once, as
			// the outer POST /mcp request; counting its inner REST call too
			// would charge every tool call twice.
			if limits.PerToken != nil && !isInternalDispatch(r.Context()) &&
				!limits.PerToken.limiterFor(strconv.FormatInt(u.TokenID, 10)).Allow() {
				writeRateLimited(w, limits.PerToken)
				return
			}

			if u.ReadOnly && !isSafeMethod(r.Method) && !isMCPTransport(r) {
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
	return RequireCookieGuard(RequireSession(sessions))
}

// RequireCookieGuard is RequireCookieSession over any session guard (#0132).
func RequireCookieGuard(sessionGuard func(http.Handler) http.Handler) func(http.Handler) http.Handler {
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

// MCPPath is the one route exempt from the read-only method check (#0149).
const MCPPath = "/mcp"

// isMCPTransport reports whether r is the MCP transport's own POST to /mcp.
// The MCP protocol carries every call, reads included, in a POST, so the
// method check cannot tell a read tool from a write tool there. The exemption
// is exactly this one path and method: /mcp enforces read-only per tool (a
// read-only session never lists a write tool and refuses one called anyway),
// and every tool dispatches back through this middleware on its real
// /api route, where a write by a read-only token is still refused with 403.
func isMCPTransport(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == MCPPath
}

type internalDispatchKey struct{}

// WithInternalDispatch marks ctx as an in-process dispatch from an already
// authenticated, already rate-limited request (the MCP server's inner REST
// calls). It only skips the per-token rate limit; authentication, the
// read-only check and everything downstream still run. The key is unexported,
// so a network client cannot set it.
func WithInternalDispatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalDispatchKey{}, true)
}

func isInternalDispatch(ctx context.Context) bool {
	v, _ := ctx.Value(internalDispatchKey{}).(bool)
	return v
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
