package clicks

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DetailRetention is how long a click keeps its visitor detail (ip_hash and
	// user_agent). It is the published privacy promise, so it is not configurable.
	DetailRetention = 90 * 24 * time.Hour

	sweepInterval = time.Hour
	sweepTimeout  = 30 * time.Second
)

// Sweeper strips visitor detail from old clicks (#0121). Rows are kept as
// anonymous events so every count, chart, and breakdown stays correct.
type Sweeper struct {
	pool      *pgxpool.Pool
	log       *slog.Logger
	retention time.Duration
}

// NewSweeper constructs a Sweeper using DetailRetention. A nil log uses the default.
func NewSweeper(pool *pgxpool.Pool, log *slog.Logger) *Sweeper {
	if log == nil {
		log = slog.Default()
	}
	return &Sweeper{pool: pool, log: log, retention: DetailRetention}
}

// Sweep nulls ip_hash and user_agent on clicks older than the retention window
// relative to now and returns how many rows changed. referer, utm_* and is_bot
// are kept (stats dimensions; is_bot is classified at record time).
func (s *Sweeper) Sweep(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE clicks SET ip_hash = NULL, user_agent = NULL
		  WHERE clicked_at < $1
		    AND (ip_hash IS NOT NULL OR user_agent IS NOT NULL)`,
		now.Add(-s.retention))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Run sweeps once immediately, then hourly until ctx is cancelled. Failures are
// logged, never fatal.
func (s *Sweeper) Run(ctx context.Context) {
	pass := func() {
		c, cancel := context.WithTimeout(ctx, sweepTimeout)
		defer cancel()
		n, err := s.Sweep(c, time.Now())
		if err != nil {
			s.log.Error("clicks: retention sweep failed", slog.Any("error", err))
			return
		}
		if n > 0 {
			s.log.Info("clicks: stripped visitor detail from old clicks", slog.Int64("rows", n))
		}
	}
	pass()
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		}
	}
}
