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

// linkColumns is the SELECT list (in scanLink's expected order) shared by
// every query that returns a base Link row: the original nine columns plus
// the #0099 campaign_id/utm_*/placement columns. Click count is appended by
// each caller separately (a correlated subquery in lockExisting/GetLink, a
// LEFT JOIN aggregate in ListLinks/ListLinksForCampaign), so it is not part
// of this shared constant.
const linkColumns = `l.id, l.user_id, l.key, l.destination_url, l.title,
	        l.active, l.denied_reason, l.created_at, l.expires_at,
	        l.campaign_id, l.utm_source, l.utm_medium, l.utm_campaign, l.utm_term, l.utm_content, l.placement`

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
//
// CampaignID/UTM*/Placement are the #0099 discrete columns: additive
// alongside DestinationURL, which keeps carrying the composed (baked) URL.
// CampaignName/CampaignSlug are populated ONLY by GetLink's LEFT JOIN onto
// campaigns (empty "" elsewhere — ListLinks/ListLinksForCampaign/Create* do
// not join campaigns, since the issue only requires the campaign's name/slug
// on the single-link detail response).
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
	CampaignID     *int64 // nil = not assigned to a campaign
	UTMSource      string // empty when the column is NULL
	UTMMedium      string
	UTMCampaign    string
	UTMTerm        string
	UTMContent     string
	Placement      string
	CampaignName   string // populated only by GetLink; "" elsewhere
	CampaignSlug   string // populated only by GetLink; "" elsewhere
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
	// CampaignID assigns the link to a campaign at create time; nil = no
	// campaign. The caller (handler) is responsible for having verified the
	// campaign belongs to UserID before setting this — the store does not
	// re-check ownership here since the INSERT has no existing row to scope a
	// WHERE clause against (unlike UPDATE/DELETE elsewhere in this package).
	CampaignID *int64
	// UTMSource..UTMContent are the #0099 discrete UTM columns, populated by
	// the handler from the SAME request fields the composed DestinationURL
	// was built from — the two are expected to agree (see
	// TestCreateLink_DiscreteUTMColumnsMatchBakedURL), but the store does not
	// verify that itself; it stores exactly what it is given.
	UTMSource   string // "" stored as SQL NULL
	UTMMedium   string
	UTMCampaign string
	UTMTerm     string
	UTMContent  string
	Placement   string
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
	link := newLinkFromInput(in)
	var title *string
	if in.Title != "" {
		title = &in.Title
	}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at,
		                     campaign_id, utm_source, utm_medium, utm_campaign, utm_term, utm_content, placement)
		 VALUES ($1, $2, $3, $4, $5, TRUE, 0, now(), $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at, expires_at`,
		in.UserID, in.Key, in.DestinationURL, title, in.ExpiresAt,
		in.CampaignID, nullIfEmpty(in.UTMSource), nullIfEmpty(in.UTMMedium), nullIfEmpty(in.UTMCampaign),
		nullIfEmpty(in.UTMTerm), nullIfEmpty(in.UTMContent), nullIfEmpty(in.Placement),
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

// newLinkFromInput builds the in-memory Link that mirrors a fresh INSERT's
// column values (everything except the DB-generated id/created_at, and
// expires_at which the RETURNING clause echoes back identically). Shared by
// CreateLink, CreateDeniedLink, and CreateOrReactivateLink's insert branch so
// the three insert paths cannot drift on which NewLink fields get copied.
func newLinkFromInput(in NewLink) Link {
	return Link{
		UserID:         in.UserID,
		Key:            in.Key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         true,
		DeniedReason:   0,
		CampaignID:     in.CampaignID,
		UTMSource:      in.UTMSource,
		UTMMedium:      in.UTMMedium,
		UTMCampaign:    in.UTMCampaign,
		UTMTerm:        in.UTMTerm,
		UTMContent:     in.UTMContent,
		Placement:      in.Placement,
	}
}

// nullIfEmpty maps an empty string to nil so an unset nullable TEXT column
// stores SQL NULL rather than an empty string.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateDeniedLink inserts a denied link row (active=false, denied_reason=code)
// and returns the full row. It is the data-layer side of the #0024 URL-filter
// check: when a destination URL matches an active filter rule the handler
// records the denied attempt here BEFORE the dedup path runs, so the admin audit
// log accrues one row per blocked submission (the PRD's intentional per-attempt
// count). A generated, unique key is minted via genKey so the denied row does
// not collide with the global UNIQUE(key) constraint; reasonCode must be a
// non-zero denial code.
//
// Denied links never participate in deduplication (the dedup index is partial on
// denied_reason = 0), so each call inserts a fresh row — re-submitting a blocked
// URL is re-evaluated against the current rules rather than reactivated.
func (s *Store) CreateDeniedLink(ctx context.Context, in NewLink, reasonCode int16, genKey func(exists func(key string) (bool, error)) (string, error)) (Link, error) {
	key, err := genKey(func(candidate string) (bool, error) {
		return s.KeyExists(ctx, candidate)
	})
	if err != nil {
		return Link{}, err
	}

	link := newLinkFromInput(in)
	link.Key = key
	link.Active = false
	link.DeniedReason = reasonCode
	var title *string
	if in.Title != "" {
		title = &in.Title
	}
	err = s.pool.QueryRow(ctx,
		`INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at,
		                     campaign_id, utm_source, utm_medium, utm_campaign, utm_term, utm_content, placement)
		 VALUES ($1, $2, $3, $4, $5, FALSE, $6, now(), $7, $8, $9, $10, $11, $12, $13)
		 RETURNING id, created_at, expires_at`,
		in.UserID, key, in.DestinationURL, title, in.ExpiresAt, reasonCode,
		in.CampaignID, nullIfEmpty(in.UTMSource), nullIfEmpty(in.UTMMedium), nullIfEmpty(in.UTMCampaign),
		nullIfEmpty(in.UTMTerm), nullIfEmpty(in.UTMContent), nullIfEmpty(in.Placement),
	).Scan(&link.ID, &link.CreatedAt, &link.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return Link{}, ErrKeyTaken
		}
		return Link{}, fmt.Errorf("links: inserting denied link: %w", err)
	}
	return link, nil
}

// CreateOutcome describes which branch CreateOrReactivateLink took, so the
// handler can set the response's "duplicate" flag and decide whether to fire the
// post-create seams (audit/SSE). The three outcomes mirror the PRD's
// deduplication table.
type CreateOutcome int

const (
	// OutcomeInserted means no matching non-denied link existed, so a brand-new
	// link was inserted. duplicate=false; fires the SSE/audit (link.created) seams.
	OutcomeInserted CreateOutcome = iota
	// OutcomeActiveDuplicate means an active non-denied link for (user,dest)
	// already existed; it was returned unchanged with NO write. duplicate=true;
	// does NOT fire SSE (per PRD), only the link.created-with-{duplicate:true}
	// audit seam.
	OutcomeActiveDuplicate
	// OutcomeReactivated means an inactive non-denied link for (user,dest) existed
	// and was reactivated (active=true). duplicate=true; fires SSE (link.created)
	// and the link.reactivated audit seam.
	OutcomeReactivated
)

// CreateOrReactivateLink implements per-user URL deduplication for the
// GENERATED-KEY create path (#0023). It runs the dedup decision and any write
// inside a single transaction so two concurrent creates of the same URL cannot
// both insert a duplicate.
//
// Within the transaction it looks up an existing non-denied link for
// (userID, destinationURL) — the (user_id, destination_url) WHERE denied_reason=0
// index backs this:
//
//   - active match found  → OutcomeActiveDuplicate (see #0099 note below).
//   - inactive match found → reactivated (active=true), OutcomeReactivated
//     (same #0099 note applies).
//   - no match            → genKey mints a unique key (collisions checked inside
//     the tx) and a new active link is inserted, OutcomeInserted.
//
// This is ONLY for generated keys. Custom aliases bypass dedup entirely and the
// handler calls CreateLink directly. The returned Link always carries a full,
// authoritative row (ClickCount is 0 for a fresh insert; the dedup/reactivate
// branches re-read the row's click count is left at the persisted aggregate via
// the RETURNING-then-count path below).
//
// #0099 DECISION (review item 4): on a duplicate/reactivate match, this
// method used to return the existing row completely unchanged — silently
// discarding any campaign_id/utm_*/placement the CALLER of this specific
// request supplied. That was a bad experience: a user who picks a campaign
// on the create form and happens to hit an existing URL would see the
// campaign silently not applied. Instead, applyRequestedMetadataTx
// FORWARD-MERGES: a field the request actually supplied (non-nil
// CampaignID, non-blank UTM/placement) is written onto the matched row; a
// field the request left blank is NEVER cleared here. This is deliberately
// NOT a full overwrite — a bare `POST {"destination_url": "..."}`
// re-submission (no campaign, no UTM) must not wipe out a campaign
// assignment or UTM values an earlier create already set on that same row.
// See TestLinksCreate_DedupForwardMergesCampaignAndUTMOnActiveDuplicate,
// TestLinksCreate_DedupDoesNotClearExistingMetadataOnBareResubmit, and
// TestLinksCreate_DedupForwardMergesOnReactivate in internal/handlers.
//
// CORRECTION (a later review pass caught the original version of this
// comment overclaiming): forward-merge does NOT close #0105's batch-creation
// trap. Two batch rows differing only in `placement` still compose to the
// IDENTICAL destination_url (placement is never baked into the URL), so the
// dedup lookup above still matches them to the same row — row 2 does not
// become a second link. What forward-merge changes is what happens to that
// one row: before, row 2's placement was silently ignored; now, row 2's
// placement silently OVERWRITES row 1's. That is arguably a worse outcome
// for a genuine two-row batch, though still the right behavior for a
// genuine duplicate submission of the same link. #0105 owns the actual fix
// (giving batch rows distinguishable destination_urls, or a dedup key that
// is not destination_url alone); this method does not attempt it.
func (s *Store) CreateOrReactivateLink(ctx context.Context, in NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) (Link, CreateOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Link{}, 0, fmt.Errorf("links: begin dedup tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit; this guards every error path.
	defer func() { _ = tx.Rollback(ctx) }()

	// Dedup lookup: an existing non-denied link for this user + destination. The
	// row is locked FOR UPDATE so a concurrent create of the same URL serializes
	// behind this one rather than racing to a second insert.
	existing, err := s.lockExisting(ctx, tx, in.UserID, in.DestinationURL)
	switch {
	case err == nil:
		// A match exists. Active → forward-merge requested metadata, return;
		// inactive → reactivate, then forward-merge.
		if existing.Active {
			existing, err = applyRequestedMetadataTx(ctx, tx, existing, in)
			if err != nil {
				return Link{}, 0, err
			}
			if err := tx.Commit(ctx); err != nil {
				return Link{}, 0, fmt.Errorf("links: commit dedup tx: %w", err)
			}
			return existing, OutcomeActiveDuplicate, nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE links SET active = TRUE WHERE id = $1`, existing.ID,
		); err != nil {
			return Link{}, 0, fmt.Errorf("links: reactivating link: %w", err)
		}
		existing.Active = true
		existing, err = applyRequestedMetadataTx(ctx, tx, existing, in)
		if err != nil {
			return Link{}, 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Link{}, 0, fmt.Errorf("links: commit dedup tx: %w", err)
		}
		return existing, OutcomeReactivated, nil
	case errors.Is(err, pgx.ErrNoRows):
		// No match: fall through to the insert below.
	default:
		return Link{}, 0, fmt.Errorf("links: dedup lookup: %w", err)
	}

	// No existing link — mint a unique key and insert, all inside the tx so the
	// key-collision check and the insert are consistent.
	key, err := genKey(func(candidate string) (bool, error) {
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM links WHERE key = $1)`, candidate,
		).Scan(&exists); err != nil {
			return false, fmt.Errorf("links: checking key exists in tx: %w", err)
		}
		return exists, nil
	})
	if err != nil {
		return Link{}, 0, err
	}

	link := newLinkFromInput(in)
	link.Key = key
	var title *string
	if in.Title != "" {
		title = &in.Title
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at,
		                     campaign_id, utm_source, utm_medium, utm_campaign, utm_term, utm_content, placement)
		 VALUES ($1, $2, $3, $4, $5, TRUE, 0, now(), $6, $7, $8, $9, $10, $11, $12)
		 RETURNING id, created_at, expires_at`,
		in.UserID, key, in.DestinationURL, title, in.ExpiresAt,
		in.CampaignID, nullIfEmpty(in.UTMSource), nullIfEmpty(in.UTMMedium), nullIfEmpty(in.UTMCampaign),
		nullIfEmpty(in.UTMTerm), nullIfEmpty(in.UTMContent), nullIfEmpty(in.Placement),
	).Scan(&link.ID, &link.CreatedAt, &link.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return Link{}, 0, ErrKeyTaken
		}
		return Link{}, 0, fmt.Errorf("links: inserting link: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Link{}, 0, fmt.Errorf("links: commit dedup tx: %w", err)
	}
	return link, OutcomeInserted, nil
}

