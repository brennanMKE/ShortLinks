package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// recoveryRecordingMailer captures SendRecovery calls so a test can assert the
// recovery mailer was invoked, recover the emailed token (which never leaves
// the server otherwise), and confirm SendVerification is never called by the
// recovery flow.
type recoveryRecordingMailer struct {
	mu            sync.Mutex
	recoveryCalls int
	verifyCalls   int
	lastTo        string
	token         string
}

func (m *recoveryRecordingMailer) SendVerification(_ context.Context, _, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verifyCalls++
	return nil
}

func (m *recoveryRecordingMailer) SendRecovery(_ context.Context, toEmail, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryCalls++
	m.lastTo = toEmail
	m.token = token
	return nil
}

func (m *recoveryRecordingMailer) recorded() (recoveryCalls, verifyCalls int, to, token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoveryCalls, m.verifyCalls, m.lastTo, m.token
}

// newRecoveryService builds a RecoveryService over the test pool with the given
// mailer.
func newRecoveryService(t *testing.T, pool *pgxpool.Pool, mailer Mailer) *RecoveryService {
	t.Helper()
	cfg := &config.Config{
		WebAuthnRPID:     testRPID,
		WebAuthnRPOrigin: testRPOrigin,
	}
	wa, err := NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	return NewRecoveryService(NewStore(pool), wa, mailer, nil)
}

// TestStartRecovery_UnknownEmailNoLeak confirms a recovery request for an
// unregistered email returns no error (the handler responds with the same
// generic 200), creates no token, and sends no mail.
func TestStartRecovery_UnknownEmailNoLeak(t *testing.T) {
	pool := testPool(t)
	mailer := &recoveryRecordingMailer{}
	svc := newRecoveryService(t, pool, mailer)

	if err := svc.StartRecovery(context.Background(), "ghost@example.com", ""); err != nil {
		t.Fatalf("StartRecovery for unknown email error = %v, want nil (generic success)", err)
	}

	rc, _, _, _ := mailer.recorded()
	if rc != 0 {
		t.Errorf("recovery mailer called %d times for unknown email, want 0", rc)
	}
	if n := countPending(t, pool, "ghost@example.com"); n != 0 {
		t.Errorf("pending_registrations rows = %d, want 0 (no token for unknown email)", n)
	}
}

// TestStartRecovery_InactiveUserNoLeak confirms a recovery request for a
// deactivated account also sends nothing and creates no token.
func TestStartRecovery_InactiveUserNoLeak(t *testing.T) {
	pool := testPool(t)
	insertInactiveUser(t, pool, "frozen@example.com")
	mailer := &recoveryRecordingMailer{}
	svc := newRecoveryService(t, pool, mailer)

	if err := svc.StartRecovery(context.Background(), "frozen@example.com", ""); err != nil {
		t.Fatalf("StartRecovery for inactive user error = %v, want nil", err)
	}

	rc, _, _, _ := mailer.recorded()
	if rc != 0 {
		t.Errorf("recovery mailer called %d times for inactive user, want 0", rc)
	}
	if n := countPending(t, pool, "frozen@example.com"); n != 0 {
		t.Errorf("pending_registrations rows = %d, want 0 for inactive user", n)
	}
}

// TestStartRecovery_ActiveUserCreatesTokenAndMails confirms the happy start
// path: an active account yields a token and a recovery email with a link.
func TestStartRecovery_ActiveUserCreatesTokenAndMails(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "user@example.com", false)
	mailer := &recoveryRecordingMailer{}
	svc := newRecoveryService(t, pool, mailer)

	if err := svc.StartRecovery(context.Background(), "User@Example.com", ""); err != nil {
		t.Fatalf("StartRecovery: %v", err)
	}

	rc, vc, to, token := mailer.recorded()
	if rc != 1 {
		t.Fatalf("recovery mailer calls = %d, want 1", rc)
	}
	if vc != 0 {
		t.Errorf("verification mailer calls = %d, want 0 (recovery must not send a verification email)", vc)
	}
	if to != "user@example.com" {
		t.Errorf("recovery recipient = %q, want lowercased user@example.com", to)
	}
	if token == "" {
		t.Error("recovery mailer token is empty")
	}
	if n := countPending(t, pool, "user@example.com"); n != 1 {
		t.Errorf("pending_registrations rows = %d, want 1", n)
	}
}

