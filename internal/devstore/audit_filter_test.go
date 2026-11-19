package devstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
)

func TestListAuditLog_Filters(t *testing.T) {
	s := devstore.New("admin@localhost")
	ctx := context.Background()
	all, total, err := s.ListAuditLog(ctx, audit.Filter{}, 100, 0)
	if err != nil || total == 0 || int64(len(all)) != total {
		t.Fatalf("seeded log: total=%d rows=%d err=%v", total, len(all), err)
	}
	if all[0].CreatedAt.Before(all[len(all)-1].CreatedAt) {
		t.Error("not newest-first")
	}

	_, byEmail, _ := s.ListAuditLog(ctx, audit.Filter{Email: "SAM@example.com"}, 100, 0)
	if byEmail == 0 || byEmail >= total {
		t.Errorf("email filter total = %d of %d", byEmail, total)
	}
	rows, n, _ := s.ListAuditLog(ctx, audit.Filter{Actions: []string{audit.ActionAccountLogin}}, 100, 0)
	if n == 0 {
		t.Fatal("no login rows seeded")
	}
	for _, r := range rows {
		if r.Action != audit.ActionAccountLogin || r.ActorEmail == nil {
			t.Errorf("row %+v: want login with actor email", r)
		}
	}
	cut := time.Now().Add(-24 * time.Hour)
	_, recent, _ := s.ListAuditLog(ctx, audit.Filter{From: &cut}, 100, 0)
	_, older, _ := s.ListAuditLog(ctx, audit.Filter{To: &cut}, 100, 0)
	if recent+older != total || recent == 0 || older == 0 {
		t.Errorf("from/to split %d + %d != %d", recent, older, total)
	}
	if _, none, _ := s.ListAuditLog(ctx, audit.Filter{Email: "nobody@x"}, 100, 0); none != 0 {
		t.Errorf("unknown email total = %d, want 0", none)
	}
}
