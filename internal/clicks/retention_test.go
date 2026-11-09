package clicks

import (
	"context"
	"testing"
	"time"
)

func TestSweeper_StripsOnlyOldDetail(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "sweep1", "https://example.com/")

	now := time.Now()
	for _, age := range []time.Duration{91 * 24 * time.Hour, 24 * time.Hour} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO clicks (link_id, clicked_at, ip_hash, user_agent, referer, utm_source, is_bot)
			 VALUES ($1, $2, '\x01'::bytea, 'ua', 'https://ref.example', 'news', TRUE)`,
			linkID, now.Add(-age)); err != nil {
			t.Fatalf("seed click: %v", err)
		}
	}

	s := NewSweeper(pool, nil)
	n, err := s.Sweep(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v; want 1, nil", n, err)
	}

	type row struct {
		hashNull, uaNull, refKept, srcKept, botKept bool
	}
	read := func(old bool) row {
		var r row
		cmp := ">"
		if old {
			cmp = "<"
		}
		if err := pool.QueryRow(ctx,
			`SELECT ip_hash IS NULL, user_agent IS NULL, referer = 'https://ref.example',
			        utm_source = 'news', is_bot
			   FROM clicks WHERE clicked_at `+cmp+` $1`, now.Add(-30*24*time.Hour),
		).Scan(&r.hashNull, &r.uaNull, &r.refKept, &r.srcKept, &r.botKept); err != nil {
			t.Fatalf("read row: %v", err)
		}
		return r
	}
	if r := read(true); !r.hashNull || !r.uaNull || !r.refKept || !r.srcKept || !r.botKept {
		t.Errorf("old row not stripped correctly: %+v", r)
	}
	if r := read(false); r.hashNull || r.uaNull {
		t.Errorf("recent row should be untouched: %+v", r)
	}

	var total int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM clicks`).Scan(&total); err != nil || total != 2 {
		t.Errorf("COUNT(*) = %d, %v; want 2", total, err)
	}
	if n, err := s.Sweep(ctx, now); err != nil || n != 0 {
		t.Errorf("second Sweep = %d, %v; want 0, nil", n, err)
	}
}
