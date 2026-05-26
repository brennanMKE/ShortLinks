package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// testRPID / testRPOrigin are the relying-party values used across the auth
// integration tests. They match the virtual authenticator's relying party.
const (
	testRPID     = "go.sstools.co"
	testRPOrigin = "https://go.sstools.co"
)

// recordingMailer captures the verification calls so a test can assert the
// mailer was invoked and recover the emailed token (which never leaves the
// server otherwise).
type recordingMailer struct {
	mu     sync.Mutex
	calls  int
	lastTo string
	token  string
}

func (m *recordingMailer) SendVerification(_ context.Context, toEmail, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastTo = toEmail
	m.token = token
	return nil
}

func (m *recordingMailer) SendRecovery(_ context.Context, _, _ string) error { return nil }

func (m *recordingMailer) recorded() (int, string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.lastTo, m.token
}

// testPool connects to TEST_DATABASE_URL or skips. It also registers a cleanup
// that truncates the auth tables so each test run starts from a clean slate and
// re-runs are deterministic.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncateAuthTables(t, pool)
	t.Cleanup(func() {
		truncateAuthTables(t, pool)
		pool.Close()
	})
	return pool
}

// truncateAuthTables clears every auth-related table (and its dependents) so
// the database is left clean. RESTART IDENTITY resets the serial sequences.
func truncateAuthTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx,
		`TRUNCATE webauthn_challenges, pending_registrations, sessions,
		          passkey_credentials, audit_log, clicks, links, users
		 RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate auth tables: %v", err)
	}
}

// setRegistrationsEnabled writes the registrations_enabled setting.
func setRegistrationsEnabled(t *testing.T, pool *pgxpool.Pool, enabled bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value := "false"
	if enabled {
		value = "true"
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		settingRegistrationsEnabled, value)
	if err != nil {
		t.Fatalf("set registrations_enabled: %v", err)
	}
}

// newService builds a RegistrationService over the test pool with the given
// mailer and admin email.
func newService(t *testing.T, pool *pgxpool.Pool, mailer Mailer, adminEmail string) *RegistrationService {
	t.Helper()
	cfg := &config.Config{
		WebAuthnRPID:     testRPID,
		WebAuthnRPOrigin: testRPOrigin,
		AdminEmail:       adminEmail,
	}
	wa, err := NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	return NewRegistrationService(NewStore(pool), wa, mailer, nil, cfg)
}

// TestStartRegistration_DisabledReturns403 confirms the registrations_enabled
// gate is enforced (read fresh from the DB) and no pending row is created.
func TestStartRegistration_DisabledReturns403(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, false)
	mailer := &recordingMailer{}
	svc := newService(t, pool, mailer, "")

	err := svc.StartRegistration(context.Background(), "alice@example.com", "")
	if err != ErrRegistrationsDisabled {
		t.Fatalf("StartRegistration error = %v, want ErrRegistrationsDisabled", err)
	}
	if calls, _, _ := mailer.recorded(); calls != 0 {
		t.Errorf("mailer called %d times, want 0", calls)
	}
	if n := countPending(t, pool, "alice@example.com"); n != 0 {
		t.Errorf("pending_registrations rows = %d, want 0", n)
	}
}

// TestStartRegistration_EnabledCreatesPendingAndMails confirms the happy start
// path: a pending row is created and the mailer is invoked with a token.
func TestStartRegistration_EnabledCreatesPendingAndMails(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	mailer := &recordingMailer{}
	svc := newService(t, pool, mailer, "")

	if err := svc.StartRegistration(context.Background(), "Bob@Example.com", ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}

	calls, to, token := mailer.recorded()
	if calls != 1 {
		t.Fatalf("mailer calls = %d, want 1", calls)
	}
	if to != "bob@example.com" {
		t.Errorf("mailer recipient = %q, want lowercased bob@example.com", to)
	}
	if token == "" {
		t.Error("mailer token is empty")
	}
	if n := countPending(t, pool, "bob@example.com"); n != 1 {
		t.Errorf("pending_registrations rows = %d, want 1", n)
	}
}

// TestStartRegistration_DuplicateEmailNoLeak confirms an already-registered
// email does not error to the caller (no account-existence leak) and does not
// create a pending row or send mail.
func TestStartRegistration_DuplicateEmailNoLeak(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	insertUser(t, pool, "carol@example.com", false)
	mailer := &recordingMailer{}
	svc := newService(t, pool, mailer, "")

	if err := svc.StartRegistration(context.Background(), "carol@example.com", ""); err != ErrEmailRegistered {
		t.Fatalf("StartRegistration error = %v, want ErrEmailRegistered", err)
	}
	if calls, _, _ := mailer.recorded(); calls != 0 {
		t.Errorf("mailer called %d times for duplicate, want 0", calls)
	}
}

// TestVerifyRegistration_UnknownToken confirms an unknown token is rejected.
func TestVerifyRegistration_UnknownToken(t *testing.T) {
	pool := testPool(t)
	svc := newService(t, pool, &recordingMailer{}, "")

	if _, err := svc.VerifyRegistration(context.Background(), "no-such-token"); err != ErrTokenInvalid {
		t.Fatalf("VerifyRegistration error = %v, want ErrTokenInvalid", err)
	}
}

// TestVerifyRegistration_ExpiredToken confirms a token past its 5-minute TTL is
// rejected.
func TestVerifyRegistration_ExpiredToken(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")

	// Backdate the clock so the pending row's expiry is already in the past.
	svc.now = func() time.Time { return time.Now().Add(-10 * time.Minute) }
	if err := svc.StartRegistration(context.Background(), "dave@example.com", ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	token := lastPendingToken(t, pool, "dave@example.com")

	svc.now = time.Now // restore to now; the row is now expired
	if _, err := svc.VerifyRegistration(context.Background(), token); err != ErrTokenInvalid {
		t.Fatalf("VerifyRegistration error = %v, want ErrTokenInvalid", err)
	}
}

// TestVerifyRegistration_OptionsShape confirms BeginRegistration produces the
// PRD-mandated options: residentKey required, userVerification required,
// authenticatorAttachment omitted, and ES256+RS256 pubKeyCredParams with a
// random 16-byte user handle.
func TestVerifyRegistration_OptionsShape(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")

	if err := svc.StartRegistration(context.Background(), "erin@example.com", ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	token := lastPendingToken(t, pool, "erin@example.com")

	creation, err := svc.VerifyRegistration(context.Background(), token)
	if err != nil {
		t.Fatalf("VerifyRegistration: %v", err)
	}

	sel := creation.Response.AuthenticatorSelection
	if sel.ResidentKey != protocol.ResidentKeyRequirementRequired {
		t.Errorf("residentKey = %q, want required", sel.ResidentKey)
	}
	if sel.UserVerification != protocol.VerificationRequired {
		t.Errorf("userVerification = %q, want required", sel.UserVerification)
	}
	if sel.AuthenticatorAttachment != "" {
		t.Errorf("authenticatorAttachment = %q, want omitted (empty)", sel.AuthenticatorAttachment)
	}

	params := creation.Response.Parameters
	if len(params) != 2 ||
		params[0].Algorithm != webauthncose.AlgES256 ||
		params[1].Algorithm != webauthncose.AlgRS256 {
		t.Errorf("pubKeyCredParams = %+v, want [ES256, RS256]", params)
	}

	handle, ok := creation.Response.User.ID.(protocol.URLEncodedBase64)
	if !ok {
		t.Fatalf("user.id type = %T, want protocol.URLEncodedBase64", creation.Response.User.ID)
	}
	if len(handle) != userHandleLen {
		t.Errorf("user.id length = %d, want %d", len(handle), userHandleLen)
	}

	// Verify the authenticatorAttachment key is genuinely absent from the JSON
	// (not just empty), since iCloud Keychain compatibility depends on it.
	raw, err := json.Marshal(creation)
	if err != nil {
		t.Fatalf("marshal creation: %v", err)
	}
	if bytes.Contains(raw, []byte("authenticatorAttachment")) {
		t.Errorf("serialized options must omit authenticatorAttachment; got: %s", raw)
	}

	// The challenge must have been persisted, linked to the token.
	if n := countChallenges(t, pool, token); n != 1 {
		t.Errorf("webauthn_challenges rows for token = %d, want 1", n)
	}
}

// TestFullCeremony_EndToEnd drives start → verify → finish against a virtual
// authenticator, exercising the real cryptographic FinishRegistration path. It
// asserts a user, credential, and session land in the DB and that the first
// user is promoted to admin.
func TestFullCeremony_EndToEnd(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")

	const email = "frank@example.com"
	ctx := context.Background()

	// Step 1: start.
	if err := svc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	token := lastPendingToken(t, pool, email)

	// Step 2: verify → options.
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatalf("VerifyRegistration: %v", err)
	}

	// Build a virtual authenticator + credential and produce a real attestation.
	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "ShortLinks", Origin: testRPOrigin}
	authenticator := virtualwebauthn.NewAuthenticator()
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	attestationResponse := virtualwebauthn.CreateAttestationResponse(rp, authenticator, cred, *attOpts)

	// Step 3: finish. The attestation JSON is the request body.
	req := httptest.NewRequest(http.MethodPost,
		"/auth/register/finish?token="+token+"&device_name=Test+Key",
		bytes.NewReader([]byte(attestationResponse)))
	result, err := svc.FinishRegistration(ctx, token, "Test Key", "", req)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	if result.User.Email != email {
		t.Errorf("user email = %q, want %q", result.User.Email, email)
	}
	if !result.User.IsAdmin {
		t.Error("first user should be promoted to admin")
	}
	if result.SessionToken == "" {
		t.Error("session token is empty")
	}

	// Verify rows landed and ephemeral rows were cleaned up.
	if n := countCredentials(t, pool, result.User.ID); n != 1 {
		t.Errorf("passkey_credentials rows = %d, want 1", n)
	}
	if n := countSessions(t, pool, result.SessionToken); n != 1 {
		t.Errorf("sessions rows for token = %d, want 1", n)
	}
	if n := countPending(t, pool, email); n != 0 {
		t.Errorf("pending_registrations rows after finish = %d, want 0", n)
	}
	if n := countChallenges(t, pool, token); n != 0 {
		t.Errorf("webauthn_challenges rows after finish = %d, want 0", n)
	}

	// The stored credential id must match the authenticator's credential.
	storedID := credentialID(t, pool, result.User.ID)
	if !bytes.Equal(storedID, cred.ID) {
		t.Errorf("stored credential_id = %x, want %x", storedID, cred.ID)
	}

	// device_name was persisted.
	if got := credentialDeviceName(t, pool, result.User.ID); got != "Test Key" {
		t.Errorf("credential device_name = %q, want %q", got, "Test Key")
	}
}

// TestFullCeremony_AdminEmailPromotion confirms a non-first user whose email
// matches ADMIN_EMAIL is promoted to admin, while another non-matching user is
// not.
func TestFullCeremony_AdminEmailPromotion(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)

	// Pre-seed an existing user so the next registrant is NOT the first user.
	insertUser(t, pool, "existing@example.com", true)

	adminEmail := "boss@example.com"
	svc := newService(t, pool, &recordingMailer{}, adminEmail)

	// A registrant matching ADMIN_EMAIL is promoted even though not first.
	adminUser := runCeremony(t, svc, pool, adminEmail)
	if !adminUser.IsAdmin {
		t.Errorf("ADMIN_EMAIL registrant should be admin")
	}

	// A registrant not matching ADMIN_EMAIL and not first is a normal user.
	normalUser := runCeremony(t, svc, pool, "regular@example.com")
	if normalUser.IsAdmin {
		t.Errorf("non-admin, non-first registrant should not be admin")
	}
}

// runCeremony drives a full start→verify→finish ceremony for email and returns
// the created user. Used by promotion tests.
func runCeremony(t *testing.T, svc *RegistrationService, pool *pgxpool.Pool, email string) CreatedUser {
	t.Helper()
	ctx := context.Background()
	if err := svc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatalf("StartRegistration(%s): %v", email, err)
	}
	token := lastPendingToken(t, pool, email)
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatalf("VerifyRegistration(%s): %v", email, err)
	}

	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "ShortLinks", Origin: testRPOrigin}
	authenticator := virtualwebauthn.NewAuthenticator()
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		t.Fatalf("marshal options: %v", err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	attestationResponse := virtualwebauthn.CreateAttestationResponse(rp, authenticator, cred, *attOpts)

	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish?token="+token,
		bytes.NewReader([]byte(attestationResponse)))
	result, err := svc.FinishRegistration(ctx, token, "", "", req)
	if err != nil {
		t.Fatalf("FinishRegistration(%s): %v", email, err)
	}
	return result.User
}

// --- small query helpers used by the tests ---

func insertUser(t *testing.T, pool *pgxpool.Pool, email string, admin bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (email, is_admin, active, created_at) VALUES ($1, $2, TRUE, now())`,
		email, admin); err != nil {
		t.Fatalf("insert user %s: %v", email, err)
	}
}

