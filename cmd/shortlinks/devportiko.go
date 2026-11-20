package main

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
)

// devPortikoLogin is the dev-mode stand-in for Portiko's sign-in (#0134),
// enabled by DEV_PORTIKO=true (scripts/dev.sh --portiko). Unlike DevAutoLogin
// it never signs anyone in on its own: a browser is signed out until it visits
// GET /auth/portiko/login?return_to=<local path>, which mints a dev session for
// the seeded mock admin and redirects to return_to, mirroring the redirect
// round trip of the real flow. Everything else passes through untouched.
//
// Like DevAutoLogin it panics outside dev mode, so it cannot be wired into the
// Postgres path.
func devPortikoLogin(ds *devstore.Store, devMode bool) func(http.Handler) http.Handler {
	if !devMode {
		panic("devPortikoLogin: must not be called outside dev mode (STORAGE=json)")
	}
	slog.Warn("DEV MODE: Portiko sign-in stand-in is active — /auth/portiko/login signs in as the mock admin; NEVER run this in production")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/auth/portiko/login" || r.Method != http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}
			token, exp, err := ds.CreateDevSession(1, 24*time.Hour)
			if err != nil {
				http.Error(w, "dev portiko: session creation failed", http.StatusInternalServerError)
				return
			}
			auth.SetSessionCookie(w, token, exp)
			http.Redirect(w, r, devLocalPath(r.URL.Query().Get("return_to")), http.StatusFound)
		})
	}
}

// devLocalPath accepts only a same-origin path (optionally with a #fragment);
// anything else becomes "/".
func devLocalPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}
