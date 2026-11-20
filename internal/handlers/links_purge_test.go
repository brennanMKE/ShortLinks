package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

const purgeSecretURL = "https://example.com/join?token=SECRET-TOKEN-123&x=1"

// purgeMux serves create, get, patch and delete behind the session-or-token
// guard with a store that writes audit entries (needed for link.purged).
func purgeMux(t *testing.T, pool *pgxpool.Pool, ev *evictRecorder, onPurge ...func()) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	al := audit.New(pool)
	h := NewLinksHandler(links.NewStore(pool).WithAudit(al), ev, nil, al, nil, nil, nil, testBaseURL)
	for _, fn := range onPurge {
		h.OnPurge(fn)
	}
	guard := middleware.RequireSessionOrAPIToken(authStore, authStore, middleware.APITokenLimits{})
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", guard(http.HandlerFunc(h.Create)))
	mux.Handle("PATCH /api/links/{key}", guard(http.HandlerFunc(h.Patch)))
	mux.Handle("DELETE /api/links/{key}", guard(http.HandlerFunc(h.Delete)))
	return mux
}

func purgeReq(h http.Handler, method, path, token, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func countWhere(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func TestPurgeLink(t *testing.T) {
	pool := filterTestPool(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `TRUNCATE purged_keys, idempotency_keys`) })
	ev := &evictRecorder{}
	purged := 0
	h := purgeMux(t, pool, ev, func() { purged++ })
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	rw := mintToken(t, pool, alice, "rw", false)
	ro := mintToken(t, pool, alice, "ro", true)
	bobTok := mintToken(t, pool, bob, "bobrw", false)

	// Create through the API so link.created holds the secret destination.
	rec := purgeReq(h, "POST", "/api/links", rw, `{"destination_url":"`+purgeSecretURL+`","title":"Invite","key":"inv-purge"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var linkID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM links WHERE key='inv-purge'`).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	seedClick(t, pool, linkID)
	seedClick(t, pool, linkID)
	// A stored idempotent create response holds the destination too; so does
	// another user's unrelated record, which must survive.
	if _, err := pool.Exec(ctx,
		`INSERT INTO idempotency_keys (user_id, key, method, path, request_sha256, claim_id, status, response_body)
		 VALUES ($1,'k1','POST','/api/links','\x00','c',201,$3), ($2,'k2','POST','/api/links','\x00','c',201,$3)`,
		alice, bob, rec.Body.Bytes()); err != nil {
		t.Fatal(err)
	}
	other := seedLink(t, pool, alice, "other-keep", "https://example.com/keep")
	seedClick(t, pool, other)
	if countWhere(t, pool, `SELECT COUNT(*) FROM audit_log WHERE metadata->>'destination_url' = $1`, purgeSecretURL) == 0 {
		t.Fatal("setup: link.created audit entry should hold the destination before the purge")
	}

	confirm := map[string]string{"X-Confirm-Purge": "inv-purge"}
	// Confirming header required.
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", rw, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no header = %d, want 400", rec.Code)
	}
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", rw, "", map[string]string{"X-Confirm-Purge": "wrong"}); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong header = %d, want 400", rec.Code)
	}
	// Read-only token: 403. Another user's token: 404.
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", ro, "", confirm); rec.Code != http.StatusForbidden {
		t.Errorf("read-only = %d, want 403", rec.Code)
	}
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", bobTok, "", confirm); rec.Code != http.StatusNotFound {
		t.Errorf("not owner = %d, want 404", rec.Code)
	}
	if countWhere(t, pool, `SELECT COUNT(*) FROM links WHERE key='inv-purge'`) != 1 {
		t.Fatal("link must survive every refused purge")
	}
	if ev.count("inv-purge") != 0 || purged != 0 {
		t.Error("cache evicted or OnPurge fired by a refused purge")
	}

	// The real purge.
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", rw, "", confirm); rec.Code != http.StatusOK {
		t.Fatalf("purge = %d %s", rec.Code, rec.Body)
	}
	if countWhere(t, pool, `SELECT COUNT(*) FROM links WHERE key='inv-purge'`) != 0 {
		t.Error("link row still present")
	}
	if countWhere(t, pool, `SELECT COUNT(*) FROM clicks WHERE link_id=$1`, linkID) != 0 {
		t.Error("clicks still present")
	}
	if countWhere(t, pool, `SELECT COUNT(*) FROM clicks WHERE link_id=$1`, other) != 1 {
		t.Error("another link's clicks were touched")
	}
	if ev.count("inv-purge") != 1 {
		t.Errorf("cache evictions = %d, want 1", ev.count("inv-purge"))
	}
	if purged != 1 {
		t.Errorf("OnPurge calls = %d, want 1", purged)
	}
	// The secret is nowhere in audit_log (metadata cast to text catches any field).
	if n := countWhere(t, pool, `SELECT COUNT(*) FROM audit_log WHERE metadata::text LIKE '%SECRET-TOKEN%'`); n != 0 {
		t.Errorf("%d audit entries still hold the secret", n)
	}
	created := lastAuditFor(t, pool, audit.ActionLinkCreated)
	if created.Metadata["key"] != "inv-purge" || created.Metadata["destination_redacted"] != true {
		t.Errorf("link.created entry = %v, want key kept and destination_redacted", created.Metadata)
	}
	p := lastAuditFor(t, pool, audit.ActionLinkPurged)
	if p.Metadata["key"] != "inv-purge" || p.Metadata["title"] != "Invite" {
		t.Errorf("link.purged metadata = %v", p.Metadata)
	}
	if _, has := p.Metadata["destination_url"]; has {
		t.Error("link.purged holds the destination")
	}
	if p.ActorID == nil || *p.ActorID != alice {
		t.Errorf("link.purged actor = %v, want alice", p.ActorID)
	}
	// Idempotency: owner's record gone, the other user's kept.
	if countWhere(t, pool, `SELECT COUNT(*) FROM idempotency_keys WHERE user_id=$1`, alice) != 0 {
		t.Error("owner's idempotency record still holds the response")
	}
	if countWhere(t, pool, `SELECT COUNT(*) FROM idempotency_keys WHERE user_id=$1`, bob) != 1 {
		t.Error("another user's idempotency record was deleted")
	}
	// The key is tombstoned: creating a custom alias with it is a 409.
	rec = purgeReq(h, "POST", "/api/links", rw, `{"destination_url":"https://example.com/new","key":"inv-purge"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("reusing a purged key = %d, want 409", rec.Code)
	}
	// Purging again is a 404.
	if rec := purgeReq(h, "DELETE", "/api/links/inv-purge?purge=true", rw, "", confirm); rec.Code != http.StatusNotFound {
		t.Errorf("second purge = %d, want 404", rec.Code)
	}
	// A plain DELETE still only deactivates.
	if rec := purgeReq(h, "DELETE", "/api/links/other-keep", rw, "", nil); rec.Code != http.StatusOK ||
		countWhere(t, pool, `SELECT COUNT(*) FROM links WHERE key='other-keep' AND NOT active`) != 1 {
		t.Errorf("plain DELETE = %d, link should be inactive", rec.Code)
	}
}

func TestPurgeAfterExpiry_ValidationAndSweeper(t *testing.T) {
	pool := filterTestPool(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `TRUNCATE purged_keys`) })
	h := purgeMux(t, pool, &evictRecorder{})
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)

	// Needs an expiry, and a sane range.
	if rec := purgeReq(h, "POST", "/api/links", tok, `{"destination_url":"https://example.com/a","purge_after_expiry_days":3}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no expiry = %d, want 400", rec.Code)
	}
	exp := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	if rec := purgeReq(h, "POST", "/api/links", tok, `{"destination_url":"https://example.com/a","expires_at":"`+exp+`","purge_after_expiry_days":0}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("zero days = %d, want 400", rec.Code)
	}
	// Created with the field, it round-trips.
	rec := purgeReq(h, "POST", "/api/links", tok, `{"destination_url":"https://example.com/due?t=SECRET-TOKEN-9","key":"due","expires_at":"`+exp+`","purge_after_expiry_days":1}`, nil)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"purge_after_expiry_days":1`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	// Set on an existing link through PATCH, then cleared with null.
	seedLink(t, pool, alice, "later", "https://example.com/later")
	if rec := purgeReq(h, "PATCH", "/api/links/later", tok, `{"purge_after_expiry_days":5}`, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("patch without expiry = %d, want 400", rec.Code)
	}
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	if rec := purgeReq(h, "PATCH", "/api/links/later", tok, `{"expires_at":"`+future+`","purge_after_expiry_days":5}`, nil); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"purge_after_expiry_days":5`) {
		t.Fatalf("patch = %d %s", rec.Code, rec.Body)
	}
	// An off-by-default link that expired long ago is never purged.
	seedLink(t, pool, alice, "plain", "https://example.com/plain")
	if _, err := pool.Exec(ctx, `UPDATE links SET expires_at = now() - interval '400 days' WHERE key='plain'`); err != nil {
		t.Fatal(err)
	}

	// Expired 2 days ago with purge after 1 day: due. "later" is not due yet.
	purged, err := links.NewStore(pool).WithAudit(audit.New(pool)).PurgeExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if len(purged) != 1 || purged[0].Key != "due" {
		t.Fatalf("purged = %+v, want only due", purged)
	}
	for key, want := range map[string]int{"due": 0, "later": 1, "plain": 1} {
		if got := countWhere(t, pool, `SELECT COUNT(*) FROM links WHERE key=$1`, key); got != want {
			t.Errorf("links %q count = %d, want %d", key, got, want)
		}
	}
	if n := countWhere(t, pool, `SELECT COUNT(*) FROM audit_log WHERE metadata::text LIKE '%SECRET-TOKEN-9%'`); n != 0 {
		t.Errorf("%d audit entries hold the secret after the sweep", n)
	}
	p := lastAuditFor(t, pool, audit.ActionLinkPurged)
	if p.ActorID != nil || p.Metadata["via"] != "retention" || p.Metadata["key"] != "due" {
		t.Errorf("sweeper audit entry = %+v", p)
	}
	// Later, "later" falls due too.
	later, _ := links.NewStore(pool).WithAudit(audit.New(pool)).PurgeExpired(ctx, time.Now().Add(10*24*time.Hour))
	if len(later) != 1 || later[0].Key != "later" {
		t.Errorf("second sweep purged %+v, want later", later)
	}
}
