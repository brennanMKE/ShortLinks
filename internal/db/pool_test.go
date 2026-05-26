package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestConnectAndPing exercises the real pool against a live PostgreSQL. It is
// gated on TEST_DATABASE_URL and skipped when unset so the default `go test`
// run needs no database. Point it at the test DSN to run it.
func TestConnectAndPing(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
