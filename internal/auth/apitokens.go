package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// API tokens are named, long-lived bearer credentials a user mints in the web
// UI and hands to a script or agent so it can call the /api/* endpoints as that
// user. They differ from sessions in three deliberate ways:
//
//   - Only the SHA-256 digest is stored. The plaintext is returned once, from
//     CreateAPIToken, and can never be read back — not by the UI, not by an
//     endpoint, not from a database dump.
//   - Expiry is fixed at creation (or absent, for a token that never expires);
//     there is no sliding window.
//   - They carry the APITokenPrefix, so the middleware can route a bearer value
//     to the right resolver without a speculative lookup, and so a leaked token
//     is easy to spot (secret scanners, grep over logs).

// APITokenPrefix starts every API token's plaintext.
const APITokenPrefix = "slk_"

// apiTokenLen is the number of random bytes behind an API token (same strength
// as a session token).
const apiTokenLen = 32

// apiTokenDisplayLen is how many leading characters of the plaintext (prefix
// included) are kept in token_prefix for display, e.g. "slk_AbCdEfGh".
const apiTokenDisplayLen = len(APITokenPrefix) + 8

// MaxAPITokenNameLen caps a token's display name.
const MaxAPITokenNameLen = 100

// apiTokenTouchInterval throttles the last_used_at/last_used_ip write so a busy
// client does not turn every API call into a database write.
const apiTokenTouchInterval = time.Minute

// ErrAPITokenInvalid is returned by ResolveAPIToken for an unknown, revoked, or
// expired token, or one whose owner is deactivated. The cases are deliberately
// indistinguishable to the caller.
var ErrAPITokenInvalid = errors.New("auth: api token invalid, revoked, or expired")

// ErrAPITokenNotFound is returned by RevokeAPIToken when the id does not name a
// live token owned by the caller.
var ErrAPITokenNotFound = errors.New("auth: api token not found")

// ErrAPITokenNameTaken is returned by CreateAPIToken when the caller already
// has a live token with the same name.
var ErrAPITokenNameTaken = errors.New("auth: api token name already in use")

// APIToken is the display metadata for one token. It never carries the secret.
type APIToken struct {
	ID         int64
	Name       string
	Prefix     string
	ReadOnly   bool
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	RevokedAt  *time.Time
}

// NewAPIToken describes a token to mint. A nil ExpiresAt means it never
// expires.
type NewAPIToken struct {
	UserID    int64
	Name      string
	ReadOnly  bool
	ExpiresAt *time.Time
}

// APITokenUser is the principal an API token authenticates as.
type APITokenUser struct {
	SessionUser
	TokenID   int64
	TokenName string
	ReadOnly  bool
}

// HashAPIToken returns the digest stored in api_tokens.token_hash. A plain
// SHA-256 (no salt, no KDF) is correct here: the input is 256 bits of
// crypto/rand output, not a guessable password, and the lookup must be by
// exact digest.
func HashAPIToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// IsAPIToken reports whether a bearer value is shaped like an API token.
func IsAPIToken(token string) bool {
	return strings.HasPrefix(token, APITokenPrefix)
}

// NewAPITokenSecret generates a fresh plaintext API token.
func NewAPITokenSecret() (string, error) {
	raw, err := randomURLToken(apiTokenLen)
	if err != nil {
		return "", err
	}
	return APITokenPrefix + raw, nil
}

