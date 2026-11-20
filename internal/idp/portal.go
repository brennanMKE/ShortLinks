package idp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Kolonnade/Portiko/rp"
	"github.com/Kolonnade/Portiko/rp/session"
	"github.com/Kolonnade/Portiko/rp/session/pgstore"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// Portal is ShortLinks as a relying party of its own embedded provider (#0132):
// the rp client, the Postgres session store, and the routes and guards built on
// them. It is only used when PORTIKO_ENABLED is true; with the switch off
// nothing here is constructed, wired or reachable.
//
// The guards are needed when the route table is built, before the provider has
// started, so a Portal is created first and bound to a live rp client later by
// MountPortal. Until then (or if the provider failed to start) the guards answer
// 503 rather than letting an unauthenticated request through.
type Portal struct {
	cfg   *config.Config
	pool  *pgxpool.Pool
	users *auth.Store
	audit *audit.Logger
	state atomic.Pointer[portalState]
}

type portalState struct {
	client   *rp.Client
	sessions *pgstore.Store
	guard    func(http.Handler) http.Handler
}

// NewPortal returns an unbound Portal.
func NewPortal(cfg *config.Config, pool *pgxpool.Pool) *Portal {
	return &Portal{cfg: cfg, pool: pool, users: auth.NewStore(pool), audit: audit.New(pool)}
}

