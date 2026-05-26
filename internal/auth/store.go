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
