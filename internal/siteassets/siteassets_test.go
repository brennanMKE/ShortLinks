package siteassets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func sample(sha string, n int) Asset {
	return Asset{ContentType: "image/jpeg", Bytes: bytes.Repeat([]byte{7}, n), Width: 1200, Height: 400, SHA256: sha, Alt: "alt"}
}

// exercise runs the same contract against any store.
func exercise(t *testing.T, s interface {
	HeroMeta(context.Context) (Asset, error)
	Hero(context.Context) (Asset, error)
	PutHero(context.Context, Asset) (string, error)
	DeleteHero(context.Context) (Asset, error)
}) {
	ctx := context.Background()
	if _, err := s.Hero(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Hero err = %v, want ErrNotFound", err)
	}
	if _, err := s.DeleteHero(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty Delete err = %v, want ErrNotFound", err)
	}
	if old, err := s.PutHero(ctx, sample("aaa", 10)); err != nil || old != "" {
		t.Fatalf("first Put = %q, %v", old, err)
	}
	if old, err := s.PutHero(ctx, sample("bbb", 20)); err != nil || old != "aaa" {
		t.Fatalf("second Put old = %q, %v, want aaa", old, err)
	}
	full, err := s.Hero(ctx)
	if err != nil || len(full.Bytes) != 20 || full.SHA256 != "bbb" || full.Width != 1200 || full.Alt != "alt" {
		t.Fatalf("Hero = %+v, %v", full, err)
	}
	meta, err := s.HeroMeta(ctx)
	if err != nil || len(meta.Bytes) != 0 || meta.SHA256 != "bbb" {
		t.Fatalf("HeroMeta = %+v, %v (bytes must be empty)", meta, err)
	}
	del, err := s.DeleteHero(ctx)
	if err != nil || del.SHA256 != "bbb" {
		t.Fatalf("Delete = %+v, %v", del, err)
	}
	if _, err := s.Hero(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
}

func TestMemStore(t *testing.T) { exercise(t, NewMemStore()) }

func TestStore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	clear := func() { _, _ = pool.Exec(context.Background(), `DELETE FROM site_assets`) }
	clear()
	defer clear()
	s := NewStore(pool)
	exercise(t, s)

	// The size CHECK constraint refuses an oversized blob at the database.
	if _, err := s.PutHero(context.Background(), sample("big", 2097153)); err == nil {
		t.Error("PutHero accepted 2 MB + 1 byte; the CHECK constraint should refuse it")
	}
	if _, err := s.PutHero(context.Background(), sample("max", 2097152)); err != nil {
		t.Errorf("PutHero refused exactly 2 MB: %v", err)
	}
}
