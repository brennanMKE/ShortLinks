package clicks

import (
	"context"
	"log/slog"
	"net"
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
	// X-Forwarded-For / RemoteAddr by the handler). Empty stores SQL NULL.
	IPAddress string
	UserAgent string
	Referer   string

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
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewRecorder constructs a Recorder over the shared connection pool. If log is
// nil the default slog logger is used.
func NewRecorder(pool *pgxpool.Pool, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{pool: pool, log: log}
}

// Record inserts one click row for the link identified by c.Key. It resolves the
// key to link_id in the INSERT itself (a scalar subquery), so an unknown/deleted
// key inserts zero rows rather than erroring. Empty string metadata is stored as
// SQL NULL (so the analytics "(none)" bucket is driven by genuine absence, not
// empty strings). It returns an error for callers (and tests) that want to assert
// the write; the fire-and-forget redirect path should use RecordClick instead.
func (r *Recorder) Record(ctx context.Context, c Click) error {
	clickedAt := c.ClickedAt
	if clickedAt.IsZero() {
		clickedAt = time.Now()
	}

	_, err := r.pool.Exec(ctx,
		`INSERT INTO clicks
		     (link_id, clicked_at, ip_address, user_agent, referer,
		      utm_source, utm_medium, utm_campaign, utm_term, utm_content)
		 SELECT l.id, $2, $3, $4, $5, $6, $7, $8, $9, $10
		   FROM links l
		  WHERE l.key = $1`,
		c.Key,
		clickedAt,
		nullableIP(c.IPAddress),
		nullStr(c.UserAgent),
		nullStr(c.Referer),
		nullStr(c.UTMSource),
		nullStr(c.UTMMedium),
		nullStr(c.UTMCampaign),
		nullStr(c.UTMTerm),
		nullStr(c.UTMContent),
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

// nullableIP returns the IP for storage in the INET column, or nil (SQL NULL)
// when the value is empty or not a parseable IP. An unparseable value is dropped
// rather than failing the whole INSERT, since the IP is incidental metadata.
func nullableIP(s string) any {
	if s == "" {
		return nil
	}
	if net.ParseIP(s) == nil {
		return nil
	}
	return s
}
