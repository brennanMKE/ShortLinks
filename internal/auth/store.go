package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// registrationTTL is the lifetime of a pending registration and its WebAuthn
// challenge. The PRD fixes both at a 5-minute TTL.
const registrationTTL = 5 * time.Minute

// sessionTTL is the lifetime of a new session. The PRD specifies a 30-day
// sliding window; this is the initial expiry set at creation.
const sessionTTL = 30 * 24 * time.Hour

// settingRegistrationsEnabled is the settings key gating new registrations.
const settingRegistrationsEnabled = "registrations_enabled"

// ErrTokenInvalid is returned when a magic-link token is unknown or expired.
var ErrTokenInvalid = errors.New("auth: registration token invalid or expired")

// ErrUserNotFound is returned when no users row matches a lookup.
var ErrUserNotFound = errors.New("auth: user not found")

// ErrCredentialNotFound is returned when no passkey_credentials row matches a
// credential id.
var ErrCredentialNotFound = errors.New("auth: credential not found")

// ErrChallengeInvalid is returned when an authentication challenge is unknown or
// expired (already consumed, never issued, or past its TTL).
var ErrChallengeInvalid = errors.New("auth: challenge invalid or expired")

// querier is the subset of pgx used by the store. *pgxpool.Pool and pgx.Tx
// both satisfy it, so the store works against the pool directly or inside a
// transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the data-access layer for the auth ceremonies. It wraps the shared
// pgx pool and is reused across registration, authentication (#0016), and
// recovery (#0017). Methods take an explicit querier where they may run inside
// a transaction; the rest use the pool directly.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store over the shared connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers (e.g. session middleware in
// #0016) that need transaction control beyond the Store's methods.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// RegistrationsEnabled reads the registrations_enabled setting fresh from the
// database on every call (never cached) so an admin toggle takes effect
// immediately, per the PRD's Registration Settings section. A missing row is
// treated as disabled.
func (s *Store) RegistrationsEnabled(ctx context.Context) (bool, error) {
	var value string
	err := s.pool.QueryRow(ctx,
		`SELECT value FROM settings WHERE key = $1`,
		settingRegistrationsEnabled,
	).Scan(&value)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("auth: reading %s: %w", settingRegistrationsEnabled, err)
	}
	return value == "true", nil
}

// EmailRegistered reports whether a users row already exists for the given
// (already-lowercased) email.
func (s *Store) EmailRegistered(ctx context.Context, email string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`,
		email,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("auth: checking email registered: %w", err)
	}
	return exists, nil
}

// CreatePendingRegistration inserts a pending_registrations row with the given
// token and a 5-minute TTL and returns the expiry. The caller supplies the
// token so the same value can be emailed and later looked up.
func (s *Store) CreatePendingRegistration(ctx context.Context, email, token string, now time.Time) (time.Time, error) {
	expiresAt := now.Add(registrationTTL)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO pending_registrations (email, token, expires_at)
		 VALUES ($1, $2, $3)`,
		email, token, expiresAt,
	); err != nil {
		return time.Time{}, fmt.Errorf("auth: creating pending registration: %w", err)
	}
	return expiresAt, nil
}

// LookupPendingRegistration resolves a token to its email, enforcing the
// 5-minute TTL. ErrTokenInvalid is returned for an unknown or expired token so
// callers can map both to the same client-facing response without leaking which
// case occurred.
func (s *Store) LookupPendingRegistration(ctx context.Context, token string, now time.Time) (email string, err error) {
	var expiresAt time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT email, expires_at FROM pending_registrations WHERE token = $1`,
		token,
	).Scan(&email, &expiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrTokenInvalid
	case err != nil:
		return "", fmt.Errorf("auth: looking up pending registration: %w", err)
	}
	if !expiresAt.After(now) {
		return "", ErrTokenInvalid
	}
	return email, nil
}

// SaveRegistrationChallenge persists a registration WebAuthn challenge linked to
// the pending registration token, with a 5-minute TTL. user_id is NULL during
// registration because no account exists yet.
func (s *Store) SaveRegistrationChallenge(ctx context.Context, challenge []byte, token string, now time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO webauthn_challenges
		     (challenge, user_id, pending_registration_token, purpose, expires_at)
		 VALUES ($1, NULL, $2, 'registration', $3)`,
		challenge, token, now.Add(registrationTTL),
	); err != nil {
		return fmt.Errorf("auth: saving registration challenge: %w", err)
	}
	return nil
}

