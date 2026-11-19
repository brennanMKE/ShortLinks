package idp

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Kolonnade/Portiko/provider"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/config"
)

// ImportReport is what one run of Import did, or with dryRun would do.
type ImportReport struct {
	// Users is how many ShortLinks users were looked at.
	Users int
	// UsersCreated is how many got a new Portiko account on this run.
	UsersCreated int
	// UsersAdopted is how many already had a Portiko account (found by their
	// recorded subject or their verified email) and kept it.
	UsersAdopted int
	// CredentialsImported is how many passkeys were newly copied.
	CredentialsImported int
	// CredentialsPresent is how many were already in Portiko and left alone.
	CredentialsPresent int
	// CredentialsSkipped is how many have no captured user handle, so cannot be
	// imported. Those users recover through Portiko (#0135).
	CredentialsSkipped int
	// AdoptedByEmail lists users whose account was matched by verified email,
	// not by a recorded subject, so an operator can review each match.
	AdoptedByEmail []string
	// Skipped lists the users with at least one credential skipped, by email.
	Skipped []SkippedUser
	// Failed lists users whose import failed and was rolled back; the rest of the
	// run still completed.
	Failed []FailedUser
}

// SkippedUser is a user with passkeys that could not be imported.
type SkippedUser struct {
	Email string
	Count int
}

// FailedUser is a user the import could not finish.
type FailedUser struct {
	Email string
	Err   error
}

// Print writes the report as plain text.
func (r *ImportReport) Print(w io.Writer, dryRun bool) {
	mode := "import"
	if dryRun {
		mode = "dry run (nothing written)"
	}
	_, _ = fmt.Fprintf(w, "portiko-import %s\n", mode)
	_, _ = fmt.Fprintf(w, "  users:                           %d\n", r.Users)
	_, _ = fmt.Fprintf(w, "  accounts created:                %d\n", r.UsersCreated)
	_, _ = fmt.Fprintf(w, "  accounts already in Portiko:     %d\n", r.UsersAdopted)
	_, _ = fmt.Fprintf(w, "  credentials imported:            %d\n", r.CredentialsImported)
	_, _ = fmt.Fprintf(w, "  credentials already imported:    %d\n", r.CredentialsPresent)
	_, _ = fmt.Fprintf(w, "  credentials skipped (no handle): %d\n", r.CredentialsSkipped)
	for _, e := range r.AdoptedByEmail {
		_, _ = fmt.Fprintf(w, "    %s: matched an existing Portiko account by verified email (review)\n", e)
	}
	for _, s := range r.Skipped {
		_, _ = fmt.Fprintf(w, "    %s: %d credential(s) need recovery through Portiko\n", s.Email, s.Count)
	}
	for _, f := range r.Failed {
		_, _ = fmt.Fprintf(w, "  FAILED %s: %v\n", f.Email, f.Err)
	}
}

type slUser struct {
	id        int64
	email     string
	createdAt time.Time
	lastLogin *time.Time
	subject   *string
}

type slCredential struct {
	credentialID, publicKey, userHandle []byte
	aaguid                              *string
	signCount                           int64
	deviceName                          *string
	backupEligible, backupState         bool
	createdAt                           *time.Time
	lastUsedAt                          *time.Time
}

