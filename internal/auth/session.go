package auth

import (
	"net/http"
	"time"
)

// SessionCookieName is the name of the session cookie set on a successful auth.
const SessionCookieName = "shortlinks_session"

// NewSessionToken returns a fresh URL-safe session token over 32 random bytes.
// It is exported so the login (#0016) and recovery (#0017) ceremonies mint
// tokens the same way as registration.
func NewSessionToken() (string, error) {
	return randomURLToken(sessionTokenLen)
}

// SetSessionCookie writes the session cookie on the response with the security
// attributes mandated by the PRD: HttpOnly, Secure, SameSite=Strict, scoped to
// the whole site, expiring with the session row.
//
// This is the single place cookie attributes are defined so every auth flow
// (registration, login, recovery) produces an identical cookie.
func SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}