// ConsumeRegistrationChallenge atomically deletes and returns the registration
// challenge for a pending-registration token, enforcing the TTL. A challenge is
// single-use: deleting it on read prevents replay. ErrTokenInvalid is returned
// when no live challenge exists for the token.
func (s *Store) ConsumeRegistrationChallenge(ctx context.Context, q querier, token string, now time.Time) ([]byte, error) {
	var challenge []byte
	var expiresAt time.Time
	err := q.QueryRow(ctx,
		`DELETE FROM webauthn_challenges
		 WHERE pending_registration_token = $1 AND purpose = 'registration'
		 RETURNING challenge, expires_at`,
		token,
	).Scan(&challenge, &expiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrTokenInvalid
	case err != nil:
		return nil, fmt.Errorf("auth: consuming registration challenge: %w", err)
	}
	if !expiresAt.After(now) {
		return nil, ErrTokenInvalid
	}
	return challenge, nil
}

// CreatedUser is the result of inserting a new account.
type CreatedUser struct {
	ID      int64
	Email   string
	IsAdmin bool
}

// CreateUser inserts a users row inside the given querier (transaction). The
// account is active; is_admin is set when promoteAdmin is true (first user on a
// fresh install OR email matches ADMIN_EMAIL — the caller decides).
func (s *Store) CreateUser(ctx context.Context, q querier, email string, promoteAdmin bool, now time.Time) (CreatedUser, error) {
	var u CreatedUser
	err := q.QueryRow(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, $2, TRUE, $3)
		 RETURNING id, email, is_admin`,
		email, promoteAdmin, now,
	).Scan(&u.ID, &u.Email, &u.IsAdmin)
	if err != nil {
		return CreatedUser{}, fmt.Errorf("auth: creating user: %w", err)
	}
	return u, nil
}

// UserCount returns the number of accounts. Zero means a fresh install, in
// which case the first registrant is promoted to admin.
func (s *Store) UserCount(ctx context.Context, q querier) (int64, error) {
	var count int64
	if err := q.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("auth: counting users: %w", err)
	}
	return count, nil
}

// StoredCredential carries the fields persisted for a registered passkey.
type StoredCredential struct {
	UserID       int64
	CredentialID []byte
	PublicKey    []byte
	AAGUID       []byte // raw 16 bytes; nil/empty stored as SQL NULL
	SignCount    uint32
	DeviceName   string
}

// InsertCredential stores a passkey_credentials row inside the given querier.
// The AAGUID is written as a UUID; an empty/all-zero AAGUID is stored as NULL.
func (s *Store) InsertCredential(ctx context.Context, q querier, c StoredCredential, now time.Time) error {
	aaguid := aaguidArg(c.AAGUID)
	if _, err := q.Exec(ctx,
		`INSERT INTO passkey_credentials
		     (user_id, credential_id, public_key, aaguid, sign_count, device_name, created_at, last_used_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`,
		c.UserID, c.CredentialID, c.PublicKey, aaguid, int64(c.SignCount), c.DeviceName, now,
	); err != nil {
		return fmt.Errorf("auth: inserting credential: %w", err)
	}
	return nil
}

// DeletePendingRegistration removes the pending_registrations row for a token.
// Run after a successful finish (inside the transaction) so the magic link
// cannot be reused.
func (s *Store) DeletePendingRegistration(ctx context.Context, q querier, token string) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM pending_registrations WHERE token = $1`, token,
	); err != nil {
		return fmt.Errorf("auth: deleting pending registration: %w", err)
	}
	return nil
}

