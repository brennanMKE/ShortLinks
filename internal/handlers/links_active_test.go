package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// evictRecorder is a cacheEvictor that records evicted keys.
type evictRecorder struct {
	mu   sync.Mutex
	keys []string
}

func (e *evictRecorder) Delete(k string) { e.mu.Lock(); e.keys = append(e.keys, k); e.mu.Unlock() }
func (e *evictRecorder) count(k string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, x := range e.keys {
		if x == k {
			n++
		}
	}
	return n
}

// activeMux serves PATCH/DELETE behind the session-or-API-token guard (so
// read-only tokens are enforced), with a real audit logger and filter rules.
func activeMux(t *testing.T, pool *pgxpool.Pool, ev *evictRecorder) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewLinksHandler(links.NewStore(pool), ev, newFilterRuleCache(pool), audit.New(pool), nil, nil, nil, testBaseURL)
	guard := middleware.RequireSessionOrAPIToken(authStore, authStore, middleware.APITokenLimits{})
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/links/{key}", guard(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/links/{key}", guard(http.HandlerFunc(h.Delete)))
	return mux
}

func patchActive(t *testing.T, h http.Handler, key, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/links/"+key, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

func TestPatchActive_DeactivateMatchesDelete(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	h := activeMux(t, pool, ev)
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "act", "https://ok.example.com")

	rec := patchActive(t, h, "act", tok, `{"active":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var v linkView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Active {
		t.Error("response should show active=false")
	}
	if _, _, active, _ := linkRow(t, pool, "act"); active {
		t.Error("link still active in DB")
	}
	if got := auditCount(t, pool, audit.ActionLinkDeactivated); got != 1 {
		t.Errorf("link.deactivated entries = %d, want 1", got)
	}
	row := lastAuditFor(t, pool, audit.ActionLinkDeactivated)
	if row.Metadata["key"] != "act" || row.Metadata["destination_url"] != "https://ok.example.com" {
		t.Errorf("deactivated metadata = %v", row.Metadata)
	}
	if ev.count("act") < 1 {
		t.Error("cache not evicted on deactivate")
	}
}

func TestPatchActive_Reactivate(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	h := activeMux(t, pool, ev)
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "act", "https://ok.example.com")
	if rec := patchActive(t, h, "act", tok, `{"active":false}`); rec.Code != 200 {
		t.Fatalf("deactivate: %d", rec.Code)
	}
	before := ev.count("act")

	rec := patchActive(t, h, "act", tok, `{"active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate status = %d: %s", rec.Code, rec.Body)
	}
	var v linkView
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.Active {
		t.Error("response should show active=true")
	}
	if _, _, active, _ := linkRow(t, pool, "act"); !active {
		t.Error("link not active in DB")
	}
	if ev.count("act") <= before {
		t.Error("cache not evicted on reactivate")
	}
	if got := auditCount(t, pool, audit.ActionLinkReactivated); got != 1 {
		t.Errorf("link.reactivated entries = %d, want 1", got)
	}
	row := lastAuditFor(t, pool, audit.ActionLinkReactivated)
	if row.Metadata["key"] != "act" {
		t.Errorf("reactivated metadata = %v", row.Metadata)
	}
}

