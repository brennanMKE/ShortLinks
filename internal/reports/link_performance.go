package reports

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

const defaultLinkBreakdownLimit = 10

// PerfLink identifies the link a link-performance report is about.
type PerfLink struct {
	Key            string  `json:"key"`
	ShortURL       string  `json:"short_url"`
	Title          string  `json:"title"`
	Label          string  `json:"label"`
	ContentKey     string  `json:"content_key"`
	DestinationURL string  `json:"destination_url"`
	CampaignSlug   *string `json:"campaign_slug"`
	Active         bool    `json:"active"`
}

// LinkPerfTotals is the link-performance summary.
type LinkPerfTotals struct {
	Clicks int64 `json:"clicks"`
	// PreviousClicks is null when there is no previous window.
	PreviousClicks *int64 `json:"previous_clicks"`
	// AllTime and LastClickedAt ignore the window and the UTM filters (bots
	// still follow include_bots); LastClickedAt is null for a link never clicked.
	AllTime       int64      `json:"all_time"`
	LastClickedAt *time.Time `json:"last_clicked_at"`
}

// Sibling is another link of the caller's to the same content.
type Sibling struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	CampaignSlug *string `json:"campaign_slug"`
	Active       bool    `json:"active"`
	Clicks       int64   `json:"clicks"`
}

// LinkPerformanceData is the link-performance payload.
type LinkPerformanceData struct {
	Link               PerfLink       `json:"link"`
	Totals             LinkPerfTotals `json:"totals"`
	Timeseries         []DayCount     `json:"timeseries"`
	PreviousTimeseries []DayCount     `json:"previous_timeseries"`
	BySource           []BreakdownRow `json:"by_source"`
	ByMedium           []BreakdownRow `json:"by_medium"`
	ByContent          []BreakdownRow `json:"by_content"`
	ByReferer          []BreakdownRow `json:"by_referer"`
	Siblings           []Sibling      `json:"siblings"`
	Truncated          bool           `json:"truncated,omitempty"`
}

var linkPerformanceReport = Report{
	Name: "link-performance",
	Description: "One link in a single call: clicks against the previous period, all-time clicks and last click, daily series, breakdowns by source, medium, content and referrer, " +
		"and the caller's other links to the same content (any campaign).",
	Params: []ParamSpec{
		{Name: "key", Type: "string", Required: true, Description: "Link key. Unknown or not yours: 404 link_not_found."},
	},
	DefaultLimit: defaultLinkBreakdownLimit,
	Run:          runLinkPerformance,
}

// linkBreakdown lists the clicks of link id per value of dimension d.
func linkBreakdown(ctx context.Context, req *Request, id int64, d string, w Window) (map[string]int64, error) {
	ds, _ := dimensionSQL(d)
	sc := req.Params.ScopedQuery(req.UserID, w, ScopeOpts{Where: fmt.Sprintf("l.id = %d", id)})
	rows, err := req.Q.Query(ctx, `SELECT `+ds.value+`, count(*)`+sc.SQL+` GROUP BY 1`, sc.Args...)
	if err != nil {
		return nil, fmt.Errorf("reports: link %s: %w", d, err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var v string
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			return nil, fmt.Errorf("reports: link %s scan: %w", d, err)
		}
		out[v] = n
	}
	return out, rows.Err()
}

