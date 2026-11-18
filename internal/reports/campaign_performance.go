package reports

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultMatrixRows = 50
	maxMatrixColumns  = 6
	keepMatrixColumns = 5
	// OtherColumn is the matrix column that sums the columns beyond the top five.
	OtherColumn = "other"
)

var matrixColumnValues = []string{"source", "medium", "placement"}

// PerfCampaign identifies the campaign a campaign-performance report is about.
type PerfCampaign struct {
	Slug     string  `json:"slug"`
	Name     string  `json:"name"`
	Status   string  `json:"status"`
	StartsOn *string `json:"starts_on"`
	EndsOn   *string `json:"ends_on"`
}

// CampaignPerfTotals is the campaign-performance summary.
type CampaignPerfTotals struct {
	Clicks int64 `json:"clicks"`
	// PreviousClicks is null when there is no previous window.
	PreviousClicks *int64 `json:"previous_clicks"`
	// PerActiveDay is clicks over the days of the window the campaign's dates
	// cover (the whole window without dates); null when it covers none.
	PerActiveDay *float64 `json:"per_active_day"`
	// Links is how many links are in the campaign now; LinksClicked how many
	// of them were clicked in the window.
	Links        int64 `json:"links"`
	LinksClicked int64 `json:"links_clicked"`
}

// MatrixCell is one content x column pairing. Key is set when exactly one
// link has clicks there, Keys (most clicks first) when several do.
type MatrixCell struct {
	Clicks int64    `json:"clicks"`
	Key    string   `json:"key,omitempty"`
	Keys   []string `json:"keys,omitempty"`
}

// MatrixRow is one content group. A pairing nobody clicked is absent from
// Cells, not zero.
type MatrixRow struct {
	ContentKey string                `json:"content_key"`
	Title      string                `json:"title"`
	Cells      map[string]MatrixCell `json:"cells"`
	Total      int64                 `json:"total"`
}

// Matrix is the content x channel table.
type Matrix struct {
	// By is the dimension the columns are values of.
	By string `json:"by"`
	// Columns are ranked by clicks; more than six collapse to the top five plus other.
	Columns      []string         `json:"columns"`
	Rows         []MatrixRow      `json:"rows"`
	ColumnTotals map[string]int64 `json:"column_totals"`
	// TotalRows is the number of content groups before limit cut Rows.
	TotalRows int `json:"total_rows"`
}

// PlacementClicks is one placement's clicks.
type PlacementClicks struct {
	Placement string `json:"placement"`
	IsNone    bool   `json:"is_none,omitempty"`
	Clicks    int64  `json:"clicks"`
}

// CampaignPerformanceData is the campaign-performance payload.
type CampaignPerformanceData struct {
	Campaign   PerfCampaign       `json:"campaign"`
	Totals     CampaignPerfTotals `json:"totals"`
	Matrix     Matrix             `json:"matrix"`
	Timeseries []DayCount         `json:"timeseries"`
	// PreviousTimeseries is the previous window bucketed by position, so index
	// i lines up with Timeseries[i]; null without a previous window.
	PreviousTimeseries []DayCount        `json:"previous_timeseries"`
	Placements         []PlacementClicks `json:"placements"`
	// RemovedLinksClicks counts clicks attributed to the campaign whose link
	// is no longer in it. They are included in clicks and the matrix.
	RemovedLinksClicks int64 `json:"removed_links_clicks"`
	Truncated          bool  `json:"truncated,omitempty"`
}

var campaignPerformanceReport = Report{
	Name: "campaign-performance",
	Description: "One campaign in a single call: totals, a content x channel matrix (columns by source, medium or placement), daily clicks, placements and clicks from links since removed. " +
		"The window defaults to the campaign's own dates; the campaign filter is ignored.",
	Params: []ParamSpec{
		{Name: "slug", Type: "string", Required: true, Description: "Campaign slug. Unknown or not yours: 404 campaign_not_found."},
		{Name: "columns", Type: "enum", Default: "source", Values: matrixColumnValues, Description: "What the matrix columns are. More than six values keep the top five and an other column."},
	},
	DefaultLimit:  defaultMatrixRows,
	Windows:       campaignWindows,
	CampaignParam: "slug",
	Run:           runCampaignPerformance,
}

type perfCampaignRow struct {
	id           int64
	slug, name   string
	starts, ends *time.Time
	archived     bool
}

func loadPerfCampaign(ctx context.Context, req *Request, slug string) (*perfCampaignRow, error) {
	c := &perfCampaignRow{}
	err := req.Q.QueryRow(ctx,
		`SELECT id, slug, name, starts_at, ends_at, archived FROM campaigns WHERE user_id = $1 AND slug = $2`,
		req.UserID, slug).Scan(&c.id, &c.slug, &c.name, &c.starts, &c.ends, &c.archived)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &NotFoundError{Code: "campaign_not_found", Message: "no campaign with that slug"}
	}
	if err != nil {
		return nil, fmt.Errorf("reports: campaign lookup: %w", err)
	}
	return c, nil
}

