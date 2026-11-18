package reports

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/clicks"
)

// DirectValue is the breakdown value for clicks with no referrer.
const DirectValue = "(direct)"

const (
	defaultBreakdownLimit = 20
	defaultSeriesLimit    = 5
	maxSeriesLimit        = 50
)

// BreakdownDimensions lists every dimension breakdown reports on and
// timeseries splits by: the filterable UTM-style ones plus referer, campaign,
// link and content_group.
var BreakdownDimensions = []string{
	"source", "medium", "utm_campaign", "content", "term", "referer",
	"placement", "campaign", "link", "content_group",
}

// refererHost extracts the lower-cased host of clicks.referer: the scheme, any
// userinfo, port, path, query and fragment are dropped. A non-empty value with
// no parseable host is kept as is; empty or NULL (including clicks whose
// detail aged out, see clicks.DetailRetention) is NULL here and DirectValue
// once coalesced.
const refererHost = `COALESCE(lower(substring(btrim(k.referer) from '^(?:[A-Za-z][A-Za-z0-9+.-]*:)?//(?:[^/?#@]*@)?(\[[^]]+\]|[^/?#:]+)')), NULLIF(btrim(k.referer), ''))`

// dimSQL is how a SQL-grouped dimension reads its value and label.
type dimSQL struct {
	value, label, joins string
}

// dimensionSQL returns the SELECT expressions for dimension d, or false for
// content_group (grouped in Go) and unknown names. Values are never NULL:
// empty maps to NoneValue (CampaignNone for campaign, DirectValue for referer),
// so each is usable as a filter token where one exists.
func dimensionSQL(d string) (dimSQL, bool) {
	switch d {
	case "referer":
		return dimSQL{value: `COALESCE(` + refererHost + `, '` + DirectValue + `')`, label: `''`}, true
	case "campaign":
		return dimSQL{value: `COALESCE(c.slug, '` + CampaignNone + `')`, label: `COALESCE(c.name, 'No campaign')`,
			joins: "LEFT JOIN campaigns c ON c.id = k.campaign_id"}, true
	case "link":
		return dimSQL{value: `l.key`, label: `COALESCE(l.title, '')`}, true
	}
	if expr, ok := DimensionExpr(d); ok {
		return dimSQL{value: `COALESCE(` + expr + `, '` + NoneValue + `')`, label: `''`}, true
	}
	return dimSQL{}, false
}

// bucket is one value of a dimension: its clicks in the current window and,
// for breakdown, the previous one.
type bucket struct {
	value, label string
	clicks, prev int64
}

// sortBuckets orders by clicks descending, then value, so output is stable.
func sortBuckets(bs []bucket) {
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].clicks != bs[j].clicks {
			return bs[i].clicks > bs[j].clicks
		}
		return bs[i].value < bs[j].value
	})
}

