package campaigns

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// ErrCampaignNotFound is returned by the store when no campaign matches the
// lookup for the given owner. Ownership is part of the lookup, so a campaign
// that exists but belongs to another user is reported as not found — the
// handler maps this to a 404 and never reveals that the slug exists. This
// mirrors links.ErrLinkNotFound.
var ErrCampaignNotFound = errors.New("campaigns: campaign not found")

// pgUniqueViolation is the PostgreSQL SQLSTATE for a unique_violation.
const pgUniqueViolation = "23505"

// pgCheckViolation is the PostgreSQL SQLSTATE for a check_violation — surfaced
// when a direct insert/update violates the migration 000010
// starts_at <= ends_at CHECK constraint (see
// TestSchema_InvertedWindowRejectedByCheckConstraint).
const pgCheckViolation = "23514"

// querier is the subset of pgx the store uses. *pgxpool.Pool and pgx.Tx both
// satisfy it, mirroring links.querier, so the same code can run on the pool
// directly or inside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the data-access layer for campaigns: create, list, fetch, update,
// archive, and delete. Every method is scoped to an owning user id so a
// request can only ever touch its own campaigns; ownership is enforced in
// SQL (the WHERE clause), not in the handler, mirroring internal/links.Store.
//
// Every method name is prefixed with "Campaign" (CreateCampaign, not Create).
// This is required, not stylistic: handlers.CampaignsHandler depends on an
// interface with these exact names, and devstore.Store is a single Go type
// that must simultaneously satisfy this interface AND the filterRuleStore
// interface (Create/Update/Delete/Get/List for filters.FilterRule) — bare
// names would collide with different signatures, making a dev-mode
// implementation structurally impossible.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store over the shared connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers that need transaction control
// beyond the Store's methods.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Campaign is the full domain representation of a campaigns row.
type Campaign struct {
	ID                 int64
	UserID             int64
	Name               string
	Slug               string
	Description        string // empty when the column is NULL
	StartsAt           *time.Time
	EndsAt             *time.Time
	Archived           bool
	DefaultUTMSource   string // empty when the column is NULL
	DefaultUTMMedium   string
	DefaultUTMCampaign string
	DefaultUTMTerm     string
	DefaultUTMContent  string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewCampaign carries the validated input for creating a campaign. The
// handler fills it after validating the request; the store resolves a unique
// per-user slug from Name and performs the insert.
type NewCampaign struct {
	UserID             int64
	Name               string
	Description        string     // "" stored as SQL NULL
	StartsAt           *time.Time // nil = no start date
	EndsAt             *time.Time // nil = no end date
	DefaultUTMSource   string     // "" stored as SQL NULL
	DefaultUTMMedium   string
	DefaultUTMCampaign string // "" -> defaults to the generated slug (see CreateCampaign)
	DefaultUTMTerm     string
	DefaultUTMContent  string
}

// CreateCampaign inserts a new campaign. The slug is derived deterministically from
// Name (Slugify) and, on collision with an existing campaign of the SAME
// user, suffixed ("-2", "-3", ...) rather than rejected — the per-user
// UNIQUE(user_id, slug) constraint is the uniqueness domain, so two different
// users can hold the same base slug unsuffixed. When DefaultUTMCampaign is
// not supplied it defaults to the resolved slug, per the issue's decided
// schema addition.
//
// The insert and the audit write both happen inside one transaction: when
// auditor is non-nil, entry is written via WriteTx with TargetID set to the
// new campaign's id, BEFORE tx.Commit. If the audit insert itself fails (e.g.
// a foreign-key violation on actor_id), WriteTx returns an error, this method
// returns without ever calling tx.Commit, and the deferred Rollback discards
// the campaign insert along with it — so a committed campaign always carries
// its audit row and a failed audit write leaves neither behind. There is no
// test seam here: forcing the audit INSERT itself to fail (see
// TestCreateCampaign_AuditInsertFailureRollsBackMutation) exercises the real
// rollback path directly.
func (s *Store) CreateCampaign(ctx context.Context, in NewCampaign, auditor *audit.Logger, entry audit.Entry) (Campaign, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Campaign{}, fmt.Errorf("campaigns: begin create tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	base := Slugify(in.Name)
	slug, err := GenerateUniqueSlug(base, func(candidate string) (bool, error) {
		return slugExists(ctx, tx, in.UserID, candidate)
	})
	if err != nil {
		return Campaign{}, err
	}

	defaultUTMCampaign := in.DefaultUTMCampaign
	if defaultUTMCampaign == "" {
		defaultUTMCampaign = slug
	}

	c := Campaign{
		UserID:             in.UserID,
		Name:               in.Name,
		Slug:               slug,
		Description:        in.Description,
		StartsAt:           in.StartsAt,
		EndsAt:             in.EndsAt,
		Archived:           false,
		DefaultUTMSource:   in.DefaultUTMSource,
		DefaultUTMMedium:   in.DefaultUTMMedium,
		DefaultUTMCampaign: defaultUTMCampaign,
		DefaultUTMTerm:     in.DefaultUTMTerm,
		DefaultUTMContent:  in.DefaultUTMContent,
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO campaigns
		     (user_id, name, slug, description, starts_at, ends_at, archived,
		      default_utm_source, default_utm_medium, default_utm_campaign,
		      default_utm_term, default_utm_content, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, FALSE, $7, $8, $9, $10, $11, now(), now())
		 RETURNING id, created_at, updated_at`,
		in.UserID, in.Name, slug, nullIfEmpty(in.Description), in.StartsAt, in.EndsAt,
		nullIfEmpty(in.DefaultUTMSource), nullIfEmpty(in.DefaultUTMMedium), nullIfEmpty(defaultUTMCampaign),
		nullIfEmpty(in.DefaultUTMTerm), nullIfEmpty(in.DefaultUTMContent),
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			// The slug existence check above ran in the same tx, so this can only
			// happen under a genuine concurrent create for the same (user, slug) —
			// not exercised by the deterministic collision-suffix path.
			return Campaign{}, fmt.Errorf("campaigns: slug %q collided for user %d: %w", slug, in.UserID, err)
		}
		return Campaign{}, fmt.Errorf("campaigns: inserting campaign: %w", err)
	}

	if auditor != nil {
		e := entry
		id := c.ID
		e.TargetID = &id
		if err := auditor.WriteTx(ctx, tx, e); err != nil {
			return Campaign{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Campaign{}, fmt.Errorf("campaigns: commit create tx: %w", err)
	}
	return c, nil
}

// slugExists reports whether userID already has a campaign with slug.
func slugExists(ctx context.Context, q querier, userID int64, slug string) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM campaigns WHERE user_id = $1 AND slug = $2)`,
		userID, slug,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("campaigns: checking slug exists: %w", err)
	}
	return exists, nil
}

// ListCampaignsForUser returns all of the user's campaigns, most recently
// created first (ORDER BY created_at DESC, id DESC — the id tiebreaker keeps
// the order deterministic even when two rows share a created_at timestamp).
// Link count and total clicks are NOT included here — #0102 adds those; the
// handler is responsible for reporting them as 0 until then.
func (s *Store) ListCampaignsForUser(ctx context.Context, userID int64) ([]Campaign, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, user_id, name, slug, description, starts_at, ends_at, archived,
		        default_utm_source, default_utm_medium, default_utm_campaign,
		        default_utm_term, default_utm_content, created_at, updated_at
		   FROM campaigns
		  WHERE user_id = $1
		  ORDER BY created_at DESC, id DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("campaigns: listing campaigns: %w", err)
	}
	defer rows.Close()

	out := make([]Campaign, 0)
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("campaigns: iterating campaign rows: %w", err)
	}
	return out, nil
}