// campaignWindows defaults the window to the campaign's own dates: when both
// dates are set the window runs from starts_on to ends_on, with the end clamped
// to today. ends_on is an inclusive last day in tz and today so far is
// included. This differs from campaignWindow in package clicks (behind
// GET /api/campaigns/{slug}/stats), which excludes the last day and today, so
// that endpoint's totals can be lower; the gap is tracked in #0119. Otherwise
// the standard window applies. An explicit
// range, from or to keeps the standard resolution. Over the campaign's own
// dates the previous window (the days before the campaign began) is only
// reported when compare=previous is sent explicitly.
func campaignWindows(ctx context.Context, req *Request) (ResolvedWindows, error) {
	slug, err := req.RequiredString("slug")
	if err != nil {
		return ResolvedWindows{}, err
	}
	c, err := loadPerfCampaign(ctx, req, slug)
	if err != nil {
		return ResolvedWindows{}, err
	}
	if req.Params.RangeExplicit || c.starts == nil || c.ends == nil {
		return req.Windows, nil
	}
	p := req.Params
	p.From, p.To = CampaignDate(*c.starts), CampaignDate(*c.ends)
	rw := ResolveWindows(p, req.Now, nil)
	if req.Raw.Get("compare") != ComparePrevious {
		rw.Previous = nil
	}
	return rw, nil
}

// activeDaysIn counts the days of w that fall within the campaign's dates.
func activeDaysIn(c *perfCampaignRow, w Window) int {
	from, to := w.From, w.To
	if c.starts != nil && CampaignDate(*c.starts) > from {
		from = CampaignDate(*c.starts)
	}
	if c.ends != nil && CampaignDate(*c.ends) < to {
		to = CampaignDate(*c.ends)
	}
	if from > to {
		return 0
	}
	f, _ := parseDay(from)
	t, _ := parseDay(to)
	return int(t.Sub(f).Hours()/24+0.5) + 1
}

func sumDays(ds []DayCount) (n int64) {
	for _, d := range ds {
		n += d.Clicks
	}
	return n
}

func runCampaignPerformance(ctx context.Context, req *Request) (Output, error) {
	slug, err := req.RequiredString("slug")
	if err != nil {
		return Output{}, err
	}
	cols, err := req.EnumParam("columns", "source", matrixColumnValues...)
	if err != nil {
		return Output{}, err
	}
	c, err := loadPerfCampaign(ctx, req, slug)
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultMatrixRows)

	// Every shared helper reads Params.Campaign, so scoping the copy to this
	// campaign scopes them all. The caller's own campaign filter is replaced.
	r := *req
	r.Params.Campaign = slug
	cur := r.Windows.Current
	scope := ScopeOpts{}

	data := CampaignPerformanceData{Campaign: PerfCampaign{Slug: c.slug, Name: c.name,
		Status: CampaignStatus(c.starts, c.ends, c.archived, req.Now, req.Params.TZ)}}
	if c.starts != nil {
		s := CampaignDate(*c.starts)
		data.Campaign.StartsOn = &s
	}
	if c.ends != nil {
		s := CampaignDate(*c.ends)
		data.Campaign.EndsOn = &s
	}

	// Daily series, zero-filled.
	byDay := map[string]int64{}
	if err := dayRowsWhere(ctx, &r, cur, scope, "'all'", "''", func(_, _, day string, n int64) { byDay[day] += n }); err != nil {
		return Output{}, err
	}
	for _, d := range cur.DayList() {
		data.Timeseries = append(data.Timeseries, DayCount{Date: d, Clicks: byDay[d]})
	}
	data.Totals.Clicks = sumDays(data.Timeseries)
	if req.Windows.Previous != nil {
		if data.PreviousTimeseries, err = previousSeries(ctx, &r, "day"); err != nil {
			return Output{}, err
		}
		pc := sumDays(data.PreviousTimeseries)
		data.Totals.PreviousClicks = &pc
	}
	if d := activeDaysIn(c, cur); d > 0 {
		v := math.Round(float64(data.Totals.Clicks)/float64(d)*10) / 10
		data.Totals.PerActiveDay = &v
	}

	// Links and the matrix.
	if err := req.Q.QueryRow(ctx, `SELECT count(*) FROM links WHERE user_id = $1 AND campaign_id = $2`, req.UserID, c.id).Scan(&data.Totals.Links); err != nil {
		return Output{}, fmt.Errorf("reports: campaign links: %w", err)
	}
	set, err := loadLinks(ctx, &r, false)
	if err != nil {
		return Output{}, err
	}
	data.Truncated = set.truncated
	for _, li := range set.links {
		if li.clicks > 0 && li.campaign != nil && *li.campaign == slug {
			data.Totals.LinksClicked++
		}
	}
	if data.Matrix, err = buildMatrix(ctx, &r, set, cols, limit); err != nil {
		return Output{}, err
	}

	if data.Placements, err = campaignPlacements(ctx, &r); err != nil {
		return Output{}, err
	}

	rm := r.Params.ScopedQuery(req.UserID, cur, ScopeOpts{Where: fmt.Sprintf("l.campaign_id IS DISTINCT FROM %d", c.id)})
	if err := req.Q.QueryRow(ctx, `SELECT count(*)`+rm.SQL, rm.Args...).Scan(&data.RemovedLinksClicks); err != nil {
		return Output{}, fmt.Errorf("reports: removed links clicks: %w", err)
	}
	return Output{Highlights: campaignPerfHighlights(data, req), Data: data}, nil
}