// RequireSession is the Portiko counterpart of middleware.RequireSession, for
// the route table's requireSession, requireAdmin, requireAPI and requireCookie.
func (p *Portal) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := p.state.Load()
		if st == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"sign_in_unavailable"}`))
			return
		}
		st.guard(next).ServeHTTP(w, r)
	})
}

// deriveKey separates PORTIKO_CRYPTO_KEY into independent keys for the session
// store and the rp's flow cookies, so one configured secret serves both without
// being reused, and neither changes across restarts.
func deriveKey(root [32]byte, purpose string) []byte {
	h := sha256.New()
	h.Write([]byte("shortlinks portiko " + purpose + "\x00"))
	h.Write(root[:])
	return h.Sum(nil)
}

// inProcess sends the rp's own HTTP calls (discovery, keys, token exchange) to
// the embedded provider's handler directly. The provider is part of this
// process, so there is no reason to go out to Apache and back, and no startup
// ordering problem: the rp can be built before the listener is up.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.RequestURI = req.URL.RequestURI()
	r.RemoteAddr = "127.0.0.1:0"
	r.Host = req.URL.Host
	r.URL = &url.URL{Path: req.URL.Path, RawPath: req.URL.RawPath, RawQuery: req.URL.RawQuery}
	if r.Body == nil {
		r.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

// bind builds the rp client over the provider's handler and makes the guards
// live.
func (p *Portal) bind(ctx context.Context, provider http.Handler) error {
	base := strings.TrimSuffix(p.cfg.BaseURL, "/")
	store, err := pgstore.New(pgstore.Config{
		Pool:  p.pool,
		Key:   deriveKey(p.cfg.PortikoCryptoKey, "rp sessions"),
		Table: "rp_sessions", // created by migration 000028
	})
	if err != nil {
		return fmt.Errorf("idp: session store: %w", err)
	}
	client, err := rp.New(ctx, rp.Config{
		Issuer:       base + Prefix,
		ClientID:     ClientID,
		ClientSecret: p.cfg.PortikoClientSecret,
		RedirectURL:  base + "/auth/portiko/callback",
		Sessions:     store,
		Scopes:       []string{"email", "profile"}, // email is what OnSignIn maps users on
		HTTPClient:   &http.Client{Transport: inProcess{provider}, Timeout: 15 * time.Second},
		Insecure:     strings.HasPrefix(base, "http://"),
		CookieKey:    deriveKey(p.cfg.PortikoCryptoKey, "rp cookies"),
		OnSignIn:     p.signIn,
	})
	if err != nil {
		return fmt.Errorf("idp: relying party: %w", err)
	}
	p.state.Store(&portalState{
		client: client, sessions: store,
		guard: middleware.RequirePortikoSession(client, p.users),
	})
	return nil
}

// Errors OnSignIn can end a sign-in with. rp turns any of them into a plain
// "sign-in failed" response and creates no session.
var (
	ErrRegistrationClosed = errors.New("idp: registration is closed and this account is new to ShortLinks")
	ErrAccountDeactivated = errors.New("idp: account deactivated")
	errSubjectConflict    = errors.New("idp: the verified email belongs to a ShortLinks user already linked to a different Portiko account")
	errAmbiguousEmail     = errors.New("idp: the verified email matches more than one ShortLinks user")
)

// refusalMessage is what the callback tells the browser for each refusal, or ""
// when err is not a refusal (a real failure stays a 500).
func refusalMessage(err error) string {
	switch {
	case errors.Is(err, ErrAccountDeactivated):
		return "account deactivated"
	case errors.Is(err, ErrRegistrationClosed):
		return "Registration is currently closed."
	case errors.Is(err, errSubjectConflict), errors.Is(err, errAmbiguousEmail):
		return "this account cannot be signed in; contact an administrator"
	}
	return ""
}

type refusalKey struct{}

// refusalSlot carries a refusal from OnSignIn (which rp calls with the request's
// context) back to the callback wrapper.
type refusalSlot struct{ msg string }

// refusalWriter turns rp's bare 500 into a 403 with the refusal's message when
// OnSignIn recorded one.
type refusalWriter struct {
	http.ResponseWriter
	slot *refusalSlot
	done bool
}

func (w *refusalWriter) WriteHeader(code int) {
	if code == http.StatusInternalServerError && w.slot.msg != "" {
		w.done = true
		w.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w.ResponseWriter).Encode(map[string]string{"error": w.slot.msg})
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *refusalWriter) Write(b []byte) (int, error) {
	if w.done {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// callback wraps rp's CallbackHandler so a refused sign-in is a 403 with a
// message, not "500 sign-in failed".
func callback(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slot := &refusalSlot{}
		h.ServeHTTP(&refusalWriter{ResponseWriter: w, slot: slot}, r.WithContext(context.WithValue(r.Context(), refusalKey{}, slot)))
	})
}

// signIn runs onSignIn and logs a refusal: rp answers any OnSignIn error with a
// bare "sign-in failed", so the reason would otherwise be lost.
func (p *Portal) signIn(ctx context.Context, s *session.Session) error {
	err := p.onSignIn(ctx, s)
	if slot, ok := ctx.Value(refusalKey{}).(*refusalSlot); ok && err != nil {
		slot.msg = refusalMessage(err)
	}
	if err != nil {
		slog.WarnContext(ctx, "portiko sign-in refused", "subject", s.Subject, "err", err)
	}
	return err
}

const bySubject = `SELECT id, active FROM users WHERE subject = $1`

// onSignIn is rp's OnSignIn: it maps the Portiko subject onto a ShortLinks
// users row, inside one transaction.
//
//  1. Look up users by subject.
//  2. Otherwise by the verified email, and record the subject there (this
//     catches accounts the #0131 import missed). A user already linked to a
//     different subject is never re-pointed.
//  3. Otherwise this is a new user: refuse it while registrations are closed,
//     else insert it, with the admin rule (first user, or ADMIN_EMAIL).
//  4. Refuse a deactivated account.
//  5. Update last_login_at, and write account.login (account.registered for a
//     new user).
//
// Steps 2 and 3 run under an advisory lock and the insert is insert-if-absent,
// so two concurrent first sign-ins cannot both decide they are the first user.
func (p *Portal) onSignIn(ctx context.Context, s *session.Session) error {
	subject := s.Subject
	email := strings.ToLower(strings.TrimSpace(s.Email))
	if subject == "" || email == "" {
		return errors.New("idp: the sign-in carries no subject or email")
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id     int64
		active bool
		isNew  bool
	)
	found, err := scanRow(tx.QueryRow(ctx, bySubject, subject), &id, &active)
	if err != nil {
		return err
	}
	if !found {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('shortlinks:provision-user'))`); err != nil {
			return err
		}
		if found, err = scanRow(tx.QueryRow(ctx, bySubject, subject), &id, &active); err != nil {
			return err
		}
	}
	if !found {
		type match struct {
			id       int64
			active   bool
			existing *string
		}
		rows, err := tx.Query(ctx, `SELECT id, active, subject FROM users WHERE lower(email) = $1 FOR UPDATE`, email)
		if err != nil {
			return err
		}
		var matches []match
		for rows.Next() {
			var m match
			if err := rows.Scan(&m.id, &m.active, &m.existing); err != nil {
				rows.Close()
				return err
			}
			matches = append(matches, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(matches) > 1 {
			return errAmbiguousEmail
		}
		if len(matches) == 1 {
			id, active = matches[0].id, matches[0].active
			if matches[0].existing != nil && *matches[0].existing != subject {
				return errSubjectConflict
			}
			tag, err := tx.Exec(ctx, `UPDATE users SET subject = $1 WHERE id = $2 AND subject IS NULL`, subject, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 && matches[0].existing == nil {
				return errSubjectConflict // set concurrently
			}
		} else {
			on, err := p.users.RegistrationsEnabled(ctx)
			if err != nil {
				return err
			}
			if !on {
				return ErrRegistrationClosed
			}
			var count int64
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
				return err
			}
			admin := count == 0 || (p.cfg.AdminEmail != "" && email == strings.ToLower(strings.TrimSpace(p.cfg.AdminEmail)))
			inserted, err := scanRow(tx.QueryRow(ctx,
				`INSERT INTO users (email, is_admin, active, created_at, subject)
				 VALUES ($1, $2, TRUE, now(), $3)
				 ON CONFLICT (subject) DO NOTHING
				 RETURNING id`, email, admin, subject), &id)
			if err != nil {
				return err
			}
			if inserted {
				isNew, active = true, true
			} else if _, err := scanRow(tx.QueryRow(ctx, bySubject, subject), &id, &active); err != nil {
				return err
			}
		}
	}

	if !active {
		return ErrAccountDeactivated
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	action := audit.ActionAccountLogin
	if isNew {
		action = audit.ActionAccountRegistered
	}
	if err := p.audit.WriteTx(ctx, tx, audit.Entry{
		ActorID: &id, UserID: &id, Action: action, TargetType: audit.TargetUser, TargetID: &id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// scanRow scans one row, reporting whether there was one.
func scanRow(row pgx.Row, dest ...any) (bool, error) {
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// oldAuthPath reports whether path belongs to the passkey sign-in, registration
// or recovery routes that Portiko replaces. With the switch on they are not
// mounted.
func oldAuthPath(path string) bool {
	return strings.HasPrefix(path, "/auth/register/") || strings.HasPrefix(path, "/auth/login/") ||
		path == "/auth/recover" || strings.HasPrefix(path, "/auth/recover/")
}

// Wrap serves the Portiko sign-in routes in front of next and hides the old
// passkey routes:
//
//	GET  /auth/portiko/login       start a sign-in (?return_to=/path)
//	GET  /auth/portiko/callback    finish it
//	POST /auth/portiko/backchannel the provider signs a browser out
//	POST /auth/portiko/events      profile changes
//	POST /auth/logout              end this session here and at the provider
//	POST /auth/logout/all          end every session of this account
//
// Everything else reaches next untouched.
func (p *Portal) Wrap(next http.Handler) http.Handler {
	type route struct {
		method string
		h      func(*portalState) http.Handler
	}
	routes := map[string]route{
		"/auth/portiko/login":       {http.MethodGet, func(s *portalState) http.Handler { return s.client.LoginHandler() }},
		"/auth/portiko/callback":    {http.MethodGet, func(s *portalState) http.Handler { return callback(s.client.CallbackHandler()) }},
		"/auth/portiko/backchannel": {http.MethodPost, func(s *portalState) http.Handler { return s.client.BackchannelLogoutHandler() }},
		"/auth/portiko/events":      {http.MethodPost, func(s *portalState) http.Handler { return s.client.EventsHandler() }},
		"/auth/logout":              {http.MethodPost, p.logout},
	}
	logoutAll := p.RequireSession(http.HandlerFunc(p.logoutAll))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if oldAuthPath(path) {
			http.NotFound(w, r)
			return
		}
		if path == "/auth/logout/all" && r.Method == http.MethodPost {
			logoutAll.ServeHTTP(w, r)
			return
		}
		rt, ok := routes[path]
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != rt.method {
			w.Header().Set("Allow", rt.method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := p.state.Load()
		if st == nil {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
			return
		}
		rt.h(st).ServeHTTP(w, r)
	})
}

// logout ends this browser's session here and at the provider, then goes to /.
// The account.logout audit row is written first, while the session can still be
// read.
func (p *Portal) logout(st *portalState) http.Handler {
	end := st.client.EndSessionHandler("/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := p.sessionUserID(r, st); id != 0 {
			p.audit.Record(r.Context(), audit.Entry{
				ActorID: &id, UserID: &id, Action: audit.ActionAccountLogout, TargetType: audit.TargetUser, TargetID: &id,
			})
		}
		end.ServeHTTP(w, r)
	})
}

func (p *Portal) sessionUserID(r *http.Request, st *portalState) int64 {
	ck, err := r.Cookie(session.CookieName)
	if err != nil {
		return 0
	}
	sess, err := st.sessions.Get(r.Context(), ck.Value)
	if err != nil {
		return 0
	}
	var id int64
	if err := p.pool.QueryRow(r.Context(), `SELECT id FROM users WHERE subject = $1`, sess.Subject).Scan(&id); err != nil {
		return 0
	}
	return id
}

// headerOnly collects a response's headers and drops its body, so the rp's
// EndSessionHandler can be used for its cookie clearing and provider call while
// logoutAll writes its own JSON.
type headerOnly struct{ h http.Header }

func (b *headerOnly) Header() http.Header         { return b.h }
func (b *headerOnly) Write(p []byte) (int, error) { return len(p), nil }
func (b *headerOnly) WriteHeader(int)             {}

// logoutAll is "sign out of every browser" (#0094, #0132): every rp session of
// the signed-in subject is deleted, and the current one is also ended at the
// provider, which tells every site that held a session for the account through
// back-channel logout. Provider-side sessions in other browsers end through the
// provider's own sign-out everywhere (POST /id/accounts/v1/logout/all), which
// needs that browser's SSO cookie, so ShortLinks cannot make that call for
// them. The response keeps the old shape.
func (p *Portal) logoutAll(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	st := p.state.Load()
	if !ok || st == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	var subject string
	if err := p.pool.QueryRow(r.Context(), `SELECT subject FROM users WHERE id = $1`, u.ID).Scan(&subject); err != nil {
		slog.Error("idp: logout-all: reading the subject", "err", err, "user_id", u.ID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	// Was this request's own session still stored? Only then does it count.
	currentStored := false
	if ck, err := r.Cookie(session.CookieName); err == nil {
		_, gerr := st.sessions.Get(r.Context(), ck.Value)
		currentStored = gerr == nil
	}
	// The current session first, at the provider; it also clears the cookie.
	buf := &headerOnly{h: http.Header{}}
	st.client.EndSessionHandler("/").ServeHTTP(buf, r)
	for _, c := range buf.h.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", c)
	}
	n, err := st.sessions.DeleteForLogout(r.Context(), subject, "")
	if currentStored {
		n++ // the session ended above
	}
	if err != nil {
		slog.Error("idp: logout-all failed", "err", err, "user_id", u.ID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	id := u.ID
	p.audit.Record(r.Context(), audit.Entry{
		ActorID: &id, UserID: &id, Action: audit.ActionSessionsRevokedAll, TargetType: audit.TargetUser, TargetID: &id,
		Metadata: map[string]any{"revoked_count": n},
	})
	writeJSON(w, http.StatusOK, map[string]any{"message": "Signed out everywhere", "revoked_count": n})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
