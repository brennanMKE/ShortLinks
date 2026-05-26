package links

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrLinkNotFound is returned by the store when no link matches the lookup for
// the given owner. Ownership is part of the lookup, so a link that exists but
// belongs to another user is reported as not found — the handler maps this to a
// 404 and never reveals that the key exists. This keeps one user's links opaque
// to another.
var ErrLinkNotFound = errors.New("links: link not found")

// ErrKeyTaken is returned by CreateLink when a user-supplied custom alias is
// already in use by any link (across all users — the key column is globally
// UNIQUE). Custom aliases are never deduplicated, so a clash is a hard conflict
// the handler maps to 409. Generated keys never surface this error because they
// are checked for collision before the insert via GenerateUniqueKey.
var ErrKeyTaken = errors.New("links: key already taken")

// pgUniqueViolation is the PostgreSQL SQLSTATE for a unique_violation, returned
// when an INSERT collides with a UNIQUE constraint (here, links.key). It lets
// CreateLink distinguish a custom-alias clash from any other DB failure.
const pgUniqueViolation = "23505"

// querier is the subset of pgx the store uses. *pgxpool.Pool and pgx.Tx both
// satisfy it, mirroring auth.Store so link data access can run on the pool
// directly or inside a transaction once #0023's reactivation/dedup flow needs
// one.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is the data-access layer for links: create, list, fetch, update, and
// deactivate. Every method is scoped to an owning user id so a request can only
// ever touch its own links; ownership is enforced in SQL (the WHERE clause),
// not in the handler, so it cannot be bypassed.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store over the shared connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool for callers that need transaction control
// beyond the Store's methods (e.g. #0023's atomic dedup-or-insert).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Link is the full domain representation of a links row plus its aggregated
// click count. It is the shape returned by every store read and is mapped 1:1
// to the API JSON by the handler.
type Link struct {
	ID             int64
	UserID         int64
	Key            string
	DestinationURL string
	Title          string // empty when the column is NULL
	Active         bool
	DeniedReason   int16
	CreatedAt      time.Time
	ExpiresAt      *time.Time // nil = never expires
	ClickCount     int64
}

// KeyExists reports whether any link already uses the given key. The key column
// is globally UNIQUE, so this check is not user-scoped. It backs the closure
// passed to GenerateUniqueKey when minting a generated key.
func (s *Store) KeyExists(ctx context.Context, key string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM links WHERE key = $1)`, key,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("links: checking key exists: %w", err)
	}
	return exists, nil
}

// NewLink carries the validated input for creating a link. The handler fills it
// after validating destination_url and resolving the key (generated or custom);
// the store performs only the insert.
type NewLink struct {
	UserID         int64
	Key            string
	DestinationURL string
	Title          string     // "" stored as SQL NULL
	ExpiresAt      *time.Time // nil = never expires
}

// CreateLink inserts a new active, non-denied link and returns the full row
// (ClickCount is 0 for a freshly created link). The caller is responsible for
// having resolved a unique key: a generated key is pre-checked via
// GenerateUniqueKey, and a user-supplied custom alias is attempted directly. If
// the insert collides with the UNIQUE(key) constraint — only possible for a
// custom alias that was taken between the caller's check and the insert, or one
// the caller did not pre-check — ErrKeyTaken is returned so the handler can
// answer 409.
//
// SEAMS for the layered issues, in their required order, all live in the
// handler's create path (POST /api/links), not here — this method is the final
// "insert a brand-new link" step only:
//   - #0024 URL filter check runs FIRST (before this insert); on a match it
//     inserts a denied link instead and returns 422.
//   - #0023 dedup check runs after the filter (before this insert); on an
//     existing/inactive match it returns/reactivates rather than inserting.
//   - #0025 audit (link.created) and #0026 SSE (link.created broadcast) run
//     AFTER a successful create/reactivate.
func (s *Store) CreateLink(ctx context.Context, in NewLink) (Link, error) {
	link := Link{
		UserID:         in.UserID,
		Key:            in.Key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         true,
		DeniedReason:   0,
	}
	var title *string
	if in.Title != "" {
		title = &in.Title
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, $4, $5, TRUE, 0, now())
		 RETURNING id, created_at, expires_at`,
		in.UserID, in.Key, in.DestinationURL, title, in.ExpiresAt,
	).Scan(&link.ID, &link.CreatedAt, &link.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return Link{}, ErrKeyTaken
		}
		return Link{}, fmt.Errorf("links: inserting link: %w", err)
	}
	return link, nil
}

// ListLinks returns one page of the user's links, most recent first (created_at
// DESC, id DESC as a stable tiebreaker), each carrying its aggregated click
// count. The result is strictly scoped to userID. limit and offset implement
// pagination; the handler derives them from ?page=/?per_page=. A LEFT JOIN
// aggregate yields click_count in the same query so the list does not issue one
// COUNT per row.
func (s *Store) ListLinks(ctx context.Context, userID int64, limit, offset int) ([]Link, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT l.id, l.user_id, l.key, l.destination_url, l.title,
		        l.active, l.denied_reason, l.created_at, l.expires_at,
		        COUNT(c.id) AS click_count
		   FROM links l
		   LEFT JOIN clicks c ON c.link_id = l.id
		  WHERE l.user_id = $1
		  GROUP BY l.id
		  ORDER BY l.created_at DESC, l.id DESC
		  LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("links: listing links: %w", err)
	}
	defer rows.Close()

	out := make([]Link, 0)
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("links: iterating link rows: %w", err)
	}
	return out, nil
}

