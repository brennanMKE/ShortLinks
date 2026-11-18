package reports

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// Series is one line of a timeseries. Other marks the rollup of the series
// beyond series_limit (its Name is "other").
type Series struct {
	Name   string     `json:"name"`
	Label  string     `json:"label,omitempty"`
	Other  bool       `json:"other,omitempty"`
	Clicks int64      `json:"clicks"`
	Points []DayCount `json:"points"`
}

// TimeseriesData is the timeseries report's payload. Point dates are the day,
// or for weekly buckets the Monday (in tz) that starts the week; the first and
// last weeks of a window count only the window's own days.
type TimeseriesData struct {
	Granularity string   `json:"granularity"`
	SplitBy     string   `json:"split_by,omitempty"`
	Series      []Series `json:"series"`
	// Previous is the previous window's total (all series combined), bucketed
	// the same way with its own dates; null when there is no previous window.
	Previous  []DayCount `json:"previous"`
	Truncated bool       `json:"truncated,omitempty"`
}

var timeseriesReport = Report{
	Name:        "timeseries",
	Description: "Clicks per day or week across all your links, optionally split into series by a dimension, with the previous period.",
	Params: []ParamSpec{
		{Name: "granularity", Type: "enum", Default: "day", Values: []string{"day", "week"}, Description: "Bucket size; weeks start on Monday in tz."},
		{Name: "split_by", Type: "enum", Values: BreakdownDimensions, Description: "Split into one series per value of this dimension (as breakdown's dimension). Default: one series named all."},
		{Name: "series_limit", Type: "int", Default: "5", Description: "With split_by, the most series to return (1-50); the rest are summed into an other series."},
	},
	Run: runTimeseries,
}

// mondayOf returns the Monday on or before civil date d (YYYY-MM-DD). It does
// calendar arithmetic on the date alone, so DST never moves it.
func mondayOf(d string) string {
	t, _ := parseDay(d)
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format(dayLayout)
}

// bucketDates lists the bucket dates covering w in order.
func bucketDates(w Window, gran string) []string {
	days := w.DayList()
	if gran != "week" {
		return days
	}
	var out []string
	for _, d := range days {
		m := mondayOf(d)
		if len(out) == 0 || out[len(out)-1] != m {
			out = append(out, m)
		}
	}
	return out
}

// bucketOf maps a day to its bucket date.
func bucketOf(day, gran string) string {
	if gran == "week" {
		return mondayOf(day)
	}
	return day
}

// seriesCounts holds clicks per series per bucket date.
type seriesCounts struct {
	label map[string]string
	n     map[string]map[string]int64
	total map[string]int64
}

func newSeriesCounts() *seriesCounts {
	return &seriesCounts{label: map[string]string{}, n: map[string]map[string]int64{}, total: map[string]int64{}}
}

func (s *seriesCounts) add(name, label, bucket string, n int64) {
	if s.n[name] == nil {
		s.n[name] = map[string]int64{}
	}
	s.n[name][bucket] += n
	s.total[name] += n
	if label != "" {
		s.label[name] = label
	}
}

// points returns every series summed, zero-filled over dates.
func (s *seriesCounts) points(names []string, dates []string) []DayCount {
	out := make([]DayCount, len(dates))
	for i, d := range dates {
		out[i].Date = d
		for _, n := range names {
			out[i].Clicks += s.n[n][d]
		}
	}
	return out
}

// dayRows queries clicks per (group, day) for w. group and label are SQL
// expressions ("'all'" and "”" for no split).
func dayRows(ctx context.Context, req *Request, w Window, joins, group, label string, fn func(group, label, day string, n int64)) error {
	return dayRowsWhere(ctx, req, w, ScopeOpts{Joins: joins}, group, label, fn)
}

