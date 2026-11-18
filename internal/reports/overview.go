package reports

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/clicks"
)

// defaultFacetLimit caps each facet's value list unless limit is supplied.
const defaultFacetLimit = 50

// Totals is the overview's headline counts for the current window.
type Totals struct {
	Clicks       int64 `json:"clicks"`
	VisitorDays  int64 `json:"visitor_days"`
	ExcludedBots int64 `json:"excluded_bots"`
	LinksClicked int64 `json:"links_clicked"`
	LinksTotal   int64 `json:"links_total"`
}

// PreviousTotals are the comparison window's counts. VisitorDays is nil when
// the window reaches past the click-detail retention (ip_hash is gone).
type PreviousTotals struct {
	Clicks      int64  `json:"clicks"`
	VisitorDays *int64 `json:"visitor_days"`
}

// DayCount is one day of a timeseries.
type DayCount struct {
	Date   string `json:"date"`
	Clicks int64  `json:"clicks"`
}

// CampaignRow is one campaign's click count; Slug is nil for "No campaign".
type CampaignRow struct {
	Slug   *string `json:"slug"`
	Name   string  `json:"name"`
	Clicks int64   `json:"clicks"`
}

// FacetValue is one value of a dimension. Value is NoneValue for empty.
type FacetValue struct {
	Value  string `json:"value"`
	Clicks int64  `json:"clicks"`
}

// Facets holds the value list of every filterable dimension.
type Facets struct {
	Source      []FacetValue `json:"source"`
	Medium      []FacetValue `json:"medium"`
	UTMCampaign []FacetValue `json:"utm_campaign"`
	Content     []FacetValue `json:"content"`
	Term        []FacetValue `json:"term"`
	Placement   []FacetValue `json:"placement"`
}

// OverviewData is the overview report's data payload.
type OverviewData struct {
	Totals             Totals          `json:"totals"`
	Previous           *PreviousTotals `json:"previous"`
	Timeseries         []DayCount      `json:"timeseries"`
	PreviousTimeseries []DayCount      `json:"previous_timeseries"`
	BestDay            *DayCount       `json:"best_day"`
	Campaigns          []CampaignRow   `json:"campaigns"`
	Facets             Facets          `json:"facets"`
}

var overviewReport = Report{
	Name:        "overview",
	Description: "Clicks across all your links: totals, daily trend, previous-period comparison, campaigns and UTM facets.",
	// limit (a common parameter) caps the values in each facet.
	DefaultLimit: defaultFacetLimit,
	Run:          runOverview,
}

