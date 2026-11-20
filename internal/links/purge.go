package links

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// purgeBatchLimit caps how many links one sweeper pass purges, so a large
// backlog cannot hold the 30 second sweep window open. The rest wait for the
// next hourly pass.
const purgeBatchLimit = 500

// Purged describes a link that PurgeLink or PurgeExpired removed. It carries
// only non-secret fields, never the destination URL.
type Purged struct {
	Key          string
	Title        string
	CampaignSlug string
	UserID       int64
}

// WithAudit gives the Store the audit logger it writes link.purged entries
// with. A purge writes its audit entry in the same transaction as the delete,
// so the Store needs the logger itself rather than the handler recording it
// afterwards.
func (s *Store) WithAudit(l *audit.Logger) *Store {
	s.auditor = l
	return s
}

// PurgeLink permanently deletes the user's own link (#0183) so a secret in its
// destination URL does not stay in the database. In one transaction it:
//
//   - deletes the link's clicks and then the link row;
//   - records the key in purged_keys so it is never issued again;
//   - redacts destination_url, fallback_url and default_fallback_url from every earlier audit entry targeting the link
//     (link.created, link.reactivated, link.deactivated, link.denied);
//   - deletes the owner's Idempotency-Key records whose stored response names
//     the link (they hold the full create response, destination included);
//   - writes a link.purged audit entry with key, title and campaign, and never
//     the destination.
//
// Scoped to userID: another user's link is ErrLinkNotFound. The handler evicts
// the redirect cache.
func (s *Store) PurgeLink(ctx context.Context, userID int64, key string) (Purged, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Purged{}, fmt.Errorf("links: begin purge tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := s.purgeTx(ctx, tx, userID, key, &userID, "", nil)
	if err != nil {
		return Purged{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Purged{}, fmt.Errorf("links: commit purge tx: %w", err)
	}
	return p, nil
}

// PurgeExpired purges every link whose purge_after_expiry_days has elapsed
// since its expires_at (#0183), as the retention sweeper's task. It returns the
// purged links so the caller can evict their keys from the redirect cache.
// Links with no purge_after_expiry_days are never touched.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) ([]Purged, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, key FROM links
		  WHERE purge_after_expiry_days IS NOT NULL
		    AND expires_at IS NOT NULL
		    AND expires_at + make_interval(days => purge_after_expiry_days) <= $1
		  ORDER BY expires_at
		  LIMIT $2`, now, purgeBatchLimit)
	if err != nil {
		return nil, fmt.Errorf("links: finding expired links to purge: %w", err)
	}
	type target struct {
		userID int64
		key    string
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.userID, &t.key); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var out []Purged
	for _, t := range targets {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return out, fmt.Errorf("links: begin purge tx: %w", err)
		}
		p, err := s.purgeTx(ctx, tx, t.userID, t.key, nil, "retention", &now)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, ErrLinkNotFound) {
				continue // purged concurrently
			}
			return out, err
		}
		if err := tx.Commit(ctx); err != nil {
			return out, fmt.Errorf("links: commit purge tx: %w", err)
		}
		out = append(out, p)
	}
	return out, nil
}

// purgeTx does the work of a purge inside tx. actor is the acting user (nil for
// the sweeper); via, when set, is recorded in the audit metadata.
// dueAt, when non-nil (the sweeper), re-checks inside the locked SELECT that the
// link is still due at that time, so an owner who extended expires_at or cleared
// purge_after_expiry_days after the sweeper listed it keeps the link: no row
// comes back and ErrLinkNotFound is returned (the sweeper skips it).
func (s *Store) purgeTx(ctx context.Context, tx pgx.Tx, userID int64, key string, actor *int64, via string, dueAt *time.Time) (Purged, error) {
	var (
		id         int64
		p          Purged
		title      *string
		campaignID *int64
		slug       *string
	)
	err := tx.QueryRow(ctx,
		`SELECT l.id, l.user_id, l.key, l.title, l.campaign_id, camp.slug
		   FROM links l LEFT JOIN campaigns camp ON camp.id = l.campaign_id
		  WHERE l.user_id = $1 AND l.key = $2
		    AND ($3::timestamptz IS NULL OR (l.purge_after_expiry_days IS NOT NULL AND l.expires_at IS NOT NULL
		         AND l.expires_at + make_interval(days => l.purge_after_expiry_days) <= $3))
		    FOR UPDATE OF l`, userID, key, dueAt,
	).Scan(&id, &p.UserID, &p.Key, &title, &campaignID, &slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Purged{}, ErrLinkNotFound
	}
	if err != nil {
		return Purged{}, fmt.Errorf("links: locking link to purge: %w", err)
	}
	if title != nil {
		p.Title = *title
	}
	if slug != nil {
		p.CampaignSlug = *slug
	}

	steps := []struct {
		what string
		sql  string
		args []any
	}{
		{"deleting clicks", `DELETE FROM clicks WHERE link_id = $1`, []any{id}},
		{"deleting link", `DELETE FROM links WHERE id = $1`, []any{id}},
		{"recording purged key", `INSERT INTO purged_keys (key) VALUES ($1) ON CONFLICT (key) DO NOTHING`, []any{p.Key}},
		// Earlier audit entries keep their shape and timestamps; only the
		// URLs that can carry a secret (destination_url, and the #0180
		// fallback_url / default_fallback_url) are removed, and a flag records
		// that they were.
		{"redacting audit", `UPDATE audit_log
		    SET metadata = (metadata - 'destination_url' - 'fallback_url' - 'default_fallback_url')
		                   || '{"destination_redacted":true}'::jsonb
		  WHERE target_type = 'link' AND target_id = $1
		    AND metadata ?| array['destination_url', 'fallback_url', 'default_fallback_url']`, []any{id}},
		// A stored create response holds the destination URL. Matching the
		// compact JSON `"key":"<key>"` finds single and batch responses.
		{"deleting idempotency records", `DELETE FROM idempotency_keys
		  WHERE user_id = $1 AND response_body IS NOT NULL
		    AND position(convert_to('"key":"' || $2::text || '"', 'UTF8') in response_body) > 0`, []any{p.UserID, p.Key}},
	}
	for _, st := range steps {
		if _, err := tx.Exec(ctx, st.sql, st.args...); err != nil {
			return Purged{}, fmt.Errorf("links: purge %s: %w", st.what, err)
		}
	}

	if s.auditor != nil {
		owner := p.UserID
		meta := map[string]any{"key": p.Key, "title": p.Title}
		if campaignID != nil {
			meta["campaign_id"] = *campaignID
			meta["campaign_slug"] = p.CampaignSlug
		}
		if via != "" {
			meta["via"] = via
		}
		if err := s.auditor.WriteTx(ctx, tx, audit.Entry{
			ActorID:    actor,
			UserID:     &owner,
			Action:     audit.ActionLinkPurged,
			TargetType: audit.TargetLink,
			TargetID:   &id,
			Metadata:   meta,
		}); err != nil {
			return Purged{}, fmt.Errorf("links: purge audit: %w", err)
		}
	}
	return p, nil
}
