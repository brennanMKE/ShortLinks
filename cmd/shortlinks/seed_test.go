package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/db"
)

// TestSeedIdempotent exercises the seed helpers against a live PostgreSQL. It is
// gated on TEST_DATABASE_URL and skipped when unset so the default `go test`
// run needs no database. It runs the ensure* helpers twice and confirms no
// duplicate admin or link rows are created on the second run.
func TestSeedIdempotent(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	const email = "seed-idempotency-test@example.com"

	// Clean up any residue from a previous run, and again at the end.
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM links WHERE user_id IN (SELECT id FROM users WHERE email = $1)`, email)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, email)
	}
	cleanup()
	defer cleanup()

	// First run.
	id1, err := ensureAdminUser(ctx, pool, email)
	if err != nil {
		t.Fatalf("ensureAdminUser (run 1): %v", err)
	}
	key1, err := ensureTestLink(ctx, pool, id1)
	if err != nil {
		t.Fatalf("ensureTestLink (run 1): %v", err)
	}

	// Second run must not duplicate or error and must reuse the same rows.
	id2, err := ensureAdminUser(ctx, pool, email)
	if err != nil {
		t.Fatalf("ensureAdminUser (run 2): %v", err)
	}
	key2, err := ensureTestLink(ctx, pool, id2)
	if err != nil {
		t.Fatalf("ensureTestLink (run 2): %v", err)
	}

	if id1 != id2 {
		t.Fatalf("admin id changed between runs: %d != %d", id1, id2)
	}
	if key1 != key2 {
		t.Fatalf("link key changed between runs: %q != %q", key1, key2)
	}

	var userCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = $1`, email).Scan(&userCount); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if userCount != 1 {
		t.Fatalf("expected exactly 1 admin user, got %d", userCount)
	}

	var linkCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM links WHERE user_id = $1 AND destination_url = $2`,
		id1, seedDestination,
	).Scan(&linkCount); err != nil {
		t.Fatalf("counting links: %v", err)
	}
	if linkCount != 1 {
		t.Fatalf("expected exactly 1 test link, got %d", linkCount)
	}
}