// CountLinks returns the total number of links owned by userID. It backs the
// pagination metadata (total/page count) so the client can render a pager
// without scanning every page.
func (s *Store) CountLinks(ctx context.Context, userID int64) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM links WHERE user_id = $1`, userID,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("links: counting links: %w", err)
	}
	return n, nil
}

// GetLink returns a single link by key, scoped to userID, with its click count.
// ErrLinkNotFound is returned when the key does not exist OR belongs to another
// user — the two are deliberately indistinguishable so the detail endpoint never
// leaks the existence of another user's link.
func (s *Store) GetLink(ctx context.Context, userID int64, key string) (Link, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT l.id, l.user_id, l.key, l.destination_url, l.title,
		        l.active, l.denied_reason, l.created_at, l.expires_at,
		        (SELECT COUNT(*) FROM clicks c WHERE c.link_id = l.id) AS click_count
		   FROM links l
		  WHERE l.user_id = $1 AND l.key = $2`,
		userID, key,
	)
	link, err := scanLink(row)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Link{}, ErrLinkNotFound
	case err != nil:
		return Link{}, err
	}
	return link, nil
}

// LinkUpdate carries the optional fields a PATCH may change. A nil pointer means
// "leave unchanged"; a non-nil pointer sets the field (Title to "" clears it).
// The handler builds this from the request body, validating DestinationURL when
// present before calling the store.
type LinkUpdate struct {
	Title          *string
	DestinationURL *string
	ExpiresAt      **time.Time // nil = unchanged; non-nil = set to *ExpiresAt (which may itself be nil to clear)
}

// UpdateLink applies a partial update to the user's own link and returns the
// updated row with its click count. Only the provided fields change; the update
// is scoped to userID so another user's link does not match (ErrLinkNotFound →
// 404). A COALESCE-style approach is avoided in favor of a dynamic SET so that
// clearing title/expires_at to NULL is possible and distinguishable from "leave
// unchanged".
func (s *Store) UpdateLink(ctx context.Context, userID int64, key string, upd LinkUpdate) (Link, error) {
	setClauses := make([]string, 0, 3)
	args := make([]any, 0, 5)
	// $1 and $2 are reserved for the WHERE clause (userID, key); SET params start
	// at $3.
	args = append(args, userID, key)
	next := 3

	if upd.Title != nil {
		setClauses = append(setClauses, fmt.Sprintf("title = $%d", next))
		if *upd.Title == "" {
			args = append(args, nil)
		} else {
			args = append(args, *upd.Title)
		}
		next++
	}
	if upd.DestinationURL != nil {
		setClauses = append(setClauses, fmt.Sprintf("destination_url = $%d", next))
		args = append(args, *upd.DestinationURL)
		next++
	}
	if upd.ExpiresAt != nil {
		setClauses = append(setClauses, fmt.Sprintf("expires_at = $%d", next))
		args = append(args, *upd.ExpiresAt) // *upd.ExpiresAt is a *time.Time; nil clears the column
		next++
	}

	// Nothing to change: behave as a no-op fetch so the caller still gets the
	// current row (and a proper 404 if it is not theirs).
	if len(setClauses) == 0 {
		return s.GetLink(ctx, userID, key)
	}

	setSQL := setClauses[0]
	for _, c := range setClauses[1:] {
		setSQL += ", " + c
	}
	sql := fmt.Sprintf(
		`UPDATE links SET %s
		  WHERE user_id = $1 AND key = $2
		 RETURNING id`, setSQL)

	var id int64
	err := s.pool.QueryRow(ctx, sql, args...).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Link{}, ErrLinkNotFound
	case err != nil:
		return Link{}, fmt.Errorf("links: updating link: %w", err)
	}
	// Re-read so the response carries the authoritative row plus click count.
	return s.GetLink(ctx, userID, key)
}

// DeactivateLink soft-deletes the user's own link by setting active = false. It
// is the data-layer side of DELETE /api/links/{key}: the row is retained (for
// audit/analytics), only its active flag flips. Scoped to userID, so another
// user's link does not match (ErrLinkNotFound → 404). Already-inactive links
// flip harmlessly to false again, so the operation is idempotent.
//
// Redirect-cache eviction for the freed key is the HANDLER's responsibility
// (the cache lives outside this package); see the LinksHandler delete path.
func (s *Store) DeactivateLink(ctx context.Context, userID int64, key string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE links SET active = FALSE WHERE user_id = $1 AND key = $2`,
		userID, key,
	)
	if err != nil {
		return fmt.Errorf("links: deactivating link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// scanLink decodes a links row (joined with its click count) from a pgx.Row or
// pgx.Rows, mapping NULL title to "" and NULL expires_at to nil. The column
// order must match the SELECT lists above.
func scanLink(row pgx.Row) (Link, error) {
	var l Link
	var title *string
	var expiresAt *time.Time
	if err := row.Scan(
		&l.ID, &l.UserID, &l.Key, &l.DestinationURL, &title,
		&l.Active, &l.DeniedReason, &l.CreatedAt, &expiresAt, &l.ClickCount,
	); err != nil {
		return Link{}, err
	}
	if title != nil {
		l.Title = *title
	}
	l.ExpiresAt = expiresAt
	return l, nil
}
