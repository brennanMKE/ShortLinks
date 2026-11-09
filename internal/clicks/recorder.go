package clicks

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// recordTimeout bounds how long a single best-effort click INSERT may run. The
// redirect handler invokes RecordClick from a detached goroutine after the
// response is written, so the request context is gone; this caps the work so a
// slow/stuck DB cannot leak goroutines indefinitely.
const recordTimeout = 5 * time.Second

// Click is the data the recorder persists for a single redirect. It mirrors the
// handler's ClickInfo (key + request metadata + the five utm_* values) but is
// declared here so internal/clicks does not depend on internal/handlers — the
// handler adapts its ClickInfo to this shape at the call site.
type Click struct {
	// Key is the short-link key that was resolved. The recorder maps it to the
	// link's id in SQL, so a click is never attributed to the wrong link and an
	// unknown key simply records nothing.
	Key string
	// ClickedAt is when the redirect happened; the handler snapshots it from the
	// request. Zero means "use now()".
	ClickedAt time.Time
	// IPAddress is the originating client IP (already extracted from
	// X-Forwarded-For / RemoteAddr by the handler). It is hashed by the Recorder
	// before storage and never persisted raw (#0121). Empty or unparseable
	// stores SQL NULL.
	IPAddress string
	UserAgent string
	Referer   string

	// UTMSource..UTMContent are the INBOUND (short-URL query string) UTM
	// values. As of #0100 each is only the request's contribution to the
	// stored value, not the final value: Record falls back, per key, to the
	// resolved link's own stored discrete UTM column (#0099) whenever the
	// inbound value here is "" (absent). campaign_id is NOT a field on Click
	// at all — it is resolved from the link inside Record's SQL, never
	// supplied by the caller, since it must reflect the link's campaign at
	// record time regardless of what the request carried.
	UTMSource   string
	UTMMedium   string
	UTMCampaign string
	UTMTerm     string
	UTMContent  string
}

// Recorder persists click events to the clicks table. It is safe for concurrent
// use (the pgx pool is) and is designed to be called from the redirect handler's
// detached goroutine: recording is best-effort and never propagates an error to
// the caller, so a DB hiccup can never break a redirect.
type Recorder struct {
	pool   *pgxpool.Pool
	hasher *IPHasher
	log    *slog.Logger
}

// NewRecorder constructs a Recorder over the shared connection pool. hasher
// hashes visitor IPs before storage (#0121); a nil hasher stores NULL, never the
// raw IP. If log is nil the default slog logger is used.
func NewRecorder(pool *pgxpool.Pool, hasher *IPHasher, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{pool: pool, hasher: hasher, log: log}
}

// Record inserts one click row for the link identified by c.Key. It resolves the
// key to link_id (and, #0100, campaign_id) in the INSERT itself (a scalar
// subquery), so an unknown/deleted key inserts zero rows rather than erroring.
//
// campaign_id is denormalized onto the click row from the link's CURRENT
// campaign_id at record time, not joined at read time. This is deliberate: a
// link can be reassigned to a different campaign or unassigned later, and a
// historical click must stay attributed to whatever campaign was running when
// it happened. Resolving it here means a later reassignment can never rewrite
// that history.
//
// Each utm_* value resolves independently, per key, with the inbound value
// (c.UTM*) always winning when present:
//
//	COALESCE(NULLIF($n, ''), l.utm_source)
//
// NULLIF folds an inbound empty string to SQL NULL first (so an empty string is
// treated as ABSENT, never as an override with ""), then COALESCE falls back to
// the link's own stored discrete UTM column (#0099) when the inbound value is
// absent. This is a per-key fallback, not all-or-nothing: a request can supply
// utm_source while leaving utm_medium blank, and each resolves on its own —
// utm_source takes the inbound value, utm_medium falls back to the link's
// stored value (or NULL if the link has none either).
//
// Non-UTM metadata (user_agent/referer) has no fallback and keeps the
// pre-#0100 empty-string-to-NULL behavior via nullStr below. The visitor IP is
// never stored: it is replaced by ip_hash, a keyed daily HMAC (#0121, see
// IPHasher), computed with the salt for the click's own UTC day.
//
// is_bot (#0101) is classified here, in Go, from c.UserAgent via IsBot before
// the INSERT ever runs — not in SQL — so the substring list stays a plain,
// unit-testable Go value (botdetect.go) with no SQL round-trip required to
// exercise it. A click is ALWAYS written regardless of the classification: a
// bot click is flagged (is_bot = TRUE), never dropped, so the raw data stays
// inspectable even though stats queries exclude it by default.
//
// It returns an error for callers (and tests) that want to assert the write;
// the fire-and-forget redirect path should use RecordClick instead.
func (r *Recorder) Record(ctx context.Context, c Click) error {
	clickedAt := c.ClickedAt
	if clickedAt.IsZero() {
		clickedAt = time.Now()
	}

	_, err := r.pool.Exec(ctx,
		`INSERT INTO clicks
		     (link_id, clicked_at, ip_hash, user_agent, referer,
		      utm_source, utm_medium, utm_campaign, utm_term, utm_content,
		      campaign_id, is_bot)
		 SELECT l.id, $2, $3, $4, $5,
		        COALESCE(NULLIF($6, ''), l.utm_source),
		        COALESCE(NULLIF($7, ''), l.utm_medium),
		        COALESCE(NULLIF($8, ''), l.utm_campaign),
		        COALESCE(NULLIF($9, ''), l.utm_term),
		        COALESCE(NULLIF($10, ''), l.utm_content),
		        l.campaign_id, $11
		   FROM links l
		  WHERE l.key = $1`,
		c.Key,
		clickedAt,
		r.hashIP(c.IPAddress, clickedAt),
		nullStr(c.UserAgent),
		nullStr(c.Referer),
		c.UTMSource,
		c.UTMMedium,
		c.UTMCampaign,
		c.UTMTerm,
		c.UTMContent,
		IsBot(c.UserAgent),
	)
	return err
}

// RecordClick is the best-effort, fire-and-forget entry point used on the
// redirect hot path. It runs the INSERT under a bounded background context and
// logs (never returns) any failure, so click recording can never break or block
// a redirect. The handler already calls this from its own goroutine.
func (r *Recorder) RecordClick(c Click) {
	ctx, cancel := context.WithTimeout(context.Background(), recordTimeout)
	defer cancel()
	if err := r.Record(ctx, c); err != nil {
		r.log.Error("clicks: recording click failed",
			slog.String("key", c.Key),
			slog.Any("error", err))
	}
}

// nullStr maps an empty string to a nil any so pgx stores SQL NULL rather than
// an empty string. This keeps the UTM analytics "(none)" bucket meaning "no
// value" consistently.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// hashIP returns the daily keyed hash of ip for storage, or nil (SQL NULL) when
// there is no hasher or the value is empty/unparseable. It fails closed: the raw
// IP is never returned.
func (r *Recorder) hashIP(ip string, at time.Time) any {
	if r.hasher == nil {
		return nil
	}
	h := r.hasher.HashAt(ip, at)
	if h == nil {
		return nil
	}
	return h
}
