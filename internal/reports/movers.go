package reports

import (
	"context"
	"fmt"
	"sort"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

const (
	defaultMoversLimit     = 5
	defaultMoversMinClicks = 5
)

// MoverRow is one content group or link in a movers list.
type MoverRow struct {
	Title      string `json:"title"`
	ContentKey string `json:"content_key"`
	// Key and ShortURL are set for unit=link only.
	Key            string   `json:"key,omitempty"`
	ShortURL       string   `json:"short_url,omitempty"`
	Clicks         int64    `json:"clicks"`
	PreviousClicks int64    `json:"previous_clicks"`
	Change         int64    `json:"change"`
	ChangePct      *float64 `json:"change_pct"`
}

// MoversData is the movers payload.
type MoversData struct {
	Unit      string `json:"unit"`
	MinClicks int    `json:"min_clicks"`
	// Compared is false when the request has no previous window (compare=none
	// or range=all); every list is then empty.
	Compared  bool       `json:"compared"`
	Rising    []MoverRow `json:"rising"`
	Falling   []MoverRow `json:"falling"`
	New       []MoverRow `json:"new"`
	GoneQuiet []MoverRow `json:"gone_quiet"`
	Truncated bool       `json:"truncated,omitempty"`
}

var moversReport = Report{
	Name:        "movers",
	Description: "Content or links with the biggest change in clicks against the previous period: rising, falling, new and gone quiet.",
	Params: []ParamSpec{
		{Name: "unit", Type: "enum", Default: "content", Values: []string{"content", "link"}, Description: "content groups links to the same destination (as top-content does); link compares individual links."},
		{Name: "min_clicks", Type: "int", Default: "5", Description: "Ignore items whose larger period has fewer clicks than this, so tiny numbers do not dominate. For new and gone_quiet it is the clicks in the period that has any."},
	},
	DefaultLimit: defaultMoversLimit,
	Run:          runMovers,
}

func runMovers(ctx context.Context, req *Request) (Output, error) {
	unit, err := req.EnumParam("unit", "content", "content", "link")
	if err != nil {
		return Output{}, err
	}
	minClicks, err := req.IntParam("min_clicks", defaultMoversMinClicks, 0, 1000000)
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultMoversLimit)
	set, err := loadLinks(ctx, req, false)
	if err != nil {
		return Output{}, err
	}
	data := MoversData{Unit: unit, MinClicks: minClicks, Compared: set.hasPrev, Truncated: set.truncated,
		Rising: []MoverRow{}, Falling: []MoverRow{}, New: []MoverRow{}, GoneQuiet: []MoverRow{}}
	if !set.hasPrev {
		return Output{Highlights: moversHighlights(data, req), Data: data}, nil
	}

	var items []MoverRow
	if unit == "link" {
		for _, li := range set.links {
			title := li.title
			if title == "" {
				title = li.key
			}
			items = append(items, MoverRow{Title: title, ContentKey: li.contentKey, Key: li.key,
				ShortURL: qr.ShortURL(req.BaseURL, li.key), Clicks: li.clicks, PreviousClicks: li.prev})
		}
	} else {
		// The same grouping and titles as top-content, but keeping groups that
		// only had clicks in the previous period (those are the gone-quiet ones).
		members := map[string][]*linkInfo{}
		var order []string
		for _, li := range set.links {
			if _, ok := members[li.contentKey]; !ok {
				order = append(order, li.contentKey)
			}
			members[li.contentKey] = append(members[li.contentKey], li)
		}
		for _, k := range order {
			ms := members[k]
			// Same member order as top-content, so ContentTitle ties break alike.
			sort.SliceStable(ms, func(i, j int) bool {
				if ms[i].clicks != ms[j].clicks {
					return ms[i].clicks > ms[j].clicks
				}
				return ms[i].key < ms[j].key
			})
			row := MoverRow{ContentKey: k}
			ti := make([]TitleInput, 0, len(ms))
			for _, li := range ms {
				row.Clicks += li.clicks
				row.PreviousClicks += li.prev
				ti = append(ti, TitleInput{Title: li.title, UTMSource: li.utmSource})
			}
			row.Title = ContentTitle(ti, ms[0].dest)
			items = append(items, row)
		}
	}

	min := int64(minClicks)
	for _, it := range items {
		it.Change = it.Clicks - it.PreviousClicks
		it.ChangePct = changePct(it.Clicks, it.PreviousClicks, true)
		switch {
		case it.PreviousClicks == 0 && it.Clicks > 0 && it.Clicks >= min:
			data.New = append(data.New, it)
		case it.Clicks == 0 && it.PreviousClicks > 0 && it.PreviousClicks >= min:
			data.GoneQuiet = append(data.GoneQuiet, it)
		case it.PreviousClicks > 0 && it.Clicks > it.PreviousClicks && it.Clicks >= min:
			data.Rising = append(data.Rising, it)
		case it.Clicks > 0 && it.PreviousClicks > it.Clicks && it.PreviousClicks >= min:
			data.Falling = append(data.Falling, it)
		}
	}
	absChange := func(r MoverRow) int64 {
		if r.Change < 0 {
			return -r.Change
		}
		return r.Change
	}
	absPct := func(r MoverRow) float64 {
		if r.ChangePct == nil {
			return 0
		}
		if *r.ChangePct < 0 {
			return -*r.ChangePct
		}
		return *r.ChangePct
	}
	rank := func(rows []MoverRow, by func(a, b MoverRow) bool) []MoverRow {
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if by(a, b) != by(b, a) {
				return by(a, b)
			}
			if a.ContentKey != b.ContentKey {
				return a.ContentKey < b.ContentKey
			}
			return a.Key < b.Key
		})
		if len(rows) > limit {
			rows = rows[:limit]
		}
		return rows
	}
	// Rising and falling: absolute change, then percentage.
	byChange := func(a, b MoverRow) bool {
		if absChange(a) != absChange(b) {
			return absChange(a) > absChange(b)
		}
		return absPct(a) > absPct(b)
	}
	data.Rising = rank(data.Rising, byChange)
	data.Falling = rank(data.Falling, byChange)
	data.New = rank(data.New, func(a, b MoverRow) bool { return a.Clicks > b.Clicks })
	data.GoneQuiet = rank(data.GoneQuiet, func(a, b MoverRow) bool { return a.PreviousClicks > b.PreviousClicks })
	return Output{Highlights: moversHighlights(data, req), Data: data}, nil
}

