package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// fakeAuditReader records the filter the handler built, so the query-string
// parsing is tested without a database.
type fakeAuditReader struct{ got audit.Filter }

func (f *fakeAuditReader) ListAuditLog(_ context.Context, flt audit.Filter, _, _ int) ([]audit.Record, int64, error) {
	f.got = flt
	return []audit.Record{}, 0, nil
}

func TestAdminAudit_ParsesFilters(t *testing.T) {
	fake := &fakeAuditReader{}
	h := NewAdminAuditHandler(fake)

	req := httptest.NewRequest(http.MethodGet,
		"/admin/audit?user_id=7&email=%20A%40b.com%20&action=link.created,%20account.login,,&from=2026-05-20&to=2026-05-21", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	g := fake.got
	if g.UserID == nil || *g.UserID != 7 {
		t.Errorf("UserID = %v, want 7", g.UserID)
	}
	if g.Email != "A@b.com" {
		t.Errorf("Email = %q, want trimmed A@b.com", g.Email)
	}
	if len(g.Actions) != 2 || g.Actions[0] != "link.created" || g.Actions[1] != "account.login" {
		t.Errorf("Actions = %v", g.Actions)
	}
	if want := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC); g.From == nil || !g.From.Equal(want) {
		t.Errorf("From = %v, want %v", g.From, want)
	}
	// A date-only upper bound covers that whole day.
	if want := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC); g.To == nil || !g.To.Equal(want) {
		t.Errorf("To = %v, want %v", g.To, want)
	}
}

func TestAdminAudit_NoFiltersIsZeroValue(t *testing.T) {
	fake := &fakeAuditReader{}
	rec := httptest.NewRecorder()
	NewAdminAuditHandler(fake).List(rec, httptest.NewRequest(http.MethodGet, "/admin/audit", nil))
	g := fake.got
	if rec.Code != http.StatusOK || g.UserID != nil || g.Email != "" || g.Actions != nil || g.From != nil || g.To != nil {
		t.Errorf("status %d filter %+v, want zero filter", rec.Code, g)
	}
}

func TestAdminAudit_BadTimeIs400(t *testing.T) {
	for _, q := range []string{"from=nope", "to=2026-13-40", "from=2026-05-22&to=2026-05-22T00:00:00Z"} {
		rec := httptest.NewRecorder()
		NewAdminAuditHandler(&fakeAuditReader{}).List(rec, httptest.NewRequest(http.MethodGet, "/admin/audit?"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
}