// authenticationTTL is the lifetime of an authentication (login) WebAuthn
// challenge. The PRD fixes it at the same 5-minute TTL as registration.
const authenticationTTL = 5 * time.Minute

// AccountByEmail is a lightweight view of a users row used by the login
// ceremony to decide whether to populate allowCredentials and, after a verified
// assertion, whether the account is active.
type AccountByEmail struct {
	ID     int64
	Active bool
}

// LookupUserByEmail resolves an (already-lowercased) email to its account id and
// active flag. ErrUserNotFound is returned when no row exists so the login start
// path can stay generic without leaking account existence.
func (s *Store) LookupUserByEmail(ctx context.Context, email string) (AccountByEmail, error) {
	var a AccountByEmail
	err := s.pool.QueryRow(ctx,
		`SELECT id, active FROM users WHERE email = $1`, email,
	).Scan(&a.ID, &a.Active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return AccountByEmail{}, ErrUserNotFound
	case err != nil:
		return AccountByEmail{}, fmt.Errorf("auth: looking up user by email: %w", err)
	}
	return a, nil
}

// CredentialRecord carries the stored fields of a passkey needed to verify an
// assertion and apply the sign_count rules.
type CredentialRecord struct {
	UserID       int64
	CredentialID []byte
	PublicKey    []byte
	AAGUID       []byte // raw 16 bytes; NULL in the DB decodes to nil
	SignCount    uint32
}

// CredentialsForUser loads every stored passkey for an account. Used to build
// the allowCredentials list and the login webauthn.User's credential set.
func (s *Store) CredentialsForUser(ctx context.Context, userID int64) ([]CredentialRecord, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, credential_id, public_key, aaguid, sign_count
		   FROM passkey_credentials WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: loading credentials for user: %w", err)
	}
	defer rows.Close()
	return scanCredentialRecords(rows)
}

// CredentialByID loads a single stored passkey by its credential id, along with
// the owning account's active flag. ErrCredentialNotFound is returned when the
// credential is unknown. This is the resolution path for both discoverable and
// non-discoverable login: the credential id always arrives in the assertion's
// rawID.
func (s *Store) CredentialByID(ctx context.Context, credentialID []byte) (CredentialRecord, bool, error) {
	var rec CredentialRecord
	var active bool
	err := s.pool.QueryRow(ctx,
		`SELECT c.user_id, c.credential_id, c.public_key, c.aaguid, c.sign_count, u.active
		   FROM passkey_credentials c
		   JOIN users u ON u.id = c.user_id
		  WHERE c.credential_id = $1`, credentialID,
	).Scan(&rec.UserID, &rec.CredentialID, &rec.PublicKey, &rec.AAGUID, &rec.SignCount, &active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return CredentialRecord{}, false, ErrCredentialNotFound
	case err != nil:
		return CredentialRecord{}, false, fmt.Errorf("auth: looking up credential: %w", err)
	}
	return rec, active, nil
}

// scanCredentialRecords decodes credential rows, mapping a NULL/zero AAGUID UUID
// back to nil bytes.
func scanCredentialRecords(rows pgx.Rows) ([]CredentialRecord, error) {
	var out []CredentialRecord
	for rows.Next() {
		var rec CredentialRecord
		var aaguid *[16]byte
		var signCount int64
		if err := rows.Scan(&rec.UserID, &rec.CredentialID, &rec.PublicKey, &aaguid, &signCount); err != nil {
			return nil, fmt.Errorf("auth: scanning credential row: %w", err)
		}
		if aaguid != nil {
			rec.AAGUID = append([]byte(nil), aaguid[:]...)
		}
		rec.SignCount = uint32(signCount)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: iterating credential rows: %w", err)
	}
	return out, nil
}

