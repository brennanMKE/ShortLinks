package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// auditRow is one persisted audit_log row, decoded for assertions.
type auditRow struct {
	ActorID    *int64
	UserID     *int64
	Action     string
	TargetType *string
	TargetID   *int64
	Metadata   map[string]any
}

// lastAuditFor returns the most recent audit_log row with the given action, or
// fails the test if none exists. It proves a seam fired AND lets the caller
// assert actor/target/metadata.
func lastAuditFor(t *testing.T, pool *pgxpool.Pool, action string) auditRow {
	t.Helper()
	var (
		row     auditRow
		metaRaw []byte
	)
	err := pool.QueryRow(context.Background(),
		`SELECT actor_id, user_id, action, target_type, target_id, metadata
		   FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, action,
	).Scan(&row.ActorID, &row.UserID, &row.Action, &row.TargetType, &row.TargetID, &metaRaw)
	if err != nil {
		t.Fatalf("no audit_log row for action %q: %v", action, err)
	}
	if metaRaw != nil {
		if err := json.Unmarshal(metaRaw, &row.Metadata); err != nil {
			t.Fatalf("decode metadata for %q: %v", action, err)
		}
	}
	return row
}

// auditLinksMux wires the link CRUD routes with a real audit.Logger so the
// #0025 link.* seams write rows against the live DB. The rule cache is wired so
// the filter/denied path also runs.
func auditLinksMux(t *testing.T, pool *pgxpool.Pool, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	// Pass an untyped-nil ruleProvider when no cache is wired so the handler's
	// h.rules != nil guard sees a true nil interface (a typed-nil *RuleCache would
	// be a non-nil interface and panic in Rules).
	var rules ruleProvider
	if ruleCache != nil {
		rules = ruleCache
	}
	h := NewLinksHandler(links.NewStore(pool), nil, rules, audit.New(pool), nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("DELETE /api/links/{key}", requireSession(http.HandlerFunc(h.Delete)))
	return mux
}

// TestAudit_LinkCreated proves a successful POST /api/links writes a
// link.created audit row attributed to the user with the PRD metadata shape.
func TestAudit_LinkCreated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(auditLinksMux(t, pool, nil))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://www.wikipedia.org","title":"Wiki"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionLinkCreated)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.TargetType == nil || *row.TargetType != audit.TargetLink {
		t.Errorf("target_type = %v, want %q", row.TargetType, audit.TargetLink)
	}
	if row.Metadata["destination_url"] != "https://www.wikipedia.org" {
		t.Errorf("metadata.destination_url = %v", row.Metadata["destination_url"])
	}
	if _, ok := row.Metadata["key"]; !ok {
		t.Errorf("metadata missing key: %v", row.Metadata)
	}
	if row.Metadata["duplicate"] != false {
		t.Errorf("metadata.duplicate = %v, want false", row.Metadata["duplicate"])
	}
}

// TestAudit_LinkReactivated proves the dedup reactivation branch writes a
// link.reactivated row (not link.created).
func TestAudit_LinkReactivated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(auditLinksMux(t, pool, nil))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	// Seed an INACTIVE link so the generated-key create path reactivates it.
	id := seedLink(t, pool, alice, "reuse1", "https://reuse.example.com")
	if _, err := pool.Exec(context.Background(),
		`UPDATE links SET active = FALSE WHERE id = $1`, id); err != nil {
		t.Fatalf("deactivate seed link: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://reuse.example.com"}`))
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionLinkReactivated)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.Metadata["destination_url"] != "https://reuse.example.com" {
		t.Errorf("metadata.destination_url = %v", row.Metadata["destination_url"])
	}
}

// TestAudit_LinkDenied proves a filter-denied create writes a link.denied row
// with the reason_code/reason_label/matched_rule_id metadata.
func TestAudit_LinkDenied(t *testing.T) {
	pool := filterTestPool(t)
	ruleID := seedFilterRule(t, pool, `evil\.example\.com`, 1) // 1 = malware
	ruleCache := newFilterRuleCache(pool)
	srv := httptest.NewServer(auditLinksMux(t, pool, ruleCache))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
		jsonBody(`{"destination_url":"https://evil.example.com/x"}`))
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionLinkDenied)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.Metadata["reason_code"] != float64(1) {
		t.Errorf("metadata.reason_code = %v, want 1", row.Metadata["reason_code"])
	}
	if row.Metadata["reason_label"] == nil || row.Metadata["reason_label"] == "" {
		t.Errorf("metadata.reason_label missing: %v", row.Metadata)
	}
	if row.Metadata["matched_rule_id"] != float64(ruleID) {
		t.Errorf("metadata.matched_rule_id = %v, want %d", row.Metadata["matched_rule_id"], ruleID)
	}
}

