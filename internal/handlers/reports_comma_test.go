package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// TestReports_FilterValueWithComma filters on utm_content values that contain
// a comma (#0187), end to end over HTTP: repeated parameters are whole values,
// a lone parameter takes \, for a literal comma, and the legacy comma list
// still works.
func TestReports_FilterValueWithComma(t *testing.T) {
	pool := credsTestPool(t)
	h := reportsMux(pool)
	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-session")
	linkID := seedLink(t, pool, alice, "ccc", "https://example.com/")
	for content, n := range map[string]int{"spring, 2026": 3, "spring": 2, "2026": 1} {
		for i := 0; i < n; i++ {
			if _, err := pool.Exec(context.Background(),
				`INSERT INTO clicks (link_id, clicked_at, is_bot, utm_content) VALUES ($1, $2, FALSE, $3)`,
				linkID, time.Now().Add(-time.Minute), content); err != nil {
				t.Fatalf("seed click: %v", err)
			}
		}
	}
	cookie := func(r *http.Request) { withCookie(r, "alice-session") }
	clicksFor := func(q string) int64 {
		rec := reportsGet(t, h, "/api/reports/overview?range=7d&"+q, cookie)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", q, rec.Code, rec.Body)
		}
		var env struct {
			Data struct {
				Totals struct {
					Clicks int64 `json:"clicks"`
				} `json:"totals"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		return env.Data.Totals.Clicks
	}
	cases := []struct {
		name, q string
		want    int64
	}{
		{"escaped lone value", "content=" + url.QueryEscape(`spring\, 2026`), 3},
		{"repeated, whole values", "content=" + url.QueryEscape("spring, 2026") + "&content=2026", 4},
		{"repeated, one with a comma", "content=" + url.QueryEscape("spring, 2026") + "&content=spring", 5},
		{"legacy comma list", "content=spring,2026", 3},
		{"unescaped lone value is the legacy list", "content=" + url.QueryEscape("spring, 2026"), 3},
	}
	for _, tc := range cases {
		if got := clicksFor(tc.q); got != tc.want {
			t.Errorf("%s (%s) = %d, want %d", tc.name, tc.q, got, tc.want)
		}
	}
}
