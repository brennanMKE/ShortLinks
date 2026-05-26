package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/links"
)

// seedDestination is the destination URL used for the bootstrap test link.
const seedDestination = "https://www.wikipedia.org"

// seedLinkPrefix is the canonical short-URL prefix printed for the seeded link.
// It is the production namespace from the PRD (`https://go.sstools.co/u/{key}`)
// and is intentionally fixed rather than derived from BASE_URL so the seed
// output is stable regardless of the local development host.
const seedLinkPrefix = "https://go.sstools.co/u/"

// seed bootstraps a fresh install: it ensures the admin user (from ADMIN_EMAIL)
// exists and that the admin owns a test link pointing at Wikipedia. Both steps
// are idempotent, so `shortlinks seed` is safe to re-run without creating
// duplicate rows.
func seed() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	email := strings.ToLower(strings.TrimSpace(cfg.AdminEmail))
	if email == "" {
		return errors.New("seed: ADMIN_EMAIL is empty")
	}

	adminID, err := ensureAdminUser(ctx, pool, email)
	if err != nil {
		return err
	}

	key, err := ensureTestLink(ctx, pool, adminID)
	if err != nil {
		return err
	}

	fmt.Printf("Seed admin: %s (id=%d)\n", email, adminID)
	fmt.Printf("Seed link: %s%s -> %s\n", seedLinkPrefix, key, seedDestination)
	return nil
}

// ensureAdminUser inserts the admin user if it does not already exist and
// returns its id. The insert uses ON CONFLICT (email) DO NOTHING for
// idempotency; when a row already exists it is reused and (re)promoted to an
// active admin so re-running seed converges on the intended state.
func ensureAdminUser(ctx context.Context, pool *pgxpool.Pool, email string) (int64, error) {
	// Insert-or-ignore. RETURNING yields no row when the conflict fires, so the
	// id is fetched with a follow-up SELECT below.
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, TRUE, TRUE, now())
		 ON CONFLICT (email) DO NOTHING`,
		email,
	); err != nil {
		return 0, fmt.Errorf("seed: inserting admin user: %w", err)
	}

	// Ensure an existing user is an active admin, then read back the id. This
	// also covers the case where the row pre-existed as a non-admin.
	var id int64
	if err := pool.QueryRow(ctx,
		`UPDATE users SET is_admin = TRUE, active = TRUE
		 WHERE email = $1
		 RETURNING id`,
		email,
	).Scan(&id); err != nil {
		return 0, fmt.Errorf("seed: loading admin user: %w", err)
	}

	return id, nil
}

// ensureTestLink ensures the admin owns a non-denied link to seedDestination
// and returns its key. If such a link already exists it is reused; otherwise a
// unique key is generated and a new link inserted. The dedup check mirrors the
// idx_links_user_destination partial index (denied_reason = 0).
func ensureTestLink(ctx context.Context, pool *pgxpool.Pool, adminID int64) (string, error) {
	// Reuse an existing non-denied link to the same destination if present.
	var key string
	err := pool.QueryRow(ctx,
		`SELECT key FROM links
		 WHERE user_id = $1 AND destination_url = $2 AND denied_reason = 0
		 ORDER BY id
		 LIMIT 1`,
		adminID, seedDestination,
	).Scan(&key)
	switch {
	case err == nil:
		return key, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("seed: checking for existing test link: %w", err)
	}

	// No existing link; generate a unique key backed by a DB existence check.
	key, err = links.GenerateUniqueKey(func(candidate string) (bool, error) {
		var exists bool
		if qErr := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM links WHERE key = $1)`,
			candidate,
		).Scan(&exists); qErr != nil {
			return false, qErr
		}
		return exists, nil
	})
	if err != nil {
		return "", fmt.Errorf("seed: generating link key: %w", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, TRUE, 0, now())`,
		adminID, key, seedDestination,
	); err != nil {
		return "", fmt.Errorf("seed: inserting test link: %w", err)
	}

	return key, nil
}
