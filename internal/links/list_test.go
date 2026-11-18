package links

import (
	"context"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike(`50%_a\b`); got != `50\%\_a\\b` {
		t.Errorf("EscapeLike = %q", got)
	}
	if got := ContentKeyHost("www.example.com/a/b?x=1"); got != "www.example.com" {
		t.Errorf("ContentKeyHost = %q", got)
	}
}

// TestListLinksFiltered_StoreCombinations covers the store contract directly:
// per-user scope, a status+query combination, a bound-parameter query
// containing SQL metacharacters, window counts and the content_key total.
func TestListLinksFiltered_StoreCombinations(t *testing.T) {
	pool := resolverTestPool(t)
	ctx := context.Background()
	owner := seedUserL(t, pool)
	var other int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, is_admin, active, created_at) VALUES ('o2@example.com', FALSE, TRUE, now()) RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	ins := func(user int64, key, dest, title string, active bool) int64 {
		var id int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO links (user_id, key, destination_url, title, active, denied_reason) VALUES ($1,$2,$3,$4,$5,0) RETURNING id`,
			user, key, dest, title, active).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := ins(owner, "a", "https://example.com/p", "Alpha 100%", true)
	ins(owner, "b", "https://example.com/p/?utm_source=x", "Beta", false)
	ins(owner, "c", "https://example.com/other", "Gamma", true)
	ins(other, "z", "https://example.com/p", "Alpha 100%", true)
	now := time.Now()
	if _, err := pool.Exec(ctx, `INSERT INTO clicks (link_id, clicked_at, is_bot) VALUES ($1,$2,FALSE),($1,$3,FALSE),($1,$2,TRUE)`, a, now, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool)

	got, total, err := s.ListLinksFiltered(ctx, owner, ListOptions{Limit: 10, Query: "100%", Status: StatusActive, Now: now})
	if err != nil || total != 1 || len(got) != 1 || got[0].Key != "a" {
		t.Fatalf("q+status: %v total=%d %+v", err, total, got)
	}
	// SQL metacharacters in q are data, not syntax.
	got, total, err = s.ListLinksFiltered(ctx, owner, ListOptions{Limit: 10, Query: `x'; DROP TABLE links; --`})
	if err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("metachar q: %v total=%d", err, total)
	}
	got, _, err = s.ListLinksFiltered(ctx, owner, ListOptions{
		Limit: 10, HasWindow: true, WindowStart: now.Add(-time.Hour), WindowEnd: now.Add(time.Hour), Sort: SortClicksWindow,
	})
	if err != nil || len(got) != 3 || got[0].Key != "a" || got[0].ClicksWindow != 1 || got[0].ClickCount != 2 {
		t.Fatalf("window: %v %+v", err, got)
	}
	got, total, err = s.ListLinksFiltered(ctx, owner, ListOptions{Limit: 1, Offset: 1, ContentKey: reports.ContentKey("https://example.com/p")})
	if err != nil || total != 2 || len(got) != 1 {
		t.Fatalf("content_key: %v total=%d len=%d", err, total, len(got))
	}
}
