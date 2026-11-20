package idp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Kolonnade/Portiko/rp/session"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

func portalReset(ctx context.Context, pool *pgxpool.Pool) {
	_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS portiko CASCADE`)
	_, _ = pool.Exec(ctx, `TRUNCATE rp_sessions, webauthn_challenges, pending_registrations, sessions, passkey_credentials, audit_log, clicks, links, users RESTART IDENTITY CASCADE`)
}

func jwtClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestPortikoSignInRoundTrip is the #0132 acceptance test: real ShortLinks
// registrations, the #0131 import, then a full rp sign-in against the embedded
// provider with the same virtual authenticators, through ShortLinks' real
// guards. It asserts that the ShortLinks session belongs to the right user and
// that the token's sub equals users.subject, which #0131 did not check.
func TestPortikoSignInRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	portalReset(ctx, pool)
	defer portalReset(ctx, pool)

	srv := httptest.NewUnstartedServer(nil)
	origin := "http://" + srv.Listener.Addr().String()
	cfg := &config.Config{
		BaseURL: origin, DatabaseURL: dsn, WebAuthnRPID: "localhost", WebAuthnRPOrigin: origin,
		PortikoEnabled: true, PortikoClientSecret: "0123456789abcdef0123",
		PortikoCryptoKey: [32]byte{9, 8, 7, 6, 5, 4, 3, 2, 1},
	}
	rp := virtualwebauthn.RelyingParty{ID: "localhost", Name: "ShortLinks", Origin: origin}
	if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('registrations_enabled', 'true', now())
		ON CONFLICT (key) DO UPDATE SET value = 'true'`); err != nil {
		t.Fatal(err)
	}
	wa, err := auth.NewWebAuthn(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reg := auth.NewRegistrationService(auth.NewStore(pool), wa, auth.NoOpMailer{}, nil, cfg)
	alice := registerUser(t, ctx, reg, pool, rp, "alice@example.com") // first user: admin
	bob := registerUser(t, ctx, reg, pool, rp, "bob@example.com")

	rep, err := Import(ctx, cfg, pool, false)
	if err != nil || rep.CredentialsImported != 2 {
		t.Fatalf("import = %+v, %v", rep, err)
	}
	subjectOf := func(id int64) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// ShortLinks' guards, built exactly as cmd/shortlinks builds them.
	portal := NewPortal(cfg, pool)
	store := auth.NewStore(pool)
	requireAPI := middleware.RequireGuardOrAPIToken(portal.RequireSession, store, middleware.DefaultAPITokenLimits())
	requireAdmin := func(next http.Handler) http.Handler {
		return portal.RequireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/me", requireAPI(http.HandlerFunc(handlers.NewMeHandler(cfg.BaseURL).Me)))
	mux.Handle("GET /admin/ping", requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })))
	mux.HandleFunc("GET /auth/login/start", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("old")) })
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	h, closeFn, err := MountPortal(ctx, cfg, pool, mux, portal)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if portal.state.Load() == nil {
		t.Fatal("the relying party did not start")
	}
	srv.Config.Handler = h
	srv.Start()
	defer srv.Close()

	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 15 * time.Second}
	}
	get := func(c *http.Client, path string) (int, string) {
		t.Helper()
		if !strings.HasPrefix(path, "http") {
			path = origin + path
		}
		resp, err := c.Get(path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	post := func(c *http.Client, path string) (int, string) {
		t.Helper()
		resp, err := c.Post(origin+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// signIn drives /auth/portiko/login through the provider's passkey sign-in
	// and back, ending on returnTo. It returns the callback's final status.
	signIn := func(c *http.Client, s seeded, returnTo string) (int, string) {
		t.Helper()
		resp, err := c.Get(origin + "/auth/portiko/login?return_to=" + url.QueryEscape(returnTo))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		reqID := resp.Request.URL.Query().Get("authRequestID")
		if reqID == "" || !strings.HasPrefix(resp.Request.URL.Path, "/id/") {
			t.Fatalf("the sign-in did not land on the provider with an authRequestID: %s", resp.Request.URL)
		}
		st, err := c.Get(origin + "/id/accounts/v1/login/start")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(st.Body)
		st.Body.Close()
		opts, err := virtualwebauthn.ParseAssertionOptions(string(raw))
		if err != nil {
			t.Fatalf("assertion options: %s", raw)
		}
		assertion := virtualwebauthn.CreateAssertionResponse(s.rp, s.auth, s.cred, *opts)
		fin, err := c.Post(origin+"/id/accounts/v1/login/finish?authRequestID="+url.QueryEscape(reqID), "application/json", strings.NewReader(assertion))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(fin.Body)
		fin.Body.Close()
		if fin.StatusCode != http.StatusOK {
			t.Fatalf("provider sign-in: %d %s", fin.StatusCode, body)
		}
		var out struct {
			Redirect string `json:"redirect"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.Redirect == "" {
			t.Fatalf("provider sign-in gave no redirect: %s", body)
		}
		return get(c, out.Redirect) // provider callback -> /auth/portiko/callback -> returnTo
	}

	cbStale := func(c *http.Client) *http.Client {
		u, _ := url.Parse(origin)
		c.Jar.SetCookies(u, []*http.Cookie{{Name: session.CookieName, Value: "no-such-session", Path: "/"}})
		return c
	}

	// --- old routes are gone; unrelated routes are untouched ---
	anon := newClient()
	if code, body := get(anon, "/api/me"); code != http.StatusUnauthorized || body != `{"error":"unauthenticated"}` {
		t.Errorf("anonymous /api/me = %d %s, want 401 unauthenticated", code, body)
	}
	if code, body := get(cbStale(newClient()), "/api/me"); code != http.StatusUnauthorized || body != `{"error":"unauthenticated"}` {
		t.Errorf("stale-cookie /api/me = %d %s, want 401 unauthenticated", code, body)
	}
	if code, _ := get(anon, "/auth/login/start"); code != http.StatusNotFound {
		t.Errorf("old /auth/login/start = %d, want 404 with Portiko on", code)
	}
	if code, body := get(anon, "/health"); code != 200 || body != "ok" {
		t.Errorf("/health = %d %q", code, body)
	}

	// --- alice: import, sign in, /api/me ---
	ca := newClient()
	code, body := signIn(ca, alice, "/api/me")
	if code != http.StatusOK {
		t.Fatalf("sign-in landing on /api/me = %d %s", code, body)
	}
	var me struct {
		ID      int64  `json:"id"`
		Email   string `json:"email"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal([]byte(body), &me); err != nil {
		t.Fatal(err)
	}
	if me.ID != alice.userID || me.Email != "alice@example.com" || !me.IsAdmin {
		t.Errorf("/api/me after sign-in = %+v, want alice (%d), admin", me, alice.userID)
	}
	if code, body := get(ca, "/admin/ping"); code != 200 || body != "pong" {
		t.Errorf("alice /admin/ping = %d %q", code, body)
	}

	// The stored session: its subject is users.subject, and so is the token's sub.
	u, _ := url.Parse(origin)
	var sid string
	for _, ck := range ca.Jar.Cookies(u) {
		if ck.Name == session.CookieName {
			sid = ck.Value
		}
	}
	if sid == "" {
		t.Fatal("no rp session cookie after sign-in")
	}
	sess, err := portal.state.Load().sessions.Get(ctx, sid)
	if err != nil {
		t.Fatalf("session not in rp_sessions: %v", err)
	}
	want := subjectOf(alice.userID)
	if sess.Subject != want {
		t.Errorf("rp_sessions subject = %q, want users.subject %q", sess.Subject, want)
	}
	if sub := jwtClaims(t, sess.AccessToken)["sub"]; sub != want {
		t.Errorf("access token sub = %v, want users.subject %q", sub, want)
	}
	if sub := jwtClaims(t, sess.IDToken)["sub"]; sub != want {
		t.Errorf("ID token sub = %v, want users.subject %q", sub, want)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action = 'account.login' AND user_id = $1`, alice.userID); n != 1 {
		t.Errorf("account.login audit rows for alice = %d, want 1", n)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM users WHERE last_login_at IS NOT NULL AND id = $1`, alice.userID); n != 1 {
		t.Error("last_login_at not set")
	}
	// The cookie is an opaque id; the table never holds it, or a token, in the clear.
	if n := count(t, ctx, pool, `SELECT count(*) FROM rp_sessions WHERE id_hash = $1 OR position(convert_to($2, 'UTF8') in sealed) > 0`, sid, sess.AccessToken[:20]); n != 0 {
		t.Error("rp_sessions holds the session id or a token in the clear")
	}

	// --- bob: not admin; deactivating him ends access on the next request ---
	cb := newClient()
	if code, body := signIn(cb, bob, "/api/me"); code != 200 || !strings.Contains(body, `"email":"bob@example.com"`) {
		t.Fatalf("bob sign-in = %d %s", code, body)
	}
	if code, _ := get(cb, "/admin/ping"); code != http.StatusForbidden {
		t.Errorf("bob /admin/ping = %d, want 403", code)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET active = FALSE WHERE id = $1`, bob.userID); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(cb, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("deactivated bob /api/me = %d, want 401", code)
	}
	// ...and he cannot sign in again: no session is created.
	before := count(t, ctx, pool, `SELECT count(*) FROM rp_sessions`)
	cb2 := newClient()
	if code, body := signIn(cb2, bob, "/api/me"); code != http.StatusForbidden || !strings.Contains(body, `"error":"account deactivated"`) {
		t.Errorf("deactivated bob signing in again = %d %s, want 403 account deactivated", code, body)
	}
	if after := count(t, ctx, pool, `SELECT count(*) FROM rp_sessions`); after != before {
		t.Errorf("rp_sessions grew from %d to %d on a refused sign-in", before, after)
	}

	// --- sign out everywhere, then sign out ---
	ca2 := newClient()
	if code, _ := signIn(ca2, alice, "/api/me"); code != 200 {
		t.Fatalf("alice second browser = %d", code)
	}
	if code, body := post(ca, "/auth/logout/all"); code != 200 || !strings.Contains(body, `"revoked_count":2`) {
		t.Errorf("logout/all = %d %s, want 200 and revoked_count 2", code, body)
	}
	for name, c := range map[string]*http.Client{"first browser": ca, "second browser": ca2} {
		if code, _ := get(c, "/api/me"); code != http.StatusUnauthorized {
			t.Errorf("%s after sign out everywhere: /api/me = %d, want 401", name, code)
		}
	}
	ca3 := newClient()
	if code, _ := signIn(ca3, alice, "/api/me"); code != 200 {
		t.Fatalf("alice sign-in after logout-all = %d", code)
	}
	if code, _ := post(ca3, "/auth/logout"); code != 200 { // redirects to / (the mux has no "/": 404 is fine too)
		if code != http.StatusNotFound {
			t.Errorf("logout = %d", code)
		}
	}
	if code, _ := get(ca3, "/api/me"); code != http.StatusUnauthorized {
		t.Errorf("/api/me after logout = %d, want 401", code)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action IN ('account.logout','session.revoked_all')`); n < 2 {
		t.Errorf("logout audit rows = %d, want at least 2", n)
	}
}

// TestPortalOnSignIn exercises the users mapping directly: by subject, by
// verified email (recording the subject), a new user, the admin rule under
// concurrency, closed registration, a deactivated account, and a subject
// conflict.
func TestPortalOnSignIn(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	portalReset(ctx, pool)
	defer portalReset(ctx, pool)
	setReg := func(on string) {
		if _, err := pool.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ('registrations_enabled', $1, now())
			ON CONFLICT (key) DO UPDATE SET value = $1`, on); err != nil {
			t.Fatal(err)
		}
	}
	portal := NewPortal(&config.Config{AdminEmail: "boss@example.com"}, pool)
	in := func(sub, email string) error {
		return portal.onSignIn(ctx, &session.Session{Subject: sub, Email: email})
	}
	row := func(email string) (id int64, admin bool, subject *string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT id, is_admin, subject FROM users WHERE email = $1`, email).Scan(&id, &admin, &subject); err != nil {
			t.Fatalf("no users row for %s: %v", email, err)
		}
		return
	}

	// Registrations closed: an account new to ShortLinks is refused, nothing is written.
	setReg("false")
	if err := in("usr_new", "new@example.com"); err != ErrRegistrationClosed {
		t.Fatalf("closed registration: %v, want ErrRegistrationClosed", err)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM users`); n != 0 {
		t.Fatalf("a refused sign-in wrote %d users", n)
	}

	// Registrations open; eight concurrent first sign-ins: exactly one is admin.
	setReg("true")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := "usr_race_" + string(rune('a'+i))
			if err := in(sub, sub+"@example.com"); err != nil {
				t.Errorf("concurrent sign-in %s: %v", sub, err)
			}
		}()
	}
	wg.Wait()
	if n := count(t, ctx, pool, `SELECT count(*) FROM users WHERE is_admin`); n != 1 {
		t.Errorf("admins after 8 concurrent first sign-ins = %d, want exactly 1", n)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action = 'account.registered'`); n != 8 {
		t.Errorf("account.registered rows = %d, want 8", n)
	}

	// ADMIN_EMAIL promotes; repeat sign-in is a login, not a new user.
	if err := in("usr_boss", "Boss@Example.com"); err != nil {
		t.Fatal(err)
	}
	if _, admin, _ := row("boss@example.com"); !admin {
		t.Error("ADMIN_EMAIL user was not promoted")
	}
	if err := in("usr_boss", "boss@example.com"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action = 'account.login'`); n != 1 {
		t.Errorf("account.login rows = %d, want 1", n)
	}

	// An existing user with no subject (the import missed them) is adopted by email,
	// even with registrations closed.
	setReg("false")
	if _, err := pool.Exec(ctx, `INSERT INTO users (email, is_admin, active, created_at) VALUES ('missed@example.com', FALSE, TRUE, now())`); err != nil {
		t.Fatal(err)
	}
	if err := in("usr_missed", "missed@example.com"); err != nil {
		t.Fatalf("adopting by email: %v", err)
	}
	if _, _, s := row("missed@example.com"); s == nil || *s != "usr_missed" {
		t.Errorf("subject after adoption = %v, want usr_missed", s)
	}
	// ...but never re-pointed at a different subject.
	if err := in("usr_other", "missed@example.com"); err == nil {
		t.Error("a user linked to usr_missed was re-pointed at usr_other")
	}
	if _, _, s := row("missed@example.com"); *s != "usr_missed" {
		t.Errorf("subject changed to %v", *s)
	}

	// A deactivated account is refused.
	if _, err := pool.Exec(ctx, `UPDATE users SET active = FALSE WHERE email = 'missed@example.com'`); err != nil {
		t.Fatal(err)
	}
	if err := in("usr_missed", "missed@example.com"); err != ErrAccountDeactivated {
		t.Errorf("deactivated: %v, want ErrAccountDeactivated", err)
	}
}

// TestCallbackRefusals checks that every refusal OnSignIn can make reaches the
// browser as a 403 with its message, through the callback wrapper, and that a
// real failure stays a 500.
func TestCallbackRefusals(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	portalReset(ctx, pool)
	defer portalReset(ctx, pool)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO settings (key, value, updated_at) VALUES ('registrations_enabled', 'false', now()) ON CONFLICT (key) DO UPDATE SET value = 'false'`)
	exec(`INSERT INTO users (email, is_admin, active, created_at, subject) VALUES ('off@example.com', FALSE, FALSE, now(), 'usr_off')`)
	exec(`INSERT INTO users (email, is_admin, active, created_at, subject) VALUES ('linked@example.com', FALSE, TRUE, now(), 'usr_linked')`)
	exec(`INSERT INTO users (email, is_admin, active, created_at) VALUES ('Dup@example.com', FALSE, TRUE, now()), ('dup@example.com', FALSE, TRUE, now())`)

	portal := NewPortal(&config.Config{}, pool)
	h := callback(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := &session.Session{Subject: r.URL.Query().Get("sub"), Email: r.URL.Query().Get("email")}
		if err := portal.signIn(r.Context(), sess); err != nil {
			http.Error(w, "sign-in failed", http.StatusInternalServerError) // what rp does
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for name, tc := range map[string]struct {
		sub, email string
		code       int
		body       string
	}{
		"deactivated":         {"usr_off", "off@example.com", 403, "account deactivated"},
		"registration closed": {"usr_brand_new", "brand@example.com", 403, "Registration is currently closed."},
		"subject conflict":    {"usr_other", "linked@example.com", 403, "contact an administrator"},
		"ambiguous email":     {"usr_dup", "dup@example.com", 403, "contact an administrator"},
		"no email (failure)":  {"usr_x", "", 500, "sign-in failed"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/cb?sub="+tc.sub+"&email="+url.QueryEscape(tc.email), nil))
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s: %d %q, want %d containing %q", name, rec.Code, rec.Body.String(), tc.code, tc.body)
		}
		if tc.code == 403 && !strings.Contains(rec.Header().Get("Content-Type"), "json") {
			t.Errorf("%s: content type %q", name, rec.Header().Get("Content-Type"))
		}
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM users WHERE subject IS NOT NULL AND lower(email) = 'dup@example.com'`); n != 0 {
		t.Error("an ambiguous email got a subject recorded")
	}
}