// CreateAPIToken mints a token, stores its digest, and returns the metadata
// plus the plaintext. The plaintext exists only in this return value; the
// caller shows it to the user once and must not log it. The api_token.created
// audit row is written in the same transaction.
func (s *Store) CreateAPIToken(ctx context.Context, in NewAPIToken, auditor *audit.Logger, entry audit.Entry) (APIToken, string, error) {
	secret, err := NewAPITokenSecret()
	if err != nil {
		return APIToken{}, "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIToken{}, "", fmt.Errorf("auth: begin create api token tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	t := APIToken{Name: in.Name, Prefix: secret[:apiTokenDisplayLen], ReadOnly: in.ReadOnly}
	err = tx.QueryRow(ctx,
		`INSERT INTO api_tokens (user_id, name, token_prefix, token_hash, read_only, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id, created_at, expires_at`,
		in.UserID, in.Name, t.Prefix, HashAPIToken(secret), in.ReadOnly, in.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt, &t.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "idx_api_tokens_user_name_live" {
			return APIToken{}, "", ErrAPITokenNameTaken
		}
		return APIToken{}, "", fmt.Errorf("auth: inserting api token: %w", err)
	}

	if auditor != nil {
		entry.TargetID = &t.ID
		if err := auditor.WriteTx(ctx, tx, entry); err != nil {
			return APIToken{}, "", fmt.Errorf("auth: auditing api token create: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, "", fmt.Errorf("auth: commit create api token: %w", err)
	}
	return t, secret, nil
}

// ListAPITokensForUser returns the caller's tokens, newest first, including
// revoked ones (the UI shows them greyed out). Never returns secrets.
func (s *Store) ListAPITokensForUser(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, name, token_prefix, read_only, created_at, expires_at,
		        last_used_at, COALESCE(host(last_used_ip), ''), revoked_at
		   FROM api_tokens
		  WHERE user_id = $1
		  ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: listing api tokens: %w", err)
	}
	defer rows.Close()

	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.ReadOnly, &t.CreatedAt, &t.ExpiresAt,
			&t.LastUsedAt, &t.LastUsedIP, &t.RevokedAt); err != nil {
			return nil, fmt.Errorf("auth: scanning api token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken marks one of the caller's live tokens revoked and writes the
// api_token.revoked audit row in the same transaction. Revocation is
// immediate: ResolveAPIToken reads the row on every request, there is no
// cache. A token that does not exist, belongs to someone else, or is already
// revoked yields ErrAPITokenNotFound.
func (s *Store) RevokeAPIToken(ctx context.Context, userID, id int64, now time.Time, auditor *audit.Logger, entry audit.Entry) (APIToken, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return APIToken{}, fmt.Errorf("auth: begin revoke api token tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	t := APIToken{ID: id}
	err = tx.QueryRow(ctx,
		`UPDATE api_tokens SET revoked_at = $3
		  WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		 RETURNING name, token_prefix, read_only, created_at, expires_at, last_used_at, revoked_at`,
		id, userID, now,
	).Scan(&t.Name, &t.Prefix, &t.ReadOnly, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return APIToken{}, ErrAPITokenNotFound
	case err != nil:
		return APIToken{}, fmt.Errorf("auth: revoking api token %d: %w", id, err)
	}

	if auditor != nil {
		entry.TargetID = &t.ID
		if entry.Metadata == nil {
			entry.Metadata = map[string]any{"name": t.Name, "prefix": t.Prefix}
		}
		if err := auditor.WriteTx(ctx, tx, entry); err != nil {
			return APIToken{}, fmt.Errorf("auth: auditing api token revoke: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return APIToken{}, fmt.Errorf("auth: commit revoke api token: %w", err)
	}
	return t, nil
}

// ResolveAPIToken authenticates a plaintext API token. It succeeds only for a
// live (not revoked, not expired) token whose owner is active, and returns the
// owner plus the token's identity and scope. On success it records
// last_used_at/last_used_ip, throttled to once per apiTokenTouchInterval; a
// failure of that bookkeeping write never fails the request.
//
// The returned SessionUser.IsAdmin is always false: an API token never carries
// admin authority, even when an admin minted it, so a leaked agent token cannot
// manage users, settings, or URL filters.
func (s *Store) ResolveAPIToken(ctx context.Context, token, ip string, now time.Time) (APITokenUser, error) {
	if !IsAPIToken(token) {
		return APITokenUser{}, ErrAPITokenInvalid
	}

	var u APITokenUser
	var lastUsed *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT t.id, t.name, t.read_only, t.last_used_at, u.id, u.email
		   FROM api_tokens t
		   JOIN users u ON u.id = t.user_id
		  WHERE t.token_hash = $1
		    AND t.revoked_at IS NULL
		    AND (t.expires_at IS NULL OR t.expires_at > $2)
		    AND u.active`,
		HashAPIToken(token), now,
	).Scan(&u.TokenID, &u.TokenName, &u.ReadOnly, &lastUsed, &u.ID, &u.Email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return APITokenUser{}, ErrAPITokenInvalid
	case err != nil:
		return APITokenUser{}, fmt.Errorf("auth: resolving api token: %w", err)
	}

	if lastUsed == nil || now.Sub(*lastUsed) >= apiTokenTouchInterval {
		var ipArg any
		if ip != "" {
			ipArg = ip
		}
		// Best-effort: an unparseable IP fails the INET cast, and that (like
		// any other error here) only loses the bookkeeping, never the request.
		_, _ = s.pool.Exec(ctx,
			`UPDATE api_tokens SET last_used_at = $2, last_used_ip = $3::inet WHERE id = $1`,
			u.TokenID, now, ipArg)
	}
	return u, nil
}
