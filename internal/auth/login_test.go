package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// registeredAccount bundles everything a login test needs after running the
// real registration ceremony against a virtual authenticator: the created user,
// the virtual relying party / authenticator / credential, and the raw user
// handle the authenticator reports as the assertion's userHandle.
type registeredAccount struct {
	user          CreatedUser
	rp            virtualwebauthn.RelyingParty
	authenticator virtualwebauthn.Authenticator
	cred          virtualwebauthn.Credential
}

// registerWithAuthenticator drives a full start→verify→finish registration so a
// real credential is created and owned by a virtual authenticator the caller
// keeps, enabling a subsequent genuine login assertion. The authenticator is
// configured with a user handle and holds the credential, mimicking a synced
// discoverable passkey.
func registerWithAuthenticator(t *testing.T, svc *RegistrationService, pool *pgxpool.Pool, email string) registeredAccount {
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
	// The user handle is the random 16-byte value the registration options carry;
	// the authenticator reports it back on every assertion (discoverable login).
	handle, ok := creation.Response.User.ID.(protocol.URLEncodedBase64)
	if !ok {
		t.Fatalf("user.id type = %T, want protocol.URLEncodedBase64", creation.Response.User.ID)
	}
	authenticator := virtualwebauthn.NewAuthenticatorWithOptions(virtualwebauthn.AuthenticatorOptions{
		UserHandle: []byte(handle),
	})
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
	result, err := svc.FinishRegistration(ctx, token, "Synced Passkey", "", req)
	if err != nil {
		t.Fatalf("FinishRegistration(%s): %v", email, err)
	}

	authenticator.AddCredential(cred)
	return registeredAccount{user: result.User, rp: rp, authenticator: authenticator, cred: cred}
}

// driveLogin runs StartLogin then produces a real assertion with the virtual
// authenticator and runs FinishLogin, returning the result. email is passed to
// StartLogin (empty exercises the discoverable path).
func driveLogin(t *testing.T, loginSvc *LoginService, acct registeredAccount, email string) (LoginResult, error) {
	t.Helper()
	ctx := context.Background()

	assertion, err := loginSvc.StartLogin(ctx, email)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	optionsJSON, err := json.Marshal(assertion)
	if err != nil {
		t.Fatalf("marshal assertion options: %v", err)
	}
	assertOpts, err := virtualwebauthn.ParseAssertionOptions(string(optionsJSON))
	if err != nil {
		t.Fatalf("ParseAssertionOptions: %v", err)
	}
	assertionResponse := virtualwebauthn.CreateAssertionResponse(acct.rp, acct.authenticator, acct.cred, *assertOpts)

	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		bytes.NewReader([]byte(assertionResponse)))
	return loginSvc.FinishLogin(ctx, "", req)
}

// newLoginService builds a LoginService over the same test pool and RP config as
// the registration service.
func newLoginService(t *testing.T, pool *pgxpool.Pool) *LoginService {
	t.Helper()
	cfg := &config.Config{WebAuthnRPID: testRPID, WebAuthnRPOrigin: testRPOrigin}
	wa, err := NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	return NewLoginService(NewStore(pool), wa, nil, nil)
}

// TestLogin_EndToEnd_DiscoverableCreatesSession is the key proof: a credential
// is registered with a real virtual authenticator, then a real discoverable
// login assertion (no email) is verified and a NEW session row is created.
func TestLogin_EndToEnd_DiscoverableCreatesSession(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "login-disc@example.com")
	sessionsBefore := countSessionsForUser(t, pool, acct.user.ID)

	result, err := driveLogin(t, loginSvc, acct, "")
	if err != nil {
		t.Fatalf("FinishLogin (discoverable): %v", err)
	}
	if result.UserID != acct.user.ID {
		t.Errorf("result UserID = %d, want %d", result.UserID, acct.user.ID)
	}
	if result.SessionToken == "" {
		t.Fatal("session token is empty")
	}
	if n := countSessions(t, pool, result.SessionToken); n != 1 {
		t.Errorf("sessions for new token = %d, want 1", n)
	}
	if after := countSessionsForUser(t, pool, acct.user.ID); after != sessionsBefore+1 {
		t.Errorf("user sessions = %d, want %d (one new)", after, sessionsBefore+1)
	}
	// last_login_at must be set; the challenge must have been consumed.
	if !lastLoginSet(t, pool, acct.user.ID) {
		t.Error("users.last_login_at was not set")
	}
	if n := countAuthChallenges(t, pool); n != 0 {
		t.Errorf("authentication challenges after finish = %d, want 0 (consumed)", n)
	}
}

// TestLogin_EndToEnd_WithEmailAllowCredentials drives the non-discoverable path:
// StartLogin is given the account email so allowCredentials is populated, and a
// real assertion still verifies and issues a session.
func TestLogin_EndToEnd_WithEmailAllowCredentials(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	const email = "login-email@example.com"
	acct := registerWithAuthenticator(t, regSvc, pool, email)

	// Confirm StartLogin actually scopes allowCredentials to this account.
	assertion, err := loginSvc.StartLogin(context.Background(), email)
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if len(assertion.Response.AllowedCredentials) != 1 {
		t.Fatalf("allowCredentials = %d, want 1", len(assertion.Response.AllowedCredentials))
	}
	if !bytes.Equal(assertion.Response.AllowedCredentials[0].CredentialID, acct.cred.ID) {
		t.Errorf("allowCredentials id mismatch")
	}

	result, err := driveLogin(t, loginSvc, acct, email)
	if err != nil {
		t.Fatalf("FinishLogin (email): %v", err)
	}
	if n := countSessions(t, pool, result.SessionToken); n != 1 {
		t.Errorf("sessions for token = %d, want 1", n)
	}
}

