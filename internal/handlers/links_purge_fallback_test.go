package handlers

import (
	"context"
	"net/http"
	"testing"
)

// Audit redaction also covers the #0180 fallback URLs, and a purged link's
// external_id (#0182) becomes free again: unlike a key, it is never printed on a
// short URL, so nothing outside the database depends on it staying unused.
func TestPurgeLink_RedactsFallbackURLsAndFreesExternalID(t *testing.T) {
	pool := filterTestPool(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `TRUNCATE purged_keys`) })
	h := purgeMux(t, pool, &evictRecorder{})
	alice := seedUser(t, pool, "alice@example.com")
	tok := mintToken(t, pool, alice, "rw", false)

	rec := purgeReq(h, "POST", "/api/links", tok,
		`{"destination_url":"https://example.com/d?t=SECRET-DEST","key":"fb-purge","external_id":"inv-42","fallback_url":"https://example.com/f?t=SECRET-FALLBACK"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM links WHERE key='fb-purge'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Entries that carry the fallback URLs, as a future or external writer might.
	if _, err := pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, user_id, action, target_type, target_id, metadata) VALUES
		 ($1,$1,'link.updated','link',$2,'{"key":"fb-purge","fallback_url":"https://example.com/f?t=SECRET-FALLBACK"}'),
		 ($1,$1,'link.updated','link',$2,'{"key":"fb-purge","default_fallback_url":"https://example.com/c?t=SECRET-DEFAULT","title":"kept"}')`,
		alice, id); err != nil {
		t.Fatal(err)
	}
	if rec := purgeReq(h, "DELETE", "/api/links/fb-purge?purge=true", tok, "", map[string]string{"X-Confirm-Purge": "fb-purge"}); rec.Code != http.StatusOK {
		t.Fatalf("purge = %d %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"SECRET-DEST", "SECRET-FALLBACK", "SECRET-DEFAULT"} {
		if n := countWhere(t, pool, `SELECT COUNT(*) FROM audit_log WHERE metadata::text LIKE '%'||$1||'%'`, secret); n != 0 {
			t.Errorf("%d audit entries still hold %s", n, secret)
		}
	}
	if n := countWhere(t, pool, `SELECT COUNT(*) FROM audit_log WHERE action='link.updated' AND metadata->>'title'='kept' AND metadata->>'destination_redacted'='true'`); n != 1 {
		t.Errorf("redacted entry should keep its other fields and carry destination_redacted (got %d)", n)
	}

	// The external_id is free again; the key is not.
	rec = purgeReq(h, "POST", "/api/links", tok, `{"destination_url":"https://example.com/new","external_id":"inv-42"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Errorf("reusing a purged link's external_id = %d %s, want 201", rec.Code, rec.Body)
	}
	rec = purgeReq(h, "POST", "/api/links", tok, `{"destination_url":"https://example.com/new2","key":"fb-purge"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("reusing the purged key = %d, want 409", rec.Code)
	}
}
