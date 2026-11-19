package idp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
)

type seeded struct {
	userID int64
	email  string
	rp     virtualwebauthn.RelyingParty
	auth   virtualwebauthn.Authenticator
	cred   virtualwebauthn.Credential
}

// registerUser drives a real ShortLinks registration (start, verify, finish)
// with a virtual authenticator that is a synced, backed-up passkey (BE=1, BS=1),
// so the Backup Eligible flag has a non-default value to be copied.
func registerUser(t *testing.T, ctx context.Context, svc *auth.RegistrationService, pool *pgxpool.Pool, rp virtualwebauthn.RelyingParty, email string) seeded {
	t.Helper()
	if err := svc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	var token string
	if err := pool.QueryRow(ctx, `SELECT token FROM pending_registrations WHERE email = lower($1) ORDER BY id DESC LIMIT 1`, email).Scan(&token); err != nil {
		t.Fatal(err)
	}
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatalf("VerifyRegistration: %v", err)
	}
	handle := []byte(creation.Response.User.ID.(protocol.URLEncodedBase64))
	authn := virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
		UserHandle: handle, BackupEligible: true, BackupState: true,
	})
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	raw, _ := json.Marshal(creation)
	opts, err := virtualwebauthn.ParseAttestationOptions(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(rp, authn, cred, *opts)
	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish", bytes.NewReader([]byte(resp)))
	res, err := svc.FinishRegistration(ctx, token, "Synced Passkey", "", req)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	authn.AddCredential(cred)
	return seeded{userID: res.User.ID, email: email, rp: rp, auth: authn, cred: cred}
}

