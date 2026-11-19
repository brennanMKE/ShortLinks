// Package siteassets stores admin-uploaded site images (#0176) in Postgres.
// The only asset today is the public home page hero image, name "hero".
package siteassets

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HeroName is the site_assets row name of the home page hero image.
const HeroName = "hero"

// ErrNotFound is returned when the asset does not exist.
var ErrNotFound = errors.New("siteassets: not found")

// Asset is one stored image. Bytes is empty when only metadata was read.
type Asset struct {
	ContentType string
	Bytes       []byte
	Width       int
	Height      int
	SHA256      string // lowercase hex of Bytes
	Alt         string
	UpdatedAt   time.Time
}

// Store is the Postgres-backed asset store.
type Store struct{ pool *pgxpool.Pool }

// NewStore constructs a Store over the shared pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// HeroMeta reads the hero without its bytes.
func (s *Store) HeroMeta(ctx context.Context) (Asset, error) {
	var a Asset
	err := s.pool.QueryRow(ctx,
		`SELECT content_type, width, height, sha256, alt, updated_at
		   FROM site_assets WHERE name = $1`, HeroName,
	).Scan(&a.ContentType, &a.Width, &a.Height, &a.SHA256, &a.Alt, &a.UpdatedAt)
	return a, mapErr(err)
}

// Hero reads the hero including its bytes.
func (s *Store) Hero(ctx context.Context) (Asset, error) {
	var a Asset
	err := s.pool.QueryRow(ctx,
		`SELECT content_type, bytes, width, height, sha256, alt, updated_at
		   FROM site_assets WHERE name = $1`, HeroName,
	).Scan(&a.ContentType, &a.Bytes, &a.Width, &a.Height, &a.SHA256, &a.Alt, &a.UpdatedAt)
	return a, mapErr(err)
}

// PutHero inserts or replaces the hero and returns the previous sha256, or ""
// when there was none.
func (s *Store) PutHero(ctx context.Context, a Asset) (oldSHA string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("siteassets: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qerr := tx.QueryRow(ctx,
		`SELECT sha256 FROM site_assets WHERE name = $1 FOR UPDATE`, HeroName).Scan(&oldSHA)
	if qerr != nil && !errors.Is(qerr, pgx.ErrNoRows) {
		return "", fmt.Errorf("siteassets: reading previous hero: %w", qerr)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO site_assets (name, content_type, bytes, width, height, sha256, alt, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		 ON CONFLICT (name) DO UPDATE SET
		   content_type = EXCLUDED.content_type, bytes = EXCLUDED.bytes,
		   width = EXCLUDED.width, height = EXCLUDED.height,
		   sha256 = EXCLUDED.sha256, alt = EXCLUDED.alt, updated_at = EXCLUDED.updated_at`,
		HeroName, a.ContentType, a.Bytes, a.Width, a.Height, a.SHA256, a.Alt); err != nil {
		return "", fmt.Errorf("siteassets: writing hero: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("siteassets: commit: %w", err)
	}
	return oldSHA, nil
}

// DeleteHero removes the hero and returns its metadata, or ErrNotFound.
func (s *Store) DeleteHero(ctx context.Context) (Asset, error) {
	var a Asset
	err := s.pool.QueryRow(ctx,
		`DELETE FROM site_assets WHERE name = $1
		 RETURNING content_type, width, height, sha256, alt, updated_at`, HeroName,
	).Scan(&a.ContentType, &a.Width, &a.Height, &a.SHA256, &a.Alt, &a.UpdatedAt)
	return a, mapErr(err)
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	default:
		return fmt.Errorf("siteassets: %w", err)
	}
}

// MemStore is an in-memory Store for dev mode (no database).
type MemStore struct {
	mu   sync.Mutex
	hero *Asset
}

// NewMemStore constructs an empty MemStore.
func NewMemStore() *MemStore { return &MemStore{} }

// HeroMeta reads the hero without its bytes.
func (m *MemStore) HeroMeta(ctx context.Context) (Asset, error) {
	a, err := m.Hero(ctx)
	a.Bytes = nil
	return a, err
}

// Hero reads the hero including its bytes.
func (m *MemStore) Hero(context.Context) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hero == nil {
		return Asset{}, ErrNotFound
	}
	return *m.hero, nil
}

// PutHero replaces the hero and returns the previous sha256.
func (m *MemStore) PutHero(_ context.Context, a Asset) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := ""
	if m.hero != nil {
		old = m.hero.SHA256
	}
	a.UpdatedAt = time.Now()
	m.hero = &a
	return old, nil
}

// DeleteHero removes the hero.
func (m *MemStore) DeleteHero(context.Context) (Asset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hero == nil {
		return Asset{}, ErrNotFound
	}
	a := *m.hero
	a.Bytes = nil
	m.hero = nil
	return a, nil
}
