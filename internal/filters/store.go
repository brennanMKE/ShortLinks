package filters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrRuleNotFound is returned by the store when no url_filter_rules row matches
// the lookup. The admin handler maps it to a 404.
var ErrRuleNotFound = errors.New("filters: rule not found")

// FilterRule is the full url_filter_rules row, used by the admin CRUD layer
// (list/create/update/delete). The engine's lighter Rule type carries only what
// evaluation needs; FilterRule carries the whole row for the admin API.
type FilterRule struct {
	ID          int64
	Pattern     string
	ReasonCode  int16
	Description string // empty when the column is NULL
	Active      bool
	CreatedBy   *int64 // nil when NULL
	CreatedAt   time.Time
}

// Store is the data-access layer for url_filter_rules: list, create, update,
// delete, plus the engine's LoadActiveRules query (which lives in filters.go and
// takes any Querier). It backs the admin CRUD endpoints; the 60-second rule
// cache sits in front of LoadActiveRules for the link-creation hot path.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store over the shared connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool so callers can run LoadActiveRules through
// the Store.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// LoadActive is a convenience wrapper that loads the active rules through the
// Store's pool. It is what the rule cache's loader calls.
func (s *Store) LoadActive(ctx context.Context) ([]Rule, error) {
	return LoadActiveRules(ctx, s.pool)
}

// List returns every rule (active and inactive) in id order, for the admin list
// endpoint.
func (s *Store) List(ctx context.Context) ([]FilterRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, pattern, reason_code, description, active, created_by, created_at
		   FROM url_filter_rules
		  ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("filters: listing rules: %w", err)
	}
	defer rows.Close()

	out := make([]FilterRule, 0)
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filters: iterating rules: %w", err)
	}
	return out, nil
}

// NewRule carries the validated input for creating a rule. The handler fills it
// after validating the pattern compiles and the reason code is in range.
type NewRule struct {
	Pattern     string
	ReasonCode  int16
	Description string // "" stored as SQL NULL
	CreatedBy   int64  // the admin's user id
}

// Create inserts a new active rule and returns the full row. active defaults to
// TRUE per the schema; the row is attributed to the creating admin.
func (s *Store) Create(ctx context.Context, in NewRule) (FilterRule, error) {
	var description *string
	if in.Description != "" {
		description = &in.Description
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO url_filter_rules (pattern, reason_code, description, active, created_by, created_at)
		 VALUES ($1, $2, $3, TRUE, $4, now())
		 RETURNING id, pattern, reason_code, description, active, created_by, created_at`,
		in.Pattern, in.ReasonCode, description, in.CreatedBy,
	)
	r, err := scanRule(row)
	if err != nil {
		return FilterRule{}, fmt.Errorf("filters: inserting rule: %w", err)
	}
	return r, nil
}

// RuleUpdate carries the optional fields a PATCH may change. A nil pointer means
// "leave unchanged"; a non-nil pointer sets the field.
type RuleUpdate struct {
	Pattern     *string
	ReasonCode  *int16
	Description *string
	Active      *bool
}

// Get returns a single rule by id. ErrRuleNotFound when absent.
func (s *Store) Get(ctx context.Context, id int64) (FilterRule, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, pattern, reason_code, description, active, created_by, created_at
		   FROM url_filter_rules WHERE id = $1`, id)
	r, err := scanRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return FilterRule{}, ErrRuleNotFound
	}
	if err != nil {
		return FilterRule{}, fmt.Errorf("filters: getting rule: %w", err)
	}
	return r, nil
}

// Update applies a partial update to a rule and returns the updated row.
// ErrRuleNotFound when the id does not exist. A dynamic SET is built so a
// description can be cleared to NULL (Description -> "") distinctly from "leave
// unchanged".
func (s *Store) Update(ctx context.Context, id int64, upd RuleUpdate) (FilterRule, error) {
	setClauses := make([]string, 0, 4)
	args := make([]any, 0, 5)
	args = append(args, id) // $1 is the WHERE id; SET params start at $2.
	next := 2

	if upd.Pattern != nil {
		setClauses = append(setClauses, fmt.Sprintf("pattern = $%d", next))
		args = append(args, *upd.Pattern)
		next++
	}
	if upd.ReasonCode != nil {
		setClauses = append(setClauses, fmt.Sprintf("reason_code = $%d", next))
		args = append(args, *upd.ReasonCode)
		next++
	}
	if upd.Description != nil {
		setClauses = append(setClauses, fmt.Sprintf("description = $%d", next))
		if *upd.Description == "" {
			args = append(args, nil)
		} else {
			args = append(args, *upd.Description)
		}
		next++
	}
	if upd.Active != nil {
		setClauses = append(setClauses, fmt.Sprintf("active = $%d", next))
		args = append(args, *upd.Active)
		next++
	}

	// Nothing to change: behave as a fetch so the caller still gets the current
	// row (and a proper 404 if it does not exist).
	if len(setClauses) == 0 {
		return s.Get(ctx, id)
	}

	setSQL := setClauses[0]
	for _, c := range setClauses[1:] {
		setSQL += ", " + c
	}
	sql := fmt.Sprintf(
		`UPDATE url_filter_rules SET %s WHERE id = $1
		 RETURNING id, pattern, reason_code, description, active, created_by, created_at`,
		setSQL)

	row := s.pool.QueryRow(ctx, sql, args...)
	r, err := scanRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return FilterRule{}, ErrRuleNotFound
	}
	if err != nil {
		var pgErr *pgconn.PgError
		_ = errors.As(err, &pgErr)
		return FilterRule{}, fmt.Errorf("filters: updating rule: %w", err)
	}
	return r, nil
}

// Delete removes a rule by id. ErrRuleNotFound when no row matched.
func (s *Store) Delete(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM url_filter_rules WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("filters: deleting rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

// scanRule decodes a url_filter_rules row from a pgx.Row or pgx.Rows, mapping
// NULL description to "" and NULL created_by to nil. Column order must match the
// SELECT/RETURNING lists above.
func scanRule(row pgx.Row) (FilterRule, error) {
	var r FilterRule
	var description *string
	var createdBy *int64
	if err := row.Scan(&r.ID, &r.Pattern, &r.ReasonCode, &description, &r.Active, &createdBy, &r.CreatedAt); err != nil {
		return FilterRule{}, err
	}
	if description != nil {
		r.Description = *description
	}
	r.CreatedBy = createdBy
	return r, nil
}
