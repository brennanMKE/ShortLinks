package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/idempotency"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

func isBase62String(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// TestLinksCreate_KeyLength (#0179) asserts key_length controls the generated
// key length across its whole range, and that out-of-range values are a 400.
func TestLinksCreate_KeyLength(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	for i, n := range []string{"6", "12", "21", "32"} {
		body := `{"destination_url":"https://example.com/` + n + `","key_length":` + n + `}`
		got, status := postLink(t, srv, "alice-token", body)
		if status != http.StatusCreated {
			t.Fatalf("key_length=%s: status = %d, want 201", n, status)
		}
		want := []int{6, 12, 21, 32}[i]
		if len(got.Key) != want || !isBase62String(got.Key) {
			t.Errorf("key_length=%s: key = %q, want %d base-62 chars", n, got.Key, want)
		}
	}

	for _, n := range []string{"0", "5", "33", "-1", "1000"} {
		body := `{"destination_url":"https://example.com/bad","key_length":` + n + `}`
		if _, status := postLink(t, srv, "alice-token", body); status != http.StatusBadRequest {
			t.Errorf("key_length=%s: status = %d, want 400", n, status)
		}
	}
}

// TestLinksCreate_CustomKeyLimit (#0179): a 32-character custom key is accepted
// and 33 is rejected.
func TestLinksCreate_CustomKeyLimit(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	k32 := strings.Repeat("aB3_-", 6) + "xy"
	got, status := postLink(t, srv, "alice-token", `{"destination_url":"https://example.com/a","key":"`+k32+`"}`)
	if status != http.StatusCreated || got.Key != k32 {
		t.Fatalf("32-char key: status = %d, key = %q, want 201 %q", status, got.Key, k32)
	}
	if _, status := postLink(t, srv, "alice-token", `{"destination_url":"https://example.com/b","key":"`+k32+`z"}`); status != http.StatusBadRequest {
		t.Errorf("33-char key: status = %d, want 400", status)
	}
}

// TestLinksCreate_InviteLink (#0179) exercises the CommunityPlatform invite
// request through the real idempotency wrapper: a client-chosen 12-character
// base-62 key, Idempotency-Key invite-<id>, expires_at and UTM labels. A retry
// replays the 201 (not a 409). Then a redirect through the key.
func TestLinksCreate_InviteLink(t *testing.T) {
	pool := credsTestPool(t)
	h := NewLinksHandler(links.NewStore(pool), nil, nil, audit.New(pool), nil, nil, nil, testBaseURL)
	requireSession := middleware.RequireSession(auth.NewStore(pool))
	mux := http.NewServeMux()
	mux.Handle("POST /api/links",
		requireSession(idempotency.Middleware(idempotency.NewStore(pool))(http.HandlerFunc(h.Create))))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	redirect := httptest.NewServer(redirectE2EMux(t, pool))
	defer redirect.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	key := "q7Zr0TnKd3Vb"
	body := `{"destination_url":"https://app.coupledozen.com/join/abc","key":"` + key + `",` +
		`"expires_at":"2099-01-01T00:00:00Z","utm_source":"invite","utm_medium":"share"}`
	post := func() (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links", jsonBody(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "invite-8841")
		resp, err := srv.Client().Do(withCookie(req, "alice-token"))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	first, firstBody := post()
	if first.StatusCode != http.StatusCreated || first.Header.Get("Idempotent-Replayed") != "" {
		t.Fatalf("first = %d replayed=%q body=%s", first.StatusCode, first.Header.Get("Idempotent-Replayed"), firstBody)
	}
	for _, want := range []string{`"key":"` + key + `"`, `"utm_source":"invite"`, `"utm_medium":"share"`} {
		if !strings.Contains(firstBody, want) {
			t.Errorf("invite response missing %s: %s", want, firstBody)
		}
	}
	var created linkView
	if err := json.Unmarshal([]byte(firstBody), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC); created.ExpiresAt == nil || !created.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, want %v", created.ExpiresAt, want)
	}
	second, secondBody := post()
	if second.StatusCode != http.StatusCreated || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry = %d replayed=%q, want replayed 201 (body=%s)", second.StatusCode, second.Header.Get("Idempotent-Replayed"), secondBody)
	}
	if secondBody != firstBody {
		t.Errorf("replayed body differs:\n first: %s\nsecond: %s", firstBody, secondBody)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM links WHERE key = $1`, key).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows for key = %d, err = %v, want 1", n, err)
	}

	client := redirect.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(redirect.URL + "/u/" + key)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d, want 302", resp.StatusCode)
	}
	if loc, _ := url.Parse(resp.Header.Get("Location")); loc == nil || loc.Host != "app.coupledozen.com" {
		t.Errorf("Location = %q", resp.Header.Get("Location"))
	}
}

// TestRedirect_GeneratedLongKey (#0179): a generated 32-character key redirects.
func TestRedirect_GeneratedLongKey(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	redirect := httptest.NewServer(redirectE2EMux(t, pool))
	defer redirect.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	got, status := postLink(t, srv, "alice-token", `{"destination_url":"https://example.com/long","key_length":32}`)
	if status != http.StatusCreated || len(got.Key) != 32 {
		t.Fatalf("create: status = %d, key = %q", status, got.Key)
	}
	client := redirect.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(redirect.URL + "/u/" + got.Key)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("redirect status = %d, want 302", resp.StatusCode)
	}
}

// TestCampaignsBatchCreate_KeyLength (#0179): key_length applies to every row
// of a batch, and out-of-range values are a 400.
func TestCampaignsBatchCreate_KeyLength(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Invites"}`)

	rows := `"rows":[
		{"destination_url":"https://example.com/p","utm_source":"a","utm_medium":"x"},
		{"destination_url":"https://example.com/p","utm_source":"b","utm_medium":"x"}]`
	got, status := batchCreate(t, srv, "alice-token", c.Slug, `{`+rows+`,"key_length":16}`)
	if status != http.StatusCreated || len(got.Links) != 2 {
		t.Fatalf("status = %d, links = %d", status, len(got.Links))
	}
	for _, l := range got.Links {
		if len(l.Key) != 16 || !isBase62String(l.Key) {
			t.Errorf("key = %q, want 16 base-62 chars", l.Key)
		}
	}
	for _, n := range []string{"5", "33"} {
		if _, status := batchCreate(t, srv, "alice-token", c.Slug, `{`+rows+`,"key_length":`+n+`}`); status != http.StatusBadRequest {
			t.Errorf("key_length=%s: status = %d, want 400", n, status)
		}
	}
}