func TestPatchActive_ReactivateActiveIsNoOp(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	h := activeMux(t, pool, ev)
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "act", "https://ok.example.com")

	rec := patchActive(t, h, "act", tok, `{"active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := auditCount(t, pool, audit.ActionLinkReactivated); got != 0 {
		t.Errorf("no-op wrote %d link.reactivated entries", got)
	}
}

func TestPatchActive_ReactivateDeniedByNewRule(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "act", "https://evil.example.com/x")
	// Deactivate before the rule exists, then add the rule.
	if rec := patchActive(t, activeMux(t, pool, ev), "act", tok, `{"active":false}`); rec.Code != 200 {
		t.Fatalf("deactivate: %d", rec.Code)
	}
	seedFilterRule(t, pool, `evil\.example\.com`, int16(filters.ReasonMalware))
	h := activeMux(t, pool, ev) // fresh rule cache sees the new rule

	rec := patchActive(t, h, "act", tok, `{"active":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Error  string `json:"error"`
		Reason int    `json:"reason"`
		Label  string `json:"label"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error != "destination_denied" || body.Reason != int(filters.ReasonMalware) || body.Label == "" {
		t.Errorf("body = %+v", body)
	}
	if _, _, active, _ := linkRow(t, pool, "act"); active {
		t.Error("denied reactivation must leave link inactive")
	}
	if got := auditCount(t, pool, audit.ActionLinkReactivated); got != 0 {
		t.Errorf("link.reactivated entries = %d, want 0", got)
	}
}

// A refused reactivation saves nothing: editing the destination to a blocked
// URL with active:true is a 422 and the old destination stays.
func TestPatchActive_EditThenReactivateFiltersNewDestination(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "act", "https://ok.example.com")
	seedFilterRule(t, pool, `evil\.example\.com`, int16(filters.ReasonMalware))
	h := activeMux(t, pool, ev)
	if rec := patchActive(t, h, "act", tok, `{"active":false}`); rec.Code != 200 {
		t.Fatalf("deactivate: %d", rec.Code)
	}

	rec := patchActive(t, h, "act", tok, `{"destination_url":"https://evil.example.com/y","active":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	dest, _, active, _ := linkRow(t, pool, "act")
	if dest != "https://ok.example.com" || active {
		t.Errorf("dest=%q active=%v; refused reactivation must save nothing", dest, active)
	}

	// A clean destination plus active:true succeeds in one call.
	rec = patchActive(t, h, "act", tok, `{"destination_url":"https://fine.example.com","title":"T","active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	dest, title, active, _ := linkRow(t, pool, "act")
	if dest != "https://fine.example.com" || title != "T" || !active {
		t.Errorf("dest=%q title=%q active=%v", dest, title, active)
	}
}

// A link denied at creation is never reactivated, even when no rule matches now.
func TestPatchActive_DeniedAtCreationNeverReactivates(t *testing.T) {
	pool := filterTestPool(t)
	ev := &evictRecorder{}
	h := activeMux(t, pool, ev)
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, 'den', 'https://ok.example.com', FALSE, $2, now())`,
		alice, int16(filters.ReasonMalware)); err != nil {
		t.Fatal(err)
	}
	rec := patchActive(t, h, "den", tok, `{"active":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if _, _, active, _ := linkRow(t, pool, "den"); active {
		t.Error("denied link was reactivated")
	}
}

func TestPatchActive_ExpiryUntouched(t *testing.T) {
	pool := filterTestPool(t)
	h := activeMux(t, pool, &evictRecorder{})
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)
	seedLink(t, pool, alice, "exp", "https://ok.example.com")
	if _, err := pool.Exec(context.Background(),
		`UPDATE links SET expires_at = now() - interval '1 day', active = FALSE WHERE key = 'exp'`); err != nil {
		t.Fatal(err)
	}
	rec := patchActive(t, h, "exp", tok, `{"active":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var past bool
	_ = pool.QueryRow(context.Background(), `SELECT expires_at < now() FROM links WHERE key='exp'`).Scan(&past)
	if !past {
		t.Error("expires_at was changed by reactivation")
	}
}

func TestPatchActive_ReadOnlyToken403(t *testing.T) {
	pool := filterTestPool(t)
	h := activeMux(t, pool, &evictRecorder{})
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "ro", true)
	seedLink(t, pool, alice, "act", "https://ok.example.com")
	for _, body := range []string{`{"active":false}`, `{"active":true}`} {
		if rec := patchActive(t, h, "act", tok, body); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", body, rec.Code)
		}
	}
	if _, _, active, _ := linkRow(t, pool, "act"); !active {
		t.Error("read-only token changed the link")
	}
}
