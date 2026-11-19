package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// TestAdminAudit_EmailActionDateFilters covers the email, action and from/to
// filters against real rows, and that rows carry resolved emails.
func TestAdminAudit_EmailActionDateFilters(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, admin, "admin-token")

	d1 := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC)
	d3 := time.Date(2026, 5, 22, 9, 0, 0, 0, time.UTC)
	insertAuditRow(t, pool, &alice, &alice, audit.ActionLinkCreated, audit.TargetLink, nil, "", nil, d1)
	insertAuditRow(t, pool, &admin, &alice, audit.ActionAccountDeactivated, audit.TargetUser, &alice, "", nil, d2)
	insertAuditRow(t, pool, &bob, &bob, audit.ActionLinkCreated, audit.TargetLink, nil, "", nil, d3)
	insertAuditRow(t, pool, nil, nil, audit.ActionAccountRegistrationStarted, "", nil, "", nil, d3.Add(time.Hour))

	get := func(query string) auditListResponse {
		t.Helper()
		resp, body := getAudit(t, srv, "admin-token", query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", query, resp.StatusCode)
		}
		return body
	}

	// Email matches the actor or the affected user, case-insensitively: alice's
	// own row plus the deactivation an admin did to her.
	if b := get("?email=ALICE@example.com"); b.Total != 2 {
		t.Errorf("email=alice total = %d, want 2", b.Total)
	}
	if b := get("?email=admin@example.com"); b.Total != 1 {
		t.Errorf("email=admin total = %d, want 1 (actor of the deactivation)", b.Total)
	}
	if b := get("?email=nobody@example.com"); b.Total != 0 || len(b.AuditLog) != 0 {
		t.Errorf("email=nobody total = %d rows = %d, want 0", b.Total, len(b.AuditLog))
	}

	// Emails are resolved onto each row.
	b := get("?email=bob@example.com")
	if len(b.AuditLog) != 1 || b.AuditLog[0].ActorEmail == nil || *b.AuditLog[0].ActorEmail != "bob@example.com" ||
		b.AuditLog[0].UserEmail == nil || *b.AuditLog[0].UserEmail != "bob@example.com" {
		t.Errorf("bob row emails = %+v", b.AuditLog)
	}

	// Action: one value, then a comma-separated list.
	if b := get("?action=link.created"); b.Total != 2 {
		t.Errorf("action=link.created total = %d, want 2", b.Total)
	}
	if b := get("?action=link.created,account.deactivated"); b.Total != 3 {
		t.Errorf("action list total = %d, want 3", b.Total)
	}

	// Dates: from is inclusive; a date-only to covers the whole day.
	if b := get("?from=2026-05-21"); b.Total != 3 {
		t.Errorf("from=05-21 total = %d, want 3", b.Total)
	}
	if b := get("?from=2026-05-21&to=2026-05-21"); b.Total != 1 {
		t.Errorf("05-21..05-21 total = %d, want 1", b.Total)
	}
	if b := get("?to=2026-05-20T10:00:00Z"); b.Total != 1 {
		t.Errorf("to=instant total = %d, want 1 (exclusive instant)", b.Total)
	}

	// Filters combine, and pagination reports the filtered total.
	if b := get("?email=alice@example.com&action=link.created&per_page=1"); b.Total != 1 || len(b.AuditLog) != 1 {
		t.Errorf("combined total = %d rows = %d, want 1/1", b.Total, len(b.AuditLog))
	}
}