type cellLink struct {
	key string
	n   int64
}

type cellAgg struct {
	clicks int64
	links  []cellLink
}

// buildMatrix groups the set's links into content groups and splits each
// group's clicks by the value of dimension cols.
func buildMatrix(ctx context.Context, req *Request, set *linkSet, cols string, limit int) (Matrix, error) {
	groups, byLink := contentGroups(set)
	title := map[string]string{}
	for _, g := range groups {
		title[g.value] = g.label
	}
	keyOf := map[int64]string{}
	for _, li := range set.links {
		keyOf[li.id] = li.key
	}

	ds, _ := dimensionSQL(cols)
	sc := req.Params.ScopedQuery(req.UserID, req.Windows.Current, ScopeOpts{})
	rows, err := req.Q.Query(ctx, `SELECT l.id, `+ds.value+`, count(*)`+sc.SQL+` GROUP BY 1, 2`, sc.Args...)
	if err != nil {
		return Matrix{}, fmt.Errorf("reports: matrix: %w", err)
	}
	defer rows.Close()
	cells := map[string]map[string]*cellAgg{}
	colTotal := map[string]int64{}
	for rows.Next() {
		var id, n int64
		var col string
		if err := rows.Scan(&id, &col, &n); err != nil {
			return Matrix{}, fmt.Errorf("reports: matrix scan: %w", err)
		}
		g, ok := byLink[id]
		if !ok {
			continue
		}
		if cells[g] == nil {
			cells[g] = map[string]*cellAgg{}
		}
		a := cells[g][col]
		if a == nil {
			a = &cellAgg{}
			cells[g][col] = a
		}
		a.clicks += n
		a.links = append(a.links, cellLink{keyOf[id], n})
		colTotal[col] += n
	}
	if err := rows.Err(); err != nil {
		return Matrix{}, fmt.Errorf("reports: matrix: %w", err)
	}

	names := make([]string, 0, len(colTotal))
	for c := range colTotal {
		names = append(names, c)
	}
	sort.Slice(names, func(i, j int) bool {
		if colTotal[names[i]] != colTotal[names[j]] {
			return colTotal[names[i]] > colTotal[names[j]]
		}
		return names[i] < names[j]
	})
	m := Matrix{By: cols, Columns: []string{}, Rows: []MatrixRow{}, ColumnTotals: map[string]int64{}}
	remap := map[string]string{}
	if len(names) > maxMatrixColumns {
		var rest int64
		for i, n := range names {
			if i < keepMatrixColumns {
				continue
			}
			remap[n] = OtherColumn
			rest += colTotal[n]
		}
		names = append(names[:keepMatrixColumns:keepMatrixColumns], OtherColumn)
		colTotal2 := map[string]int64{OtherColumn: rest}
		for n, v := range colTotal {
			if remap[n] == "" {
				colTotal2[n] = v
			}
		}
		colTotal = colTotal2
	}
	m.Columns = append(m.Columns, names...)
	m.ColumnTotals = colTotal

	for g, byCol := range cells {
		row := MatrixRow{ContentKey: g, Title: title[g], Cells: map[string]MatrixCell{}}
		merged := map[string]*cellAgg{}
		for col, a := range byCol {
			if to, ok := remap[col]; ok {
				col = to
			}
			if merged[col] == nil {
				merged[col] = &cellAgg{}
			}
			merged[col].clicks += a.clicks
			for _, l := range a.links { // a link spread over merged columns counts once
				seen := false
				for i := range merged[col].links {
					if merged[col].links[i].key == l.key {
						merged[col].links[i].n += l.n
						seen = true
					}
				}
				if !seen {
					merged[col].links = append(merged[col].links, l)
				}
			}
		}
		for col, a := range merged {
			sort.Slice(a.links, func(i, j int) bool {
				if a.links[i].n != a.links[j].n {
					return a.links[i].n > a.links[j].n
				}
				return a.links[i].key < a.links[j].key
			})
			cell := MatrixCell{Clicks: a.clicks}
			if len(a.links) == 1 {
				cell.Key = a.links[0].key
			} else {
				for _, l := range a.links {
					cell.Keys = append(cell.Keys, l.key)
				}
			}
			row.Cells[col] = cell
			row.Total += a.clicks
		}
		m.Rows = append(m.Rows, row)
	}
	sort.Slice(m.Rows, func(i, j int) bool {
		if m.Rows[i].Total != m.Rows[j].Total {
			return m.Rows[i].Total > m.Rows[j].Total
		}
		return m.Rows[i].ContentKey < m.Rows[j].ContentKey
	})
	m.TotalRows = len(m.Rows)
	if len(m.Rows) > limit {
		m.Rows = m.Rows[:limit]
	}
	return m, nil
}