// GetCampaignBySlug returns a single campaign by slug, scoped to userID.
// ErrCampaignNotFound is returned when the slug does not exist OR belongs to
// another user — the two are deliberately indistinguishable so the detail
// endpoint never leaks the existence of another user's campaign.
func (s *Store) GetCampaignBySlug(ctx context.Context, userID int64, slug string) (Campaign, error) {
	return getBySlug(ctx, s.pool, userID, slug)
}

func getBySlug(ctx context.Context, q querier, userID int64, slug string) (Campaign, error) {
	row := q.QueryRow(ctx,
		`SELECT id, user_id, name, slug, description, starts_at, ends_at, archived,
		        default_utm_source, default_utm_medium, default_utm_campaign,
		        default_utm_term, default_utm_content, created_at, updated_at
		   FROM campaigns
		  WHERE user_id = $1 AND slug = $2`,
		userID, slug,
	)
	c, err := scanCampaign(row)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Campaign{}, ErrCampaignNotFound
	case err != nil:
		return Campaign{}, err
	}
	return c, nil
}

// CampaignUpdate carries the optional fields a PATCH may change. A nil
// pointer means "leave unchanged". Description and the default_utm_* fields
// follow links.LinkUpdate.Title's convention: a non-nil pointer to "" clears
// the column to NULL. StartsAt/EndsAt follow ExpiresAt's double-pointer
// convention: a nil field is unchanged; a non-nil field sets the column to
// *StartsAt/*EndsAt, which may itself be nil to clear. The slug is
// deliberately not updatable — it is fixed at creation (see CreateCampaign).
type CampaignUpdate struct {
	Name               *string
	Description        *string
	StartsAt           **time.Time
	EndsAt             **time.Time
	Archived           *bool
	DefaultUTMSource   *string
	DefaultUTMMedium   *string
	DefaultUTMCampaign *string
	DefaultUTMTerm     *string
	DefaultUTMContent  *string
}

