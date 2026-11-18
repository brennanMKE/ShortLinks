package handlers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/idempotency"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// TestIdempotency_RealCreateRoute runs the real linksH.Create behind the auth
// guard and the idempotency wrapper (the same composition as main.go): a retry
// with the same key creates nothing and replays the first response.
func TestIdempotency_RealCreateRoute(t *testing.T) {
	pool := credsTestPool(t)
	h := NewLinksHandler(links.NewStore(pool), nil, nil, audit.New(pool), nil, nil, nil, testBaseURL)
	requireSession := middleware.RequireSession(auth.NewStore(pool))
	mux := http.NewServeMux()
	mux.Handle("POST /api/links",
		requireSession(idempotency.Middleware(idempotency.NewStore(pool))(http.HandlerFunc(h.Create))))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	post := func() (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links",
			jsonBody(`{"destination_url":"https://idem.example.com/page"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "retry-1")
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
	second, secondBody := post()
	if second.StatusCode != http.StatusCreated || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("second = %d replayed=%q body=%s", second.StatusCode, second.Header.Get("Idempotent-Replayed"), secondBody)
	}
	if secondBody != firstBody {
		t.Fatalf("replayed body differs:\n first: %s\nsecond: %s", firstBody, secondBody)
	}

	count := func(q string, args ...any) int {
		var n int
		if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM links WHERE user_id = $1`, alice); n != 1 {
		t.Errorf("links rows = %d; want 1", n)
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE action = $1`, audit.ActionLinkCreated); n != 1 {
		t.Errorf("link.created audit rows = %d; want 1", n)
	}
}
