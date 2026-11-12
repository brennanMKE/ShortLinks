package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/descope/virtualwebauthn"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// storedUserHandle returns the passkey_credentials.user_handle for a credential
// id; ok is false when the column is NULL.
func storedUserHandle(t *testing.T, pool *pgxpool.Pool, credID []byte) (handle []byte, ok bool) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT user_handle FROM passkey_credentials WHERE credential_id = $1`, credID,
	).Scan(&handle); err != nil {
		t.Fatalf("select user_handle: %v", err)
	}
	return handle, handle != nil
}

func creationUserID(t *testing.T, creation *protocol.CredentialCreation) []byte {
	t.Helper()
	id, ok := creation.Response.User.ID.(protocol.URLEncodedBase64)
	if !ok {
		t.Fatalf("user.id type = %T", creation.Response.User.ID)
	}
	return []byte(id)
}

func attestationRequest(t *testing.T, creation *protocol.CredentialCreation, path string) (*http.Request, virtualwebauthn.Credential) {
	t.Helper()
	rp := virtualwebauthn.RelyingParty{ID: testRPID, Name: "ShortLinks", Origin: testRPOrigin}
	authenticator := virtualwebauthn.NewAuthenticator()
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	optionsJSON, err := json.Marshal(creation)
	if err != nil {
		t.Fatal(err)
	}
	attOpts, err := virtualwebauthn.ParseAttestationOptions(string(optionsJSON))
	if err != nil {
		t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAttestationResponse(rp, authenticator, cred, *attOpts)
	return httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(resp))), cred
}

func TestRegistration_StoresBeginTimeUserHandle(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")
	ctx := context.Background()

	if err := svc.StartRegistration(ctx, "uh-reg@example.com", ""); err != nil {
		t.Fatal(err)
	}
	token := lastPendingToken(t, pool, "uh-reg@example.com")
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	req, cred := attestationRequest(t, creation, "/auth/register/finish?token="+token)
	if _, err := svc.FinishRegistration(ctx, token, "Key", "", req); err != nil {
		t.Fatal(err)
	}
	got, ok := storedUserHandle(t, pool, cred.ID)
	if !ok || !bytes.Equal(got, creationUserID(t, creation)) {
		t.Errorf("stored user_handle = %x (set=%v), want user.id from creation options %x", got, ok, creationUserID(t, creation))
	}
}

func TestRegistration_NullHandleChallengeStillCompletes(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	svc := newService(t, pool, &recordingMailer{}, "")
	ctx := context.Background()

	if err := svc.StartRegistration(ctx, "uh-null@example.com", ""); err != nil {
		t.Fatal(err)
	}
	token := lastPendingToken(t, pool, "uh-null@example.com")
	creation, err := svc.VerifyRegistration(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a challenge row written by the pre-#0127 binary.
	if _, err := pool.Exec(ctx, `UPDATE webauthn_challenges SET user_handle = NULL WHERE pending_registration_token = $1`, token); err != nil {
		t.Fatal(err)
	}
	req, cred := attestationRequest(t, creation, "/auth/register/finish?token="+token)
	if _, err := svc.FinishRegistration(ctx, token, "Key", "", req); err != nil {
		t.Fatalf("FinishRegistration with NULL-handle challenge: %v", err)
	}
	if _, ok := storedUserHandle(t, pool, cred.ID); ok {
		t.Error("user_handle should be NULL for a NULL-handle challenge")
	}
}

func TestRecovery_StoresBeginTimeUserHandle(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	ctx := context.Background()
	const email = "uh-rec@example.com"

	regSvc := newService(t, pool, &recordingMailer{}, "")
	acct := registerWithAuthenticator(t, regSvc, pool, email)

	mailer := &recoveryRecordingMailer{}
	recSvc := newRecoveryService(t, pool, mailer)
	if err := recSvc.StartRecovery(ctx, email, ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, recToken := mailer.recorded()
	creation, err := recSvc.VerifyRecovery(ctx, recToken)
	if err != nil {
		t.Fatal(err)
	}
	req, cred := attestationRequest(t, creation, "/auth/recover/finish?token="+recToken)
	if _, err := recSvc.FinishRecovery(ctx, recToken, "Recovered", "", req); err != nil {
		t.Fatal(err)
	}
	got, ok := storedUserHandle(t, pool, cred.ID)
	if !ok || !bytes.Equal(got, creationUserID(t, creation)) {
		t.Errorf("recovery stored user_handle = %x (set=%v), want %x", got, ok, creationUserID(t, creation))
	}
	if bytes.Equal(got, mustHandle(t, pool, acct.cred.ID)) {
		t.Error("recovery handle unexpectedly equals the original credential's handle")
	}
}

func mustHandle(t *testing.T, pool *pgxpool.Pool, id []byte) []byte {
	h, _ := storedUserHandle(t, pool, id)
	return h
}

func TestLogin_CapturesUserHandleOnFirstLoginAndKeepsIt(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)
	ctx := context.Background()

	acct := registerWithAuthenticator(t, regSvc, pool, "uh-login@example.com")
	want, ok := storedUserHandle(t, pool, acct.cred.ID)
	if !ok {
		t.Fatal("registration should have stored the handle")
	}
	// Simulate a credential from before the migration.
	if _, err := pool.Exec(ctx, `UPDATE passkey_credentials SET user_handle = NULL`); err != nil {
		t.Fatal(err)
	}

	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("first login: %v", err)
	}
	got, ok := storedUserHandle(t, pool, acct.cred.ID)
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("after first login user_handle = %x (set=%v), want %x", got, ok, want)
	}

	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if got2, _ := storedUserHandle(t, pool, acct.cred.ID); !bytes.Equal(got2, want) {
		t.Errorf("after second login user_handle = %x, want unchanged %x", got2, want)
	}
}

func TestLogin_KeepsDifferentStoredUserHandle(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)
	var logs bytes.Buffer
	loginSvc.log = slog.New(slog.NewTextHandler(&logs, nil))

	acct := registerWithAuthenticator(t, regSvc, pool, "uh-diff@example.com")
	assertionHandle, _ := storedUserHandle(t, pool, acct.cred.ID)
	preset := []byte("preset-handle-xx")
	if _, err := pool.Exec(context.Background(), `UPDATE passkey_credentials SET user_handle = $1`, preset); err != nil {
		t.Fatal(err)
	}
	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got, _ := storedUserHandle(t, pool, acct.cred.ID); !bytes.Equal(got, preset) {
		t.Errorf("user_handle = %x, want preset %x kept", got, preset)
	}
	out := logs.String()
	if !strings.Contains(out, "differs from stored handle") {
		t.Errorf("mismatch warning not logged; log:\n%s", out)
	}
	for _, h := range [][]byte{preset, assertionHandle} {
		for _, enc := range []string{
			hex.EncodeToString(h),
			base64.RawURLEncoding.EncodeToString(h),
			base64.StdEncoding.EncodeToString(h),
		} {
			if strings.Contains(out, enc) {
				t.Errorf("log leaks handle encoding %q", enc)
			}
		}
	}
}

// An assertion without a userHandle (non-discoverable login) must still
// succeed and must not write anything to user_handle.
func TestLogin_EmptyAssertionHandleLeavesNull(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	const email = "uh-empty@example.com"
	acct := registerWithAuthenticator(t, regSvc, pool, email)
	if _, err := pool.Exec(context.Background(), `UPDATE passkey_credentials SET user_handle = NULL`); err != nil {
		t.Fatal(err)
	}
	// Same credential, but an authenticator that reports no userHandle.
	noHandle := virtualwebauthn.NewAuthenticator()
	noHandle.AddCredential(acct.cred)
	acct.authenticator = noHandle

	if _, err := driveLogin(t, loginSvc, acct, email); err != nil {
		t.Fatalf("login without userHandle: %v", err)
	}
	if _, ok := storedUserHandle(t, pool, acct.cred.ID); ok {
		t.Error("user_handle should stay NULL when the assertion has no userHandle")
	}
}
