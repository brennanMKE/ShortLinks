package links

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// Status filter values for ListOptions.Status (#0144). The three are mutually
// exclusive, decided in this order: expired (expires_at has passed), inactive
// (deactivated or filter-denied), otherwise active.
const (
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusExpired  = "expired"
	StatusAll      = "all"
)

// Sort values for ListOptions.Sort (#0144).
const (
	SortCreated      = "created"  // newest first, the long-standing default
	SortCreatedAsc   = "-created" // oldest first
	SortClicks       = "clicks"   // most all-time clicks first
	SortClicksWindow = "clicks_window"
	SortTitle        = "title" // A-Z, untitled last
)

// ListOptions narrows and orders ListLinksFiltered. The zero value lists every
// link newest first with no window.
type ListOptions struct {
	Limit, Offset int
	// Query is a case-insensitive substring over title, key and destination.
	Query string
	// Filter carries the campaign (slug or "none") and source/medium filters,
	// with the same semantics as the reports' link filters.
	Filter reports.Params
	// Status is one of the Status* values; "" means all.
	Status string
	// ContentKey keeps only links whose reports.ContentKey equals it.
	ContentKey string
	// ExternalID keeps only the link with this external id (#0182).
	ExternalID string
	Sort       string
	// HasWindow adds ClicksWindow to every link: non-bot clicks
	// with clicked_at in [Start, End).
	WindowStart, WindowEnd time.Time
	HasWindow              bool
	// Now decides what has expired; zero means time.Now().
	Now time.Time
}

// EscapeLike escapes %, _ and \ so s matches literally inside a LIKE/ILIKE
// pattern that uses ESCAPE '\'.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ContentKeyHost returns the host part of a content key (everything before the
// first "/" or "?"), the SQL-side prefilter for content_key matching.
func ContentKeyHost(key string) string {
	if i := strings.IndexAny(key, "/?"); i >= 0 {
		return key[:i]
	}
	return key
}