func runOverview(ctx context.Context, req *Request) (Output, error) {
	p := req.Params
	limit := p.LimitOr(defaultFacetLimit)
	cur := req.Windows.Current
	dayExpr := DayExpr
	visitors := `count(DISTINCT (k.ip_hash, ` + dayExpr + `)) FILTER (WHERE k.ip_hash IS NOT NULL)`

	scope := p.ScopedQuery(req.UserID, cur, ScopeOpts{})
	data := OverviewData{}

	// Totals.
	if err := req.Q.QueryRow(ctx,
		`SELECT count(*), `+visitors+`, count(DISTINCT l.id)`+scope.SQL, scope.Args...,
	).Scan(&data.Totals.Clicks, &data.Totals.VisitorDays, &data.Totals.LinksClicked); err != nil {
		return Output{}, fmt.Errorf("reports: overview totals: %w", err)
	}
	if !p.IncludeBots {
		bots := p.ScopedQuery(req.UserID, cur, ScopeOpts{Skip: []string{SkipBots}, Where: "k.is_bot = TRUE"})
		if err := req.Q.QueryRow(ctx, `SELECT count(*)`+bots.SQL, bots.Args...).Scan(&data.Totals.ExcludedBots); err != nil {
			return Output{}, fmt.Errorf("reports: overview excluded bots: %w", err)
		}
	}
	if err := req.Q.QueryRow(ctx, `SELECT count(*) FROM links WHERE user_id = $1`, req.UserID).Scan(&data.Totals.LinksTotal); err != nil {
		return Output{}, fmt.Errorf("reports: overview links total: %w", err)
	}

	// Timeseries (zero-filled).
	var err error
	data.Timeseries, err = timeseries(ctx, req, cur)
	if err != nil {
		return Output{}, err
	}
	data.PreviousTimeseries = []DayCount{}
	for _, d := range data.Timeseries {
		if data.BestDay == nil || d.Clicks > data.BestDay.Clicks {
			d := d
			data.BestDay = &d
		}
	}
	if data.BestDay != nil && data.BestDay.Clicks == 0 {
		data.BestDay = nil
	}

	// Previous window.
	visitorsTruncated := false
	if prev := req.Windows.Previous; prev != nil {
		ps := p.ScopedQuery(req.UserID, *prev, ScopeOpts{})
		pt := &PreviousTotals{}
		var vd int64
		if err := req.Q.QueryRow(ctx, `SELECT count(*), `+visitors+ps.SQL, ps.Args...).Scan(&pt.Clicks, &vd); err != nil {
			return Output{}, fmt.Errorf("reports: overview previous totals: %w", err)
		}
		if prev.Start.Before(req.Now.Add(-clicks.DetailRetention)) {
			visitorsTruncated = true
		} else {
			pt.VisitorDays = &vd
		}
		data.Previous = pt
		data.PreviousTimeseries, err = timeseries(ctx, req, *prev)
		if err != nil {
			return Output{}, err
		}
	}

	// Campaigns: UTM filters apply, the campaign filter does not.
	cs := p.ScopedQuery(req.UserID, cur, ScopeOpts{
		Joins: "LEFT JOIN campaigns c ON c.id = k.campaign_id",
		Skip:  []string{SkipCampaign},
	})
	rows, err := req.Q.Query(ctx,
		`SELECT c.slug, COALESCE(c.name, 'No campaign'), count(*) AS n`+cs.SQL+
			` GROUP BY c.slug, c.name ORDER BY n DESC, c.slug ASC NULLS LAST`, cs.Args...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: overview campaigns: %w", err)
	}
	data.Campaigns = []CampaignRow{}
	for rows.Next() {
		var r CampaignRow
		if err := rows.Scan(&r.Slug, &r.Name, &r.Clicks); err != nil {
			rows.Close()
			return Output{}, fmt.Errorf("reports: overview campaigns scan: %w", err)
		}
		data.Campaigns = append(data.Campaigns, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: overview campaigns: %w", err)
	}

	// Facets: each ignores its own dimension's filter.
	for _, d := range Dimensions {
		fs := p.ScopedQuery(req.UserID, cur, ScopeOpts{Skip: []string{d}})
		expr, _ := DimensionExpr(d)
		rows, err := req.Q.Query(ctx,
			`SELECT `+expr+` AS v, count(*) AS n`+fs.SQL+
				` GROUP BY v ORDER BY n DESC, v ASC NULLS LAST LIMIT `+strconv.Itoa(limit),
			fs.Args...)
		if err != nil {
			return Output{}, fmt.Errorf("reports: overview facet %s: %w", d, err)
		}
		vals := []FacetValue{}
		for rows.Next() {
			var v *string
			var f FacetValue
			if err := rows.Scan(&v, &f.Clicks); err != nil {
				rows.Close()
				return Output{}, fmt.Errorf("reports: overview facet %s scan: %w", d, err)
			}
			f.Value = NoneValue
			if v != nil {
				f.Value = *v
			}
			vals = append(vals, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Output{}, fmt.Errorf("reports: overview facet %s: %w", d, err)
		}
		switch d {
		case "source":
			data.Facets.Source = vals
		case "medium":
			data.Facets.Medium = vals
		case "utm_campaign":
			data.Facets.UTMCampaign = vals
		case "content":
			data.Facets.Content = vals
		case "term":
			data.Facets.Term = vals
		case "placement":
			data.Facets.Placement = vals
		}
	}

	return Output{Highlights: overviewHighlights(data, req, visitorsTruncated), Data: data}, nil
}

// timeseries returns one DayCount per day of w, zero-filled.
func timeseries(ctx context.Context, req *Request, w Window) ([]DayCount, error) {
	sc := req.Params.ScopedQuery(req.UserID, w, ScopeOpts{})
	rows, err := req.Q.Query(ctx, `SELECT `+DayExpr+` AS d, count(*)`+sc.SQL+` GROUP BY d`, sc.Args...)
	if err != nil {
		return nil, fmt.Errorf("reports: overview timeseries: %w", err)
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var d time.Time
		var n int64
		if err := rows.Scan(&d, &n); err != nil {
			return nil, fmt.Errorf("reports: overview timeseries scan: %w", err)
		}
		counts[d.Format(dayLayout)] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reports: overview timeseries: %w", err)
	}
	days := w.DayList()
	out := make([]DayCount, len(days))
	for i, d := range days {
		out[i] = DayCount{Date: d, Clicks: counts[d]}
	}
	return out, nil
}

// overviewHighlights writes up to five plain-English sentences from data.
func overviewHighlights(d OverviewData, req *Request, visitorsTruncated bool) []string {
	cur := req.Windows.Current
	var out []string

	if d.Totals.Clicks == 0 {
		out = append(out, fmt.Sprintf("No clicks between %s and %s.", cur.From, cur.To))
	} else {
		s := fmt.Sprintf("%s %s between %s and %s", commas(d.Totals.Clicks), plural(d.Totals.Clicks, "click", "clicks"), cur.From, cur.To)
		if d.Previous != nil {
			s += ", " + delta(d.Totals.Clicks, d.Previous.Clicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
		}
		out = append(out, s+".")
	}

	cutoff := req.Now.Add(-clicks.DetailRetention)
	switch {
	case visitorsTruncated:
		out = append(out, "Visitor-days for the previous period are unavailable because visitor detail is only kept for 90 days.")
	case cur.Start.Before(cutoff):
		out = append(out, "Visitor-days undercount this window because visitor detail is only kept for 90 days.")
	}

	if d.BestDay != nil {
		out = append(out, fmt.Sprintf("Best day: %s with %s %s.", d.BestDay.Date, commas(d.BestDay.Clicks), plural(d.BestDay.Clicks, "click", "clicks")))
	}
	for _, c := range d.Campaigns {
		if c.Slug != nil {
			out = append(out, fmt.Sprintf("Top campaign: %s (%s).", c.Name, commas(c.Clicks)))
			break
		}
	}
	if v, ok := topValue(d.Facets.Source); ok {
		out = append(out, fmt.Sprintf("Top source: %s (%s).", v.Value, commas(v.Clicks)))
	}
	if v, ok := topValue(d.Facets.Content); ok {
		out = append(out, fmt.Sprintf("Top content: %s (%s).", v.Value, commas(v.Clicks)))
	}
	if d.Totals.ExcludedBots > 0 {
		out = append(out, fmt.Sprintf("%s bot %s excluded.", commas(d.Totals.ExcludedBots), plural(d.Totals.ExcludedBots, "click was", "clicks were")))
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// topValue returns the first facet value that is not NoneValue.
func topValue(vs []FacetValue) (FacetValue, bool) {
	for _, v := range vs {
		if v.Value != NoneValue {
			return v, true
		}
	}
	return FacetValue{}, false
}

// delta phrases cur against prev, e.g. "up 18% on", "down 5% on", "flat on",
// or "up from none on" when the previous count is zero.
func delta(cur, prev int64) string {
	switch {
	case prev == 0 && cur == 0:
		return "flat on"
	case prev == 0:
		return "up from zero in"
	case cur == prev:
		return "flat on"
	case cur > prev:
		return fmt.Sprintf("up %d%% on", pct(cur-prev, prev))
	default:
		return fmt.Sprintf("down %d%% on", pct(prev-cur, prev))
	}
}

func pct(n, of int64) int64 { return (n*100 + of/2) / of }

func plural(n int64, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// commas formats n with thousands separators.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		if s[i-1] == '-' {
			break
		}
		s = s[:i] + "," + s[i:]
	}
	return s
}