func countPending(t *testing.T, pool *pgxpool.Pool, email string) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM pending_registrations WHERE email = $1`, email)
}

func countChallenges(t *testing.T, pool *pgxpool.Pool, token string) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM webauthn_challenges WHERE pending_registration_token = $1`, token)
}

func countCredentials(t *testing.T, pool *pgxpool.Pool, userID int64) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM passkey_credentials WHERE user_id = $1`, userID)
}

func countSessions(t *testing.T, pool *pgxpool.Pool, token string) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM sessions WHERE token = $1`, token)
}

func scanCount(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", sql, err)
	}
	return n
}

func lastPendingToken(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var token string
	if err := pool.QueryRow(ctx,
		`SELECT token FROM pending_registrations WHERE email = $1 ORDER BY id DESC LIMIT 1`,
		email).Scan(&token); err != nil {
		t.Fatalf("fetch pending token for %s: %v", email, err)
	}
	return token
}

func credentialID(t *testing.T, pool *pgxpool.Pool, userID int64) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id []byte
	if err := pool.QueryRow(ctx,
		`SELECT credential_id FROM passkey_credentials WHERE user_id = $1`, userID).Scan(&id); err != nil {
		t.Fatalf("fetch credential_id: %v", err)
	}
	return id
}

func credentialDeviceName(t *testing.T, pool *pgxpool.Pool, userID int64) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(device_name, '') FROM passkey_credentials WHERE user_id = $1`, userID).Scan(&name); err != nil {
		t.Fatalf("fetch device_name: %v", err)
	}
	return name
}
