package clicks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL or skips, truncating the clicks/links/
// users tables before and after so each run starts and leaves clean.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`TRUNCATE clicks, links, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// seedUser inserts an active account and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

// seedLink inserts an active, non-denied link and returns its id.
func seedLink(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, TRUE, 0, now()) RETURNING id`,
		userID, key, dest,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

// TestRecorder_PersistsClick records one click and reads it back, asserting the
// link_id, metadata, and all five utm_* values round-trip exactly.
func TestRecorder_PersistsClick(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com/page")

	when := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := Click{
		Key:         "abc123",
		ClickedAt:   when,
		IPAddress:   "203.0.113.7",
		UserAgent:   "test-agent",
		Referer:     "https://referrer.example",
		UTMSource:   "news",
		UTMMedium:   "email",
		UTMCampaign: "launch",
		UTMTerm:     "go",
		UTMContent:  "hero",
	}
	if err := rec.Record(context.Background(), c); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var (
		gotLinkID                                    int64
		gotIP, ua, ref, src, med, camp, term, cont   string
		clickedAt                                    time.Time
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT link_id, clicked_at, host(ip_address), user_agent, referer,
		        utm_source, utm_medium, utm_campaign, utm_term, utm_content
		   FROM clicks WHERE link_id = $1`, linkID,
	).Scan(&gotLinkID, &clickedAt, &gotIP, &ua, &ref, &src, &med, &camp, &term, &cont); err != nil {
		t.Fatalf("read back click: %v", err)
	}

	if gotLinkID != linkID {
		t.Errorf("link_id = %d, want %d", gotLinkID, linkID)
	}
	if !clickedAt.Equal(when) {
		t.Errorf("clicked_at = %v, want %v", clickedAt, when)
	}
	if gotIP != "203.0.113.7" {
		t.Errorf("ip_address = %q, want 203.0.113.7", gotIP)
	}
	if ua != "test-agent" || ref != "https://referrer.example" {
		t.Errorf("metadata mismatch: ua=%q ref=%q", ua, ref)
	}
	if src != "news" || med != "email" || camp != "launch" || term != "go" || cont != "hero" {
		t.Errorf("utm mismatch: src=%q med=%q camp=%q term=%q cont=%q", src, med, camp, term, cont)
	}
}

// TestRecorder_UnknownKeyInsertsNothing asserts that recording a click for a key
// that does not exist inserts zero rows (and does not error), so the redirect
// path's best-effort recording never fails on a bogus key.
func TestRecorder_UnknownKeyInsertsNothing(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	if err := rec.Record(context.Background(), Click{Key: "nope42"}); err != nil {
		t.Fatalf("Record unknown key: %v", err)
	}
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM clicks`).Scan(&n); err != nil {
		t.Fatalf("count clicks: %v", err)
	}
	if n != 0 {
		t.Errorf("clicks rows = %d, want 0 for unknown key", n)
	}
}

// TestRecorder_EmptyUTMStoredAsNull asserts empty-string metadata folds to SQL
// NULL so the analytics "(none)" bucket is driven by genuine absence.
func TestRecorder_EmptyUTMStoredAsNull(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "bob@example.com")
	seedLink(t, pool, uid, "bare01", "https://example.com")

	if err := rec.Record(context.Background(), Click{Key: "bare01"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var srcNull, ipNull bool
	if err := pool.QueryRow(context.Background(),
		`SELECT utm_source IS NULL, ip_address IS NULL FROM clicks WHERE link_id IN
		   (SELECT id FROM links WHERE key = 'bare01')`,
	).Scan(&srcNull, &ipNull); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !srcNull {
		t.Error("utm_source should be NULL when empty")
	}
	if !ipNull {
		t.Error("ip_address should be NULL when empty")
	}
}
