package links

import (
	"context"
	"errors"
	"testing"
)

func TestReactivateLink(t *testing.T) {
	pool := resolverTestPool(t)
	owner := seedUserL(t, pool)
	var other int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email) VALUES ('other@example.com') RETURNING id`).Scan(&other); err != nil {
		t.Skipf("cannot seed second user: %v", err)
	}
	s := NewStore(pool)
	ctx := context.Background()
	const dest = "https://ok.example.com"
	if _, err := pool.Exec(ctx,
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at) VALUES
		 ($1,'off',$2,FALSE,0,now()), ($1,'den',$2,FALSE,3,now())`, owner, dest); err != nil {
		t.Fatal(err)
	}
	active := func(key string) bool {
		var a bool
		_ = pool.QueryRow(ctx, `SELECT active FROM links WHERE key=$1`, key).Scan(&a)
		return a
	}

	if err := s.ReactivateLink(ctx, owner, "off", "https://changed.example.com"); !errors.Is(err, ErrLinkChanged) {
		t.Errorf("destination mismatch err = %v, want ErrLinkChanged", err)
	}
	if active("off") {
		t.Error("mismatch must not reactivate")
	}
	if err := s.ReactivateLink(ctx, other, "off", dest); !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("other user's key err = %v, want ErrLinkNotFound", err)
	}
	if err := s.ReactivateLink(ctx, owner, "den", dest); !errors.Is(err, ErrLinkDenied) {
		t.Errorf("denied err = %v, want ErrLinkDenied", err)
	}
	if active("den") {
		t.Error("denied link was reactivated")
	}
	if err := s.ReactivateLink(ctx, owner, "off", dest); err != nil {
		t.Fatalf("success: %v", err)
	}
	if !active("off") {
		t.Error("link not active after success")
	}
}