// UpdateCampaign applies a partial update to the user's own campaign (by
// slug) and returns the updated row. Only the provided fields change. The
// update is scoped to userID so another user's campaign does not match
// (ErrCampaignNotFound). The mutation and, when auditor is non-nil, the audit
// write (entry, with TargetID set to the resolved campaign id) happen inside
// one transaction, mirroring CreateCampaign/ArchiveCampaign/DeleteCampaign.
func (s *Store) UpdateCampaign(ctx context.Context, userID int64, slug string, upd CampaignUpdate, auditor *audit.Logger, entry audit.Entry) (Campaign, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Campaign{}, fmt.Errorf("campaigns: begin update tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	setClauses := make([]string, 0, 10)
	args := make([]any, 0, 12)
	// $1 and $2 are reserved for the WHERE clause (userID, slug); SET params
	// start at $3.
	args = append(args, userID, slug)
	next := 3

	// addNullableString is for the columns that are actually nullable: a
	// non-nil pointer to "" clears the column to SQL NULL. NOT applicable to
	// name (see below) — that column is NOT NULL, so mapping "" to NULL there
	// would surface as a raw constraint-violation error instead of meaningful
	// validation.
	addNullableString := func(col string, v *string) {
		if v == nil {
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, next))
		if *v == "" {
			args = append(args, nil)
		} else {
			args = append(args, *v)
		}
		next++
	}
	if upd.Name != nil {
		// name is NOT NULL: always bind the literal value, never SQL NULL,
		// regardless of whether it is empty. The handler validates non-empty
		// before calling UpdateCampaign; this just guarantees the store itself
		// can never attempt to null out a NOT NULL column no matter who calls it.
		setClauses = append(setClauses, fmt.Sprintf("name = $%d", next))
		args = append(args, *upd.Name)
		next++
	}
	addNullableString("description", upd.Description)
	addNullableString("default_utm_source", upd.DefaultUTMSource)
	addNullableString("default_utm_medium", upd.DefaultUTMMedium)
	addNullableString("default_utm_campaign", upd.DefaultUTMCampaign)
	addNullableString("default_utm_term", upd.DefaultUTMTerm)
	addNullableString("default_utm_content", upd.DefaultUTMContent)

	if upd.StartsAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("starts_at = $%d", next))
		args = append(args, *upd.StartsAt)
		next++
	}
	if upd.EndsAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("ends_at = $%d", next))
		args = append(args, *upd.EndsAt)
		next++
	}
	if upd.Archived != nil {
		setClauses = append(setClauses, fmt.Sprintf("archived = $%d", next))
		args = append(args, *upd.Archived)
		next++
	}

	if len(setClauses) == 0 {
		// Nothing to change: behave as a no-op fetch so the caller still gets the
		// current row (and a proper ErrCampaignNotFound if it is not theirs). The
		// deferred Rollback is harmless here since nothing was written.
		return getBySlug(ctx, tx, userID, slug)
	}

	setClauses = append(setClauses, "updated_at = now()")
	setSQL := setClauses[0]
	for _, c := range setClauses[1:] {
		setSQL += ", " + c
	}
	sql := fmt.Sprintf(
		`UPDATE campaigns SET %s
		  WHERE user_id = $1 AND slug = $2
		 RETURNING id`, setSQL)

	var id int64
	err = tx.QueryRow(ctx, sql, args...).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Campaign{}, ErrCampaignNotFound
	case err != nil:
		return Campaign{}, fmt.Errorf("campaigns: updating campaign: %w", err)
	}

	if auditor != nil {
		e := entry
		e.TargetID = &id
		if err := auditor.WriteTx(ctx, tx, e); err != nil {
			return Campaign{}, err
		}
	}

	// Re-read (inside the same tx) so the response carries the authoritative row.
	c, err := getBySlug(ctx, tx, userID, slug)
	if err != nil {
		return Campaign{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Campaign{}, fmt.Errorf("campaigns: commit update tx: %w", err)
	}
	return c, nil
}