// TestLogin_DeactivatedReturns403NoSession asserts a deactivated account cannot
// log in (403) and that no session is created even with a valid assertion.
func TestLogin_DeactivatedReturns403NoSession(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "deactivated@example.com")
	setUserActive(t, pool, acct.user.ID, false)

	// Registration itself created one session; deactivated login must add none.
	sessionsBefore := countSessionsForUser(t, pool, acct.user.ID)

	_, err := driveLogin(t, loginSvc, acct, "")
	if err != ErrAccountDeactivated {
		t.Fatalf("FinishLogin error = %v, want ErrAccountDeactivated", err)
	}
	if after := countSessionsForUser(t, pool, acct.user.ID); after != sessionsBefore {
		t.Errorf("sessions for deactivated user = %d, want %d (no new session)", after, sessionsBefore)
	}
}

// TestLogin_SyncedZeroSignCountAcceptedSilently asserts the iCloud-Keychain
// case: stored sign_count 0 and assertion sign_count 0 is accepted, the login
// succeeds, and sign_count stays 0.
func TestLogin_SyncedZeroSignCountAcceptedSilently(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "synced@example.com")
	// Virtual EC2 credential starts at counter 0; registration stored 0.
	if got := storedSignCount(t, pool, acct.cred.ID); got != 0 {
		t.Fatalf("stored sign_count after registration = %d, want 0", got)
	}

	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if got := storedSignCount(t, pool, acct.cred.ID); got != 0 {
		t.Errorf("stored sign_count after synced login = %d, want 0 (unchanged)", got)
	}
}

// TestLogin_CloneCaseStoredCounterPreserved asserts the clone rule: when the
// stored sign_count is > 0 and the assertion returns a value <= stored, the
// login is still accepted (warning logged) and the stored counter is left
// unchanged rather than rolled backward.
func TestLogin_CloneCaseStoredCounterPreserved(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "clone@example.com")
	// Simulate a device-bound credential that has previously advanced its
	// counter to 5. The virtual authenticator still asserts at counter 0, which
	// is <= 5 → the clone path.
	setStoredSignCount(t, pool, acct.cred.ID, 5)

	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if got := storedSignCount(t, pool, acct.cred.ID); got != 5 {
		t.Errorf("stored sign_count after clone-case login = %d, want 5 (preserved, not rolled back)", got)
	}
}

// TestLogout_DeletesSessionRow registers + logs in to create a session, then
// confirms Logout removes exactly that row.
func TestLogout_DeletesSessionRow(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "logout@example.com")
	result, err := driveLogin(t, loginSvc, acct, "")
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if n := countSessions(t, pool, result.SessionToken); n != 1 {
		t.Fatalf("precondition: sessions = %d, want 1", n)
	}

	if err := loginSvc.Logout(context.Background(), result.SessionToken, ""); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if n := countSessions(t, pool, result.SessionToken); n != 0 {
		t.Errorf("sessions after logout = %d, want 0", n)
	}

	// Logout is idempotent: a second call (or unknown token) is not an error.
	if err := loginSvc.Logout(context.Background(), result.SessionToken, ""); err != nil {
		t.Errorf("idempotent Logout returned error: %v", err)
	}
	if err := loginSvc.Logout(context.Background(), "", ""); err != nil {
		t.Errorf("Logout(empty) returned error: %v", err)
	}
}

// --- helpers specific to login tests ---

func setUserActive(t *testing.T, pool *pgxpool.Pool, userID int64, active bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `UPDATE users SET active = $1 WHERE id = $2`, active, userID); err != nil {
		t.Fatalf("set user active: %v", err)
	}
}

func storedSignCount(t *testing.T, pool *pgxpool.Pool, credID []byte) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n int64
	if err := pool.QueryRow(ctx,
		`SELECT sign_count FROM passkey_credentials WHERE credential_id = $1`, credID).Scan(&n); err != nil {
		t.Fatalf("read sign_count: %v", err)
	}
	return n
}

func setStoredSignCount(t *testing.T, pool *pgxpool.Pool, credID []byte, n int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`UPDATE passkey_credentials SET sign_count = $1 WHERE credential_id = $2`, n, credID); err != nil {
		t.Fatalf("set sign_count: %v", err)
	}
}

func countSessionsForUser(t *testing.T, pool *pgxpool.Pool, userID int64) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, userID)
}

func countAuthChallenges(t *testing.T, pool *pgxpool.Pool) int {
	return scanCount(t, pool, `SELECT COUNT(*) FROM webauthn_challenges WHERE purpose = 'authentication'`)
}

func lastLoginSet(t *testing.T, pool *pgxpool.Pool, userID int64) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var set bool
	if err := pool.QueryRow(ctx,
		`SELECT last_login_at IS NOT NULL FROM users WHERE id = $1`, userID).Scan(&set); err != nil {
		t.Fatalf("read last_login_at: %v", err)
	}
	return set
}
