package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestExternalID_InactiveLinkIsNotRevived: re-creating a deactivated link by
// the same destination and external_id returns it unchanged and still
// inactive; without an id the old reactivation still happens.
func TestExternalID_InactiveLinkIsNotRevived(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	const body = `{"destination_url":"https://example.com/inv","external_id":"inv-1"}`

	_, first := extReq(t, srv, "alice-token", "POST", "/api/links", body)
	key := first["key"].(string)
	if st, _ := extReq(t, srv, "alice-token", "DELETE", "/api/links/"+key, ""); st != http.StatusOK {
		t.Fatalf("deactivate = %d", st)
	}
	st, again := extReq(t, srv, "alice-token", "POST", "/api/links", body)
	if st != http.StatusCreated || again["key"] != key || again["duplicate"] != true || again["active"] != false {
		t.Errorf("recreate = %d %v, want same key, duplicate true, active false", st, again)
	}
	if _, _, active, _ := linkRow(t, pool, key); active {
		t.Errorf("link was reactivated in the database")
	}

	// no external_id: the long-standing reactivation is unchanged
	_, p := extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/plain"}`)
	extReq(t, srv, "alice-token", "DELETE", "/api/links/"+p["key"].(string), "")
	_, pa := extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/plain"}`)
	if pa["key"] != p["key"] || pa["active"] != true {
		t.Errorf("plain recreate = %v, want reactivated", pa)
	}
}

// TestExternalID_DeniedLinkCannotTakeOne: PATCH refuses a non-empty id on a
// link the URL filter denied; clearing is still allowed.
func TestExternalID_DeniedLinkCannotTakeOne(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at) VALUES ($1, 'deny01', 'https://evil.example/x', FALSE, 1, now())`, alice); err != nil {
		t.Fatalf("seed denied link: %v", err)
	}
	if st, m := extReq(t, srv, "alice-token", "PATCH", "/api/links/deny01", `{"external_id":"x"}`); st != http.StatusBadRequest {
		t.Errorf("set on denied = %d %v, want 400", st, m)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM links WHERE key = 'deny01' AND external_id IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Errorf("denied link got an external_id")
	}
	if st, _ := extReq(t, srv, "alice-token", "PATCH", "/api/links/deny01", `{"external_id":null}`); st != http.StatusOK {
		t.Errorf("clear on denied = %d, want 200", st)
	}
}

// TestExternalID_BatchRowsDifferingOnlyInID: rows that match in everything but
// external_id are two links, not an in-batch duplicate.
func TestExternalID_BatchRowsDifferingOnlyInID(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	cs := httptest.NewServer(campaignsMux(t, pool))
	defer cs.Close()
	_, c := extReq(t, cs, "alice-token", "POST", "/api/campaigns", `{"name":"Invites"}`)
	st, m := extReq(t, cs, "alice-token", "POST", "/api/campaigns/"+c["slug"].(string)+"/links/batch", `{"rows":[
		{"destination_url":"https://example.com/a","utm_source":"s","external_id":"r-1"},
		{"destination_url":"https://example.com/a","utm_source":"s","external_id":"r-2"}]}`)
	if st != http.StatusCreated || len(m["links"].([]any)) != 2 {
		t.Errorf("batch = %d %v", st, m)
	}
}