// Import copies every ShortLinks user into the embedded Portiko provider (#0131):
// an account with a fresh opaque subject, the email as a verified identifier and
// each passkey whose user handle was captured. It then records the subject on
// users.subject.
//
// It is safe to run again. An existing Portiko account is found by the recorded
// subject, or else by the user's email, and is never duplicated; a credential
// already in Portiko is left alone (Portiko keeps its own sign count). The only
// things written to ShortLinks are users.subject, filled in where it is NULL,
// and one audit_log row per user that this run changed. Credentials, sessions
// and every other ShortLinks table are only read.
//
// With PORTIKO_ENABLED off it does nothing and returns a nil report. It uses one
// transaction per user in Portiko, then one in ShortLinks; a crash between the
// two is repaired by the next run, which finds the account by email and records
// its subject.
//
// With dryRun the transactions run and are rolled back, so the counts are exact
// and nothing is written. A dry run does not create or migrate the Portiko
// schema; a missing one is treated as empty.
func Import(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, dryRun bool) (*ImportReport, error) {
	if !cfg.PortikoEnabled {
		return nil, nil
	}
	dbURL := cfg.PortikoDatabaseURL
	if dbURL == "" {
		var err error
		if dbURL, err = DeriveDatabaseURL(cfg.DatabaseURL); err != nil {
			return nil, err
		}
	}
	if !dryRun {
		if err := ensureSchema(ctx, dbURL); err != nil {
			return nil, err
		}
		if _, err := provider.Migrate(ctx, dbURL); err != nil {
			return nil, fmt.Errorf("idp: migrating the portiko schema: %w", err)
		}
	}
	ppool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return nil, fmt.Errorf("idp: connecting to the portiko database: %w", err)
	}
	defer ppool.Close()

	var ready bool
	if err := ppool.QueryRow(ctx, `SELECT to_regclass('users') IS NOT NULL
		AND to_regclass('identifiers') IS NOT NULL
		AND to_regclass('passkey_credentials') IS NOT NULL`).Scan(&ready); err != nil {
		return nil, fmt.Errorf("idp: checking the portiko schema: %w", err)
	}
	if !ready && !dryRun {
		return nil, errors.New("idp: the portiko schema is not migrated")
	}

	users, err := loadUsers(ctx, pool)
	if err != nil {
		return nil, err
	}
	rep := &ImportReport{}
	for _, u := range users {
		rep.Users++
		creds, err := loadCredentials(ctx, pool, u.id)
		if err != nil {
			return nil, err
		}
		one, err := importUser(ctx, pool, ppool, ready, dryRun, cfg.WebAuthnRPID, u, creds)
		if err != nil {
			rep.Failed = append(rep.Failed, FailedUser{Email: u.email, Err: err})
			continue
		}
		if one.created {
			rep.UsersCreated++
		} else {
			rep.UsersAdopted++
			if one.byEmail {
				rep.AdoptedByEmail = append(rep.AdoptedByEmail, u.email)
			}
		}
		rep.CredentialsImported += one.imported
		rep.CredentialsPresent += one.present
		if one.skipped > 0 {
			rep.CredentialsSkipped += one.skipped
			rep.Skipped = append(rep.Skipped, SkippedUser{Email: u.email, Count: one.skipped})
		}
	}
	return rep, nil
}

