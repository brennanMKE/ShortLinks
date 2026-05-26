package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"
)

// RecoveryService orchestrates the account-recovery ceremony (start, verify,
// finish) for users who have lost access to all their registered passkeys. It
// sits alongside RegistrationService and LoginService, sharing the same Store
// and *webauthn.WebAuthn per the #0015 foundation.
//
// Recovery closely mirrors registration: it issues the same WebAuthn
// registration challenge (residentKey/userVerification required, ES256+RS256,
// authenticatorAttachment omitted) and runs FinishRegistration to mint a new
// credential. The crucial difference is that the credential is ADDED to the
// EXISTING account — no new users row is created and old credentials are left
// in place. The user later reviews/revokes old credentials from account
// settings (out of scope here).
type RecoveryService struct {
	store  *Store
	wa     *webauthn.WebAuthn
	mailer Mailer
	// now is injectable so TTLs are deterministic in tests; defaults to
	// time.Now.
	now func() time.Time
}

// NewRecoveryService wires the recovery ceremony from its dependencies.
func NewRecoveryService(store *Store, wa *webauthn.WebAuthn, mailer Mailer) *RecoveryService {
	return &RecoveryService{
		store:  store,
		wa:     wa,
		mailer: mailer,
		now:    time.Now,
	}
}

// StartRecovery is step 1. It looks up the user by lowercased email and, only
// when the account exists AND is active, creates a single-use recovery token
// (15-minute TTL) and emails the recovery link. To avoid leaking which emails
// are registered, it never returns an error for an unknown/inactive/invalid
// email: the handler always responds with the same generic 200.
//
// Returning nil in the no-send cases (and on any swallowed lookup error) keeps
// the response identical whether or not the account exists. Genuine
// infrastructure failures during token creation or mail delivery are surfaced
// so the handler can return a 500, since those cannot leak account existence.
func (s *RecoveryService) StartRecovery(ctx context.Context, rawEmail string) error {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		// Malformed email: respond as if sent. No leak, no work.
		return nil
	}

	account, err := s.store.LookupUserByEmail(ctx, email)
	if err != nil {
		// Unknown account (ErrUserNotFound) or a transient lookup error: stay
		// generic and send nothing. A transient error must not produce a
		// different response than "no such account".
		return nil
	}
	if !account.Active {
		// Deactivated accounts cannot recover; respond generically, send nothing.
		return nil
	}

	token, err := randomURLToken(registrationTokenLen)
	if err != nil {
		return err
	}
	if _, err := s.store.CreateRecoveryToken(ctx, email, token, s.now()); err != nil {
		return err
	}

	// TODO(#0025): write an account.recovery_started audit entry (actor_id NULL)
	// here once the audit write path lands. No-op for now.

	return s.mailer.SendRecovery(ctx, email, token)
}

// VerifyRecovery is step 2. It validates the recovery token (existence +
// 15-minute TTL), confirms the email still maps to an existing active user,
// begins a WebAuthn registration with the PRD's passkey policy bound to that
// existing user, persists the challenge (purpose 'recovery', linked to the
// token and the user_id), and returns the CredentialCreation options.
//
// ErrTokenInvalid is returned for an unknown/expired token or when the
// account has since been removed or deactivated, so the handler maps all such
// cases to a single rejection.
func (s *RecoveryService) VerifyRecovery(ctx context.Context, token string) (*protocol.CredentialCreation, error) {
	now := s.now()
	email, err := s.store.LookupRecoveryToken(ctx, token, now)
	if err != nil {
		return nil, err
	}

	account, err := s.store.LookupUserByEmail(ctx, email)
	if err != nil {
		// The token's email no longer maps to a user: treat as an invalid token.
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrTokenInvalid
		}
		return nil, err
	}
	if !account.Active {
		return nil, ErrTokenInvalid
	}

	// A fresh RegistrationUser supplies a random handle and an empty credential
	// set, so go-webauthn issues options for adding a brand-new discoverable
	// passkey — identical to initial registration. The credential will be
	// attached to account.ID on finish.
	user, err := NewRegistrationUser(email)
	if err != nil {
		return nil, err
	}

	creation, session, err := s.wa.BeginRegistration(user, registrationOptions()...)
	if err != nil {
		return nil, fmt.Errorf("auth: begin recovery registration: %w", err)
	}

	challengeBytes, err := base64.RawURLEncoding.DecodeString(session.Challenge)
	if err != nil {
		return nil, fmt.Errorf("auth: decoding challenge: %w", err)
	}
	if err := s.store.SaveRecoveryChallenge(ctx, challengeBytes, account.ID, token, now); err != nil {
		return nil, err
	}

	return creation, nil
}

// RecoveryResult is returned by FinishRecovery so the handler can set the
// session cookie and shape the response.
type RecoveryResult struct {
	UserID         int64
	SessionToken   string
	SessionExpires time.Time
}

// FinishRecovery is step 3. It consumes the recovery challenge (recovering the
// bound user_id), verifies the attestation, and — in a single transaction —
// ADDS the new credential to the EXISTING account, deletes the recovery token,
// and creates a session. It deliberately does NOT create a users row and does
// NOT touch or remove existing credentials. The returned session token must be
// written to the cookie by the caller.
//
// deviceName is an optional client-supplied label for the new credential.
func (s *RecoveryService) FinishRecovery(ctx context.Context, token, deviceName string, r *http.Request) (RecoveryResult, error) {
	now := s.now()

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	challengeBytes, userID, err := s.store.ConsumeRecoveryChallenge(ctx, tx, token, now)
	if err != nil {
		return RecoveryResult{}, err
	}

	email, err := s.store.LookupRecoveryToken(ctx, token, now)
	if err != nil {
		return RecoveryResult{}, err
	}

	// Reconstruct a registration user for the email. As in registration finish,
	// a fresh handle is fine: go-webauthn's registration verification checks the
	// session handle against the user handle (both from this object) and the
	// attestation, not against any previously issued handle.
	user, err := NewRegistrationUser(email)
	if err != nil {
		return RecoveryResult{}, err
	}

	session := webauthn.SessionData{
		Challenge:        base64.RawURLEncoding.EncodeToString(challengeBytes),
		UserID:           user.WebAuthnID(),
		UserVerification: protocol.VerificationRequired,
		CredParams: []protocol.CredentialParameter{
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
		},
		Expires: now.Add(recoveryTTL),
	}

	credential, err := s.wa.FinishRegistration(user, session, r)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("auth: finish recovery registration: %w", err)
	}

	// Attach the new credential to the EXISTING user. Existing credentials are
	// left untouched.
	if err := s.store.InsertCredential(ctx, tx, StoredCredential{
		UserID:       userID,
		CredentialID: credential.ID,
		PublicKey:    credential.PublicKey,
		AAGUID:       credential.Authenticator.AAGUID,
		SignCount:    credential.Authenticator.SignCount,
		DeviceName:   deviceName,
	}, now); err != nil {
		return RecoveryResult{}, err
	}

	// TODO(#0025): write account.recovered and credential.added audit entries
	// (the latter with {device_name, aaguid}) once the audit write path lands.

	if err := s.store.DeletePendingRegistration(ctx, tx, token); err != nil {
		return RecoveryResult{}, err
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return RecoveryResult{}, err
	}
	sessionExpires, err := s.store.CreateSession(ctx, tx, userID, sessionToken, now)
	if err != nil {
		return RecoveryResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return RecoveryResult{}, fmt.Errorf("auth: commit tx: %w", err)
	}

	return RecoveryResult{
		UserID:         userID,
		SessionToken:   sessionToken,
		SessionExpires: sessionExpires,
	}, nil
}