// sqlBuckets counts clicks per value of SQL dimension d in w.
func sqlBuckets(ctx context.Context, req *Request, d string, w Window) ([]bucket, error) {
	ds, _ := dimensionSQL(d)
	sc := req.Params.ScopedQuery(req.UserID, w, ScopeOpts{Joins: ds.joins})
	rows, err := req.Q.Query(ctx,
		`SELECT `+ds.value+`, `+ds.label+`, count(*)`+sc.SQL+` GROUP BY 1, 2`, sc.Args...)
	if err != nil {
		return nil, fmt.Errorf("reports: breakdown %s: %w", d, err)
	}
	defer rows.Close()
	var out []bucket
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.value, &b.label, &b.clicks); err != nil {
			return nil, fmt.Errorf("reports: breakdown %s scan: %w", d, err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// contentGroups groups the set's links by ContentKey, as top-content does,
// keeping groups with at least one current click. byLink maps a link id to its
// group's key.
func contentGroups(set *linkSet) (groups []*bucket, byLink map[int64]string) {
	members := map[string][]*linkInfo{}
	var order []string
	for _, li := range set.links {
		if _, ok := members[li.contentKey]; !ok {
			order = append(order, li.contentKey)
		}
		members[li.contentKey] = append(members[li.contentKey], li)
	}
	byLink = map[int64]string{}
	for _, k := range order {
		ms := members[k]
		g := &bucket{value: k}
		ti := make([]TitleInput, 0, len(ms))
		for _, li := range ms {
			g.clicks += li.clicks
			g.prev += li.prev
			byLink[li.id] = k
			ti = append(ti, TitleInput{Title: li.title, UTMSource: li.utmSource})
		}
		if g.clicks == 0 {
			continue
		}
		g.label = ContentTitle(ti, ms[0].dest)
		groups = append(groups, g)
	}
	return groups, byLink
}

// BreakdownRow is one value in a breakdown.
type BreakdownRow struct {
	Value          string  `json:"value"`
	Label          string  `json:"label,omitempty"`
	Clicks         int64   `json:"clicks"`
	Share          float64 `json:"share"`
	PreviousClicks *int64  `json:"previous_clicks"`
}

// OtherRow sums the values beyond limit.
type OtherRow struct {
	Clicks int64 `json:"clicks"`
	Values int   `json:"values"`
}

// BreakdownData is the breakdown report's payload.
type BreakdownData struct {
	Dimension string `json:"dimension"`
	Total     int64  `json:"total"`
	// PreviousTotal is the previous window's counted clicks, null without one.
	PreviousTotal *int64         `json:"previous_total"`
	Rows          []BreakdownRow `json:"rows"`
	// Other is null when every value fits within limit.
	Other *OtherRow `json:"other"`
	// Truncated mirrors top-content: a per-link query hit its row cap.
	Truncated bool `json:"truncated,omitempty"`
}

var dimensionSpec = ParamSpec{
	Name: "dimension", Type: "enum", Required: true, Values: BreakdownDimensions,
	Description: "What to group clicks by. referer is the referrer hostname, (direct) when empty; campaign values are slugs (none for no campaign); link values are link keys; content_group groups links to the same destination (see top-content).",
}

var breakdownReport = Report{
	Name:         "breakdown",
	Description:  "Clicks across all your links grouped by one dimension (source, referer, campaign, link, content group, ...), with share of total and the previous period.",
	Params:       []ParamSpec{dimensionSpec},
	DefaultLimit: defaultBreakdownLimit,
	Run:          runBreakdown,
}

// requireDimension reads the required dimension parameter.
func requireDimension(req *Request) (string, error) {
	if _, err := req.RequiredString("dimension"); err != nil {
		return "", err
	}
	return req.EnumParam("dimension", "", BreakdownDimensions...)
}

func runBreakdown(ctx context.Context, req *Request) (Output, error) {
	dim, err := requireDimension(req)
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultBreakdownLimit)
	hasPrev := req.Windows.Previous != nil
	// Referrers are cleared after the retention window, so a previous period
	// that reaches past it cannot be compared.
	prevHidden := dim == "referer" && hasPrev && refererExpired(req, *req.Windows.Previous)

	var cur []bucket
	prev := map[string]int64{}
	var prevTotal int64
	data := BreakdownData{Dimension: dim}
	if dim == "content_group" {
		set, err := loadLinks(ctx, req, false)
		if err != nil {
			return Output{}, err
		}
		groups, _ := contentGroups(set)
		for _, g := range groups {
			cur = append(cur, *g)
			prev[g.value] = g.prev
		}
		data.Truncated = set.truncated
		// Previous-only groups still count towards the previous total.
		for _, li := range set.links {
			prevTotal += li.prev
		}
	} else {
		if cur, err = sqlBuckets(ctx, req, dim, req.Windows.Current); err != nil {
			return Output{}, err
		}
		if hasPrev && !prevHidden {
			pb, err := sqlBuckets(ctx, req, dim, *req.Windows.Previous)
			if err != nil {
				return Output{}, err
			}
			for _, b := range pb {
				prev[b.value] = b.clicks
				prevTotal += b.clicks
			}
		}
	}
	if hasPrev && !prevHidden {
		data.PreviousTotal = &prevTotal
	}

	sortBuckets(cur)
	for _, b := range cur {
		data.Total += b.clicks
	}
	data.Rows = []BreakdownRow{}
	for i, b := range cur {
		if i >= limit {
			if data.Other == nil {
				data.Other = &OtherRow{}
			}
			data.Other.Clicks += b.clicks
			data.Other.Values++
			continue
		}
		row := BreakdownRow{Value: b.value, Label: b.label, Clicks: b.clicks,
			Share: math.Round(float64(b.clicks)/float64(data.Total)*1000) / 1000}
		if hasPrev && !prevHidden {
			row.PreviousClicks = prevPtr(prev[b.value], true)
		}
		data.Rows = append(data.Rows, row)
	}
	return Output{Highlights: breakdownHighlights(data, req), Data: data}, nil
}

// dimNoun is a dimension's name as it reads in a sentence.
func dimNoun(d string) string { return strings.ReplaceAll(d, "_", " ") }

// refererExpired reports whether w starts before the click-detail retention
// cutoff, after which clicks.referer has been cleared and reads as (direct).
func refererExpired(req *Request, w Window) bool {
	return w.Start.Before(req.Now.Add(-clicks.DetailRetention))
}

const (
	refererPrevUnavailable = "Referrers for the previous period are unavailable because referrer detail is only kept for 90 days."
	refererCurUndercount   = "Referrers older than 90 days are cleared, so those clicks show as (direct)."
)

// rowName is how a row reads in a sentence.
func rowName(value, label string) string {
	if label != "" {
		return label
	}
	if value == NoneValue {
		return "(none)"
	}
	return value
}

func breakdownHighlights(d BreakdownData, req *Request) []string {
	cur := req.Windows.Current
	if d.Total == 0 {
		return []string{fmt.Sprintf("No clicks between %s and %s.", cur.From, cur.To)}
	}
	noun := dimNoun(d.Dimension)
	top := d.Rows[0]
	out := []string{fmt.Sprintf("%s %s between %s and %s.", commas(d.Total), plural(d.Total, "click", "clicks"), cur.From, cur.To),
		fmt.Sprintf("Top %s: %s (%s, %d%% of clicks).", noun, rowName(top.Value, top.Label), commas(top.Clicks), pct(top.Clicks, d.Total))}
	if req.Windows.Previous != nil && d.PreviousTotal != nil {
		var best *BreakdownRow
		var bestDelta int64
		for i, r := range d.Rows {
			if r.PreviousClicks == nil {
				continue
			}
			if dl := r.Clicks - *r.PreviousClicks; dl > bestDelta {
				best, bestDelta = &d.Rows[i], dl
			}
		}
		if best != nil {
			out = append(out, fmt.Sprintf("Biggest gain: %s, up %s %s on the previous %d days.",
				rowName(best.Value, best.Label), commas(bestDelta), plural(bestDelta, "click", "clicks"), req.Windows.Previous.Days()))
		}
	}
	if d.Other != nil {
		out = append(out, fmt.Sprintf("%s other %s account for %s %s (%d%%).", strconv.Itoa(d.Other.Values),
			plural(int64(d.Other.Values), "value", "values"), commas(d.Other.Clicks), plural(d.Other.Clicks, "click", "clicks"), pct(d.Other.Clicks, d.Total)))
	}
	if d.Dimension == "referer" {
		if d.PreviousTotal == nil && req.Windows.Previous != nil {
			out = append(out, refererPrevUnavailable)
		}
		if refererExpired(req, cur) {
			out = append(out, refererCurUndercount)
		}
	}
	if d.Truncated {
		out = append(out, "Counts may be incomplete because there are too many links to total in one query.")
	}
	return out
}