// SaveAuthenticationChallenge persists a login WebAuthn challenge with a 5-minute
// TTL. user_id and pending_registration_token are NULL: the credential (and thus
// the user) is resolved from the assertion on finish, and discoverable login does
// not know the user up front.
func (s *Store) SaveAuthenticationChallenge(ctx context.Context, challenge []byte, now time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO webauthn_challenges
		     (challenge, user_id, pending_registration_token, purpose, expires_at)
		 VALUES ($1, NULL, NULL, 'authentication', $2)`,
		challenge, now.Add(authenticationTTL),
	); err != nil {
		return fmt.Errorf("auth: saving authentication challenge: %w", err)
	}
	return nil
}

// ConsumeAuthenticationChallenge atomically deletes and returns a login challenge
// by its raw bytes, enforcing the TTL. Single-use deletion prevents replay.
// ErrChallengeInvalid is returned when no live authentication challenge matches.
func (s *Store) ConsumeAuthenticationChallenge(ctx context.Context, q querier, challenge []byte, now time.Time) error {
	var expiresAt time.Time
	err := q.QueryRow(ctx,
		`DELETE FROM webauthn_challenges
		  WHERE challenge = $1 AND purpose = 'authentication'
		 RETURNING expires_at`,
		challenge,
	).Scan(&expiresAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrChallengeInvalid
	case err != nil:
		return fmt.Errorf("auth: consuming authentication challenge: %w", err)
	}
	if !expiresAt.After(now) {
		return ErrChallengeInvalid
	}
	return nil
}

// UpdateSignCount writes a new sign_count and last_used_at for a credential.
func (s *Store) UpdateSignCount(ctx context.Context, q querier, credentialID []byte, signCount uint32, now time.Time) error {
	if _, err := q.Exec(ctx,
		`UPDATE passkey_credentials SET sign_count = $1, last_used_at = $2 WHERE credential_id = $3`,
		int64(signCount), now, credentialID,
	); err != nil {
		return fmt.Errorf("auth: updating sign count: %w", err)
	}
	return nil
}

// TouchCredentialLastUsed updates only last_used_at for a credential, used when
// the sign_count is left unchanged (the synced 0/0 case or the clone case).
func (s *Store) TouchCredentialLastUsed(ctx context.Context, q querier, credentialID []byte, now time.Time) error {
	if _, err := q.Exec(ctx,
		`UPDATE passkey_credentials SET last_used_at = $1 WHERE credential_id = $2`,
		now, credentialID,
	); err != nil {
		return fmt.Errorf("auth: touching credential last_used_at: %w", err)
	}
	return nil
}

// UpdateLastLogin sets users.last_login_at for an account.
func (s *Store) UpdateLastLogin(ctx context.Context, q querier, userID int64, now time.Time) error {
	if _, err := q.Exec(ctx,
		`UPDATE users SET last_login_at = $1 WHERE id = $2`, now, userID,
	); err != nil {
		return fmt.Errorf("auth: updating last_login_at: %w", err)
	}
	return nil
}

// DeleteSession removes the sessions row for a token. Used by logout. It is not
// an error to delete a token that does not exist (idempotent logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM sessions WHERE token = $1`, token,
	); err != nil {
		return fmt.Errorf("auth: deleting session: %w", err)
	}
	return nil
}

// ErrSessionInvalid is returned when a session token is unknown or its session
// has expired. The two cases are deliberately collapsed so the auth middleware
// can map both to a single 401 without leaking which occurred.
var ErrSessionInvalid = errors.New("auth: session invalid or expired")

// ErrAccountInactive is returned when a valid session belongs to a deactivated
// account. Deactivated users must not be able to use the API even while a
// previously issued session cookie is still live.
var ErrAccountInactive = errors.New("auth: account inactive")

// SessionUser is the authenticated principal resolved from a session cookie:
// the owning account's id, email, and admin flag. It carries exactly what the
// middleware attaches to the request context.
type SessionUser struct {
	ID      int64
	Email   string
	IsAdmin bool
}

