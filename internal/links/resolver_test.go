package links

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/cache"
)

func resolverTestPool(t *testing.T) *pgxpool.Pool {
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
	clean := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, `TRUNCATE clicks, links, users RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	clean()
	t.Cleanup(func() {
		clean()
		pool.Close()
	})
	return pool
}

func seedUserL(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ('owner@example.com', FALSE, TRUE, now()) RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

// TestResolver_DBMissCachesNegative asserts an absent key resolves to not-found
// and that the resolver records a negative cache entry so a second lookup is
// served from cache.
func TestResolver_DBMissCachesNegative(t *testing.T) {
	pool := resolverTestPool(t)
	c, err := cache.New(100, time.Minute)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	defer c.Close()
	r := NewResolver(c, NewStore(pool))

	_, found, err := r.Resolve(context.Background(), "ghost1")
	if err != nil {
		t.Fatalf("Resolve miss: %v", err)
	}
	if found {
		t.Fatal("found = true, want false for absent key")
	}
	c.Wait()
	entry, ok := c.Get("ghost1")
	if !ok || entry == nil || !entry.Negative {
		t.Errorf("expected a negative cache entry, got ok=%v entry=%+v", ok, entry)
	}
}

// TestResolver_DBHitPopulatesCache asserts a key present in the DB resolves with
// the right fields and is cached as a positive entry.
func TestResolver_DBHitPopulatesCache(t *testing.T) {
	pool := resolverTestPool(t)
	c, err := cache.New(100, time.Minute)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	defer c.Close()
	store := NewStore(pool)
	r := NewResolver(c, store)

	uid := seedUserL(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, 'live01', 'https://example.com/x', TRUE, 0, now())`, uid,
	); err != nil {
		t.Fatalf("insert link: %v", err)
	}

	link, found, err := r.Resolve(context.Background(), "live01")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true")
	}
	if link.DestinationURL != "https://example.com/x" || !link.Active || link.Negative {
		t.Errorf("resolved link = %+v", link)
	}
	c.Wait()
	if entry, ok := c.Get("live01"); !ok || entry == nil || entry.Negative {
		t.Errorf("expected positive cache entry, got ok=%v entry=%+v", ok, entry)
	}
}

// TestResolver_CacheHitShortCircuitsDB asserts a pre-populated cache entry is
// returned without touching the DB (the resolver returns the cached value even
// though the store has no such key).
func TestResolver_CacheHitShortCircuitsDB(t *testing.T) {
	pool := resolverTestPool(t)
	c, err := cache.New(100, time.Minute)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	defer c.Close()
	r := NewResolver(c, NewStore(pool))

	c.Set("cached", &cache.CachedLink{DestinationURL: "https://cached.example", Active: true})
	c.Wait()

	link, found, err := r.Resolve(context.Background(), "cached")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !found || link.DestinationURL != "https://cached.example" {
		t.Errorf("cache hit not returned: found=%v link=%+v", found, link)
	}
}