// CreateLinksBatch inserts N new links in one transaction and returns them in
// the same order as rows, for #0105's campaign batch-create endpoint (one
// destination URL, a row per channel).
//
// #0105 DECISION — DEDUP IS BYPASSED ENTIRELY, not extended or worked around.
// CreateOrReactivateLink's dedup lookup keys SOLELY on (user_id,
// destination_url), and placement is deliberately NOT baked into
// destination_url (#0099's data-model note) — so two batch rows differing
// only in placement compose to a byte-identical destination_url and, run
// through CreateOrReactivateLink, collapse onto ONE row (see that method's
// doc comment and #0099's Gotchas for the full history: forward-merge only
// changed "row 2 ignored" into "row 2 overwrites row 1", it did not fix it).
// The issue names three options: bypass dedup for the batch endpoint, extend
// the dedup key beyond destination_url, or bake something per-row into the
// URL. This method takes the FIRST: it never calls lockExisting and never
// looks up an existing row by destination_url at all — every row in a batch
// unconditionally inserts, even if its destination_url is byte-identical to
// another row's (or to an existing link's). This is the simplest option that
// is unconditionally correct for the case the issue is centrally about
// (TestCreateLinksBatch_TwoRowsDifferingOnlyInPlacementCreateTwoDistinctLinks
// pins it), and it does not require redefining what "duplicate" means for
// single-create's dedup path, which #0099 already shipped and this issue does
// not reopen. The handler (internal/handlers/campaigns.go BatchCreateLinks)
// is responsible for its OWN, separate notion of "duplicate row within this
// batch" (identical source+medium+content+placement) — a client-side mistake
// this method has no way to distinguish from two legitimately identical
// links, so rejecting that is a validation decision, not a storage one.
//
// THE STRONGER CASE FOR BYPASSING RATHER THAN EXTENDING THE DEDUP KEY: inside
// one batch, an exact duplicate row (same source+medium+content+placement) is
// already rejected before this method is ever called — see BatchCreateLinks'
// duplicateKey check. That means an extended dedup key (e.g. destination_url
// + placement) could only ever match a PRE-EXISTING link from an earlier
// request, never another row in the SAME batch. And matching a pre-existing
// row means forward-merging onto it (applyRequestedMetadataTx's behavior),
// which can silently move that link out of whatever campaign it already
// belonged to and last-write-wins its UTM/placement metadata — a surprising
// side effect for what the user experiences as "create a new batch of
// links," not "maybe edit some old ones too." Bypassing dedup entirely avoids
// that failure mode altogether, not just the placement-collision one.
//
// DOUBLE-SUBMIT IS NOT DEDUPLICATED EITHER, and for the same underlying
// reason (no dedup lookup runs at all): submitting an identical N-row batch
// twice creates 2N links, not N. This is a CONTRACT CHANGE from single
// create, where CreateOrReactivateLink reliably folds a repeat submission of
// the same destination_url onto the existing row. See
// internal/handlers/campaigns.go's BatchCreateLinks doc comment for the
// full double-submit rationale (the UI mitigates the common paths; the
// residual lost-response-then-retry window is an accepted trade).
//
// ATOMICITY — the whole batch is one transaction: if any row's INSERT fails,
// every row inserted earlier IN THIS CALL rolls back and the method returns
// an error with no partial state. This is a deliberate "all or nothing"
// choice (see the issue's acceptance criteria: "either atomic, or partial
// success reported per row" — atomic is simpler to reason about for a batch
// the user submits as one semantic action, "these are the channels for one
// promotion", and avoids the #0099 assign-endpoint's documented non-atomic
// surprise ("earlier keys stayed assigned after a later key failed") for a
// CREATE path, where a half-created batch is harder for the user to clean up
// than a half-assigned one (unassigning is one click; deleting stray links
// is several). The caller (handler) is expected to validate every row BEFORE
// calling this method (URL syntax, filter-rule denial, in-batch duplicates)
// so a failure inside the transaction is the rare case (e.g. a genuine key
// exhaustion or a DB error), not the common path.
//
// KEY COLLISIONS ACROSS THE BATCH — each row's key is minted by calling
// genKey (normally links.GenerateUniqueKey) with an `exists` closure that
// queries THIS transaction, not the pool. Because Postgres transactions see
// their own uncommitted writes, row 2's existence check sees row 1's
// already-inserted (but not yet committed) key, so two rows landing on the
// same randomly generated key retry correctly within one transaction — this
// is the same pattern CreateOrReactivateLink's insert branch already uses,
// just called once per row instead of once per request. See
// TestCreateLinksBatch_KeyCollisionRetriesWithinSameTransaction, which forces
// exactly this collision with a scripted genKey and would fail (ErrKeyTaken
// from the second row's INSERT) if the exists closure queried s.pool instead
// of tx.
//
// campaign_id/UTM/placement are taken directly from each row's NewLink,
// exactly like CreateLink — no forward-merge logic applies here since there
// is no existing row to merge onto.
func (s *Store) CreateLinksBatch(ctx context.Context, rows []NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) ([]Link, error) {
	if len(rows) == 0 {
		return []Link{}, nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("links: begin batch tx: %w", err)
	}
	// Rollback is a no-op after a successful Commit; guards every error path,
	// including a failure partway through the loop below (nothing committed
	// earlier in THIS call survives — see the atomicity note above).
	defer func() { _ = tx.Rollback(ctx) }()

	out := make([]Link, 0, len(rows))
	for _, in := range rows {
		key, err := genKey(func(candidate string) (bool, error) {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM links WHERE key = $1)`, candidate,
			).Scan(&exists); err != nil {
				return false, fmt.Errorf("links: checking key exists in batch tx: %w", err)
			}
			return exists, nil
		})
		if err != nil {
			return nil, err
		}

		link := newLinkFromInput(in)
		link.Key = key
		var title *string
		if in.Title != "" {
			title = &in.Title
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO links (user_id, key, destination_url, title, expires_at, active, denied_reason, created_at,
			                     campaign_id, utm_source, utm_medium, utm_campaign, utm_term, utm_content, placement)
			 VALUES ($1, $2, $3, $4, $5, TRUE, 0, now(), $6, $7, $8, $9, $10, $11, $12)
			 RETURNING id, created_at, expires_at`,
			in.UserID, key, in.DestinationURL, title, in.ExpiresAt,
			in.CampaignID, nullIfEmpty(in.UTMSource), nullIfEmpty(in.UTMMedium), nullIfEmpty(in.UTMCampaign),
			nullIfEmpty(in.UTMTerm), nullIfEmpty(in.UTMContent), nullIfEmpty(in.Placement),
		).Scan(&link.ID, &link.CreatedAt, &link.ExpiresAt)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
				return nil, ErrKeyTaken
			}
			return nil, fmt.Errorf("links: inserting batch link: %w", err)
		}
		out = append(out, link)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("links: commit batch tx: %w", err)
	}
	return out, nil
}