func loadUsers(ctx context.Context, pool *pgxpool.Pool) ([]slUser, error) {
	rows, err := pool.Query(ctx, `SELECT id, email, created_at, last_login_at, subject FROM users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("idp: reading users: %w", err)
	}
	defer rows.Close()
	var out []slUser
	for rows.Next() {
		var u slUser
		if err := rows.Scan(&u.id, &u.email, &u.createdAt, &u.lastLogin, &u.subject); err != nil {
			return nil, fmt.Errorf("idp: reading users: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func loadCredentials(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]slCredential, error) {
	rows, err := pool.Query(ctx,
		`SELECT credential_id, public_key, user_handle, aaguid::text, sign_count, device_name,
		        backup_eligible, backup_state, created_at, last_used_at
		   FROM passkey_credentials WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("idp: reading credentials: %w", err)
	}
	defer rows.Close()
	var out []slCredential
	for rows.Next() {
		var c slCredential
		if err := rows.Scan(&c.credentialID, &c.publicKey, &c.userHandle, &c.aaguid, &c.signCount,
			&c.deviceName, &c.backupEligible, &c.backupState, &c.createdAt, &c.lastUsedAt); err != nil {
			return nil, fmt.Errorf("idp: reading credentials: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type userResult struct {
	created, byEmail           bool
	imported, present, skipped int
}

// importUser handles one user: Portiko first, then ShortLinks' subject and audit
// row. Both transactions roll back on error, and in a dry run.
func importUser(ctx context.Context, pool, ppool *pgxpool.Pool, ready, dryRun bool, rpID string, u slUser, creds []slCredential) (res userResult, err error) {
	email := strings.ToLower(strings.TrimSpace(u.email))

	var subject string
	var portikoUserID int64
	var ptx pgx.Tx
	if ready {
		if ptx, err = ppool.Begin(ctx); err != nil {
			return res, err
		}
		defer func() { _ = ptx.Rollback(ctx) }()

		portikoUserID, subject, res.created, err = ensureAccount(ctx, ptx, u, email)
		if err != nil {
			return res, err
		}
		res.byEmail = !res.created && u.subject == nil
		// Never attach this user to a Portiko account that another ShortLinks
		// user already owns (an old email re-used, or emails differing by case):
		// that would hand them the other user's account.
		var taken int64
		switch err = pool.QueryRow(ctx, `SELECT id FROM users WHERE subject = $1 AND id <> $2`, subject, u.id).Scan(&taken); {
		case err == nil:
			return res, fmt.Errorf("portiko account %s already belongs to ShortLinks user %d; not merging (resolve by hand)", subject, taken)
		case errors.Is(err, pgx.ErrNoRows):
			err = nil
		default:
			return res, err
		}
	} else {
		res.created = true // dry run against a schema that does not exist yet
	}

	for _, c := range creds {
		if len(c.userHandle) == 0 {
			res.skipped++
			continue
		}
		if !ready {
			res.imported++
			continue
		}
		// A credential already in Portiko is left exactly as it is (Portiko owns
		// its sign count and backup state from here on); the no-op UPDATE only
		// makes RETURNING report the existing row. xmax is 0 for a new row.
		var ownerID int64
		var fresh bool
		err = ptx.QueryRow(ctx,
			`INSERT INTO passkey_credentials
			   (user_id, credential_id, public_key, user_handle, aaguid, sign_count, device_name,
			    backup_eligible, backup_state, rp_id, created_at, last_used_at)
			 VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, COALESCE($11, now()), $12)
			 ON CONFLICT (credential_id) DO UPDATE SET credential_id = EXCLUDED.credential_id
			 RETURNING user_id, xmax = 0`,
			portikoUserID, c.credentialID, c.publicKey, c.userHandle, c.aaguid, c.signCount, c.deviceName,
			c.backupEligible, c.backupState, rpID, c.createdAt, c.lastUsedAt).Scan(&ownerID, &fresh)
		if err != nil {
			return res, fmt.Errorf("copying a credential: %w", err)
		}
		if ownerID != portikoUserID {
			return res, errors.New("a credential is already in Portiko under a different account; refusing to move it")
		}
		if fresh {
			res.imported++
		} else {
			res.present++
		}
	}

	changed := res.created || res.imported > 0 || u.subject == nil
	if ptx != nil && !dryRun {
		// Commit Portiko first. If ShortLinks' step then fails, the next run finds
		// this account by email and records its subject.
		if err = ptx.Commit(ctx); err != nil {
			return res, err
		}
	}
	if dryRun || !changed {
		return res, nil
	}

	stx, err := pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = stx.Rollback(ctx) }()
	if _, err = stx.Exec(ctx, `UPDATE users SET subject = $1 WHERE id = $2 AND subject IS NULL`, subject, u.id); err != nil {
		return res, fmt.Errorf("recording the subject: %w", err)
	}
	uid := u.id
	if err = audit.New(pool).WriteTx(ctx, stx, audit.Entry{
		UserID: &uid, Action: audit.ActionAccountPortikoImported,
		TargetType: audit.TargetUser, TargetID: &uid,
		Metadata: map[string]any{
			"subject":              subject,
			"account_created":      res.created,
			"credentials_imported": res.imported,
			"credentials_skipped":  res.skipped,
		},
	}); err != nil {
		return res, err
	}
	return res, stx.Commit(ctx)
}

// ensureAccount returns the Portiko account for u, creating it when it does not
// exist. It looks first by the subject already recorded on the user, then by the
// verified email.
func ensureAccount(ctx context.Context, tx pgx.Tx, u slUser, email string) (id int64, subject string, created bool, err error) {
	var byEmail *int64
	var emailUser int64
	switch err = tx.QueryRow(ctx, `SELECT user_id FROM identifiers WHERE kind = 'email' AND lower(value) = lower($1) AND verified_at IS NOT NULL`, email).Scan(&emailUser); {
	case err == nil:
		byEmail = &emailUser
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return 0, "", false, err
	}

	if u.subject != nil {
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE subject = $1`, *u.subject).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, "", false, fmt.Errorf("recorded subject %s is not in Portiko (was the portiko schema reset?)", *u.subject)
		}
		if err != nil {
			return 0, "", false, err
		}
		if byEmail != nil && *byEmail != id {
			return 0, "", false, errors.New("the email belongs to a different Portiko account than the recorded subject")
		}
		return id, *u.subject, false, nil
	}

	if byEmail != nil {
		if err = tx.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, *byEmail).Scan(&subject); err != nil {
			return 0, "", false, err
		}
		return *byEmail, subject, false, nil
	}

	if subject, err = newSubject(); err != nil {
		return 0, "", false, err
	}
	if err = tx.QueryRow(ctx,
		`INSERT INTO users (subject, active, created_at, last_login_at) VALUES ($1, TRUE, $2, $3) RETURNING id`,
		subject, u.createdAt, u.lastLogin).Scan(&id); err != nil {
		return 0, "", false, fmt.Errorf("creating the account: %w", err)
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO identifiers (user_id, kind, value, verified_at, is_primary, created_at)
		 VALUES ($1, 'email', $2, $3, TRUE, $3)`, id, email, u.createdAt); err != nil {
		return 0, "", false, fmt.Errorf("creating the identifier: %w", err)
	}
	return id, subject, true, nil
}

// newSubject returns an opaque account identifier in Portiko's own format
// ("usr_" and 16 random bytes).
func newSubject() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "usr_" + base64.RawURLEncoding.EncodeToString(b), nil
}
