package reports

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

// PublicWindow is the 7-day window a PublicActivity covers.
type PublicWindow struct {
	From           string `json:"from"`
	To             string `json:"to"`
	TZ             string `json:"tz"`
	PartialLastDay bool   `json:"partial_last_day"`
}

// PublicLink is one entry of the public top-links list. It deliberately has no
// destination, campaign, UTM or key fields: it is exactly what an anonymous
// visitor may see.
type PublicLink struct {
	Title    string `json:"title"`
	ShortURL string `json:"short_url"`
	Clicks   int64  `json:"clicks"`
}

// PublicActivity is the site-wide, anonymous summary shown on the public home
// page in activity mode (#0175).
type PublicActivity struct {
	Window      PublicWindow `json:"window"`
	TotalClicks int64        `json:"total_clicks"`
	Timeseries  []DayCount   `json:"timeseries"`
	TopLinks    []PublicLink `json:"top_links"`
}

// PublicSummary computes the instance-wide activity for the last 7 days (today
// included) in tzName, counting every user's links but never bots. TopLinks
// lists at most limit pieces of content (links sharing a ContentKey collapse
// into one entry that links to the most-clicked member), restricted to links a
// visitor can follow and that are meant to be public: active, not denied, part
// of a campaign, non-expiring and without an external id. Expiring links are
// usually invites or other private shares, and links outside a campaign are
// not curated, so neither is ever listed; a link with an external_id (#0182)
// is one a client created and tracks itself, such as an invitation, and is
// never listed either. It is NOT a
// registered report and is not reachable through /api/reports; it reveals only
// titles, short URLs and counts, never destinations, campaigns or UTM values.
func PublicSummary(ctx context.Context, q Querier, now time.Time, tzName, baseURL string, limit int) (*PublicActivity, error) {
	p, err := ParseParams(url.Values{"range": {"7d"}, "compare": {"none"}}, tzName)
	if err != nil {
		return nil, fmt.Errorf("reports: public summary params: %w", err)
	}
	w := ResolveWindows(p, now, nil).Current

	counts := map[string]int64{}
	rows, err := q.Query(ctx,
		`SELECT to_char(k.clicked_at AT TIME ZONE $3, 'YYYY-MM-DD'), count(*)
		 FROM clicks k
		 WHERE k.clicked_at >= $1 AND k.clicked_at < $2 AND NOT k.is_bot
		 GROUP BY 1`, w.Start, w.End, p.TZName)
	if err != nil {
		return nil, fmt.Errorf("reports: public daily clicks: %w", err)
	}
	for rows.Next() {
		var d string
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reports: public daily clicks scan: %w", err)
		}
		counts[d] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reports: public daily clicks: %w", err)
	}

	days := w.DayList()
	out := &PublicActivity{
		Window:     PublicWindow{From: w.From, To: w.To, TZ: w.TZName, PartialLastDay: w.PartialLastDay},
		Timeseries: make([]DayCount, 0, len(days)),
		TopLinks:   []PublicLink{},
	}
	for _, d := range days {
		out.Timeseries = append(out.Timeseries, DayCount{Date: d, Clicks: counts[d]})
		out.TotalClicks += counts[d]
	}
	if limit <= 0 {
		return out, nil
	}

	lrows, err := q.Query(ctx,
		`SELECT l.key, COALESCE(l.title, ''), l.destination_url, COALESCE(l.utm_source, ''), count(*) AS n
		 FROM clicks k JOIN links l ON l.id = k.link_id
		 WHERE k.clicked_at >= $1 AND k.clicked_at < $2 AND NOT k.is_bot
		   AND COALESCE(l.active, TRUE) AND l.denied_reason = 0
		   AND l.campaign_id IS NOT NULL AND l.expires_at IS NULL
		   AND l.external_id IS NULL
		 GROUP BY l.id
		 ORDER BY n DESC, l.key
		 LIMIT `+strconv.Itoa(maxLinkRows), w.Start, w.End)
	if err != nil {
		return nil, fmt.Errorf("reports: public top links: %w", err)
	}
	defer lrows.Close()

	type member struct {
		key, head string
		clicks    int64
	}
	groups := map[string][]member{}
	var order []string
	for lrows.Next() {
		var key, title, dest, src string
		var n int64
		if err := lrows.Scan(&key, &title, &dest, &src, &n); err != nil {
			return nil, fmt.Errorf("reports: public top links scan: %w", err)
		}
		ck := ContentKey(dest)
		if _, ok := groups[ck]; !ok {
			order = append(order, ck)
		}
		head, _ := LinkLabel(title, src)
		groups[ck] = append(groups[ck], member{key: key, head: head, clicks: n})
	}
	if err := lrows.Err(); err != nil {
		return nil, fmt.Errorf("reports: public top links: %w", err)
	}

	gs := make([]PublicLink, 0, len(order))
	for _, ck := range order {
		ms := groups[ck] // most-clicked member first, ties by key
		var total int64
		head := ""
		for _, m := range ms {
			total += m.clicks
			if head == "" {
				head = m.head
			}
		}
		short := qr.ShortURL(baseURL, ms[0].key)
		if head == "" {
			head = short
		}
		gs = append(gs, PublicLink{Title: head, ShortURL: short, Clicks: total})
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].Clicks != gs[j].Clicks {
			return gs[i].Clicks > gs[j].Clicks
		}
		return gs[i].ShortURL < gs[j].ShortURL
	})
	if len(gs) > limit {
		gs = gs[:limit]
	}
	out.TopLinks = gs
	return out, nil
}
