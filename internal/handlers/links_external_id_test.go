package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// extReq sends one request as the given session token and returns the status
// and decoded body.
func extReq(t *testing.T, srv *httptest.Server, token, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	m := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestExternalID_CreateLookupConflictPatchScoping(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	// create with an id
	st, a := extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/inv","external_id":"invite 42/x"}`)
	if st != http.StatusCreated || a["external_id"] != "invite 42/x" {
		t.Fatalf("create = %d %v", st, a)
	}
	key := a["key"].(string)

	// a link without one reports null
	_, plain := extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/plain"}`)
	if v, present := plain["external_id"]; !present || v != nil {
		t.Errorf("plain external_id = %v (present %v), want null", v, present)
	}
	plainKey := plain["key"].(string)

	// lookup, both ways
	st, got := extReq(t, srv, "alice-token", "GET", "/api/links/by-external-id/"+url.PathEscape("invite 42/x"), "")
	if st != http.StatusOK || got["key"] != key || got["external_id"] != "invite 42/x" {
		t.Errorf("by-external-id = %d %v", st, got)
	}
	st, got = extReq(t, srv, "alice-token", "GET", "/api/links?external_id="+url.QueryEscape("invite 42/x"), "")
	if st != http.StatusOK || len(got["links"].([]any)) != 1 || got["links"].([]any)[0].(map[string]any)["key"] != key {
		t.Errorf("list filter = %d %v", st, got)
	}
	st, got = extReq(t, srv, "alice-token", "GET", "/api/links?external_id=nope", "")
	if st != http.StatusOK || len(got["links"].([]any)) != 0 {
		t.Errorf("list filter miss = %d %v, want empty list", st, got)
	}
	if st, _ = extReq(t, srv, "alice-token", "GET", "/api/links/by-external-id/nope", ""); st != http.StatusNotFound {
		t.Errorf("by-external-id miss = %d, want 404", st)
	}
	// the QR routes still work next to the lookup route
	if st, _ = extReq(t, srv, "alice-token", "GET", "/api/links/"+key+"/qr.svg", ""); st != http.StatusOK {
		t.Errorf("qr.svg = %d, want 200", st)
	}

	// reuse of an id: 409 with the existing key, whatever the destination
	st, c := extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/other","external_id":"invite 42/x"}`)
	if st != http.StatusConflict || c["error"] != "external_id_taken" || c["key"] != key {
		t.Errorf("reuse = %d %v, want 409 external_id_taken with key %s", st, c, key)
	}
	st, c = extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/o2","key":"mykey1","external_id":"invite 42/x"}`)
	if st != http.StatusConflict || c["error"] != "external_id_taken" || c["key"] != key {
		t.Errorf("custom-key reuse = %d %v", st, c)
	}

	// per-user scoping: bob may use the same id and cannot see alice's link
	st, b := extReq(t, srv, "bob-token", "POST", "/api/links", `{"destination_url":"https://example.com/inv","external_id":"invite 42/x"}`)
	if st != http.StatusCreated || b["key"] == key {
		t.Errorf("bob create = %d %v", st, b)
	}
	_, bg := extReq(t, srv, "bob-token", "GET", "/api/links/by-external-id/"+url.PathEscape("invite 42/x"), "")
	if bg["key"] == key {
		t.Errorf("bob's lookup returned alice's link")
	}
	if st, _ = extReq(t, srv, "bob-token", "GET", "/api/links/by-external-id/later", ""); st != http.StatusNotFound {
		t.Errorf("bob miss = %d", st)
	}

	// invalid ids
	for name, id := range map[string]string{
		"non-ASCII": `"café"`,
		"control":   `"tab\there"`,
		"129 chars": `"` + strings.Repeat("a", 129) + `"`,
	} {
		st, _ = extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/bad","external_id":`+id+`}`)
		if st != http.StatusBadRequest {
			t.Errorf("%s external_id = %d, want 400", name, st)
		}
	}
	if st, _ = extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/ok","external_id":"`+strings.Repeat("a", 128)+`"}`); st != http.StatusCreated {
		t.Errorf("128-char id = %d, want 201", st)
	}

	// PATCH: set, conflict, clear (null and "")
	st, set := extReq(t, srv, "alice-token", "PATCH", "/api/links/"+plainKey, `{"external_id":"later"}`)
	if st != http.StatusOK || set["external_id"] != "later" {
		t.Errorf("patch set = %d %v", st, set)
	}
	st, c = extReq(t, srv, "alice-token", "PATCH", "/api/links/"+plainKey, `{"external_id":"invite 42/x"}`)
	if st != http.StatusConflict || c["error"] != "external_id_taken" || c["key"] != key {
		t.Errorf("patch conflict = %d %v", st, c)
	}
	st, cl := extReq(t, srv, "alice-token", "PATCH", "/api/links/"+plainKey, `{"external_id":null}`)
	if st != http.StatusOK || cl["external_id"] != nil {
		t.Errorf("patch null = %d %v", st, cl)
	}
	extReq(t, srv, "alice-token", "PATCH", "/api/links/"+plainKey, `{"external_id":"again"}`)
	st, cl = extReq(t, srv, "alice-token", "PATCH", "/api/links/"+plainKey, `{"external_id":""}`)
	if st != http.StatusOK || cl["external_id"] != nil {
		t.Errorf("patch empty = %d %v", st, cl)
	}
	// an unrelated patch leaves it alone
	st, un := extReq(t, srv, "alice-token", "PATCH", "/api/links/"+key, `{"title":"t"}`)
	if st != http.StatusOK || un["external_id"] != "invite 42/x" {
		t.Errorf("unrelated patch = %d %v", st, un)
	}
	// a cleared id is free again
	if st, _ = extReq(t, srv, "alice-token", "PATCH", "/api/links/"+key, `{"external_id":null}`); st != http.StatusOK {
		t.Fatalf("clear = %d", st)
	}
	if st, _ = extReq(t, srv, "alice-token", "POST", "/api/links", `{"destination_url":"https://example.com/n","external_id":"invite 42/x"}`); st != http.StatusCreated {
		t.Errorf("reuse after clear = %d", st)
	}
}