// TestVerifyRecovery_UnknownToken confirms an unknown recovery token is rejected.
func TestVerifyRecovery_UnknownToken(t *testing.T) {
	pool := testPool(t)
	svc := newRecoveryService(t, pool, &recoveryRecordingMailer{})

	if _, err := svc.VerifyRecovery(context.Background(), "no-such-token"); err != ErrTokenInvalid {
		t.Fatalf("VerifyRecovery error = %v, want ErrTokenInvalid", err)
	}
}

// TestVerifyRecovery_ExpiredToken confirms a recovery token past its 15-minute
// TTL is rejected.
func TestVerifyRecovery_ExpiredToken(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "expire@example.com", false)
	svc := newRecoveryService(t, pool, &recoveryRecordingMailer{})

	// Backdate the clock so the token's expiry is already in the past (older than
	// the 15-minute recovery TTL).
	svc.now = func() time.Time { return time.Now().Add(-20 * time.Minute) }
	if err := svc.StartRecovery(context.Background(), "expire@example.com", ""); err != nil {
		t.Fatalf("StartRecovery: %v", err)
	}
	token := lastPendingToken(t, pool, "expire@example.com")

	svc.now = time.Now // restore; the token is now expired
	if _, err := svc.VerifyRecovery(context.Background(), token); err != ErrTokenInvalid {
		t.Fatalf("VerifyRecovery error = %v, want ErrTokenInvalid", err)
	}
}