// ListLinksFiltered returns one page of the user's links under opts plus the
// total number matching (ignoring Limit/Offset). Everything is scoped to
// userID and every value is bound as a parameter.
//
// content_key is computed in Go (reports.ContentKey), not stored. SQL narrows
// candidates to destinations that contain the key's host, then Go keeps exact
// matches and pages the survivors, so total and pagination stay exact.
func (s *Store) ListLinksFiltered(ctx context.Context, userID int64, opts ListOptions) ([]Link, int64, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	args := []any{userID}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	conds := []string{"l.user_id = $1"}
	if opts.Query != "" {
		p := arg("%" + EscapeLike(opts.Query) + "%")
		conds = append(conds, fmt.Sprintf(`(l.title ILIKE %[1]s ESCAPE '\' OR l.key ILIKE %[1]s ESCAPE '\' OR l.destination_url ILIKE %[1]s ESCAPE '\')`, p))
	}
	nowP := ""
	if opts.Status == StatusActive || opts.Status == StatusInactive || opts.Status == StatusExpired {
		nowP = arg(now)
	}
	switch opts.Status {
	case StatusActive:
		conds = append(conds, fmt.Sprintf("(COALESCE(l.active, TRUE) AND l.denied_reason = 0 AND (l.expires_at IS NULL OR l.expires_at > %s))", nowP))
	case StatusInactive:
		conds = append(conds, fmt.Sprintf("((NOT COALESCE(l.active, TRUE) OR l.denied_reason <> 0) AND (l.expires_at IS NULL OR l.expires_at > %s))", nowP))
	case StatusExpired:
		conds = append(conds, fmt.Sprintf("(l.expires_at IS NOT NULL AND l.expires_at <= %s)", nowP))
	}
	if opts.ExternalID != "" {
		conds = append(conds, "l.external_id = "+arg(opts.ExternalID))
	}
	if opts.ContentKey != "" {
		conds = append(conds, fmt.Sprintf(`l.destination_url ILIKE %s ESCAPE '\'`, arg("%"+EscapeLike(ContentKeyHost(opts.ContentKey))+"%")))
	}
	filter, fargs := opts.Filter.LinkFilterSQL(len(args) + 1)
	args = append(args, fargs...)
	conds = append(conds, "("+filter+")")

	// condArgs are the parameters the WHERE conditions reference; the window
	// and paging parameters added below are bound only in the page query.
	condArgs := args
	where := strings.Join(conds, " AND ")

	// content_key is computed in Go: select just id and destination for the
	// host-narrowed candidates, keep exact matches, and let the page query
	// fetch only those ids. total is the number of exact matches.
	var contentTotal int64
	if opts.ContentKey != "" {
		crows, err := s.pool.Query(ctx, `SELECT l.id, l.destination_url FROM links l WHERE `+where, condArgs...)
		if err != nil {
			return nil, 0, fmt.Errorf("links: listing content-key candidates: %w", err)
		}
		var ids []int64
		for crows.Next() {
			var id int64
			var dest string
			if err := crows.Scan(&id, &dest); err != nil {
				crows.Close()
				return nil, 0, fmt.Errorf("links: scanning content-key candidate: %w", err)
			}
			if reports.ContentKey(dest) == opts.ContentKey {
				ids = append(ids, id)
			}
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return nil, 0, fmt.Errorf("links: iterating content-key candidates: %w", err)
		}
		contentTotal = int64(len(ids))
		if len(ids) == 0 {
			return []Link{}, 0, nil
		}
		where = "l.id = ANY(" + arg(ids) + "::bigint[]) AND " + where
	}

	windowExpr := "0::bigint"
	if opts.HasWindow {
		windowExpr = fmt.Sprintf("COUNT(c.id) FILTER (WHERE c.clicked_at >= %s AND c.clicked_at < %s)", arg(opts.WindowStart), arg(opts.WindowEnd))
	}

	var order string
	switch opts.Sort {
	case SortCreatedAsc:
		order = "l.created_at, l.id"
	case SortClicks:
		order = "click_count DESC, l.created_at DESC, l.id DESC"
	case SortClicksWindow:
		order = "clicks_window DESC, click_count DESC, l.created_at DESC, l.id DESC"
	case SortTitle:
		order = "(NULLIF(btrim(l.title), '') IS NULL), lower(l.title), l.created_at DESC, l.id DESC"
	default:
		order = "l.created_at DESC, l.id DESC"
	}

	q := `SELECT ` + linkColumns + `,
	        COUNT(c.id) AS click_count, ` + windowExpr + ` AS clicks_window, cp.slug,
	        COUNT(*) OVER () AS total
	   FROM links l
	   LEFT JOIN campaigns cp ON cp.id = l.campaign_id
	   LEFT JOIN clicks c ON c.link_id = l.id AND c.is_bot = FALSE
	  WHERE ` + where + `
	  GROUP BY l.id, cp.slug
	  ORDER BY ` + order + " LIMIT " + arg(opts.Limit) + " OFFSET " + arg(opts.Offset)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("links: listing filtered links: %w", err)
	}
	defer rows.Close()

	out := make([]Link, 0)
	var total int64
	for rows.Next() {
		var l Link
		var title, utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement, externalID, fallbackURL, slug *string
		var expiresAt *time.Time
		if err := rows.Scan(
			&l.ID, &l.UserID, &l.Key, &l.DestinationURL, &title,
			&l.Active, &l.DeniedReason, &l.CreatedAt, &expiresAt,
			&l.CampaignID, &utmSource, &utmMedium, &utmCampaign, &utmTerm, &utmContent, &placement, &externalID, &fallbackURL,
			&l.PurgeAfterExpiryDays, &l.ClickCount, &l.ClicksWindow, &slug, &total,
		); err != nil {
			return nil, 0, fmt.Errorf("links: scanning filtered link: %w", err)
		}
		applyNullableLinkFields(&l, title, expiresAt, utmSource, utmMedium, utmCampaign, utmTerm, utmContent, placement)
		if fallbackURL != nil {
			l.FallbackURL = *fallbackURL
		}
		if externalID != nil {
			l.ExternalID = *externalID
		}
		if slug != nil {
			l.CampaignSlug = *slug
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("links: iterating filtered links: %w", err)
	}
	if opts.ContentKey != "" {
		return out, contentTotal, nil
	}
	// A page past the end has no rows to carry the window total; count.
	if len(out) == 0 && opts.Offset > 0 {
		if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM links l WHERE `+where, condArgs...).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("links: counting filtered links: %w", err)
		}
	}
	return out, total, nil
}

// Page returns the offset/limit window of in (empty, never nil, past the end).
func Page[T any](in []T, limit, offset int) []T {
	if offset >= len(in) {
		return []T{}
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}
