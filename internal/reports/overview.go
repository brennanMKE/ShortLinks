package reports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

// keepOnly returns the predicate that applies just one part of the filters
// (SkipBots, SkipCampaign or a dimension name), numbered from next.
func (p Params) keepOnly(next int, keep string) (string, []any) {
	skip := []string{SkipBots, SkipCampaign}
	skip = append(skip, Dimensions...)
	out := skip[:0:0]
	for _, s := range skip {
		if s != keep {
			out = append(out, s)
		}
	}
	return p.Predicate(next, out...)
}

// runOverview reads the clicks of both windows once and derives every figure
// from that one pass (#0150). A CTE holds the rows with one boolean per filter
// (bots, campaign, each dimension); a facet counts the rows that pass every
// filter except its own, the campaign list every filter except the campaign,
// and the totals and daily series every filter. GROUPING SETS then produce
// the totals, the days, each facet and the campaigns together.
func runOverview(ctx context.Context, req *Request) (Output, error) {
	p := req.Params
	limit := p.LimitOr(defaultFacetLimit)
	cur := req.Windows.Current
	prev := req.Windows.Previous

	lo, prevStart, prevEnd := cur.Start, cur.Start, cur.Start
	if prev != nil {
		lo, prevStart, prevEnd = prev.Start, prev.Start, prev.End
		if cur.Start.Before(lo) {
			lo = cur.Start
		}
	}
	args := []any{req.UserID, lo, cur.End, p.TZName, cur.Start, prevStart, prevEnd}

	// One flag per filter part.
	parts := append([]string{SkipBots, SkipCampaign}, Dimensions...)
	flagCol := map[string]string{SkipBots: "f_bots", SkipCampaign: "f_camp"}
	var flagSQL []string
	for _, d := range Dimensions {
		flagCol[d] = "f_" + d
	}
	for _, part := range parts {
		cond, a := p.keepOnly(len(args)+1, part)
		args = append(args, a...)
		flagSQL = append(flagSQL, "("+cond+") AS "+flagCol[part])
	}
	// allBut ANDs every flag except the named ones.
	allBut := func(except ...string) string {
		var fs []string
	next:
		for _, part := range parts {
			for _, e := range except {
				if e == part {
					continue next
				}
			}
			fs = append(fs, flagCol[part])
		}
		return strings.Join(fs, " AND ")
	}

	var dimCols, dimSel, nFilters, nCases, vCases []string
	for _, d := range Dimensions {
		expr, _ := DimensionExpr(d)
		dimSel = append(dimSel, expr+" AS d_"+d)
		dimCols = append(dimCols, "d_"+d)
	}
	// GROUPING(day, d_*..., camp_id): a set's bit is 0 for the column it groups by.
	groupCols := append(append([]string{"day"}, dimCols...), "camp_id")
	full := (1 << len(groupCols)) - 1
	gidOf := func(col int) int { return full &^ (1 << (len(groupCols) - 1 - col)) }
	sets := []string{"()", "(day)"}
	for i, d := range Dimensions {
		sets = append(sets, "(d_"+d+")")
		nFilters = append(nFilters, fmt.Sprintf("count(*) FILTER (WHERE in_cur AND %s) AS n_%s", allBut(d), d))
		nCases = append(nCases, fmt.Sprintf("WHEN %d THEN n_%s", gidOf(i+1), d))
		vCases = append(vCases, fmt.Sprintf("WHEN %d THEN d_%s", gidOf(i+1), d))
	}
	sets = append(sets, "(camp_id)")
	campGID := gidOf(len(groupCols) - 1)
	nCases = append(nCases, fmt.Sprintf("WHEN %d THEN n_camp", campGID))
	vCases = append(vCases, fmt.Sprintf("WHEN %d THEN c.slug", campGID))
	dayGID := gidOf(0)

	q := `WITH w AS (
	  SELECT ` + DayExpr + ` AS day, k.is_bot, k.campaign_id AS camp_id,
	         (k.clicked_at >= $5) AS in_cur, (k.clicked_at >= $6 AND k.clicked_at < $7) AS in_prev,
	         ` + strings.Join(dimSel, ", ") + `,
	         ` + strings.Join(flagSQL, ", ") + `
	  FROM clicks k JOIN links l ON l.id = k.link_id
	  WHERE l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3
	), x AS (
	  SELECT w.*, ` + allBut() + ` AS ok_all, ` + allBut(SkipBots) + ` AS ok_nb, ` + allBut(SkipCampaign) + ` AS ok_nc FROM w
	), g AS (
	  SELECT GROUPING(` + strings.Join(groupCols, ", ") + `) AS gid, day, ` + strings.Join(dimCols, ", ") + `, camp_id,
	         count(*) FILTER (WHERE in_cur AND ok_all) AS n_cur,
	         count(*) FILTER (WHERE in_prev AND ok_all) AS n_prev,
	         count(*) FILTER (WHERE in_cur AND is_bot AND ok_nb) AS bots,
	         ` + strings.Join(nFilters, ", ") + `,
	         count(*) FILTER (WHERE in_cur AND ok_nc) AS n_camp
	  FROM x GROUP BY GROUPING SETS (` + strings.Join(sets, ", ") + `)
	), g2 AS (
	  SELECT g.*, c.slug AS camp_slug, COALESCE(c.name, 'No campaign') AS camp_name,
	         CASE gid ` + strings.Join(nCases, " ") + ` END AS nsel,
	         CASE gid ` + strings.Join(vCases, " ") + ` END AS vsel
	  FROM g LEFT JOIN campaigns c ON c.id = g.camp_id
	), g3 AS (
	  SELECT g2.*, row_number() OVER (PARTITION BY gid ORDER BY nsel DESC, vsel ASC NULLS LAST) AS rn FROM g2
	)
	SELECT gid, day, ` + strings.Join(dimCols, ", ") + `, camp_slug, camp_name, n_cur, n_prev, bots, COALESCE(nsel, 0)
	FROM g3
	WHERE gid IN (` + fmt.Sprint(full, ",", dayGID) + `) OR (nsel > 0 AND (gid = ` + fmt.Sprint(campGID) + ` OR rn <= ` + strconv.Itoa(limit) + `))
	ORDER BY gid, rn`

	rows, err := req.Q.Query(ctx, q, args...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: overview: %w", err)
	}
	defer rows.Close()

	data := OverviewData{Campaigns: []CampaignRow{}}
	facets := map[string]*[]FacetValue{}
	for _, d := range Dimensions {
		facets[d] = &[]FacetValue{}
	}
	curDays, prevDays := map[string]int64{}, map[string]int64{}
	var prevClicks int64
	for rows.Next() {
		var gid int
		var day *time.Time
		dv := make([]*string, len(Dimensions))
		var slug, name *string
		var nCur, nPrev, bots, nsel int64
		dest := []any{&gid, &day}
		for i := range dv {
			dest = append(dest, &dv[i])
		}
		dest = append(dest, &slug, &name, &nCur, &nPrev, &bots, &nsel)
		if err := rows.Scan(dest...); err != nil {
			return Output{}, fmt.Errorf("reports: overview scan: %w", err)
		}
		switch {
		case gid == full:
			data.Totals.Clicks, prevClicks = nCur, nPrev
			if !p.IncludeBots {
				data.Totals.ExcludedBots = bots
			}
		case gid == dayGID:
			if day != nil {
				curDays[day.Format(dayLayout)] = nCur
				prevDays[day.Format(dayLayout)] = nPrev
			}
		case gid == campGID:
			data.Campaigns = append(data.Campaigns, CampaignRow{Slug: slug, Name: *name, Clicks: nsel})
		default:
			for i, d := range Dimensions {
				if gid == gidOf(i+1) {
					f := FacetValue{Value: NoneValue, Clicks: nsel}
					if dv[i] != nil {
						f.Value = *dv[i]
					}
					*facets[d] = append(*facets[d], f)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: overview: %w", err)
	}
	rows.Close()
	data.Facets = Facets{
		Source: *facets["source"], Medium: *facets["medium"], UTMCampaign: *facets["utm_campaign"],
		Content: *facets["content"], Term: *facets["term"], Placement: *facets["placement"],
	}

	fill := func(w Window, counts map[string]int64) []DayCount {
		days := w.DayList()
		out := make([]DayCount, len(days))
		for i, d := range days {
			out[i] = DayCount{Date: d, Clicks: counts[d]}
		}
		return out
	}
	data.Timeseries = fill(cur, curDays)
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

	// Links clicked: each link probes the (link_id, clicked_at) index and
	// stops at its first counted click.
	pred, pargs := p.Predicate(4)
	lc := `SELECT count(*) FROM links l WHERE l.user_id = $1 AND EXISTS (SELECT 1 FROM clicks k WHERE k.link_id = l.id AND k.clicked_at >= $2 AND k.clicked_at < $3 AND (` + pred + `))`
	if err := req.Q.QueryRow(ctx, lc, append([]any{req.UserID, cur.Start, cur.End}, pargs...)...).Scan(&data.Totals.LinksClicked); err != nil {
		return Output{}, fmt.Errorf("reports: overview links clicked: %w", err)
	}
	if err := req.Q.QueryRow(ctx, `SELECT count(*) FROM links WHERE user_id = $1`, req.UserID).Scan(&data.Totals.LinksTotal); err != nil {
		return Output{}, fmt.Errorf("reports: overview links total: %w", err)
	}

	// Visitor days: distinct (visitor, day) pairs, hashed with SELECT DISTINCT
	// (count(DISTINCT (a, b)) sorts and took about twice as long). One scan
	// covers both windows.
	visitorsTruncated := false
	prevVisitors := prev != nil && !prev.Start.Before(req.Now.Add(-clicks.DetailRetention))
	visScope := p.ScopedQuery(req.UserID, Window{Start: lo, End: cur.End}, ScopeOpts{})
	n := visScope.Next
	vq := `SELECT in_cur, count(*) FROM (SELECT DISTINCT (k.clicked_at >= $` + strconv.Itoa(n) + `) AS in_cur, k.ip_hash, ` + DayExpr + visScope.SQL +
		` AND k.ip_hash IS NOT NULL AND (k.clicked_at >= $` + strconv.Itoa(n) + ` OR (k.clicked_at >= $` + strconv.Itoa(n+1) + ` AND k.clicked_at < $` + strconv.Itoa(n+2) + `))) v GROUP BY in_cur`
	vrows, err := req.Q.Query(ctx, vq, append(visScope.Args, cur.Start, prevStart, prevEnd)...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: overview visitor days: %w", err)
	}
	var curVisitors, prevVisitorDays int64
	for vrows.Next() {
		var isCur bool
		var c int64
		if err := vrows.Scan(&isCur, &c); err != nil {
			vrows.Close()
			return Output{}, fmt.Errorf("reports: overview visitor days: %w", err)
		}
		if isCur {
			curVisitors = c
		} else {
			prevVisitorDays = c
		}
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: overview visitor days: %w", err)
	}
	data.Totals.VisitorDays = curVisitors

	if prev != nil {
		pt := &PreviousTotals{Clicks: prevClicks}
		if prevVisitors {
			pt.VisitorDays = &prevVisitorDays
		} else {
			// ip_hash is gone for part of the window: no visitor count.
			visitorsTruncated = true
		}
		data.Previous = pt
		data.PreviousTimeseries = fill(*prev, prevDays)
	}

	return Output{Highlights: overviewHighlights(data, req, visitorsTruncated), Data: data}, nil
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