// ResolveSession validates a raw session cookie value and, on success, applies
// the 30-day sliding window in the same statement before returning the
// authenticated user. It is the single per-request entry point for the session
// auth middleware (#0017).
//
// The session token is the raw random value stored directly in the cookie (see
// NewSessionToken / SetSessionCookie), so this is a direct lookup by token. In
// one round-trip it:
//   - joins sessions → users by the cookie value,
//   - rejects an unknown token or one whose expires_at is already in the past
//     (ErrSessionInvalid),
//   - rejects a session whose owner is deactivated (ErrAccountInactive),
//   - otherwise bumps last_seen_at to now and extends expires_at to now+30d
//     (the sliding window) and returns the user.
//
// The expiry check, the active check, and the bump are all evaluated against
// the row as it exists at call time: the UPDATE's WHERE clause re-checks
// expires_at so a row that expired between the read and the write is not
// silently revived. A missing/expired row yields no UPDATE; the function then
// distinguishes "expired" from "inactive" with a follow-up read so the caller
// gets the right error (and can delete the dead row).
func (s *Store) ResolveSession(ctx context.Context, token string, now time.Time) (SessionUser, error) {
	if token == "" {
		return SessionUser{}, ErrSessionInvalid
	}
	newExpiry := now.Add(sessionTTL)

	var u SessionUser
	err := s.pool.QueryRow(ctx,
		`UPDATE sessions s
		    SET last_seen_at = $2, expires_at = $3
		   FROM users u
		  WHERE s.token = $1
		    AND s.user_id = u.id
		    AND s.expires_at > $2
		    AND u.active = TRUE
		RETURNING u.id, u.email, u.is_admin`,
		token, now, newExpiry,
	).Scan(&u.ID, &u.Email, &u.IsAdmin)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SessionUser{}, fmt.Errorf("auth: resolving session: %w", err)
	}

	// No row was updated. Determine why so the caller gets a precise error and
	// expired rows can be reaped: read the live row's expiry and owner active
	// flag without mutating it.
	var expiresAt time.Time
	var active bool
	derr := s.pool.QueryRow(ctx,
		`SELECT s.expires_at, u.active
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		  WHERE s.token = $1`,
		token,
	).Scan(&expiresAt, &active)
	switch {
	case errors.Is(derr, pgx.ErrNoRows):
		return SessionUser{}, ErrSessionInvalid
	case derr != nil:
		return SessionUser{}, fmt.Errorf("auth: diagnosing session: %w", derr)
	}
	if !expiresAt.After(now) {
		// Expired: best-effort delete the dead row. A failure here must not
		// mask the 401, so the delete error is ignored.
		_ = s.DeleteSession(ctx, token)
		return SessionUser{}, ErrSessionInvalid
	}
	if !active {
		return SessionUser{}, ErrAccountInactive
	}
	// Live, active, but the UPDATE matched nothing — should not happen; treat as
	// invalid rather than panicking.
	return SessionUser{}, ErrSessionInvalid
}

// CreateSession inserts a sessions row with the given token and a 30-day expiry
// and returns the expiry. Reused by the login ceremony in #0016.
func (s *Store) CreateSession(ctx context.Context, q querier, userID int64, token string, now time.Time) (time.Time, error) {
	expiresAt := now.Add(sessionTTL)
	if _, err := q.Exec(ctx,
		`INSERT INTO sessions (user_id, token, created_at, expires_at, last_seen_at)
		 VALUES ($1, $2, $3, $4, $3)`,
		userID, token, now, expiresAt,
	); err != nil {
		return time.Time{}, fmt.Errorf("auth: creating session: %w", err)
	}
	return expiresAt, nil
}

// aaguidArg maps a raw AAGUID to a value pgx encodes as a UUID, or NULL when the
// AAGUID is absent or all zeros (synced platform passkeys often report zeros).
func aaguidArg(aaguid []byte) any {
	if len(aaguid) != 16 {
		return nil
	}
	allZero := true
	for _, b := range aaguid {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil
	}
	var u [16]byte
	copy(u[:], aaguid)
	return u
}
