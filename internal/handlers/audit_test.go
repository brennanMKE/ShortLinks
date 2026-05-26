package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// adminAuditMux wires the real GET /admin/audit route guarded by RequireSession
// then RequireAdmin, backed by the real *audit.Reader and *auth.Store. Requests
// therefore flow through the genuine session + admin middleware (proving the
// route is protected exactly as wired in main.go) and read real audit_log rows.
func adminAuditMux(pool *pgxpool.Pool) http.Handler {
	store := auth.NewStore(pool)
	h := NewAdminAuditHandler(audit.NewReader(pool))
	requireSession := middleware.RequireSession(store)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}
	mux := http.NewServeMux()
	mux.Handle("GET /admin/audit", requireAdmin(http.HandlerFunc(h.List)))
	return mux
}

// insertAuditRow inserts one audit_log row directly with explicit columns so
// tests control user_id, action, metadata, ip_address and created_at (to assert
// ordering). actorID/userID/targetID/ip/metaJSON nil → SQL NULL. Returns the
// new row id.
func insertAuditRow(t *testing.T, pool *pgxpool.Pool, actorID, userID *int64, action string,
	targetType string, targetID *int64, metaJSON string, ip *string, createdAt time.Time) int64 {
	t.Helper()
	var tt any
	if targetType != "" {
		tt = targetType
	}
	var meta any
	if metaJSON != "" {
		meta = []byte(metaJSON)
	}
	var ipArg any
	if ip != nil {
		ipArg = *ip
	}
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO audit_log
		     (actor_id, user_id, action, target_type, target_id, metadata, ip_address, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING id`,
		actorID, userID, action, tt, targetID, meta, ipArg, createdAt,
	).Scan(&id); err != nil {
		t.Fatalf("insert audit row (%s): %v", action, err)
	}
	return id
}

// auditListResponse mirrors the GET /admin/audit body for decoding. metadata is
// kept as json.RawMessage so the test can assert it decodes to a JSON object
// (not a string).
type auditListResponse struct {
	AuditLog []struct {
		ID         int64           `json:"id"`
		ActorID    *int64          `json:"actor_id"`
		UserID     *int64          `json:"user_id"`
		Action     string          `json:"action"`
		TargetType *string         `json:"target_type"`
		TargetID   *int64          `json:"target_id"`
		Metadata   json.RawMessage `json:"metadata"`
		IPAddress  *string         `json:"ip_address"`
		CreatedAt  time.Time       `json:"created_at"`
	} `json:"audit_log"`
	Total   int64 `json:"total"`
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
}

func getAudit(t *testing.T, srv *httptest.Server, token, query string) (*http.Response, auditListResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/audit"+query, nil)
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("GET /admin/audit%s: %v", query, err)
	}
	var body auditListResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			resp.Body.Close()
			t.Fatalf("decode audit response: %v", err)
		}
	}
	resp.Body.Close()
	return resp, body
}

func ptrStr(s string) *string { return &s }

// TestAdminAudit_NonAdminForbidden asserts a non-admin with a VALID session is
// rejected with 403 — proving RequireAdmin guards the route and is reached only
// after RequireSession succeeds.
func TestAdminAudit_NonAdminForbidden(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	user := seedUser(t, pool, "regular@example.com") // is_admin = FALSE
	seedSession(t, pool, user, "user-token")

	resp, _ := getAudit(t, srv, "user-token", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin status = %d, want 403", resp.StatusCode)
	}
}

// TestAdminAudit_Unauthenticated asserts a request with no session cookie gets
// 401 — proving the session guard runs first.
func TestAdminAudit_Unauthenticated(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/audit", nil)
	resp, err := srv.Client().Do(req) // no cookie
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
	}
}

// TestAdminAudit_NewestFirst seeds rows with increasing created_at and asserts
// the response returns them newest-first.
func TestAdminAudit_NewestFirst(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	base := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// Insert out of chronological order to prove ORDER BY does the sorting.
	idMid := insertAuditRow(t, pool, &admin, &admin, audit.ActionAccountLogin, "", nil, "", nil, base.Add(1*time.Minute))
	idOld := insertAuditRow(t, pool, &admin, &admin, audit.ActionAccountLogin, "", nil, "", nil, base)
	idNew := insertAuditRow(t, pool, &admin, &admin, audit.ActionAccountLogin, "", nil, "", nil, base.Add(2*time.Minute))

	resp, body := getAudit(t, srv, "admin-token", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body.Total != 3 {
		t.Errorf("total = %d, want 3", body.Total)
	}
	if len(body.AuditLog) != 3 {
		t.Fatalf("got %d rows, want 3", len(body.AuditLog))
	}
	wantOrder := []int64{idNew, idMid, idOld}
	for i, want := range wantOrder {
		if body.AuditLog[i].ID != want {
			t.Errorf("row[%d].id = %d, want %d (newest-first)", i, body.AuditLog[i].ID, want)
		}
	}
}

// TestAdminAudit_Pagination asserts ?page=&per_page= returns the right slice and
// the per_page cap is enforced.
func TestAdminAudit_Pagination(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	seedSession(t, pool, admin, "admin-token")

	base := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// 5 rows, created_at increasing with i so id order == time order.
	ids := make([]int64, 5)
	for i := 0; i < 5; i++ {
		ids[i] = insertAuditRow(t, pool, &admin, &admin, audit.ActionAccountLogin, "", nil, "", nil,
			base.Add(time.Duration(i)*time.Minute))
	}
	// Newest-first overall order: ids[4], ids[3], ids[2], ids[1], ids[0].

	// page 1, per_page 2 → [ids[4], ids[3]].
	resp, body := getAudit(t, srv, "admin-token", "?page=1&per_page=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("page1 status = %d, want 200", resp.StatusCode)
	}
	if body.Total != 5 || body.Page != 1 || body.PerPage != 2 {
		t.Errorf("page1 meta total/page/per_page = %d/%d/%d, want 5/1/2", body.Total, body.Page, body.PerPage)
	}
	if len(body.AuditLog) != 2 || body.AuditLog[0].ID != ids[4] || body.AuditLog[1].ID != ids[3] {
		t.Errorf("page1 ids = %v, want [%d %d]", auditIDs(body), ids[4], ids[3])
	}

	// page 2, per_page 2 → [ids[2], ids[1]].
	_, body2 := getAudit(t, srv, "admin-token", "?page=2&per_page=2")
	if len(body2.AuditLog) != 2 || body2.AuditLog[0].ID != ids[2] || body2.AuditLog[1].ID != ids[1] {
		t.Errorf("page2 ids = %v, want [%d %d]", auditIDs(body2), ids[2], ids[1])
	}

	// page 3, per_page 2 → [ids[0]] (last partial page).
	_, body3 := getAudit(t, srv, "admin-token", "?page=3&per_page=2")
	if len(body3.AuditLog) != 1 || body3.AuditLog[0].ID != ids[0] {
		t.Errorf("page3 ids = %v, want [%d]", auditIDs(body3), ids[0])
	}

	// per_page above the cap is clamped to maxAuditPerPage in the response meta.
	_, capped := getAudit(t, srv, "admin-token", "?per_page=99999")
	if capped.PerPage != maxAuditPerPage {
		t.Errorf("per_page = %d, want capped at %d", capped.PerPage, maxAuditPerPage)
	}
}

// TestAdminAudit_UserIDFilter asserts ?user_id=N returns only that user's rows
// and ?user_id=garbage is rejected with 400.
func TestAdminAudit_UserIDFilter(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, admin, "admin-token")

	base := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	insertAuditRow(t, pool, &admin, &alice, audit.ActionAccountDeactivated, audit.TargetUser, &alice, `{"reason":"spam"}`, nil, base)
	insertAuditRow(t, pool, &admin, &alice, audit.ActionAccountReactivated, audit.TargetUser, &alice, `{"note":"x"}`, nil, base.Add(time.Minute))
	insertAuditRow(t, pool, &admin, &bob, audit.ActionAccountDeactivated, audit.TargetUser, &bob, `{"reason":"phishing"}`, nil, base.Add(2*time.Minute))

	// Filter to alice: only her 2 rows.
	resp, body := getAudit(t, srv, "admin-token", "?user_id="+itoa(alice))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("filter status = %d, want 200", resp.StatusCode)
	}
	if body.Total != 2 {
		t.Errorf("filtered total = %d, want 2", body.Total)
	}
	if len(body.AuditLog) != 2 {
		t.Fatalf("filtered rows = %d, want 2", len(body.AuditLog))
	}
	for _, row := range body.AuditLog {
		if row.UserID == nil || *row.UserID != alice {
			t.Errorf("filtered row user_id = %v, want %d", row.UserID, alice)
		}
	}

	// Garbage user_id → 400.
	bad, _ := getAudit(t, srv, "admin-token", "?user_id=notanumber")
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("garbage user_id status = %d, want 400", bad.StatusCode)
	}
}

// TestAdminAudit_NullSerializationAndMetadata asserts nullable columns serialize
// as JSON null and metadata round-trips as a JSON object (not a string).
func TestAdminAudit_NullSerializationAndMetadata(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(adminAuditMux(pool))
	defer srv.Close()

	admin := seedAdmin(t, pool, "admin@example.com")
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, admin, "admin-token")

	base := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	// Row 1 (newest): a pre-auth event — actor_id, user_id, target_id, ip all NULL,
	// metadata NULL.
	insertAuditRow(t, pool, nil, nil, audit.ActionAccountRegistrationStarted, "", nil, "", nil, base.Add(time.Minute))
	// Row 2 (older): fully populated, with a JSON-object metadata and an IP.
	insertAuditRow(t, pool, &admin, &alice, audit.ActionAccountDeactivated, audit.TargetUser, &alice,
		`{"reason":"spam","note":"junk"}`, ptrStr("203.0.113.7"), base)

	resp, body := getAudit(t, srv, "admin-token", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(body.AuditLog) != 2 {
		t.Fatalf("rows = %d, want 2", len(body.AuditLog))
	}

	// Newest row: every nullable column must be JSON null.
	nullRow := body.AuditLog[0]
	if nullRow.ActorID != nil || nullRow.UserID != nil || nullRow.TargetType != nil ||
		nullRow.TargetID != nil || nullRow.IPAddress != nil {
		t.Errorf("null row: nullable columns should be JSON null, got actor=%v user=%v type=%v target=%v ip=%v",
			nullRow.ActorID, nullRow.UserID, nullRow.TargetType, nullRow.TargetID, nullRow.IPAddress)
	}
	// metadata NULL → JSON null literal.
	if string(nullRow.Metadata) != "null" && nullRow.Metadata != nil {
		t.Errorf("null row metadata = %q, want JSON null", string(nullRow.Metadata))
	}

	// Verify in the raw JSON that the fields are literally null (not 0 / "").
	resp2, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/audit", nil)
	rawResp, err := srv.Client().Do(withCookie(resp2, "admin-token"))
	if err != nil {
		t.Fatalf("raw request: %v", err)
	}
	var rawWrap struct {
		AuditLog []map[string]json.RawMessage `json:"audit_log"`
	}
	if err := json.NewDecoder(rawResp.Body).Decode(&rawWrap); err != nil {
		rawResp.Body.Close()
		t.Fatalf("decode raw: %v", err)
	}
	rawResp.Body.Close()
	for _, key := range []string{"actor_id", "user_id", "target_type", "target_id", "ip_address", "metadata"} {
		if got := string(rawWrap.AuditLog[0][key]); got != "null" {
			t.Errorf("raw null row %q = %s, want null", key, got)
		}
	}

	// Older row: metadata must decode as a JSON object with the seeded fields,
	// proving it is not a quoted string. host(ip_address) returns the bare IP.
	popRow := body.AuditLog[1]
	var meta map[string]any
	if err := json.Unmarshal(popRow.Metadata, &meta); err != nil {
		t.Fatalf("metadata is not a JSON object: %v (raw=%s)", err, string(popRow.Metadata))
	}
	if meta["reason"] != "spam" || meta["note"] != "junk" {
		t.Errorf("metadata = %v, want reason=spam note=junk", meta)
	}
	if popRow.IPAddress == nil || *popRow.IPAddress != "203.0.113.7" {
		t.Errorf("ip_address = %v, want 203.0.113.7", popRow.IPAddress)
	}
	if popRow.ActorID == nil || *popRow.ActorID != admin {
		t.Errorf("actor_id = %v, want %d", popRow.ActorID, admin)
	}
}

// auditIDs extracts the row ids from a decoded response for error messages.
func auditIDs(b auditListResponse) []int64 {
	out := make([]int64, 0, len(b.AuditLog))
	for _, r := range b.AuditLog {
		out = append(out, r.ID)
	}
	return out
}
