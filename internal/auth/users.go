package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// ErrUserIsAdmin is returned by DeactivateUser when the target account is an
// admin. The PRD restricts deactivation to non-admin users, so an attempt to
// deactivate an admin is refused (the handler maps this to 403). Reactivation is
// not affected — only deactivation guards against admins.
var ErrUserIsAdmin = errors.New("auth: cannot deactivate an admin user")

// ErrUserAlreadyInactive is returned by DeactivateUser when the target account is
// already inactive. Deactivation is not idempotent here: a no-op would write a
// misleading second audit entry, so a second deactivate is reported as a conflict
// (the handler maps this to 409).
var ErrUserAlreadyInactive = errors.New("auth: user already inactive")

// ErrUserAlreadyActive is returned by ReactivateUser when the target account is
// already active, mirroring ErrUserAlreadyInactive for the reverse transition.
var ErrUserAlreadyActive = errors.New("auth: user already active")

// ManagedUser is the admin-view row of a users record returned by ListUsers and
// GetUser. It carries exactly the fields the admin user-management UI displays;
// CreatedAt and LastLoginAt are surfaced as the status/last-login columns per the
// PRD. LastLoginAt is nil for an account that has never completed a login.
type ManagedUser struct {
	ID          int64
	Email       string
	IsAdmin     bool
	Active      bool
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// UserDetail extends ManagedUser with the per-account counts shown on the detail
// view: how many links the user owns and how many passkeys they have registered.
type UserDetail struct {
	ManagedUser
	LinkCount    int64
	PasskeyCount int64
}

// ListUsers returns every account ordered newest-first (by id), as the admin
// user list. It backs GET /admin/users. The list is read straight from the users
// table; the status (active) and last-login columns come directly from the row.
func (s *Store) ListUsers(ctx context.Context) ([]ManagedUser, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, email, is_admin, active, created_at, last_login_at
		   FROM users
		  ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("auth: listing users: %w", err)
	}
	defer rows.Close()

	var out []ManagedUser
	for rows.Next() {
		var u ManagedUser
		var lastLogin *time.Time
		if err := rows.Scan(&u.ID, &u.Email, &u.IsAdmin, &u.Active, &u.CreatedAt, &lastLogin); err != nil {
			return nil, fmt.Errorf("auth: scanning user row: %w", err)
		}
		u.LastLoginAt = lastLogin
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: iterating user rows: %w", err)
	}
	return out, nil
}