// dayRowsWhere is dayRows with full scope options, e.g. an extra Where.
func dayRowsWhere(ctx context.Context, req *Request, w Window, opts ScopeOpts, group, label string, fn func(group, label, day string, n int64)) error {
	sc := req.Params.ScopedQuery(req.UserID, w, opts)
	rows, err := req.Q.Query(ctx, `SELECT `+group+`, `+label+`, `+DayExpr+` AS d, count(*)`+sc.SQL+` GROUP BY 1, 2, 3`, sc.Args...)
	if err != nil {
		return fmt.Errorf("reports: timeseries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g, l string
		var d time.Time
		var n int64
		if err := rows.Scan(&g, &l, &d, &n); err != nil {
			return fmt.Errorf("reports: timeseries scan: %w", err)
		}
		fn(g, l, d.Format(dayLayout), n)
	}
	return rows.Err()
}

func runTimeseries(ctx context.Context, req *Request) (Output, error) {
	gran, err := req.EnumParam("granularity", "day", "day", "week")
	if err != nil {
		return Output{}, err
	}
	split, err := req.EnumParam("split_by", "", BreakdownDimensions...)
	if err != nil {
		return Output{}, err
	}
	seriesLimit, err := req.IntParam("series_limit", defaultSeriesLimit, 1, maxSeriesLimit)
	if err != nil {
		return Output{}, err
	}

	cur := req.Windows.Current
	dates := bucketDates(cur, gran)
	sc := newSeriesCounts()
	data := TimeseriesData{Granularity: gran, SplitBy: split}
	add := func(g, l, day string, n int64) { sc.add(g, l, bucketOf(day, gran), n) }

	switch {
	case split == "":
		err = dayRows(ctx, req, cur, "", "'all'", "''", add)
	case split == "content_group":
		var set *linkSet
		if set, err = loadLinks(ctx, req, false); err != nil {
			return Output{}, err
		}
		data.Truncated = set.truncated
		groups, byLink := contentGroups(set)
		titles := map[string]string{}
		for _, g := range groups {
			titles[g.value] = g.label
		}
		err = dayRows(ctx, req, cur, "", "l.id::text", "''", func(id, _, day string, n int64) {
			li, _ := strconv.ParseInt(id, 10, 64)
			if k, ok := byLink[li]; ok {
				add(k, titles[k], day, n)
			}
		})
	default:
		ds, _ := dimensionSQL(split)
		err = dayRows(ctx, req, cur, ds.joins, ds.value, ds.label, add)
	}
	if err != nil {
		return Output{}, err
	}

	// Rank series by clicks; the tail is summed into other.
	names := make([]string, 0, len(sc.total))
	for n := range sc.total {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if sc.total[names[i]] != sc.total[names[j]] {
			return sc.total[names[i]] > sc.total[names[j]]
		}
		return names[i] < names[j]
	})
	data.Series = []Series{}
	if split == "" && len(names) == 0 {
		names = []string{"all"} // an empty window still has its one zero-filled series
	}
	keep, rest := names, []string(nil)
	if split != "" && len(names) > seriesLimit {
		keep, rest = names[:seriesLimit], names[seriesLimit:]
	}
	for _, n := range keep {
		data.Series = append(data.Series, Series{Name: n, Label: sc.label[n], Clicks: sc.total[n], Points: sc.points([]string{n}, dates)})
	}
	if len(rest) > 0 {
		o := Series{Name: "other", Other: true, Points: sc.points(rest, dates)}
		for _, p := range o.Points {
			o.Clicks += p.Clicks
		}
		data.Series = append(data.Series, o)
	}

	if prev := req.Windows.Previous; prev != nil && !(split == "referer" && refererExpired(req, *prev)) {
		data.Previous, err = previousSeries(ctx, req, gran)
		if err != nil {
			return Output{}, err
		}
	}
	return Output{Highlights: timeseriesHighlights(data, req), Data: data}, nil
}

// previousSeries returns the previous window's combined total with exactly
// as many buckets as the current series, aligned by position: bucket i covers
// the same day offsets within the previous window as current bucket i covers
// within the current one, and is dated by the first day of that span. For
// weekly buckets this means a partial first or last current week lines up with
// an equally partial previous bucket rather than a calendar week.
func previousSeries(ctx context.Context, req *Request, gran string) ([]DayCount, error) {
	return previousSeriesWhere(ctx, req, gran, "")
}

// previousSeriesWhere is previousSeries narrowed by an extra constant
// condition (see ScopeOpts.Where).
func previousSeriesWhere(ctx context.Context, req *Request, gran, where string) ([]DayCount, error) {
	cur, prev := req.Windows.Current, *req.Windows.Previous
	curDays, prevDays := cur.DayList(), prev.DayList()
	idx := make([]int, len(curDays)) // day offset -> bucket index
	var out []DayCount
	last := ""
	for i, d := range curDays {
		b := bucketOf(d, gran)
		if i == 0 || b != last {
			out = append(out, DayCount{Date: prevDays[i]})
			last = b
		}
		idx[i] = len(out) - 1
	}
	offset := map[string]int{}
	for i, d := range prevDays {
		offset[d] = i
	}
	err := dayRowsWhere(ctx, req, prev, ScopeOpts{Where: where}, "'all'", "''", func(_, _, day string, n int64) {
		if i, ok := offset[day]; ok {
			out[idx[i]].Clicks += n
		}
	})
	return out, err
}

func timeseriesHighlights(d TimeseriesData, req *Request) []string {
	cur := req.Windows.Current
	var total int64
	totals := map[string]int64{}
	for _, s := range d.Series {
		total += s.Clicks
		for _, p := range s.Points {
			totals[p.Date] += p.Clicks
		}
	}
	if total == 0 {
		return []string{fmt.Sprintf("No clicks between %s and %s.", cur.From, cur.To)}
	}
	unit := "day"
	if d.Granularity == "week" {
		unit = "week"
	}
	s := fmt.Sprintf("%s %s between %s and %s", commas(total), plural(total, "click", "clicks"), cur.From, cur.To)
	if d.Previous != nil {
		var pt int64
		for _, p := range d.Previous {
			pt += p.Clicks
		}
		s += ", " + delta(total, pt) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
	}
	out := []string{s + "."}
	var bestDate string
	var best int64
	for date, n := range totals {
		if n > best || (n == best && date < bestDate) {
			bestDate, best = date, n
		}
	}
	if unit == "week" {
		out = append(out, fmt.Sprintf("Busiest week: starting %s with %s %s.", bestDate, commas(best), plural(best, "click", "clicks")))
	} else {
		out = append(out, fmt.Sprintf("Best day: %s with %s %s.", bestDate, commas(best), plural(best, "click", "clicks")))
	}
	if d.SplitBy != "" && len(d.Series) > 0 && !d.Series[0].Other {
		top := d.Series[0]
		out = append(out, fmt.Sprintf("Top %s: %s (%s, %d%% of clicks).", dimNoun(d.SplitBy), rowName(top.Name, top.Label), commas(top.Clicks), pct(top.Clicks, total)))
	}
	if d.SplitBy == "referer" {
		if d.Previous == nil && req.Windows.Previous != nil {
			out = append(out, refererPrevUnavailable)
		}
		if refererExpired(req, cur) {
			out = append(out, refererCurUndercount)
		}
	}
	return out
}
