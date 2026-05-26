package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// ErrRegistrationsDisabled is returned by StartRegistration when the
// registrations_enabled setting is false. The handler maps it to 403.
var ErrRegistrationsDisabled = errors.New("auth: registrations disabled")

// ErrEmailRegistered is returned when the submitted email already has an
// account.
var ErrEmailRegistered = errors.New("auth: email already registered")

// ErrInvalidEmail is returned for an empty or obviously malformed email.
var ErrInvalidEmail = errors.New("auth: invalid email")

// RegistrationService orchestrates the three-step passkey registration ceremony
// (start, verify, finish). It is the reusable auth foundation: it owns the
// WebAuthn relying party, the Store, the Mailer, and the admin-promotion config,
// and exposes pure-ish methods the HTTP handler is a thin shell over. The login
// (#0016) and recovery (#0017) services will sit alongside it sharing the same
// Store and *webauthn.WebAuthn.
type RegistrationService struct {
	store  *Store
	wa     *webauthn.WebAuthn
	mailer Mailer
	// adminEmail is ADMIN_EMAIL, lowercased once at construction. A registrant
	// whose email matches is promoted to admin even if they are not the first
	// user.
	adminEmail string
	// now is injectable so TTLs are deterministic in tests; defaults to
	// time.Now.
	now func() time.Time
}

// NewRegistrationService wires the registration ceremony from its dependencies.
func NewRegistrationService(store *Store, wa *webauthn.WebAuthn, mailer Mailer, cfg *config.Config) *RegistrationService {
	return &RegistrationService{
		store:      store,
		wa:         wa,
		mailer:     mailer,
		adminEmail: strings.ToLower(strings.TrimSpace(cfg.AdminEmail)),
		now:        time.Now,
	}
}

// StartRegistration is step 1. It enforces the registrations_enabled gate (read
// fresh from the DB), rejects an already-registered email, creates a pending
// registration with a 5-minute TTL, and emails the magic link. The token is
// never returned to the caller; it travels only via email.
func (s *RegistrationService) StartRegistration(ctx context.Context, rawEmail string) error {
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return err
	}

	enabled, err := s.store.RegistrationsEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return ErrRegistrationsDisabled
	}

	registered, err := s.store.EmailRegistered(ctx, email)
	if err != nil {
		return err
	}
	if registered {
		return ErrEmailRegistered
	}

	token, err := randomURLToken(registrationTokenLen)
	if err != nil {
		return err
	}
	if _, err := s.store.CreatePendingRegistration(ctx, email, token, s.now()); err != nil {
		return err
	}

	// TODO(#0025): write an account.registration_started audit entry here once
	// the audit write path lands. No-op for now.

	return s.mailer.SendVerification(ctx, email, token)
}

// VerifyRegistration is step 2. It validates the magic-link token (existence +
// 5-minute TTL), begins a WebAuthn registration with the PRD's passkey policy,
// persists the challenge linked to the token, and returns the
// CredentialCreation options for the browser. The options are returned as the
// library's struct; the handler serializes them as JSON.
func (s *RegistrationService) VerifyRegistration(ctx context.Context, token string) (*protocol.CredentialCreation, error) {
	now := s.now()
	email, err := s.store.LookupPendingRegistration(ctx, token, now)
	if err != nil {
		return nil, err
	}

	user, err := NewRegistrationUser(email)
	if err != nil {
		return nil, err
	}

	creation, session, err := s.wa.BeginRegistration(user, registrationOptions()...)
	if err != nil {
		return nil, fmt.Errorf("auth: begin registration: %w", err)
	}

	// session.Challenge is base64url(challenge bytes); store the raw bytes in
	// the BYTEA column. The handle does not need persisting: FinishRegistration
	// only checks the session handle equals the user handle, and both come from
	// a RegistrationUser we reconstruct on finish.
	challengeBytes, err := base64.RawURLEncoding.DecodeString(session.Challenge)
	if err != nil {
		return nil, fmt.Errorf("auth: decoding challenge: %w", err)
	}
	if err := s.store.SaveRegistrationChallenge(ctx, challengeBytes, token, now); err != nil {
		return nil, err
	}

	return creation, nil
}

