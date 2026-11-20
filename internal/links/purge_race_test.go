package links

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestPurgeTx_SweeperRechecksDue covers the race where the sweeper lists a link
// as due and the owner then extends expires_at (or clears
// purge_after_expiry_days) before the purge runs: the locked SELECT re-checks
// the condition, so the link survives (#0183 review).
func TestPurgeTx_SweeperRechecksDue(t *testing.T) {
	pool := resolverTestPool(t)
	owner := seedUserL(t, pool)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `TRUNCATE purged_keys`) })
	s := NewStore(pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, expires_at, purge_after_expiry_days)
		 VALUES ($1,'race','https://example.com/s?t=SECRET',TRUE,0,now(), now() - interval '3 days', 1)`, owner); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if got, err := s.PurgeExpired(ctx, now.Add(-72*time.Hour)); err != nil || len(got) != 0 {
		t.Fatalf("not yet due: purged %v, err %v", got, err)
	}

	try := func(due time.Time) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := s.purgeTx(ctx, tx, owner, "race", nil, "retention", &due); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	exists := func() bool {
		var n int
		_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM links WHERE key='race'`).Scan(&n)
		return n == 1
	}

	// The sweeper decided at `now`; the owner extends the expiry before the purge.
	if _, err := pool.Exec(ctx, `UPDATE links SET expires_at = now() + interval '30 days' WHERE key='race'`); err != nil {
		t.Fatal(err)
	}
	if err := try(now); !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("extended expiry: err = %v, want ErrLinkNotFound", err)
	}
	if !exists() {
		t.Fatal("link purged although the owner extended expires_at")
	}
	// Clearing purge_after_expiry_days also protects it.
	if _, err := pool.Exec(ctx, `UPDATE links SET expires_at = now() - interval '3 days', purge_after_expiry_days = NULL WHERE key='race'`); err != nil {
		t.Fatal(err)
	}
	if err := try(now); !errors.Is(err, ErrLinkNotFound) || !exists() {
		t.Errorf("cleared setting: err = %v, exists = %v", err, exists())
	}
	// Still due: it is purged.
	if _, err := pool.Exec(ctx, `UPDATE links SET purge_after_expiry_days = 1 WHERE key='race'`); err != nil {
		t.Fatal(err)
	}
	if err := try(now); err != nil || exists() {
		t.Errorf("due link: err = %v, exists = %v, want purged", err, exists())
	}
}