// TestRecoveryEndToEnd_AddsCredentialToExistingUser is the key proof. It creates
// a user with one existing credential via the real registration ceremony, then
// drives recover → verify → finish with a NEW virtual authenticator and asserts:
//   - the user now has 2 credentials (the old one untouched),
//   - a session was created,
//   - no second users row exists,
//   - the recovery token and challenge were cleaned up.
func TestRecoveryEndToEnd_AddsCredentialToExistingUser(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	ctx := context.Background()

	const email = "recover-me@example.com"

	// --- Build an existing account with one credential via #0015's ceremony. ---
	regSvc := newService(t, pool, &recordingMailer{}, "")
	if err := regSvc.StartRegistration(ctx, email, ""); err != nil {
		t.Fatalf("StartRegistration: %v", err)
	}
	regToken := lastPendingToken(t, pool, email)
	creation, err := regSvc.VerifyRegistration(ctx, regToken)
	if err != nil {
		t.Fatalf("VerifyRegistration: %v", err)
	}

	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "ShortLinks", Origin: testRPOrigin}
	oldAuth := virtualwebauthn.NewAuthenticator()
	oldCred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	regOptionsJSON, err := json.Marshal(creation)
	if err != nil {
		t.Fatalf("marshal reg options: %v", err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(regOptionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	attResp := virtualwebauthn.CreateAttestationResponse(rp, oldAuth, oldCred, *attOpts)
	regReq := httptest.NewRequest(http.MethodPost, "/auth/register/finish?token="+regToken,
		bytes.NewReader([]byte(attResp)))
	regResult, err := regSvc.FinishRegistration(ctx, regToken, "Original Key", "", regReq)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	userID := regResult.User.ID

	if n := countCredentials(t, pool, userID); n != 1 {
		t.Fatalf("after registration: credentials = %d, want 1", n)
	}

	// --- Now run the recovery ceremony with a NEW authenticator. ---
	mailer := &recoveryRecordingMailer{}
	recSvc := newRecoveryService(t, pool, mailer)

	if err := recSvc.StartRecovery(ctx, email, ""); err != nil {
		t.Fatalf("StartRecovery: %v", err)
	}
	_, _, _, recToken := mailer.recorded()
	if recToken == "" {
		t.Fatalf("recovery token not captured from mailer")
	}

	recCreation, err := recSvc.VerifyRecovery(ctx, recToken)
	if err != nil {
		t.Fatalf("VerifyRecovery: %v", err)
	}

	// A second, distinct virtual authenticator + credential for the new passkey.
	newAuth := virtualwebauthn.NewAuthenticator()
	newCred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)

	recOptionsJSON, err := json.Marshal(recCreation)
	if err != nil {
		t.Fatalf("marshal recovery options: %v", err)
	}
	recAttOpts, err := virtualwebauthn.ParseAttestationOptions(string(recOptionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions (recovery): %v", err)
	}
	recAttResp := virtualwebauthn.CreateAttestationResponse(rp, newAuth, newCred, *recAttOpts)
	recReq := httptest.NewRequest(http.MethodPost,
		"/auth/recover/finish?token="+recToken+"&device_name=Recovered+Key",
		bytes.NewReader([]byte(recAttResp)))
	recResult, err := recSvc.FinishRecovery(ctx, recToken, "Recovered Key", "", recReq)
	if err != nil {
		t.Fatalf("FinishRecovery: %v", err)
	}

	// The recovery session belongs to the SAME existing user.
	if recResult.UserID != userID {
		t.Errorf("recovery user_id = %d, want existing user %d", recResult.UserID, userID)
	}
	if recResult.SessionToken == "" {
		t.Error("recovery session token is empty")
	}

	// The KEY assertion: the user now has exactly 2 credentials.
	if n := countCredentials(t, pool, userID); n != 2 {
		t.Errorf("after recovery: credentials = %d, want 2 (new added, old untouched)", n)
	}

	// The OLD credential must still be present, untouched.
	if !credentialExists(t, pool, oldCred.ID) {
		t.Error("old credential was removed; recovery must leave existing credentials in place")
	}
	// The NEW credential must be present and owned by the same user.
	if !credentialExists(t, pool, newCred.ID) {
		t.Error("new credential was not stored")
	}
	if got := credentialOwner(t, pool, newCred.ID); got != userID {
		t.Errorf("new credential owner = %d, want existing user %d", got, userID)
	}

	// A session was created for the user.
	if n := countSessions(t, pool, recResult.SessionToken); n != 1 {
		t.Errorf("sessions rows for recovery token = %d, want 1", n)
	}

	// No SECOND user row was created — recovery attaches to the existing account.
	if n := userCount(t, pool); n != 1 {
		t.Errorf("users rows = %d, want 1 (recovery must not create a new user)", n)
	}

	// Recovery token and challenge were cleaned up.
	if n := countPending(t, pool, email); n != 0 {
		t.Errorf("pending_registrations rows after recovery = %d, want 0", n)
	}
	if n := countChallenges(t, pool, recToken); n != 0 {
		t.Errorf("webauthn_challenges rows after recovery = %d, want 0", n)
	}
}

// --- small query helpers specific to recovery tests ---

func insertInactiveUser(t *testing.T, pool *pgxpool.Pool, email string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (email, is_admin, active, created_at) VALUES ($1, FALSE, FALSE, now())`,
		email); err != nil {
		t.Fatalf("insert inactive user %s: %v", email, err)
	}
}

func credentialExists(t *testing.T, pool *pgxpool.Pool, credentialID []byte) bool {
	return scanCount(t, pool, `SELECT COUNT(*) FROM passkey_credentials WHERE credential_id = $1`, credentialID) == 1
}

func credentialOwner(t *testing.T, pool *pgxpool.Pool, credentialID []byte) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id int64
	if err := pool.QueryRow(ctx,
		`SELECT user_id FROM passkey_credentials WHERE credential_id = $1`, credentialID).Scan(&id); err != nil {
		t.Fatalf("fetch credential owner: %v", err)
	}
	return id
}

func userCount(t *testing.T, pool *pgxpool.Pool) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM users`)
}