// FinishResult is returned by FinishRegistration so the handler can set the
// session cookie and shape the response.
type FinishResult struct {
	User           CreatedUser
	SessionToken   string
	SessionExpires time.Time
}

// FinishRegistration is step 3. It consumes the challenge for the token,
// verifies the attestation, and — in a single transaction — creates the user
// (promoting to admin on a fresh install or an ADMIN_EMAIL match), stores the
// credential, deletes the pending registration, and creates the session. The
// returned token must be written to the session cookie by the caller.
//
// deviceName is an optional client-supplied label for the credential.
func (s *RegistrationService) FinishRegistration(ctx context.Context, token, deviceName string, r *http.Request) (FinishResult, error) {
	now := s.now()

	// A short-lived transaction makes account creation atomic: either the user,
	// credential, session, and pending-registration delete all succeed, or
	// none do.
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return FinishResult{}, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	challengeBytes, err := s.store.ConsumeRegistrationChallenge(ctx, tx, token, now)
	if err != nil {
		return FinishResult{}, err
	}

	email, err := s.store.LookupPendingRegistration(ctx, token, now)
	if err != nil {
		return FinishResult{}, err
	}

	user, err := NewRegistrationUser(email)
	if err != nil {
		return FinishResult{}, err
	}

	session := webauthn.SessionData{
		Challenge:        base64.RawURLEncoding.EncodeToString(challengeBytes),
		UserID:           user.WebAuthnID(),
		UserVerification: protocol.VerificationRequired,
		CredParams: []protocol.CredentialParameter{
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
		},
		Expires: now.Add(registrationTTL),
	}

	credential, err := s.wa.FinishRegistration(user, session, r)
	if err != nil {
		return FinishResult{}, fmt.Errorf("auth: finish registration: %w", err)
	}

	// Admin promotion: first user on a fresh install, or an ADMIN_EMAIL match.
	count, err := s.store.UserCount(ctx, tx)
	if err != nil {
		return FinishResult{}, err
	}
	promoteAdmin := count == 0 || (s.adminEmail != "" && email == s.adminEmail)

	created, err := s.store.CreateUser(ctx, tx, email, promoteAdmin, now)
	if err != nil {
		return FinishResult{}, err
	}

	if err := s.store.InsertCredential(ctx, tx, StoredCredential{
		UserID:       created.ID,
		CredentialID: credential.ID,
		PublicKey:    credential.PublicKey,
		AAGUID:       credential.Authenticator.AAGUID,
		SignCount:    credential.Authenticator.SignCount,
		DeviceName:   deviceName,
	}, now); err != nil {
		return FinishResult{}, err
	}

	// TODO(#0025): write account.registered and credential.added audit entries
	// (the latter with {device_name, aaguid}) once the audit write path lands.

	if err := s.store.DeletePendingRegistration(ctx, tx, token); err != nil {
		return FinishResult{}, err
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return FinishResult{}, err
	}
	sessionExpires, err := s.store.CreateSession(ctx, tx, created.ID, sessionToken, now)
	if err != nil {
		return FinishResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return FinishResult{}, fmt.Errorf("auth: commit tx: %w", err)
	}

	return FinishResult{
		User:           created,
		SessionToken:   sessionToken,
		SessionExpires: sessionExpires,
	}, nil
}

// normalizeEmail lowercases and trims the email and applies a minimal validity
// check (non-empty, single '@' with text on both sides). Full RFC validation is
// intentionally out of scope; the magic link is the real proof of control.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" {
		return "", ErrInvalidEmail
	}
	at := strings.IndexByte(email, '@')
	if at <= 0 || at == len(email)-1 || strings.Count(email, "@") != 1 {
		return "", ErrInvalidEmail
	}
	return email, nil
}
