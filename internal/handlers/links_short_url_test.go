package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// shortURLReq sends one authenticated JSON request and returns the status and
// decoded body.
func shortURLReq(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s %s: decode %s: %v", method, path, raw, err)
	}
	return resp.StatusCode, m
}

// wantShortURL fails unless obj["short_url"] is testBaseURL/u/key.
func wantShortURL(t *testing.T, what string, obj any, key string) {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Fatalf("%s: not an object: %v", what, obj)
	}
	if m["key"] != key {
		t.Fatalf("%s: key = %v, want %s", what, m["key"], key)
	}
	if want := testBaseURL + "/u/" + key; m["short_url"] != want {
		t.Errorf("%s: short_url = %v, want %s", what, m["short_url"], want)
	}
}

// TestShortURL_EveryLinkResponse covers #0181: every link object the REST API
// returns carries short_url built from the (non-default) BASE_URL.
func TestShortURL_EveryLinkResponse(t *testing.T) {
	pool := credsTestPool(t)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	ls := httptest.NewServer(linksMux(t, pool))
	defer ls.Close()
	cs := httptest.NewServer(campaignsMux(t, pool))
	defer cs.Close()

	// create
	status, m := shortURLReq(t, ls, "POST", "/api/links", `{"destination_url":"https://example.com/a"}`)
	if status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, m)
	}
	alpha, _ := m["key"].(string)
	wantShortURL(t, "create", m, alpha)

	// duplicate create
	status, m = shortURLReq(t, ls, "POST", "/api/links", `{"destination_url":"https://example.com/a"}`)
	if status != http.StatusCreated || m["duplicate"] != true {
		t.Fatalf("duplicate create = %d %v", status, m)
	}
	wantShortURL(t, "duplicate", m, alpha)

	// get
	_, m = shortURLReq(t, ls, "GET", "/api/links/"+alpha, "")
	wantShortURL(t, "get", m, alpha)

	// patch
	status, m = shortURLReq(t, ls, "PATCH", "/api/links/"+alpha, `{"title":"Alpha"}`)
	if status != http.StatusOK {
		t.Fatalf("patch = %d %v", status, m)
	}
	wantShortURL(t, "patch", m, alpha)

	// list: plain (enriched) and grouped
	_, m = shortURLReq(t, ls, "GET", "/api/links", "")
	wantShortURL(t, "list", m["links"].([]any)[0], alpha)
	_, m = shortURLReq(t, ls, "GET", "/api/links?group=content", "")
	g := m["groups"].([]any)[0].(map[string]any)
	wantShortURL(t, "grouped list", g["links"].([]any)[0], alpha)

	// campaign: batch create, list links, get, assign
	_, c := shortURLReq(t, cs, "POST", "/api/campaigns", `{"name":"Invites"}`)
	slug, _ := c["slug"].(string)
	status, m = shortURLReq(t, cs, "POST", "/api/campaigns/"+slug+"/links/batch",
		`{"rows":[{"destination_url":"https://example.com/b","utm_source":"x"}]}`)
	if status != http.StatusCreated {
		t.Fatalf("batch = %d %v", status, m)
	}
	row := m["links"].([]any)[0]
	bkey := row.(map[string]any)["key"].(string)
	wantShortURL(t, "batch row", row, bkey)

	_, m = shortURLReq(t, cs, "GET", "/api/campaigns/"+slug+"/links", "")
	wantShortURL(t, "campaign links", m["links"].([]any)[0], bkey)
	_, m = shortURLReq(t, cs, "GET", "/api/campaigns/"+slug, "")
	wantShortURL(t, "campaign get", m["links"].([]any)[0], bkey)
	status, m = shortURLReq(t, cs, "POST", "/api/campaigns/"+slug+"/links", `{"keys":["`+alpha+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("assign = %d %v", status, m)
	}
	wantShortURL(t, "assign", m["links"].([]any)[0], alpha)
}
