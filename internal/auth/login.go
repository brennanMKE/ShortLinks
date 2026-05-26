package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// ErrAccountDeactivated is returned by FinishLogin when the assertion verifies
// against an account whose users.active flag is false. The handler maps it to a
// 403 with the "Account deactivated" message. It is checked before a session is
// issued so a deactivated user can never obtain a cookie.
var ErrAccountDeactivated = errors.New("auth: account deactivated")

// ErrLoginFailed is returned for any assertion that fails to verify (unknown
// credential, bad signature, expired/consumed challenge, ...). The handler maps
// it to a generic failure so the client cannot distinguish the cases.
var ErrLoginFailed = errors.New("auth: login failed")

// LoginService orchestrates the WebAuthn authentication ceremony (start,
// finish). It sits alongside RegistrationService sharing the same Store and
// *webauthn.WebAuthn, per the #0015 foundation. The handler is a thin shell over
// its two methods.
type LoginService struct {
	store *Store
	wa    *webauthn.WebAuthn
	log   *slog.Logger
	// auditor records the account.login and account.logout audit entries
	// (#0025). May be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
	// now is injectable so TTLs and timestamps are deterministic in tests;
	// defaults to time.Now.
	now func() time.Time
}

// NewLoginService wires the login ceremony from its dependencies. A nil logger
// falls back to the default slog logger; a nil auditor disables audit writes.
func NewLoginService(store *Store, wa *webauthn.WebAuthn, auditor *audit.Logger, logger *slog.Logger) *LoginService {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoginService{store: store, wa: wa, log: logger, auditor: auditor, now: time.Now}
}

// StartLogin is step 1. It issues an assertion challenge and returns the
// PublicKeyCredentialRequestOptions for the browser.
//
// When email is provided and the account exists, allowCredentials is populated
// with that user's stored passkeys (narrowing the device prompt). When email is
// absent — or the account does not exist — a discoverable (conditional-UI)
// challenge is issued instead, so the response never reveals whether the email
// is registered: the options are generic in either case.
//
// The raw challenge bytes are persisted to webauthn_challenges (purpose
// 'authentication', 5-minute TTL) so finish can confirm and consume them.
func (s *LoginService) StartLogin(ctx context.Context, rawEmail string) (*protocol.CredentialAssertion, error) {
	email := normalizeLoginEmail(rawEmail)

	var (
		assertion *protocol.CredentialAssertion
		session   *webauthn.SessionData
		err       error
	)

	if loginUser := s.resolveAllowCredentialsUser(ctx, email); loginUser != nil {
		// Email mapped to an existing account with credentials: scope the prompt.
		assertion, session, err = s.wa.BeginLogin(loginUser)
	} else {
		// No email, unknown account, or an account with no credentials yet:
		// fall back to a discoverable (conditional-UI) login. This keeps the
		// response identical whether or not the email is registered.
		assertion, session, err = s.wa.BeginDiscoverableLogin()
	}
	if err != nil {
		return nil, fmt.Errorf("auth: begin login: %w", err)
	}

	challengeBytes, err := base64.RawURLEncoding.DecodeString(session.Challenge)
	if err != nil {
		return nil, fmt.Errorf("auth: decoding challenge: %w", err)
	}
	if err := s.store.SaveAuthenticationChallenge(ctx, challengeBytes, s.now()); err != nil {
		return nil, err
	}

	return assertion, nil
}

// resolveAllowCredentialsUser returns a LoginUser for the email when it maps to
// an existing account that has at least one credential, or nil otherwise. Any
// lookup error is swallowed (treated as "no user") so StartLogin stays generic
// and never leaks account existence or transient DB errors through differing
// responses.
func (s *LoginService) resolveAllowCredentialsUser(ctx context.Context, email string) *LoginUser {
	if email == "" {
		return nil
	}
	account, err := s.store.LookupUserByEmail(ctx, email)
	if err != nil {
		return nil
	}
	recs, err := s.store.CredentialsForUser(ctx, account.ID)
	if err != nil || len(recs) == 0 {
		return nil
	}
	return NewLoginUser(account.ID, nil, email, credentialsFromRecords(recs))
}

