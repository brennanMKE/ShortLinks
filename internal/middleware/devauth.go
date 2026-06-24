package middleware

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// devSessionCreator is the subset of devstore.Store that the dev auth
// middleware needs. Using an interface keeps the middleware package free of a
// direct import of devstore (which itself imports many other internal packages).
type devSessionCreator interface {
	CreateDevSession(userID int64, ttl time.Duration) (token string, expiresAt time.Time, err error)
}

// devAuthOnce gates the one-time startup log so "dev auth active" is printed
// exactly once no matter how many requests arrive.
var devAuthOnce sync.Once

// devAuthSessionTTL is the lifetime of a dev session token. 24 hours is long
// enough for a local dev session without needing any renewal logic.
const devAuthSessionTTL = 24 * time.Hour

// devAuthAdminID is the fixed user id of the seeded mock admin in the dev
// store (mirrors devstore.seedAdminID).
const devAuthAdminID int64 = 1

// DevAutoLogin returns middleware that, in dev mode only, automatically
// establishes an authenticated session for the seeded mock admin user when a
// request arrives without a valid session cookie. The session token is written
// to the response (so the browser stores it) and injected into the request (so
// RequireSession can validate it on the same request).
//
// Hard guardrail: the returned middleware panics at construction time if
// devMode is false, so it is structurally impossible to wire this into the
// production (Postgres) path even by accident.
//
// This must be wired only inside serveDevMode (cmd/shortlinks/main.go), which
// itself is only reached when STORAGE=json is set.
func DevAutoLogin(creator devSessionCreator, devMode bool) func(http.Handler) http.Handler {
	if !devMode {
		// Fail loudly at startup rather than silently. If this ever fires it
		// means the caller wired dev middleware on the production path — a bug.
		panic("middleware.DevAutoLogin: must not be called outside dev mode (STORAGE=json)")
	}

	devAuthOnce.Do(func() {
		slog.Warn("DEV MODE: auto-login is active — all requests are authenticated as the mock admin; NEVER run this in production")
	})

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// If the request already carries a session cookie, let RequireSession
			// validate it normally — no new token needed.
			if c, err := r.Cookie(auth.SessionCookieName); err == nil && c.Value != "" {
				next.ServeHTTP(w, r)
				return
			}

			// No session cookie present: mint a dev session for the mock admin.
			token, expiresAt, err := creator.CreateDevSession(devAuthAdminID, devAuthSessionTTL)
			if err != nil {
				slog.Error("devauth: failed to create dev session", "error", err)
				http.Error(w, "dev auth: session creation failed", http.StatusInternalServerError)
				return
			}

			// Write the cookie to the response so the browser stores it for
			// subsequent requests.
			auth.SetSessionCookie(w, token, expiresAt)

			// Inject the cookie into the current request so RequireSession (which
			// reads r.Cookie) can validate the token on this very request without
			// needing a client round-trip.
			r.AddCookie(&http.Cookie{
				Name:  auth.SessionCookieName,
				Value: token,
			})

			next.ServeHTTP(w, r)
		})
	}
}