// GetUser returns one account's detail, including its link and passkey counts.
// ErrUserNotFound is returned when no row matches the id so the handler can answer
// 404 without leaking whether the id ever existed. The counts are computed with
// correlated subqueries in the same round trip.
func (s *Store) GetUser(ctx context.Context, id int64) (UserDetail, error) {
	var d UserDetail
	var lastLogin *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT u.id, u.email, u.is_admin, u.active, u.created_at, u.last_login_at,
		        (SELECT COUNT(*) FROM links l WHERE l.user_id = u.id)               AS link_count,
		        (SELECT COUNT(*) FROM passkey_credentials c WHERE c.user_id = u.id) AS passkey_count
		   FROM users u
		  WHERE u.id = $1`, id,
	).Scan(&d.ID, &d.Email, &d.IsAdmin, &d.Active, &d.CreatedAt, &lastLogin, &d.LinkCount, &d.PasskeyCount)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return UserDetail{}, ErrUserNotFound
	case err != nil:
		return UserDetail{}, fmt.Errorf("auth: reading user %d: %w", id, err)
	}
	d.LastLoginAt = lastLogin
	return d, nil
}

// DeactivateUser atomically deactivates a non-admin account: it sets
// users.active = false, deletes ALL of the user's sessions (forcing immediate
// logout), and writes the account.deactivated audit entry — all inside one
// transaction so a committed deactivation always carries its audit row and a
// rolled-back one leaves nothing behind. The user's links are untouched.
//
// The provided auditEntry is written via the auditor's WriteTx inside the same
// transaction; the caller fills in actor/user/target/metadata/ip and this method
// supplies the open tx. A nil auditor skips the audit write (used only by tests
// that do not assert audit rows).
//
// Errors:
//   - ErrUserNotFound  — no such account.
//   - ErrUserIsAdmin   — the account is an admin (PRD: non-admin only).
//   - ErrUserAlreadyInactive — the account is already inactive.
//
// It returns the updated account so the handler can echo it back.
func (s *Store) DeactivateUser(ctx context.Context, id int64, auditor *audit.Logger, auditEntry audit.Entry) (ManagedUser, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("auth: begin deactivate tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the row for the duration so the admin/active checks and the update are
	// race-free against a concurrent deactivate or a promotion.
	var isAdmin, active bool
	err = tx.QueryRow(ctx,
		`SELECT is_admin, active FROM users WHERE id = $1 FOR UPDATE`, id,
	).Scan(&isAdmin, &active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ManagedUser{}, ErrUserNotFound
	case err != nil:
		return ManagedUser{}, fmt.Errorf("auth: locking user %d: %w", id, err)
	}
	if isAdmin {
		return ManagedUser{}, ErrUserIsAdmin
	}
	if !active {
		return ManagedUser{}, ErrUserAlreadyInactive
	}

	var u ManagedUser
	var lastLogin *time.Time
	if err := tx.QueryRow(ctx,
		`UPDATE users SET active = FALSE
		  WHERE id = $1
		 RETURNING id, email, is_admin, active, created_at, last_login_at`, id,
	).Scan(&u.ID, &u.Email, &u.IsAdmin, &u.Active, &u.CreatedAt, &lastLogin); err != nil {
		return ManagedUser{}, fmt.Errorf("auth: deactivating user %d: %w", id, err)
	}
	u.LastLoginAt = lastLogin

	// Force logout: drop every session for the user. Links are intentionally left
	// in place.
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return ManagedUser{}, fmt.Errorf("auth: deleting sessions for user %d: %w", id, err)
	}

	// account.deactivated audit, in-band so it commits with the change.
	if auditor != nil {
		if err := auditor.WriteTx(ctx, tx, auditEntry); err != nil {
			return ManagedUser{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("auth: commit deactivate tx: %w", err)
	}
	return u, nil
}

// ReactivateUser atomically reactivates an account: it sets users.active = true
// and writes the account.reactivated audit entry inside one transaction. Sessions
// are NOT restored — the user must log in again. The provided auditEntry is
// written via WriteTx in the same transaction (nil auditor skips it).
//
// Errors:
//   - ErrUserNotFound      — no such account.
//   - ErrUserAlreadyActive — the account is already active.
//
// It returns the updated account so the handler can echo it back.
func (s *Store) ReactivateUser(ctx context.Context, id int64, auditor *audit.Logger, auditEntry audit.Entry) (ManagedUser, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedUser{}, fmt.Errorf("auth: begin reactivate tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var active bool
	err = tx.QueryRow(ctx,
		`SELECT active FROM users WHERE id = $1 FOR UPDATE`, id,
	).Scan(&active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ManagedUser{}, ErrUserNotFound
	case err != nil:
		return ManagedUser{}, fmt.Errorf("auth: locking user %d: %w", id, err)
	}
	if active {
		return ManagedUser{}, ErrUserAlreadyActive
	}

	var u ManagedUser
	var lastLogin *time.Time
	if err := tx.QueryRow(ctx,
		`UPDATE users SET active = TRUE
		  WHERE id = $1
		 RETURNING id, email, is_admin, active, created_at, last_login_at`, id,
	).Scan(&u.ID, &u.Email, &u.IsAdmin, &u.Active, &u.CreatedAt, &lastLogin); err != nil {
		return ManagedUser{}, fmt.Errorf("auth: reactivating user %d: %w", id, err)
	}
	u.LastLoginAt = lastLogin

	if auditor != nil {
		if err := auditor.WriteTx(ctx, tx, auditEntry); err != nil {
			return ManagedUser{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return ManagedUser{}, fmt.Errorf("auth: commit reactivate tx: %w", err)
	}
	return u, nil
}
