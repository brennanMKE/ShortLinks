package clicks

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A registered prune func runs on every pass; when it fails the error is logged
// and the click sweep still runs.
func TestSweeper_RunsRegisteredPrune(t *testing.T) {
	pool := testPool(t)
	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "prune1", "https://example.com/")
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, clicked_at, ip_hash, user_agent)
		 VALUES ($1, now() - interval '91 days', '\x01'::bytea, 'ua')`, linkID); err != nil {
		t.Fatalf("seed click: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	calls := 0
	s := NewSweeper(pool, log).WithPrune("prune things", func(context.Context, time.Time) (int64, error) {
		calls++
		return 0, errors.New("boom")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var stripped bool
		_ = pool.QueryRow(context.Background(),
			`SELECT ip_hash IS NULL FROM clicks WHERE link_id = $1`, linkID).Scan(&stripped)
		if stripped && calls > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	if calls == 0 {
		t.Fatal("prune func did not run")
	}
	var stripped bool
	if err := pool.QueryRow(context.Background(),
		`SELECT ip_hash IS NULL FROM clicks WHERE link_id = $1`, linkID).Scan(&stripped); err != nil || !stripped {
		t.Fatalf("click sweep did not run after prune error: %v stripped=%v", err, stripped)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("prune error not logged: %q", buf.String())
	}
}