// TestLinksCreate_CustomKeyIgnoresKeyLength (#0179): key_length is not
// validated when a custom key is sent.
func TestLinksCreate_CustomKeyIgnoresKeyLength(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	got, status := postLink(t, srv, "alice-token", `{"destination_url":"https://example.com/c","key":"abc","key_length":99}`)
	if status != http.StatusCreated || got.Key != "abc" {
		t.Fatalf("status = %d, key = %q, want 201 abc", status, got.Key)
	}
}

// TestLinksCreate_KeyLengthSkipsShorterDuplicate (#0179): dedup reuses an
// existing link only when its key is at least key_length long.
func TestLinksCreate_KeyLengthSkipsShorterDuplicate(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	dest := `"destination_url":"https://example.com/dedup"`

	short, status := postLink(t, srv, "alice-token", `{`+dest+`}`)
	if status != http.StatusCreated || len(short.Key) != 6 {
		t.Fatalf("short create: status = %d key = %q", status, short.Key)
	}
	long, status := postLink(t, srv, "alice-token", `{`+dest+`,"key_length":16}`)
	if status != http.StatusCreated || len(long.Key) != 16 || long.ID == short.ID {
		t.Fatalf("long create: status = %d key = %q id %d vs %d, want a new 16-char link", status, long.Key, long.ID, short.ID)
	}
	if long.Duplicate == nil || *long.Duplicate {
		t.Errorf("long duplicate = %v, want false", long.Duplicate)
	}
	// Repeats dedup as before: 16 -> the long link, 6 or 12 -> the first
	// link that is long enough (the 6-character one for the default).
	again, _ := postLink(t, srv, "alice-token", `{`+dest+`,"key_length":16}`)
	if again.ID != long.ID || again.Duplicate == nil || !*again.Duplicate {
		t.Errorf("repeat key_length=16 = %+v, want duplicate of the 16-char link", again)
	}
	def, _ := postLink(t, srv, "alice-token", `{`+dest+`}`)
	if def.ID != short.ID {
		t.Errorf("default repeat id = %d, want the original %d", def.ID, short.ID)
	}
}
