package reports

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// CampaignTopContent is a campaign's best content group in the window.
type CampaignTopContent struct {
	Title  string `json:"title"`
	Clicks int64  `json:"clicks"`
}

// CampaignComparisonRow is one campaign in campaign-comparison.
type CampaignComparisonRow struct {
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	Status         string   `json:"status"`
	StartsOn       *string  `json:"starts_on"`
	EndsOn         *string  `json:"ends_on"`
	Links          int      `json:"links"`
	LinksClicked   int64    `json:"links_clicked"`
	Clicks         int64    `json:"clicks"`
	PreviousClicks *int64   `json:"previous_clicks"`
	ChangePct      *float64 `json:"change_pct"`
	// ClicksPerActiveDay divides clicks by the days of the window the
	// campaign's date range covers (the whole window when it has no dates);
	// null when the campaign was not active on any day of the window.
	ClicksPerActiveDay *float64            `json:"clicks_per_active_day"`
	TopContent         *CampaignTopContent `json:"top_content"`
	TopSource          *SourceCount        `json:"top_source"`
}

// CampaignComparisonData is the campaign-comparison payload.
type CampaignComparisonData struct {
	Campaigns []CampaignComparisonRow `json:"campaigns"`
	Total     int                     `json:"total_campaigns"`
	Truncated bool                    `json:"truncated,omitempty"`
}

var campaignComparisonReport = Report{
	Name: "campaign-comparison",
	Description: "Every campaign with counted clicks in the window or a date range that overlaps it, side by side: status, clicks, previous clicks, clicks per active day, top content and top source. " +
		"The campaign filter limits it to one campaign; the UTM and placement filters apply to the counted clicks.",
	DefaultLimit: 0,
	Run:          runCampaignComparison,
}

type campRec struct {
	id           int64
	row          CampaignComparisonRow
	starts, ends *time.Time
	archived     bool
	byContent    map[string]*campContent
	bySource     map[string]int64
}

type campContent struct {
	members []TitleInput
	dest    string
	clicks  int64
}