// linkRows builds the ranked breakdown rows for dimension d, with the previous
// window beside them unless it cannot be compared.
func linkRows(ctx context.Context, req *Request, id int64, d string, limit int) ([]BreakdownRow, error) {
	cur, err := linkBreakdown(ctx, req, id, d, req.Windows.Current)
	if err != nil {
		return nil, err
	}
	hasPrev := req.Windows.Previous != nil && !(d == "referer" && refererExpired(req, *req.Windows.Previous))
	var prev map[string]int64
	if hasPrev {
		if prev, err = linkBreakdown(ctx, req, id, d, *req.Windows.Previous); err != nil {
			return nil, err
		}
	}
	bs := make([]bucket, 0, len(cur))
	var total int64
	for v, n := range cur {
		bs = append(bs, bucket{value: v, clicks: n})
		total += n
	}
	sortBuckets(bs)
	rows := []BreakdownRow{}
	for i, b := range bs {
		if i >= limit {
			break
		}
		r := BreakdownRow{Value: b.value, Clicks: b.clicks, Share: math.Round(float64(b.clicks)/float64(total)*1000) / 1000}
		if hasPrev {
			r.PreviousClicks = prevPtr(prev[b.value], true)
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func runLinkPerformance(ctx context.Context, req *Request) (Output, error) {
	key, err := req.RequiredString("key")
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultLinkBreakdownLimit)

	var id int64
	var title, dest, utmSource string
	var campaign *string
	var active bool
	err = req.Q.QueryRow(ctx,
		`SELECT l.id, COALESCE(l.title, ''), l.destination_url, COALESCE(l.utm_source, ''), c.slug, COALESCE(l.active, TRUE)
		   FROM links l LEFT JOIN campaigns c ON c.id = l.campaign_id WHERE l.user_id = $1 AND l.key = $2`,
		req.UserID, key).Scan(&id, &title, &dest, &utmSource, &campaign, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Output{}, &NotFoundError{Code: "link_not_found", Message: "no link with that key"}
	}
	if err != nil {
		return Output{}, fmt.Errorf("reports: link lookup: %w", err)
	}
	_, label := LinkLabel(title, utmSource)
	if label == "" {
		label = key
	}
	data := LinkPerformanceData{Link: PerfLink{Key: key, ShortURL: qr.ShortURL(req.BaseURL, key), Title: title, Label: label,
		ContentKey: ContentKey(dest), DestinationURL: dest, CampaignSlug: campaign, Active: active}}
	where := fmt.Sprintf("l.id = %d", id)

	cur := req.Windows.Current
	byDay := map[string]int64{}
	if err := dayRowsWhere(ctx, req, cur, ScopeOpts{Where: where}, "'all'", "''", func(_, _, day string, n int64) { byDay[day] += n }); err != nil {
		return Output{}, err
	}
	for _, d := range cur.DayList() {
		data.Timeseries = append(data.Timeseries, DayCount{Date: d, Clicks: byDay[d]})
	}
	data.Totals.Clicks = sumDays(data.Timeseries)
	if req.Windows.Previous != nil {
		if data.PreviousTimeseries, err = previousSeriesWhere(ctx, req, "day", where); err != nil {
			return Output{}, err
		}
		pc := sumDays(data.PreviousTimeseries)
		data.Totals.PreviousClicks = &pc
	}

	botCond := "AND k.is_bot = FALSE"
	if req.Params.IncludeBots {
		botCond = ""
	}
	if err := req.Q.QueryRow(ctx, `SELECT count(*), max(k.clicked_at) FROM clicks k WHERE k.link_id = $1 `+botCond, id).
		Scan(&data.Totals.AllTime, &data.Totals.LastClickedAt); err != nil {
		return Output{}, fmt.Errorf("reports: link all-time: %w", err)
	}
	if data.Totals.LastClickedAt != nil {
		t := data.Totals.LastClickedAt.UTC().Truncate(time.Second)
		data.Totals.LastClickedAt = &t
	}

	for _, b := range []struct {
		dim string
		dst *[]BreakdownRow
	}{{"source", &data.BySource}, {"medium", &data.ByMedium}, {"content", &data.ByContent}, {"referer", &data.ByReferer}} {
		if *b.dst, err = linkRows(ctx, req, id, b.dim, limit); err != nil {
			return Output{}, err
		}
	}

	set, err := loadLinks(ctx, req, true)
	if err != nil {
		return Output{}, err
	}
	data.Truncated = set.truncated
	data.Siblings = []Sibling{}
	ck := ContentKey(dest)
	for _, li := range set.links {
		if li.id == id || li.contentKey != ck {
			continue
		}
		lab := li.label
		if lab == "" {
			lab = li.key
		}
		data.Siblings = append(data.Siblings, Sibling{Key: li.key, Label: lab, CampaignSlug: li.campaign, Active: li.active, Clicks: li.clicks})
	}
	sort.Slice(data.Siblings, func(i, j int) bool {
		a, b := data.Siblings[i], data.Siblings[j]
		if a.Clicks != b.Clicks {
			return a.Clicks > b.Clicks
		}
		return a.Key < b.Key
	})
	return Output{Highlights: linkPerfHighlights(data, req), Data: data}, nil
}

func linkPerfHighlights(d LinkPerformanceData, req *Request) []string {
	cur := req.Windows.Current
	t := d.Totals
	name := d.Link.Label
	var out []string
	if t.Clicks == 0 {
		out = []string{fmt.Sprintf("%s had no clicks between %s and %s.", name, cur.From, cur.To)}
	} else {
		s := fmt.Sprintf("%s: %s %s between %s and %s", name, commas(t.Clicks), plural(t.Clicks, "click", "clicks"), cur.From, cur.To)
		if t.PreviousClicks != nil {
			s += ", " + delta(t.Clicks, *t.PreviousClicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
		}
		out = append(out, s+".")
		if len(d.BySource) > 0 {
			top := d.BySource[0]
			out = append(out, fmt.Sprintf("Top source: %s (%d%% of clicks).", rowName(top.Value, ""), pct(top.Clicks, t.Clicks)))
		}
		if len(d.ByReferer) > 0 && d.ByReferer[0].Value != DirectValue {
			top := d.ByReferer[0]
			out = append(out, fmt.Sprintf("Top referrer: %s (%d%% of clicks).", top.Value, pct(top.Clicks, t.Clicks)))
		}
	}
	if t.LastClickedAt != nil {
		out = append(out, fmt.Sprintf("%s all-time %s; last clicked %s.", commas(t.AllTime), plural(t.AllTime, "click", "clicks"), t.LastClickedAt.Format(time.RFC3339)))
	} else {
		out = append(out, "This link has never been clicked.")
	}
	if n := len(d.Siblings); n > 0 {
		var sum int64
		for _, s := range d.Siblings {
			sum += s.Clicks
		}
		best := d.Siblings[0]
		out = append(out, fmt.Sprintf("%d other %s to the same content got %s %s in the window; best is %s (%s).",
			n, plural(int64(n), "link", "links"), commas(sum), plural(sum, "click", "clicks"), best.Label, commas(best.Clicks)))
	}
	if t.Clicks > 0 {
		if req.Windows.Previous != nil && refererExpired(req, *req.Windows.Previous) {
			out = append(out, refererPrevUnavailable)
		}
		if refererExpired(req, cur) {
			out = append(out, refererCurUndercount)
		}
	}
	return truncNote(out, d.Truncated)
}
