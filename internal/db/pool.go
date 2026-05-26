package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a PostgreSQL connection pool for the given DSN and verifies it
// with an initial Ping. The returned *pgxpool.Pool is safe for concurrent use
// and is the shared pool injected into the domain packages (links, clicks,
// auth, etc.); the caller owns it and must call Close on shutdown.
//
// The supplied context bounds both the pool construction and the verifying
// ping, so a caller can apply a startup timeout. A non-nil error means the DSN
// was invalid or the database was unreachable.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parsing DSN: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: creating pool: %w", err)
	}

	// Verify connectivity up front so a bad DSN or unreachable database fails
	// at startup rather than on the first request.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: pinging database: %w", err)
	}

	return pool, nil
}