// applyRequestedMetadataTx forward-merges in's campaign_id/utm_*/placement
// onto existing (an already row-locked match from lockExisting), inside the
// caller's transaction. Only fields the request actually supplied are
// written: a non-nil in.CampaignID, and each UTM/Placement field that is
// non-empty after trimming (the handler has already trimmed these before
// building NewLink). A blank field is left completely alone — this is a
// forward merge, not an overwrite, so a bare re-submission of an existing
// URL can never silently clear a campaign assignment or UTM values a
// previous create already set. See CreateOrReactivateLink's doc comment for
// the full #0099 review rationale (item 4). Returns existing with its
// in-memory fields updated to match what was written, or an error if the
// UPDATE itself fails; when no field qualifies, it is a no-op and existing
// is returned unchanged.
//
// The UPDATE re-asserts `user_id = existing.UserID` in its own WHERE clause,
// symmetric with every other ownership-scoped write in this package. This id
// cannot actually be attacker-controlled today (existing came from
// lockExisting's own user_id-scoped, row-locked lookup earlier in the same
// transaction, so it is already known to belong to the caller), but a
// defense-in-depth check should not be the one write in the package missing
// its own re-assertion (review item 3) — cheap insurance against this
// function someday being called from a path where that invariant no longer
// holds by construction.
func applyRequestedMetadataTx(ctx context.Context, tx pgx.Tx, existing Link, in NewLink) (Link, error) {
	setClauses := make([]string, 0, 6)
	// $1 and $2 are reserved for the WHERE clause (id, user_id); SET params
	// start at $3. existing.UserID is already known-correct: it came from
	// lockExisting's own user_id-scoped, FOR UPDATE lookup earlier in this
	// same transaction, so re-asserting it here costs nothing and is not
	// reachable via any untrusted input — but every other write in this
	// package re-asserts ownership in its own WHERE clause (see
	// campaigns.Store.AssignLinkToCampaign's symmetric EXISTS check added in
	// the same review round), and this was the one exception.
	args := make([]any, 0, 8)
	args = append(args, existing.ID, existing.UserID)
	next := 3

	if in.CampaignID != nil {
		setClauses = append(setClauses, fmt.Sprintf("campaign_id = $%d", next))
		args = append(args, *in.CampaignID)
		existing.CampaignID = in.CampaignID
		next++
	}
	addForward := func(col, value string, dst *string) {
		if value == "" {
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = $%d", col, next))
		args = append(args, value)
		*dst = value
		next++
	}
	addForward("utm_source", in.UTMSource, &existing.UTMSource)
	addForward("utm_medium", in.UTMMedium, &existing.UTMMedium)
	addForward("utm_campaign", in.UTMCampaign, &existing.UTMCampaign)
	addForward("utm_term", in.UTMTerm, &existing.UTMTerm)
	addForward("utm_content", in.UTMContent, &existing.UTMContent)
	addForward("placement", in.Placement, &existing.Placement)

	if len(setClauses) == 0 {
		return existing, nil
	}
	setSQL := setClauses[0]
	for _, c := range setClauses[1:] {
		setSQL += ", " + c
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE links SET %s WHERE id = $1 AND user_id = $2`, setSQL), args...); err != nil {
		return Link{}, fmt.Errorf("links: forward-merging requested metadata onto matched link: %w", err)
	}
	return existing, nil
}

// lockExisting fetches and row-locks (FOR UPDATE) the user's existing non-denied
// link for destinationURL, with its aggregated click count. ErrNoRows means no
// such link exists. It runs inside the dedup transaction so the reactivate or
// no-op decision is made against a row no concurrent create can change.
//
// click_count excludes is_bot = TRUE clicks (#0101), matching
// clicks.UTMStatsForLink's ClickCount so a link never carries two
// contradictory totals across the API surface.
func (s *Store) lockExisting(ctx context.Context, q querier, userID int64, destinationURL string) (Link, error) {
	row := q.QueryRow(ctx,
		`SELECT `+linkColumns+`,
		        (SELECT COUNT(*) FROM clicks c WHERE c.link_id = l.id AND c.is_bot = FALSE) AS click_count
		   FROM links l
		  WHERE l.user_id = $1 AND l.destination_url = $2 AND l.denied_reason = 0
		  LIMIT 1
		  FOR UPDATE`,
		userID, destinationURL,
	)
	return scanLink(row)
}

// ListLinks returns one page of the user's links, most recent first (created_at
// DESC, id DESC as a stable tiebreaker), each carrying its aggregated click
// count. The result is strictly scoped to userID. limit and offset implement
// pagination; the handler derives them from ?page=/?per_page=. A LEFT JOIN
// aggregate yields click_count in the same query so the list does not issue one
// COUNT per row.
//
// click_count excludes is_bot = TRUE clicks (#0101), matching
// clicks.UTMStatsForLink's ClickCount. The is_bot = FALSE predicate lives in
// the JOIN's ON clause, not a WHERE clause: WHERE would run AFTER the LEFT
// JOIN and drop the link's row entirely whenever every one of its clicks is
// bot traffic (the join would produce only bot rows, WHERE would filter all
// of them out, and GROUP BY l.id would have nothing left to group — the link
// silently vanishes from its own owner's list). Filtering in ON instead
// keeps the LEFT JOIN's guarantee that every link appears at least once (as
// a row with c.id = NULL when no clicks pass the predicate), so COUNT(c.id)
// correctly yields 0 for a link whose only clicks are bot clicks, rather
// than the link disappearing.
func (s *Store) ListLinks(ctx context.Context, userID int64, limit, offset int) ([]Link, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+linkColumns+`,
		        COUNT(c.id) AS click_count
		   FROM links l
		   LEFT JOIN clicks c ON c.link_id = l.id AND c.is_bot = FALSE
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

// ListLinksForCampaign returns every link assigned to campaignID, scoped to
// userID (so a campaign id belonging to another user — which should never
// reach here since the handler resolves campaignID via
// campaigns.Store.GetCampaignBySlug/GetCampaignByID, both already
// user-scoped — still yields nothing rather than leaking another user's
// links). Ordered most-recently-created first, mirroring ListLinks. Backs
// GET /api/campaigns/{slug}/links (#0099); no pagination, matching the
// issue's scope (campaign link counts are expected to be small — bulk
// listing is #0105).
// click_count excludes is_bot = TRUE clicks (#0101); see ListLinks for why
// the exclusion is in the ON clause rather than WHERE (a WHERE-clause
// predicate would silently drop a link from the campaign's own list whenever
// every one of its clicks is bot traffic).
func (s *Store) ListLinksForCampaign(ctx context.Context, userID, campaignID int64) ([]Link, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+linkColumns+`,
		        COUNT(c.id) AS click_count
		   FROM links l
		   LEFT JOIN clicks c ON c.link_id = l.id AND c.is_bot = FALSE
		  WHERE l.user_id = $1 AND l.campaign_id = $2
		  GROUP BY l.id
		  ORDER BY l.created_at DESC, l.id DESC`,
		userID, campaignID,
	)
	if err != nil {
		return nil, fmt.Errorf("links: listing campaign links: %w", err)
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
		return nil, fmt.Errorf("links: iterating campaign link rows: %w", err)
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

// GetLink returns a single link by key, scoped to userID, with its click
// count and (#0099) its campaign's name/slug when it is assigned to one — a
// LEFT JOIN onto campaigns so an unassigned link (campaign_id NULL) still
// returns the link with CampaignName/CampaignSlug left at their zero value
// rather than failing the query. ErrLinkNotFound is returned when the key
// does not exist OR belongs to another user — the two are deliberately
// indistinguishable so the detail endpoint never leaks the existence of
// another user's link.
// click_count excludes is_bot = TRUE clicks (#0101), matching the same
// link's utm_stats.click_count and timeseries in the same API response —
// prior to this, the two disagreed whenever a link had any bot traffic.
func (s *Store) GetLink(ctx context.Context, userID int64, key string) (Link, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+linkColumns+`,
		        (SELECT COUNT(*) FROM clicks c WHERE c.link_id = l.id AND c.is_bot = FALSE) AS click_count,
		        camp.name, camp.slug
		   FROM links l
		   LEFT JOIN campaigns camp ON camp.id = l.campaign_id
		  WHERE l.user_id = $1 AND l.key = $2`,
		userID, key,
	)
	link, err := scanLinkWithCampaign(row)
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
//
// UTMSource..Placement (#0099) follow the same nil="unchanged",
// non-nil-pointing-at-""="clear to NULL" convention as Title, so the edit
// form can update the discrete columns in lockstep with a changed
// DestinationURL (the composed URL and the discrete columns must keep
// agreeing after an edit, not just at create time). Campaign membership is
// deliberately NOT patchable here — it only changes via the dedicated
// assign/unassign endpoints (campaigns.Store.AssignLinkToCampaign/
// UnassignLinkFromCampaign), which is the single sanctioned mutation path
// and the one that carries the campaign.link_assigned/unassigned audit
// trail; folding it into this generic PATCH would create a second,
// unaudited way to change the same column.
type LinkUpdate struct {
	Title          *string
	DestinationURL *string
	ExpiresAt      **time.Time // nil = unchanged; non-nil = set to *ExpiresAt (which may itself be nil to clear)
	UTMSource      *string
	UTMMedium      *string
	UTMCampaign    *string
	UTMTerm        *string
	UTMContent     *string
	Placement      *string
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

	// addNullableString mirrors Title's convention above: a non-nil pointer to
	// "" clears the nullable column to SQL NULL.
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
	addNullableString("utm_source", upd.UTMSource)
	addNullableString("utm_medium", upd.UTMMedium)
	addNullableString("utm_campaign", upd.UTMCampaign)
	addNullableString("utm_term", upd.UTMTerm)
	addNullableString("utm_content", upd.UTMContent)
	addNullableString("placement", upd.Placement)

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

// scanLink decodes a links row selected via linkColumns, joined with its
// click count, from a pgx.Row or pgx.Rows, mapping NULL title/utm_*/placement
// to "" and NULL expires_at/campaign_id to nil. The column order must match
// linkColumns followed by click_count.
func scanLink(row pgx.Row) (Link, error) {
	var l Link
	var title *string
	var expiresAt *time.Time
	var utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement *string
	if err := row.Scan(
		&l.ID, &l.UserID, &l.Key, &l.DestinationURL, &title,
		&l.Active, &l.DeniedReason, &l.CreatedAt, &expiresAt,
		&l.CampaignID, &utmSource, &utmMedium, &utmCampaign, &utmTerm, &utmContent, &placement,
		&l.ClickCount,
	); err != nil {
		return Link{}, err
	}
	applyNullableLinkFields(&l, title, expiresAt, utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement)
	return l, nil
}

// scanLinkWithCampaign decodes a links row selected via linkColumns joined
// with click_count AND a LEFT JOIN campaigns' name/slug (GetLink only). NULL
// campaign name/slug (link unassigned, or campaign_id NULL) map to "".
func scanLinkWithCampaign(row pgx.Row) (Link, error) {
	var l Link
	var title *string
	var expiresAt *time.Time
	var utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement *string
	var campaignName, campaignSlug *string
	if err := row.Scan(
		&l.ID, &l.UserID, &l.Key, &l.DestinationURL, &title,
		&l.Active, &l.DeniedReason, &l.CreatedAt, &expiresAt,
		&l.CampaignID, &utmSource, &utmMedium, &utmCampaign, &utmTerm, &utmContent, &placement,
		&l.ClickCount, &campaignName, &campaignSlug,
	); err != nil {
		return Link{}, err
	}
	applyNullableLinkFields(&l, title, expiresAt, utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement)
	if campaignName != nil {
		l.CampaignName = *campaignName
	}
	if campaignSlug != nil {
		l.CampaignSlug = *campaignSlug
	}
	return l, nil
}

// applyNullableLinkFields maps the nullable-pointer scan targets shared by
// scanLink/scanLinkWithCampaign onto l's zero-value-when-NULL string fields
// (and ExpiresAt, which stays a pointer). Factored out so the two scan
// functions cannot drift on which columns get this treatment.
func applyNullableLinkFields(l *Link, title *string, expiresAt *time.Time, utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement *string) {
	if title != nil {
		l.Title = *title
	}
	l.ExpiresAt = expiresAt
	if utmSource != nil {
		l.UTMSource = *utmSource
	}
	if utmMedium != nil {
		l.UTMMedium = *utmMedium
	}
	if utmCampaign != nil {
		l.UTMCampaign = *utmCampaign
	}
	if utmTerm != nil {
		l.UTMTerm = *utmTerm
	}
	if utmContent != nil {
		l.UTMContent = *utmContent
	}
	if placement != nil {
		l.Placement = *placement
	}
}