func moverName(unit string, r MoverRow) string {
	if unit == "link" && r.Key != "" {
		return fmt.Sprintf("%q (/u/%s)", r.Title, r.Key)
	}
	return fmt.Sprintf("%q", r.Title)
}

func moversHighlights(d MoversData, req *Request) []string {
	var out []string
	switch {
	case !d.Compared:
		out = []string{"Movers need a previous period to compare against; use a range other than all and leave compare on."}
	case len(d.Rising)+len(d.Falling)+len(d.New)+len(d.GoneQuiet) == 0:
		out = []string{fmt.Sprintf("Nothing moved by at least %d clicks against the previous %d days.", d.MinClicks, req.Windows.Previous.Days())}
	default:
		if len(d.Rising) > 0 {
			r := d.Rising[0]
			s := fmt.Sprintf("%s is up", moverName(d.Unit, r))
			if r.ChangePct != nil {
				s += fmt.Sprintf(" %s%%", trimPct(*r.ChangePct))
			}
			out = append(out, fmt.Sprintf("%s (%s to %s clicks).", s, commas(r.PreviousClicks), commas(r.Clicks)))
		}
		if len(d.Falling) > 0 {
			r := d.Falling[0]
			s := fmt.Sprintf("%s is down", moverName(d.Unit, r))
			if r.ChangePct != nil {
				s += fmt.Sprintf(" %s%%", trimPct(-*r.ChangePct))
			}
			out = append(out, fmt.Sprintf("%s (%s to %s clicks).", s, commas(r.PreviousClicks), commas(r.Clicks)))
		}
		if len(d.New) > 0 {
			r := d.New[0]
			out = append(out, fmt.Sprintf("%s is new with %s clicks.", moverName(d.Unit, r), commas(r.Clicks)))
		}
		if len(d.GoneQuiet) > 0 {
			r := d.GoneQuiet[0]
			out = append(out, fmt.Sprintf("%s has gone quiet after %s clicks in the previous period.", moverName(d.Unit, r), commas(r.PreviousClicks)))
		}
	}
	return truncNote(out, d.Truncated)
}

// trimPct formats a percentage without a trailing ".0".
func trimPct(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}
