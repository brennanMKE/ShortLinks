// Package testdb provides cross-process serialization for integration tests
// that share a single PostgreSQL test database (TEST_DATABASE_URL).
//
// `go test` runs each package's test binary concurrently. Several packages
// truncate the same shared tables in their setup, so concurrent runs corrupt
// each other's data. Each such package calls Lock from its TestMain to hold a
// PostgreSQL session-level advisory lock for the duration of its test run; only
// one package can hold the lock at a time, so they run one-at-a-time even under
// `go test ./...`. When TEST_DATABASE_URL is unset, the DB-backed tests skip
// themselves and Lock is a no-op.
package testdb

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

// advisoryLockKey is an arbitrary fixed key shared by every package that
// serializes through this helper. All callers must use the same value.
const advisoryLockKey int64 = 0x53484F52544C4B // "SHORTLK"

// Lock acquires the shared advisory lock and returns a release function the
// caller must invoke before exiting (typically right before os.Exit). It blocks
// until the lock is free. If TEST_DATABASE_URL is unset, it returns a no-op
// release immediately.
func Lock() func() {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		return func() {}
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		// Surface the problem but don't block the run: the DB-backed tests will
		// fail loudly on their own connection attempts.
		fmt.Fprintf(os.Stderr, "testdb: connect failed: %v\n", err)
		return func() {}
	}

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		fmt.Fprintf(os.Stderr, "testdb: advisory lock failed: %v\n", err)
		_ = conn.Close(ctx)
		return func() {}
	}

	return func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)
		_ = conn.Close(ctx)
	}
}