func TestExternalID_DedupBypassForDifferentIds(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	const dest = `"destination_url":"https://example.com/family"`

	_, one := extReq(t, srv, "alice-token", "POST", "/api/links", `{`+dest+`,"external_id":"inv-1"}`)
	_, two := extReq(t, srv, "alice-token", "POST", "/api/links", `{`+dest+`,"external_id":"inv-2"}`)
	if one["key"] == two["key"] || two["duplicate"] != false {
		t.Fatalf("different ids merged: %v / %v", one, two)
	}
	// same destination and same id returns the same link
	_, again := extReq(t, srv, "alice-token", "POST", "/api/links", `{`+dest+`,"external_id":"inv-1"}`)
	if again["key"] != one["key"] || again["duplicate"] != true {
		t.Errorf("same id = %v, want duplicate of %v", again, one["key"])
	}
	// a plain create does not return, or merge into, an invitation link
	_, plain := extReq(t, srv, "alice-token", "POST", "/api/links", `{`+dest+`}`)
	if plain["key"] == one["key"] || plain["key"] == two["key"] || plain["external_id"] != nil {
		t.Errorf("plain create merged into an id link: %v", plain)
	}
	// and an id create does not merge into the plain link
	_, three := extReq(t, srv, "alice-token", "POST", "/api/links", `{`+dest+`,"external_id":"inv-3"}`)
	if three["key"] == plain["key"] {
		t.Errorf("id create merged into the plain link")
	}
}

func TestExternalID_BatchRows(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	ls := httptest.NewServer(linksMux(t, pool))
	defer ls.Close()
	cs := httptest.NewServer(campaignsMux(t, pool))
	defer cs.Close()
	_, c := extReq(t, cs, "alice-token", "POST", "/api/campaigns", `{"name":"Invites"}`)
	slug := c["slug"].(string)
	path := "/api/campaigns/" + slug + "/links/batch"

	st, m := extReq(t, cs, "alice-token", "POST", path, `{"rows":[
		{"destination_url":"https://example.com/a","utm_source":"a","external_id":"b-1"},
		{"destination_url":"https://example.com/a","utm_source":"b","external_id":"b-2"}]}`)
	if st != http.StatusCreated {
		t.Fatalf("batch = %d %v", st, m)
	}
	rows := m["links"].([]any)
	if rows[0].(map[string]any)["external_id"] != "b-1" || rows[1].(map[string]any)["external_id"] != "b-2" {
		t.Errorf("batch rows = %v", rows)
	}
	if st, got := extReq(t, ls, "alice-token", "GET", "/api/links/by-external-id/b-2", ""); st != http.StatusOK || got["key"] != rows[1].(map[string]any)["key"] {
		t.Errorf("lookup of batch row = %d %v", st, got)
	}

	// existing id: 409 and nothing created
	before := countUserURLLinks(t, pool, alice, "https://example.com/z")
	st, m = extReq(t, cs, "alice-token", "POST", path, `{"rows":[
		{"destination_url":"https://example.com/z","utm_source":"a","external_id":"fresh"},
		{"destination_url":"https://example.com/z","utm_source":"b","external_id":"b-1"}]}`)
	if st != http.StatusConflict || m["error"] != "external_id_taken" || m["key"] != rows[0].(map[string]any)["key"] {
		t.Errorf("batch conflict = %d %v", st, m)
	}
	if after := countUserURLLinks(t, pool, alice, "https://example.com/z"); after != before {
		t.Errorf("batch conflict created %d links, want none", after-before)
	}
	// the same id twice in one batch: 400
	st, _ = extReq(t, cs, "alice-token", "POST", path, `{"rows":[
		{"destination_url":"https://example.com/y","utm_source":"a","external_id":"dup"},
		{"destination_url":"https://example.com/y","utm_source":"b","external_id":"dup"}]}`)
	if st != http.StatusBadRequest {
		t.Errorf("in-batch duplicate id = %d, want 400", st)
	}
}