// campaignPlacements counts the campaign's clicks per placement (normalized as
// the campaign stats do), spelled as most of its clicks' links spell it.
func campaignPlacements(ctx context.Context, req *Request) ([]PlacementClicks, error) {
	expr, _ := DimensionExpr("placement")
	sc := req.Params.ScopedQuery(req.UserID, req.Windows.Current, ScopeOpts{})
	rows, err := req.Q.Query(ctx,
		`SELECT `+expr+`, COALESCE(mode() WITHIN GROUP (ORDER BY NULLIF(btrim(l.placement), '')), ''), count(*)`+sc.SQL+
			` GROUP BY 1 ORDER BY 3 DESC, 1 NULLS LAST`, sc.Args...)
	if err != nil {
		return nil, fmt.Errorf("reports: placements: %w", err)
	}
	defer rows.Close()
	out := []PlacementClicks{}
	for rows.Next() {
		var norm *string
		var spelling string
		var p PlacementClicks
		if err := rows.Scan(&norm, &spelling, &p.Clicks); err != nil {
			return nil, fmt.Errorf("reports: placements scan: %w", err)
		}
		switch {
		case norm == nil:
			p.Placement, p.IsNone = "(none)", true
		case spelling != "":
			p.Placement = spelling
		default:
			p.Placement = *norm
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func campaignPerfHighlights(d CampaignPerformanceData, req *Request) []string {
	cur := req.Windows.Current
	t := d.Totals
	if t.Clicks == 0 {
		out := []string{fmt.Sprintf("%s had no clicks between %s and %s.", d.Campaign.Name, cur.From, cur.To)}
		if d.RemovedLinksClicks > 0 {
			out = append(out, removedNote(d.RemovedLinksClicks))
		}
		return out
	}
	s := fmt.Sprintf("%s: %s %s between %s and %s", d.Campaign.Name, commas(t.Clicks), plural(t.Clicks, "click", "clicks"), cur.From, cur.To)
	if t.PreviousClicks != nil {
		s += ", " + delta(t.Clicks, *t.PreviousClicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
	}
	out := []string{s + "."}
	if t.PerActiveDay != nil {
		out = append(out, fmt.Sprintf("%.1f clicks per active day; %d of %d links were clicked.", *t.PerActiveDay, t.LinksClicked, t.Links))
	}
	if len(d.Matrix.Rows) > 0 {
		top := d.Matrix.Rows[0]
		out = append(out, fmt.Sprintf("Best content: %q with %s clicks (%d%%).", top.Title, commas(top.Total), pct(top.Total, t.Clicks)))
	}
	if len(d.Matrix.Columns) > 0 {
		col := d.Matrix.Columns[0]
		out = append(out, fmt.Sprintf("Top %s: %s (%s clicks, %d%%).", d.Matrix.By, rowName(col, ""), commas(d.Matrix.ColumnTotals[col]), pct(d.Matrix.ColumnTotals[col], t.Clicks)))
	}
	if len(d.Placements) > 0 && !d.Placements[0].IsNone && d.Matrix.By != "placement" {
		p := d.Placements[0]
		out = append(out, fmt.Sprintf("Top placement: %s (%s clicks).", p.Placement, commas(p.Clicks)))
	}
	if d.RemovedLinksClicks > 0 {
		out = append(out, removedNote(d.RemovedLinksClicks))
	}
	return truncNote(out, d.Truncated)
}

func removedNote(n int64) string {
	return fmt.Sprintf("%s %s came from links no longer in the campaign.", commas(n), plural(n, "click", "clicks"))
}
