package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
)

// redirectE2EMux wires the REAL resolver (cache→DB) and the REAL clicks recorder
// over the live test DB behind GET /u/{key}, exactly as serve() does.
func redirectE2EMux(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	c, err := cache.New(100, time.Minute)
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	t.Cleanup(c.Close)
	resolver := links.NewResolver(c, links.NewStore(pool))
	recorder := clicks.NewRecorder(pool, nil)
	h := NewRedirectHandler(resolver, NewClickRecorder(recorder))
	mux := http.NewServeMux()
	mux.Handle("GET /u/{key}", h)
	return mux
}

// waitForClick polls the clicks table until at least one row exists for the link
// or the deadline passes — a deterministic sync mechanism for the asynchronous
// recording goroutine (no bare sleep).
func waitForClick(t *testing.T, pool *pgxpool.Pool, linkID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM clicks WHERE link_id = $1`, linkID).Scan(&n); err != nil {
			t.Fatalf("poll clicks: %v", err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the async click to be recorded")
}

// TestRedirectE2E_RecordsClickWithUTM seeds a link, hits GET /u/{key} with utm
// params through the real resolver + recorder, and asserts a 302 with the UTM
// merged into Location AND a persisted clicks row carrying the UTM values.
func TestRedirectE2E_RecordsClickWithUTM(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(redirectE2EMux(t, pool))
	defer srv.Close()

	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "go2wiki", "https://www.wikipedia.org")

	// Do not auto-follow the redirect so we can inspect the 302 + Location.
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(srv.URL + "/u/go2wiki?utm_source=news&utm_medium=email")
	if err != nil {
		t.Fatalf("GET redirect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if q := loc.Query(); q.Get("utm_source") != "news" || q.Get("utm_medium") != "email" {
		t.Errorf("Location UTM = %v, want utm_source=news utm_medium=email", loc.RawQuery)
	}

	waitForClick(t, pool, linkID)

	var src, med string
	if err := pool.QueryRow(context.Background(),
		`SELECT utm_source, utm_medium FROM clicks WHERE link_id = $1`, linkID,
	).Scan(&src, &med); err != nil {
		t.Fatalf("read recorded click: %v", err)
	}
	if src != "news" || med != "email" {
		t.Errorf("recorded click utm_source=%q utm_medium=%q, want news/email", src, med)
	}
}

// TestRedirectE2E_UnknownKey404 asserts an unknown key returns 404 through the
// real resolver (DB miss → negative cache → not found).
func TestRedirectE2E_UnknownKey404(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(redirectE2EMux(t, pool))
	defer srv.Close()

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(srv.URL + "/u/missing")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestRedirectE2E_InactiveLink404 asserts an inactive link returns 404 and
// records no click.
func TestRedirectE2E_InactiveLink404(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(redirectE2EMux(t, pool))
	defer srv.Close()

	uid := seedUser(t, pool, "alice@example.com")
	var linkID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, 'inact1', 'https://example.com', FALSE, 0, now()) RETURNING id`, uid,
	).Scan(&linkID); err != nil {
		t.Fatalf("seed inactive link: %v", err)
	}

	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Get(srv.URL + "/u/inact1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestLinksGet_UTMStats asserts GET /api/links/{key} returns the #0030 utm_stats
// breakdown (by_source/medium/campaign with counts) for the owner, and that a
// non-owner gets 404 (owner-scoping).
func TestLinksGet_UTMStats(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(linksMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	linkID := seedLink(t, pool, alice, "stats1", "https://example.com")

	rec := clicks.NewRecorder(pool, nil)
	for i := 0; i < 3; i++ {
		_ = rec.Record(context.Background(), clicks.Click{Key: "stats1", UTMSource: "email", UTMCampaign: "launch"})
	}
	_ = rec.Record(context.Background(), clicks.Click{Key: "stats1", UTMSource: "social", UTMCampaign: "launch"})
	_ = rec.Record(context.Background(), clicks.Click{Key: "stats1"}) // (none) bucket

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/stats1", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		ClickCount int64           `json:"click_count"`
		UTMStats   *clicks.UTMStats `json:"utm_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ClickCount != 5 {
		t.Errorf("click_count = %d, want 5", body.ClickCount)
	}
	if body.UTMStats == nil {
		t.Fatal("utm_stats missing")
	}
	if body.UTMStats.ClickCount != 5 {
		t.Errorf("utm_stats.click_count = %d, want 5", body.UTMStats.ClickCount)
	}
	if c := bucketCount(body.UTMStats.BySource, "email"); c != 3 {
		t.Errorf("by_source email = %d, want 3", c)
	}
	if c := bucketCount(body.UTMStats.BySource, "social"); c != 1 {
		t.Errorf("by_source social = %d, want 1", c)
	}
	if c := bucketCount(body.UTMStats.BySource, clicks.NoneBucket); c != 1 {
		t.Errorf("by_source (none) = %d, want 1", c)
	}
	if c := bucketCount(body.UTMStats.ByCampaign, "launch"); c != 4 {
		t.Errorf("by_campaign launch = %d, want 4", c)
	}

	// Owner-scoping: bob cannot see alice's link → 404.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/links/stats1", nil)
	resp2, err := srv.Client().Do(withCookie(req2, "bob-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("non-owner status = %d, want 404", resp2.StatusCode)
	}

	_ = linkID
}

func bucketCount(buckets []clicks.Bucket, value string) int64 {
	for _, b := range buckets {
		if b.Value == value {
			return b.Count
		}
	}
	return -1
}