func runCampaignComparison(ctx context.Context, req *Request) (Output, error) {
	p := req.Params
	w := req.Windows.Current
	q := `SELECT id, slug, name, starts_at, ends_at, archived,
	        (SELECT count(*) FROM links l WHERE l.campaign_id = c.id) FROM campaigns c WHERE c.user_id = $1`
	qargs := []any{req.UserID}
	if p.Campaign != "" && p.Campaign != CampaignNone {
		q += " AND c.slug = $2"
		qargs = append(qargs, p.Campaign)
	} else if p.Campaign == CampaignNone {
		q += " AND FALSE"
	}
	rows, err := req.Q.Query(ctx, q, qargs...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: campaigns: %w", err)
	}
	byID := map[int64]*campRec{}
	var recs []*campRec
	for rows.Next() {
		r := &campRec{byContent: map[string]*campContent{}, bySource: map[string]int64{}}
		if err := rows.Scan(&r.id, &r.row.Slug, &r.row.Name, &r.starts, &r.ends, &r.archived, &r.row.Links); err != nil {
			rows.Close()
			return Output{}, fmt.Errorf("reports: campaigns scan: %w", err)
		}
		r.row.Status = CampaignStatus(r.starts, r.ends, r.archived, req.Now, p.TZ)
		if r.starts != nil {
			s := CampaignDate(*r.starts)
			r.row.StartsOn = &s
		}
		if r.ends != nil {
			s := CampaignDate(*r.ends)
			r.row.EndsOn = &s
		}
		byID[r.id] = r
		recs = append(recs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: campaigns: %w", err)
	}

	// Clicks, links clicked and previous clicks per campaign. Clicks carry the
	// campaign they were recorded under, so history is not rewritten when a
	// link moves.
	data := CampaignComparisonData{Campaigns: []CampaignComparisonRow{}}
	agg := func(win Window, current bool) error {
		sc := p.ScopedQuery(req.UserID, win, ScopeOpts{Where: "k.campaign_id IS NOT NULL"})
		rs, err := req.Q.Query(ctx, `SELECT k.campaign_id, count(*), count(DISTINCT l.id)`+sc.SQL+` GROUP BY k.campaign_id`, sc.Args...)
		if err != nil {
			return fmt.Errorf("reports: campaign clicks: %w", err)
		}
		defer rs.Close()
		for rs.Next() {
			var id, n, linksClicked int64
			if err := rs.Scan(&id, &n, &linksClicked); err != nil {
				return fmt.Errorf("reports: campaign clicks scan: %w", err)
			}
			r := byID[id]
			if r == nil {
				continue
			}
			if current {
				r.row.Clicks, r.row.LinksClicked = n, linksClicked
			} else {
				r.row.PreviousClicks = &n
			}
		}
		return rs.Err()
	}
	if err := agg(w, true); err != nil {
		return Output{}, err
	}
	if req.Windows.Previous != nil {
		if err := agg(*req.Windows.Previous, false); err != nil {
			return Output{}, err
		}
	}

	// Top content and source from per-link counts, grouped like top-content.
	srcExpr, _ := DimensionExpr("source")
	sc := p.ScopedQuery(req.UserID, w, ScopeOpts{Where: "k.campaign_id IS NOT NULL"})
	rs, err := req.Q.Query(ctx, `SELECT k.campaign_id, l.id, `+srcExpr+`, count(*)`+sc.SQL+
		` GROUP BY k.campaign_id, l.id, 3 ORDER BY 4 DESC, 1, 2 LIMIT `+strconv.Itoa(maxLinkRows), sc.Args...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: campaign detail: %w", err)
	}
	type cell struct {
		camp, link, n int64
		src           *string
	}
	var cells []cell
	linkIDs := map[int64]bool{}
	for rs.Next() {
		var c cell
		if err := rs.Scan(&c.camp, &c.link, &c.src, &c.n); err != nil {
			rs.Close()
			return Output{}, fmt.Errorf("reports: campaign detail scan: %w", err)
		}
		cells = append(cells, c)
		linkIDs[c.link] = true
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: campaign detail: %w", err)
	}
	data.Truncated = len(cells) >= maxLinkRows
	type linkRec struct {
		title, dest, source, ckey string
	}
	links := map[int64]linkRec{}
	if len(linkIDs) > 0 {
		ids := make([]int64, 0, len(linkIDs))
		for id := range linkIDs {
			ids = append(ids, id)
		}
		lr, err := req.Q.Query(ctx, `SELECT id, COALESCE(title, ''), destination_url, COALESCE(utm_source, '') FROM links WHERE user_id = $1 AND id = ANY($2::bigint[])`, req.UserID, ids)
		if err != nil {
			return Output{}, fmt.Errorf("reports: campaign links: %w", err)
		}
		for lr.Next() {
			var id int64
			var l linkRec
			if err := lr.Scan(&id, &l.title, &l.dest, &l.source); err != nil {
				lr.Close()
				return Output{}, fmt.Errorf("reports: campaign links scan: %w", err)
			}
			l.ckey = ContentKey(l.dest)
			links[id] = l
		}
		lr.Close()
		if err := lr.Err(); err != nil {
			return Output{}, fmt.Errorf("reports: campaign links: %w", err)
		}
	}
	seenMember := map[[2]int64]bool{}
	for _, c := range cells {
		r, l := byID[c.camp], links[c.link]
		if r == nil {
			continue
		}
		cc := r.byContent[l.ckey]
		if cc == nil {
			cc = &campContent{dest: l.dest}
			r.byContent[l.ckey] = cc
		}
		cc.clicks += c.n
		if k := [2]int64{c.camp, c.link}; !seenMember[k] {
			seenMember[k] = true
			cc.members = append(cc.members, TitleInput{Title: l.title, UTMSource: l.source})
		}
		src := NoneValue
		if c.src != nil {
			src = *c.src
		}
		r.bySource[src] += c.n
	}

	for _, r := range recs {
		if r.row.Clicks == 0 && !overlaps(r, w) {
			continue
		}
		var bestKey string
		var best *campContent
		for k, cc := range r.byContent {
			if best == nil || cc.clicks > best.clicks || (cc.clicks == best.clicks && k < bestKey) {
				best, bestKey = cc, k
			}
		}
		if best != nil {
			r.row.TopContent = &CampaignTopContent{Title: ContentTitle(best.members, best.dest), Clicks: best.clicks}
		}
		for _, s := range sourceCounts(r.bySource) {
			if s.Value != NoneValue {
				s := s
				r.row.TopSource = &s
				break
			}
		}
		if r.row.PreviousClicks != nil || req.Windows.Previous != nil {
			if r.row.PreviousClicks == nil {
				zero := int64(0)
				r.row.PreviousClicks = &zero
			}
			r.row.ChangePct = changePct(r.row.Clicks, *r.row.PreviousClicks, true)
		}
		if d := activeDays(r, w); d > 0 {
			v := math.Round(float64(r.row.Clicks)/float64(d)*10) / 10
			r.row.ClicksPerActiveDay = &v
		}
		data.Campaigns = append(data.Campaigns, r.row)
	}
	sort.SliceStable(data.Campaigns, func(i, j int) bool {
		a, b := data.Campaigns[i], data.Campaigns[j]
		if a.Clicks != b.Clicks {
			return a.Clicks > b.Clicks
		}
		return a.Slug < b.Slug
	})
	data.Total = len(data.Campaigns)
	if limit := p.Limit; limit > 0 && len(data.Campaigns) > limit {
		data.Campaigns = data.Campaigns[:limit]
	}
	return Output{Highlights: campaignHighlights(data, req), Data: data}, nil
}

// rangeIn returns the first and last civil day (YYYY-MM-DD) of the campaign's
// range clipped to w, or ok=false when it does not touch w. A missing bound is
// open, so a campaign with no dates at all covers the whole window for the
// purpose of clicks per active day but does not count as overlapping.
func rangeIn(r *campRec, w Window) (from, to string, ok bool) {
	from, to = w.From, w.To
	if r.starts != nil && CampaignDate(*r.starts) > from {
		from = CampaignDate(*r.starts)
	}
	if r.ends != nil && CampaignDate(*r.ends) < to {
		to = CampaignDate(*r.ends)
	}
	return from, to, from <= to
}

// overlaps reports whether the campaign has a date range that touches w. A
// campaign with no dates never overlaps; it appears only through its clicks.
func overlaps(r *campRec, w Window) bool {
	if r.starts == nil && r.ends == nil {
		return false
	}
	_, _, ok := rangeIn(r, w)
	return ok
}

func activeDays(r *campRec, w Window) int {
	from, to, ok := rangeIn(r, w)
	if !ok {
		return 0
	}
	f, _ := parseDay(from)
	t, _ := parseDay(to)
	return int(t.Sub(f).Hours()/24+0.5) + 1
}

func campaignHighlights(d CampaignComparisonData, req *Request) []string {
	w := req.Windows.Current
	if len(d.Campaigns) == 0 {
		return []string{fmt.Sprintf("No campaigns had clicks or were scheduled between %s and %s.", w.From, w.To)}
	}
	top := d.Campaigns[0]
	if top.Clicks == 0 {
		return truncNote([]string{fmt.Sprintf("%d %s in range, none with clicks between %s and %s.", d.Total, plural(int64(d.Total), "campaign", "campaigns"), w.From, w.To)}, d.Truncated)
	}
	s := fmt.Sprintf("Top campaign: %q with %s %s", top.Name, commas(top.Clicks), plural(top.Clicks, "click", "clicks"))
	if top.PreviousClicks != nil {
		s += ", " + delta(top.Clicks, *top.PreviousClicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
	}
	out := []string{s + "."}
	if top.TopContent != nil {
		out = append(out, fmt.Sprintf("Its best content is %q (%s clicks).", top.TopContent.Title, commas(top.TopContent.Clicks)))
	}
	out = append(out, fmt.Sprintf("%d %s compared.", d.Total, plural(int64(d.Total), "campaign", "campaigns")))
	return truncNote(out, d.Truncated)
}