func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// TestImportAccountsAndPasskeys is the #0131 acceptance test: real ShortLinks
// registrations, an import (dry run, then for real, then again), and a Portiko
// sign-in with the same virtual authenticators.
func TestImportAccountsAndPasskeys(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	reset := func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS portiko CASCADE`)
		_, _ = pool.Exec(ctx, `TRUNCATE webauthn_challenges, pending_registrations, sessions, passkey_credentials, audit_log, clicks, links, users RESTART IDENTITY CASCADE`)
	}
	reset()
	defer reset()

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

	alice := registerUser(t, ctx, reg, pool, rp, "Alice@Example.com")
	bob := registerUser(t, ctx, reg, pool, rp, "bob@example.com")
	carol := registerUser(t, ctx, reg, pool, rp, "carol@example.com") // handle never captured
	dave := registerUser(t, ctx, reg, pool, rp, "dave@example.com")   // second passkey with another handle goes to alice
	var carolHandle []byte
	if err := pool.QueryRow(ctx, `SELECT user_handle FROM passkey_credentials WHERE user_id = $1`, carol.userID).Scan(&carolHandle); err != nil {
		t.Fatal(err)
	}
	// Carol registered before #0127 shipped: no handle was stored.
	if _, err := pool.Exec(ctx, `UPDATE passkey_credentials SET user_handle = NULL WHERE user_id = $1`, carol.userID); err != nil {
		t.Fatal(err)
	}
	// Dave's passkey becomes a second credential on Alice's account, which has a
	// different handle from her first. Dave himself keeps no passkey and no handle.
	if _, err := pool.Exec(ctx, `UPDATE passkey_credentials SET user_id = $1 WHERE user_id = $2`, alice.userID, dave.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET active = FALSE WHERE id = $1`, bob.userID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, ctx, pool, `SELECT count(DISTINCT user_handle) FROM passkey_credentials WHERE user_id = $1`, alice.userID); n != 2 {
		t.Fatalf("alice should hold credentials with 2 handles, got %d", n)
	}

	// Snapshot of ShortLinks' own tables, to prove the import leaves them alone.
	slSnapshot := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT
		  (SELECT coalesce(string_agg(t::text, '|' ORDER BY id), '') FROM passkey_credentials t) ||
		  (SELECT coalesce(string_agg((id, email, is_admin, active, created_at, last_login_at)::text, '|' ORDER BY id), '') FROM users) ||
		  (SELECT count(*)::text FROM sessions)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := slSnapshot()

	// --- disabled: a no-op ---
	off := *cfg
	off.PortikoEnabled = false
	if rep, err := Import(ctx, &off, pool, false); err != nil || rep != nil {
		t.Fatalf("Import with the switch off = %v, %v; want nil, nil", rep, err)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'portiko'`); n != 0 {
		t.Fatal("the switch is off but a portiko schema was created")
	}

	// --- dry run: counts only, nothing written ---
	rep, err := Import(ctx, cfg, pool, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Users != 4 || rep.CredentialsImported != 3 || rep.CredentialsSkipped != 1 || len(rep.Failed) != 0 {
		t.Fatalf("dry run report = %+v", rep)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0].Email != "carol@example.com" {
		t.Errorf("skipped = %+v, want carol", rep.Skipped)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'portiko'`); n != 0 {
		t.Fatal("a dry run created the portiko schema")
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM users WHERE subject IS NOT NULL`); n != 0 {
		t.Fatal("a dry run recorded a subject")
	}

	// --- the import ---
	rep, err = Import(ctx, cfg, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Users != 4 || rep.UsersCreated != 4 || rep.CredentialsImported != 3 || rep.CredentialsSkipped != 1 || len(rep.Failed) != 0 {
		t.Fatalf("import report = %+v", rep)
	}
	var out bytes.Buffer
	rep.Print(&out, false)
	t.Logf("\n%s", out.String())

	portikoCount := func(q string, args ...any) int {
		t.Helper()
		pp, err := pgxpool.New(ctx, mustDerive(t, dsn))
		if err != nil {
			t.Fatal(err)
		}
		defer pp.Close()
		return count(t, ctx, pp, q, args...)
	}
	assertState := func(label string) {
		t.Helper()
		if n := portikoCount(`SELECT count(*) FROM users`); n != 4 {
			t.Errorf("%s: portiko users = %d, want 4", label, n)
		}
		if n := portikoCount(`SELECT count(*) FROM identifiers WHERE kind = 'email' AND is_primary AND verified_at IS NOT NULL`); n != 4 {
			t.Errorf("%s: verified primary emails = %d, want 4", label, n)
		}
		if n := portikoCount(`SELECT count(*) FROM identifiers WHERE value = 'alice@example.com'`); n != 1 {
			t.Errorf("%s: alice's email should be stored normalized, once", label)
		}
		if n := portikoCount(`SELECT count(*) FROM passkey_credentials`); n != 3 {
			t.Errorf("%s: portiko credentials = %d, want 3", label, n)
		}
		if n := portikoCount(`SELECT count(*) FROM passkey_credentials WHERE rp_id = 'localhost' AND backup_eligible AND backup_state`); n != 3 {
			t.Errorf("%s: credentials with BE/BS copied and rp_id set = %d, want 3", label, n)
		}
		if n := portikoCount(`SELECT count(*) FROM users WHERE active`); n != 4 {
			t.Errorf("%s: every portiko account stays active (bob is deactivated in ShortLinks only)", label)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM users WHERE subject LIKE 'usr\_%'`); n != 4 {
			t.Errorf("%s: users.subject set on %d users, want 4", label, n)
		}
		if n := count(t, ctx, pool, `SELECT count(*) FROM audit_log WHERE action = 'account.portiko_imported'`); n != 4 {
			t.Errorf("%s: audit rows = %d, want one per user (4)", label, n)
		}
	}
	assertState("after first import")
	subjects := map[string]string{}
	rows, _ := pool.Query(ctx, `SELECT email, subject FROM users`)
	for rows.Next() {
		var e, s string
		_ = rows.Scan(&e, &s)
		subjects[e] = s
	}
	rows.Close()

	// --- again: nothing duplicated, nothing new, subjects stable ---
	rep, err = Import(ctx, cfg, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.UsersCreated != 0 || rep.CredentialsImported != 0 || rep.CredentialsPresent != 3 || rep.CredentialsSkipped != 1 || len(rep.Failed) != 0 {
		t.Fatalf("second import report = %+v", rep)
	}
	assertState("after second import")
	rows, _ = pool.Query(ctx, `SELECT email, subject FROM users`)
	for rows.Next() {
		var e, s string
		_ = rows.Scan(&e, &s)
		if subjects[e] != s {
			t.Errorf("subject for %s changed from %s to %s", e, subjects[e], s)
		}
	}
	rows.Close()

	// ShortLinks' own tables are untouched except users.subject and audit_log.
	if after := slSnapshot(); after != before {
		t.Errorf("the import changed ShortLinks' own tables:\nbefore %s\nafter  %s", before, after)
	}

	// A crash between Portiko's commit and ShortLinks' own: the subject is lost
	// on one user; the next run must adopt the existing account by email.
	if _, err := pool.Exec(ctx, `UPDATE users SET subject = NULL WHERE email = 'bob@example.com'`); err != nil {
		t.Fatal(err)
	}
	if rep, err = Import(ctx, cfg, pool, false); err != nil || rep.UsersCreated != 0 || rep.UsersAdopted != 4 {
		t.Fatalf("recovery import = %+v, %v", rep, err)
	}
	var bobSubject string
	_ = pool.QueryRow(ctx, `SELECT subject FROM users WHERE email = 'bob@example.com'`).Scan(&bobSubject)
	if bobSubject != subjects["bob@example.com"] {
		t.Errorf("bob re-adopted subject %s, want %s", bobSubject, subjects["bob@example.com"])
	}
	if n := portikoCount(`SELECT count(*) FROM users`); n != 4 {
		t.Errorf("recovery duplicated an account: %d", n)
	}

	// A handle captured since the last run is picked up by the next one.
	if _, err := pool.Exec(ctx, `UPDATE passkey_credentials SET user_handle = $1 WHERE user_id = $2`, carolHandle, carol.userID); err != nil {
		t.Fatal(err)
	}
	if rep, err = Import(ctx, cfg, pool, false); err != nil || rep.CredentialsImported != 1 || rep.CredentialsSkipped != 0 {
		t.Fatalf("late-handle import = %+v, %v", rep, err)
	}

	// --- Portiko sign-in with the imported passkeys ---
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	p, err := start(ctx, cfg, pool)
	if err != nil {
		t.Fatalf("starting the provider over the imported data: %v", err)
	}
	defer p.Close()
	srv.Config.Handler = mountAt(p.Handler(), http.NotFoundHandler())
	srv.Start()
	defer srv.Close()

	signIn := func(s seeded, account seeded) {
		t.Helper()
		resp, err := client.Get(origin + "/id/accounts/v1/login/start")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		opts, err := virtualwebauthn.ParseAssertionOptions(string(raw))
		if err != nil {
			t.Fatalf("assertion options: %s", raw)
		}
		assertion := virtualwebauthn.CreateAssertionResponse(s.rp, s.auth, s.cred, *opts)
		fin, err := client.Post(origin+"/id/accounts/v1/login/finish", "application/json", strings.NewReader(assertion))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(fin.Body)
		fin.Body.Close()
		if fin.StatusCode != http.StatusOK {
			t.Fatalf("Portiko sign-in for %s with the imported passkey: %d %s", s.email, fin.StatusCode, body)
		}
		// The new session belongs to the account whose subject ShortLinks recorded.
		var want string
		if err := pool.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, account.userID).Scan(&want); err != nil {
			t.Fatal(err)
		}
		pp, _ := pgxpool.New(ctx, mustDerive(t, dsn))
		defer pp.Close()
		var got string
		if err := pp.QueryRow(ctx, `SELECT u.subject FROM sso_sessions s JOIN users u ON u.id = s.user_id
			ORDER BY s.id DESC LIMIT 1`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s signed in as subject %s, want users.subject %s", s.email, got, want)
		}
	}
	signIn(alice, alice) // handle family 1
	signIn(dave, alice)  // handle family 2, same account as alice: per-credential handles work
	signIn(bob, bob)     // deactivated in ShortLinks only; Portiko still authenticates, #0132 enforces
	signIn(carol, carol) // handle captured late and imported on the next run
}

func mustDerive(t *testing.T, dsn string) string {
	t.Helper()
	u, err := DeriveDatabaseURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
