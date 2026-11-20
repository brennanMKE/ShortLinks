package middleware

import (
	"context"
	"errors"
	"net/http"

	"github.com/Kolonnade/Portiko/rp"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// SubjectResolver loads the ShortLinks account a Portiko subject maps to.
// *auth.Store satisfies it via ResolveSubject.
type SubjectResolver interface {
	ResolveSubject(ctx context.Context, subject string) (auth.SessionUser, error)
}

// RequirePortikoSession is RequireSession for an instance that signs in through
// Portiko (#0132). The rp client authenticates the request (cookie, session
// store, token refresh); this wrapper then loads the users row by the session's
// subject and attaches the same AuthUser RequireSession does, so no handler
// changes. active and is_admin come from ShortLinks' own table on every
// request, so deactivating a user takes effect on their next request.
//
// Like RequireSession it answers 403 api_token_not_permitted to an "slk_"
// bearer, and 401 unauthenticated to everything else it refuses.
func RequirePortikoSession(client *rp.Client, users SubjectResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, ok := rp.SessionFrom(r.Context())
			if !ok {
				writeUnauthenticated(w)
				return
			}
			u, err := users.ResolveSubject(r.Context(), sess.Subject)
			if err != nil {
				if errors.Is(err, auth.ErrSessionInvalid) || errors.Is(err, auth.ErrAccountInactive) {
					writeUnauthenticated(w)
					return
				}
				writeJSONError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			authUser := &AuthUser{ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin}
			next.ServeHTTP(w, r.WithContext(withUser(r.Context(), authUser)))
		})
		guarded := client.RequireUserAPI(inner)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if auth.IsAPIToken(bearerToken(r)) {
				writeJSONError(w, http.StatusForbidden, "api_token_not_permitted")
				return
			}
			guarded.ServeHTTP(&unauthWriter{ResponseWriter: w}, r)
		})
	}
}

// unauthWriter replaces rp's 401 body ({"error":"not signed in"}) with the
// canonical {"error":"unauthenticated"} every other ShortLinks 401 carries.
type unauthWriter struct {
	http.ResponseWriter
	swapped bool
}

func (w *unauthWriter) WriteHeader(code int) {
	if code == http.StatusUnauthorized {
		w.swapped = true
		writeUnauthenticated(w.ResponseWriter)
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *unauthWriter) Write(b []byte) (int, error) {
	if w.swapped {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