// TestAudit_LinkDeactivated proves DELETE /api/links/{key} writes a
// link.deactivated row with {key, destination_url}.
func TestAudit_LinkDeactivated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(auditLinksMux(t, pool, nil))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedLink(t, pool, alice, "del123", "https://del.example.com")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/links/del123", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionLinkDeactivated)
	if row.ActorID == nil || *row.ActorID != alice {
		t.Errorf("actor_id = %v, want %d", row.ActorID, alice)
	}
	if row.Metadata["key"] != "del123" || row.Metadata["destination_url"] != "https://del.example.com" {
		t.Errorf("metadata = %v, want key=del123 destination_url=https://del.example.com", row.Metadata)
	}
}

// auditAdminMux wires the admin settings + url-filter routes with a real
// audit.Logger so the #0025 settings.updated and url_filter.* seams write rows.
func auditAdminMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	logger := audit.New(pool)
	settingsH := NewSettingsHandler(authStore, logger)
	filterStore := filters.NewStore(pool)
	urlFiltersH := NewURLFiltersHandler(filterStore, nil, logger)
	requireSession := middleware.RequireSession(authStore)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("PATCH /admin/settings", requireAdmin(http.HandlerFunc(settingsH.Patch)))
	mux.Handle("POST /admin/url-filters", requireAdmin(http.HandlerFunc(urlFiltersH.Create)))
	mux.Handle("DELETE /admin/url-filters/{id}", requireAdmin(http.HandlerFunc(urlFiltersH.Delete)))
	return mux
}

// TestAudit_SettingsUpdated proves PATCH /admin/settings writes a
// settings.updated row with {key, old_value, new_value}.
func TestAudit_SettingsUpdated(t *testing.T) {
	pool := filterTestPool(t)
	srv := httptest.NewServer(auditAdminMux(t, pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")
	// Seed the setting at "false" so the update flips it to "true".
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO settings (key, value, updated_at) VALUES ('registrations_enabled','false', now())
		 ON CONFLICT (key) DO UPDATE SET value = 'false'`); err != nil {
		t.Fatalf("seed setting: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/admin/settings",
		jsonBody(`{"key":"registrations_enabled","value":"true"}`))
	resp, err := srv.Client().Do(withCookie(req, "admin-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	row := lastAuditFor(t, pool, audit.ActionSettingsUpdated)
	if row.ActorID == nil || *row.ActorID != admin {
		t.Errorf("actor_id = %v, want %d", row.ActorID, admin)
	}
	if row.TargetType == nil || *row.TargetType != audit.TargetSettings {
		t.Errorf("target_type = %v, want %q", row.TargetType, audit.TargetSettings)
	}
	if row.Metadata["key"] != "registrations_enabled" ||
		row.Metadata["old_value"] != "false" || row.Metadata["new_value"] != "true" {
		t.Errorf("metadata = %v, want key/old=false/new=true", row.Metadata)
	}
}

// TestAudit_URLFilterCreatedAndDeleted proves POST then DELETE on the url-filter
// admin routes write url_filter.created and url_filter.deleted rows.
func TestAudit_URLFilterCreatedAndDeleted(t *testing.T) {
	pool := filterTestPool(t)
	srv := httptest.NewServer(auditAdminMux(t, pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	// Create.
	createReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/admin/url-filters",
		jsonBody(`{"pattern":"bad\\.example\\.com","reason_code":2,"description":"phish"}`))
	createResp, err := srv.Client().Do(withCookie(createReq, "admin-token"))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	defer createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", createResp.StatusCode)
	}
	var created ruleView
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created rule: %v", err)
	}

	cRow := lastAuditFor(t, pool, audit.ActionURLFilterCreated)
	if cRow.ActorID == nil || *cRow.ActorID != admin {
		t.Errorf("created actor_id = %v, want %d", cRow.ActorID, admin)
	}
	if cRow.Metadata["pattern"] != `bad\.example\.com` || cRow.Metadata["reason_code"] != float64(2) {
		t.Errorf("created metadata = %v", cRow.Metadata)
	}

	// Delete.
	delReq, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/admin/url-filters/"+itoa(created.ID), nil)
	delResp, err := srv.Client().Do(withCookie(delReq, "admin-token"))
	if err != nil {
		t.Fatalf("delete request: %v", err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", delResp.StatusCode)
	}

	dRow := lastAuditFor(t, pool, audit.ActionURLFilterDeleted)
	if dRow.ActorID == nil || *dRow.ActorID != admin {
		t.Errorf("deleted actor_id = %v, want %d", dRow.ActorID, admin)
	}
	if dRow.Metadata["pattern"] != `bad\.example\.com` {
		t.Errorf("deleted metadata.pattern = %v", dRow.Metadata["pattern"])
	}
}