// ArchiveCampaign sets (or clears) the campaign's archived flag. Unlike
// DeleteCampaign, this is fully reversible: ArchiveCampaign(..., true)
// followed by ArchiveCampaign(..., false) returns the campaign to its prior
// visible state. It is a thin, explicitly named wrapper over UpdateCampaign —
// kept as its own method (mirroring the issue's deliverable list) so
// archive/unarchive has a single obvious call site distinct from the general
// field update, even though today's PATCH handler reaches the same effect via
// UpdateCampaign directly when the request body sets "archived".
//
// The passed-in entry's Metadata is stamped with {"archived": archived}
// (merged with, not replacing, any metadata the caller already set) so the
// resulting campaign.updated audit row is self-describing — distinguishable
// from a plain rename — regardless of what the caller populated.
func (s *Store) ArchiveCampaign(ctx context.Context, userID int64, slug string, archived bool, auditor *audit.Logger, entry audit.Entry) (Campaign, error) {
	e := entry
	meta := make(map[string]any)
	if existing, ok := e.Metadata.(map[string]any); ok {
		for k, v := range existing {
			meta[k] = v
		}
	}
	meta["archived"] = archived
	e.Metadata = meta
	return s.UpdateCampaign(ctx, userID, slug, CampaignUpdate{Archived: &archived}, auditor, e)
}

// DeleteCampaign permanently removes the user's own campaign (by slug).
// Unlike ArchiveCampaign, this is not reversible. It does not cascade to
// anything in this issue's scope — link membership (#0099) does not exist
// yet, so there is nothing to unassign. Scoped to userID, so another user's
// campaign does not match (ErrCampaignNotFound). The delete and, when
// auditor is non-nil, the audit write happen inside one transaction.
func (s *Store) DeleteCampaign(ctx context.Context, userID int64, slug string, auditor *audit.Logger, entry audit.Entry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("campaigns: begin delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	err = tx.QueryRow(ctx,
		`DELETE FROM campaigns WHERE user_id = $1 AND slug = $2 RETURNING id`,
		userID, slug,
	).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrCampaignNotFound
	case err != nil:
		return fmt.Errorf("campaigns: deleting campaign: %w", err)
	}

	if auditor != nil {
		e := entry
		e.TargetID = &id
		if err := auditor.WriteTx(ctx, tx, e); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("campaigns: commit delete tx: %w", err)
	}
	return nil
}

// nullIfEmpty maps an empty string to nil so an unset nullable TEXT column
// stores SQL NULL rather than an empty string.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// scanCampaign decodes a campaigns row from a pgx.Row or pgx.Rows, mapping
// NULL description/starts_at/ends_at/default_utm_* to their zero values. The
// column order must match the SELECT lists above.
func scanCampaign(row pgx.Row) (Campaign, error) {
	var c Campaign
	var description *string
	var startsAt, endsAt *time.Time
	var defSource, defMedium, defCampaign, defTerm, defContent *string
	if err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Slug, &description, &startsAt, &endsAt, &c.Archived,
		&defSource, &defMedium, &defCampaign, &defTerm, &defContent,
		&c.CreatedAt, &c.UpdatedAt,
	); err != nil {
		return Campaign{}, err
	}
	if description != nil {
		c.Description = *description
	}
	c.StartsAt = startsAt
	c.EndsAt = endsAt
	if defSource != nil {
		c.DefaultUTMSource = *defSource
	}
	if defMedium != nil {
		c.DefaultUTMMedium = *defMedium
	}
	if defCampaign != nil {
		c.DefaultUTMCampaign = *defCampaign
	}
	if defTerm != nil {
		c.DefaultUTMTerm = *defTerm
	}
	if defContent != nil {
		c.DefaultUTMContent = *defContent
	}
	return c, nil
}