// LoginResult is returned by FinishLogin so the handler can set the session
// cookie and shape the response.
type LoginResult struct {
	UserID         int64
	SessionToken   string
	SessionExpires time.Time
}

// FinishLogin is step 2. It parses the assertion, resolves the credential (and
// thus the account) by the assertion's credential id, enforces users.active,
// verifies the assertion's signature against the stored public key over the
// consumed challenge, applies the PRD sign_count rules, records last_login_at /
// last_used_at, and creates a session — all in one transaction.
//
// Resolution by credential id covers both discoverable and non-discoverable
// login: the credential id always arrives in the assertion's rawID, and the
// account's WebAuthn handle is mirrored from the assertion's userHandle (the
// signature over the stored public key is the real proof of possession).
func (s *LoginService) FinishLogin(ctx context.Context, ip string, r *http.Request) (LoginResult, error) {
	now := s.now()

	parsed, err := protocol.ParseCredentialRequestResponse(r)
	if err != nil {
		s.log.Warn("login: parsing assertion", "err", err)
		return LoginResult{}, ErrLoginFailed
	}

	rec, active, err := s.store.CredentialByID(ctx, parsed.RawID)
	if err != nil {
		// Unknown credential or any lookup error: generic failure, no leak.
		s.log.Warn("login: resolving credential", "err", err)
		return LoginResult{}, ErrLoginFailed
	}

	// Enforce the active flag before issuing anything. A deactivated account can
	// never obtain a session even with a valid passkey.
	if !active {
		return LoginResult{}, ErrAccountDeactivated
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return LoginResult{}, fmt.Errorf("auth: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Consume the single-use challenge inside the transaction. A missing/expired
	// challenge fails the whole ceremony.
	challengeBytes, derr := base64.RawURLEncoding.DecodeString(parsed.Response.CollectedClientData.Challenge)
	if derr != nil {
		s.log.Warn("login: decoding client challenge", "err", derr)
		return LoginResult{}, ErrLoginFailed
	}
	if err := s.store.ConsumeAuthenticationChallenge(ctx, tx, challengeBytes, now); err != nil {
		s.log.Warn("login: consuming challenge", "err", err)
		return LoginResult{}, ErrLoginFailed
	}

	// Build the login user from the stored credential. The handle is mirrored
	// from the assertion's userHandle so go-webauthn's userHandle==WebAuthnID
	// check is consistent; verification still hinges on the signature over the
	// stored public key.
	credential := credentialFromRecord(rec)
	loginUser := NewLoginUser(rec.UserID, parsed.Response.UserHandle, "", []webauthn.Credential{credential})

	session := webauthn.SessionData{
		Challenge:        parsed.Response.CollectedClientData.Challenge,
		UserID:           loginUser.WebAuthnID(),
		UserVerification: protocol.VerificationRequired,
		Expires:          now.Add(authenticationTTL),
	}

	validated, err := s.wa.ValidateLogin(loginUser, session, parsed)
	if err != nil {
		s.log.Warn("login: validating assertion", "err", err)
		return LoginResult{}, ErrLoginFailed
	}

	// Apply the PRD sign_count rules. go-webauthn's UpdateCounter has already
	// folded the assertion counter into validated.Authenticator and raised
	// CloneWarning on the regression case; we map that onto our storage policy.
	assertionCount := validated.Authenticator.SignCount
	switch {
	case validated.Authenticator.CloneWarning:
		// Stored > 0 and assertion <= stored: possible clone of a device-bound
		// credential. Log a warning, accept the login, and leave sign_count
		// unchanged (only touch last_used_at).
		s.log.Warn("login: possible cloned credential (sign_count regression)",
			"user_id", rec.UserID, "stored_sign_count", rec.SignCount, "assertion_sign_count", assertionCount)
		if err := s.store.TouchCredentialLastUsed(ctx, tx, rec.CredentialID, now); err != nil {
			return LoginResult{}, err
		}
	case rec.SignCount == 0 && assertionCount == 0:
		// Synced passkey (iCloud Keychain): both zero. Accept silently and leave
		// sign_count at 0.
		if err := s.store.TouchCredentialLastUsed(ctx, tx, rec.CredentialID, now); err != nil {
			return LoginResult{}, err
		}
	default:
		// Normal advance: store the new (higher) counter.
		if err := s.store.UpdateSignCount(ctx, tx, rec.CredentialID, assertionCount, now); err != nil {
			return LoginResult{}, err
		}
	}

	if err := s.store.UpdateLastLogin(ctx, tx, rec.UserID, now); err != nil {
		return LoginResult{}, err
	}

	// account.login: the authenticated user is both actor and affected user.
	// Written inside the ceremony transaction so a committed login always carries
	// its audit row.
	if s.auditor != nil {
		actor := rec.UserID
		if err := s.auditor.WriteTx(ctx, tx, audit.Entry{
			ActorID:    &actor,
			UserID:     &actor,
			Action:     audit.ActionAccountLogin,
			TargetType: audit.TargetUser,
			TargetID:   &actor,
			IP:         ip,
		}); err != nil {
			return LoginResult{}, err
		}
	}

	sessionToken, err := NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	sessionExpires, err := s.store.CreateSession(ctx, tx, rec.UserID, sessionToken, now)
	if err != nil {
		return LoginResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LoginResult{}, fmt.Errorf("auth: commit tx: %w", err)
	}

	return LoginResult{
		UserID:         rec.UserID,
		SessionToken:   sessionToken,
		SessionExpires: sessionExpires,
	}, nil
}

// Logout deletes the session row for the given cookie token. It is idempotent:
// an unknown or empty token is not an error, so a stale cookie still yields a
// clean 200. Cookie clearing is the handler's responsibility. ip is the actor's
// client IP, recorded on the account.logout audit entry.
func (s *LoginService) Logout(ctx context.Context, token, ip string) error {
	if token == "" {
		return nil
	}
	userID, err := s.store.DeleteSession(ctx, token)
	if err != nil {
		return err
	}
	// account.logout: only attribute the entry when a real session was deleted
	// (a stale/unknown cookie deletes nothing → userID 0 → no entry). Fire-and-
	// forget: the session is already gone, so a failed audit write must not fail
	// logout.
	if s.auditor != nil && userID != 0 {
		s.auditor.Record(ctx, audit.Entry{
			ActorID:    &userID,
			UserID:     &userID,
			Action:     audit.ActionAccountLogout,
			TargetType: audit.TargetUser,
			TargetID:   &userID,
			IP:         ip,
		})
	}
	return nil
}

// credentialFromRecord rebuilds a webauthn.Credential from a stored row for use
// in assertion verification. Flags are left at their zero value: the schema does
// not persist backup-eligible/state, and synced platform passkeys produced by
// the relying party's own registration carry the same (unset) flags here.
func credentialFromRecord(rec CredentialRecord) webauthn.Credential {
	c := webauthn.Credential{
		ID:        rec.CredentialID,
		PublicKey: rec.PublicKey,
	}
	c.Authenticator.SignCount = rec.SignCount
	if len(rec.AAGUID) == 16 {
		c.Authenticator.AAGUID = append([]byte(nil), rec.AAGUID...)
	}
	return c
}

// credentialsFromRecords maps stored rows to webauthn.Credentials for the
// allowCredentials list on login start.
func credentialsFromRecords(recs []CredentialRecord) []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(recs))
	for _, rec := range recs {
		out = append(out, credentialFromRecord(rec))
	}
	return out
}

// normalizeLoginEmail lowercases and trims an email for the optional
// allowCredentials lookup. Unlike registration it does not validate: an invalid
// or empty value simply falls through to discoverable login.
func normalizeLoginEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
