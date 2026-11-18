package reports

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

const (
	defaultTopContentLimit = 10
	defaultTopLinksLimit   = 20
)

// maxLinkRows caps the queries; a variable so tests can lower it.
var maxLinkRows = 20000

// SourceCount is clicks from one utm_source value (NoneValue for empty).
type SourceCount struct {
	Value  string `json:"value"`
	Clicks int64  `json:"clicks"`
}

// GroupLink is one member link inside a content group.
type GroupLink struct {
	Key          string  `json:"key"`
	Label        string  `json:"label"`
	UTMSource    string  `json:"utm_source"`
	UTMMedium    string  `json:"utm_medium"`
	CampaignSlug *string `json:"campaign_slug"`
	Clicks       int64   `json:"clicks"`
}

// ContentGroup is one piece of content: every link whose destination has the
// same ContentKey.
type ContentGroup struct {
	ContentKey     string        `json:"content_key"`
	Title          string        `json:"title"`
	Path           string        `json:"path"`
	Clicks         int64         `json:"clicks"`
	PreviousClicks *int64        `json:"previous_clicks"`
	ChangePct      *float64      `json:"change_pct"`
	BySource       []SourceCount `json:"by_source"`
	Links          []GroupLink   `json:"links"`

	newest time.Time
}

// TopContentData is the top-content payload.
type TopContentData struct {
	Groups      []ContentGroup `json:"groups"`
	TotalGroups int            `json:"total_groups"`
	// Truncated is set when a query hit the row cap, so counts may be incomplete.
	Truncated bool `json:"truncated,omitempty"`
}

// LinkRow is one link in top-links.
type LinkRow struct {
	Key            string    `json:"key"`
	ShortURL       string    `json:"short_url"`
	Title          string    `json:"title"`
	Label          string    `json:"label"`
	ContentKey     string    `json:"content_key"`
	ContentTitle   string    `json:"content_title"`
	Destination    string    `json:"destination"`
	CampaignSlug   *string   `json:"campaign_slug"`
	UTMSource      string    `json:"utm_source"`
	UTMMedium      string    `json:"utm_medium"`
	UTMCampaign    string    `json:"utm_campaign"`
	UTMContent     string    `json:"utm_content"`
	UTMTerm        string    `json:"utm_term"`
	Placement      string    `json:"placement"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	Clicks         int64     `json:"clicks"`
	PreviousClicks *int64    `json:"previous_clicks"`
	ChangePct      *float64  `json:"change_pct"`
}

// TopLinksData is the top-links payload.
type TopLinksData struct {
	Links      []LinkRow `json:"links"`
	TotalLinks int       `json:"total_links"`
	Truncated  bool      `json:"truncated,omitempty"`
}

var sortSpec = ParamSpec{
	Name: "sort", Type: "enum", Default: "clicks", Values: []string{"clicks", "change", "newest"},
	Description: "clicks ranks by clicks; change by growth over the previous window (clicks minus previous clicks); newest by creation date, including items with no clicks.",
}

var topContentReport = Report{
	Name:         "top-content",
	Description:  "Content ranked by clicks, with links to the same destination grouped together across platforms and campaigns.",
	Params:       []ParamSpec{sortSpec},
	DefaultLimit: defaultTopContentLimit,
	Run:          runTopContent,
}

var topLinksReport = Report{
	Name:         "top-links",
	Description:  "Individual links ranked by clicks, with their content key, campaign and UTM fields.",
	Params:       []ParamSpec{sortSpec},
	DefaultLimit: defaultTopLinksLimit,
	Run:          runTopLinks,
}

// linkInfo is a link row plus its counted clicks.
type linkInfo struct {
	id                                int64
	key, title, dest, placement       string
	utmSource, utmMedium, utmCampaign string
	utmContent, utmTerm               string
	campaign                          *string
	active                            bool
	created                           time.Time
	clicks, prev                      int64
	bySource                          map[string]int64
	contentKey, head, label           string
}

// linkSet is the per-link result of loadLinks.
type linkSet struct {
	links []*linkInfo
	// hasPrev is true when a previous window was queried.
	hasPrev bool
	// truncated is true when a capped query hit maxLinkRows.
	truncated bool
}

// loadLinks aggregates counted clicks per link (and per click utm_source) in
// the current and previous windows in SQL, then fetches the link rows. With
// all, every link of the user is returned (newest sort), otherwise only links
// with a counted click in either window; callers drop those with no current
// clicks themselves when grouping.
func loadLinks(ctx context.Context, req *Request, all bool) (*linkSet, error) {
	p := req.Params
	byID := map[int64]*linkInfo{}
	set := &linkSet{}
	get := func(id int64) *linkInfo {
		li := byID[id]
		if li == nil {
			li = &linkInfo{id: id, bySource: map[string]int64{}}
			byID[id] = li
		}
		return li
	}
	srcExpr, _ := DimensionExpr("source")
	count := func(w Window, current bool) error {
		sc := p.ScopedQuery(req.UserID, w, ScopeOpts{})
		rows, err := req.Q.Query(ctx,
			`SELECT l.id, `+srcExpr+` AS v, count(*) AS n`+sc.SQL+
				` GROUP BY l.id, v ORDER BY n DESC, l.id LIMIT `+strconv.Itoa(maxLinkRows), sc.Args...)
		if err != nil {
			return fmt.Errorf("reports: per-link clicks: %w", err)
		}
		defer rows.Close()
		seen := 0
		for rows.Next() {
			seen++
			var id, n int64
			var v *string
			if err := rows.Scan(&id, &v, &n); err != nil {
				return fmt.Errorf("reports: per-link clicks scan: %w", err)
			}
			li := get(id)
			if current {
				li.clicks += n
				src := NoneValue
				if v != nil {
					src = *v
				}
				li.bySource[src] += n
			} else {
				li.prev += n
			}
		}
		if seen >= maxLinkRows {
			set.truncated = true
		}
		return rows.Err()
	}
	if err := count(req.Windows.Current, true); err != nil {
		return nil, err
	}
	if prev := req.Windows.Previous; prev != nil {
		set.hasPrev = true
		if err := count(*prev, false); err != nil {
			return nil, err
		}
	}

	const cols = `SELECT l.id, l.key, COALESCE(l.title, ''), l.destination_url, COALESCE(l.placement, ''),
		COALESCE(l.utm_source, ''), COALESCE(l.utm_medium, ''), COALESCE(l.utm_campaign, ''),
		COALESCE(l.utm_content, ''), COALESCE(l.utm_term, ''), c.slug, COALESCE(l.active, TRUE), l.created_at
		FROM links l LEFT JOIN campaigns c ON c.id = l.campaign_id WHERE l.user_id = $1`
	var rows interface {
		Next() bool
		Scan(...any) error
		Err() error
		Close()
	}
	var err error
	if all {
		rows, err = req.Q.Query(ctx, cols+` ORDER BY l.created_at DESC, l.id DESC LIMIT `+strconv.Itoa(maxLinkRows), req.UserID)
	} else {
		ids := make([]int64, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		rows, err = req.Q.Query(ctx, cols+` AND l.id = ANY($2::bigint[])`, req.UserID, ids)
	}
	if err != nil {
		return nil, fmt.Errorf("reports: link rows: %w", err)
	}
	defer rows.Close()
	nrows := 0
	for rows.Next() {
		nrows++
		var li linkInfo
		if err := rows.Scan(&li.id, &li.key, &li.title, &li.dest, &li.placement,
			&li.utmSource, &li.utmMedium, &li.utmCampaign, &li.utmContent, &li.utmTerm,
			&li.campaign, &li.active, &li.created); err != nil {
			return nil, fmt.Errorf("reports: link rows scan: %w", err)
		}
		if c := byID[li.id]; c != nil {
			li.clicks, li.prev, li.bySource = c.clicks, c.prev, c.bySource
		} else {
			li.bySource = map[string]int64{}
		}
		li.contentKey = ContentKey(li.dest)
		li.head, li.label = LinkLabel(li.title, li.utmSource)
		set.links = append(set.links, &li)
	}
	if all && nrows >= maxLinkRows {
		set.truncated = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reports: link rows: %w", err)
	}
	return set, nil
}

// changePct returns the percentage change of cur over prev rounded to one
// decimal, or nil when there is no previous window or prev is zero.
func changePct(cur, prev int64, hasPrev bool) *float64 {
	if !hasPrev || prev == 0 {
		return nil
	}
	v := math.Round(float64(cur-prev)/float64(prev)*1000) / 10
	return &v
}

func prevPtr(prev int64, hasPrev bool) *int64 {
	if !hasPrev {
		return nil
	}
	return &prev
}

// sortGroups orders by the sort mode; ties break on clicks then key so the
// order is deterministic.
func sortGroups(gs []ContentGroup, mode string) {
	delta := func(g ContentGroup) int64 {
		if g.PreviousClicks == nil {
			return g.Clicks
		}
		return g.Clicks - *g.PreviousClicks
	}
	sort.SliceStable(gs, func(i, j int) bool {
		a, b := gs[i], gs[j]
		switch mode {
		case "change":
			if da, db := delta(a), delta(b); da != db {
				return da > db
			}
		case "newest":
			if !a.newest.Equal(b.newest) {
				return a.newest.After(b.newest)
			}
		}
		if a.Clicks != b.Clicks {
			return a.Clicks > b.Clicks
		}
		return a.ContentKey < b.ContentKey
	})
}

func runTopContent(ctx context.Context, req *Request) (Output, error) {
	mode, err := req.EnumParam("sort", "clicks", "clicks", "change", "newest")
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultTopContentLimit)
	set, err := loadLinks(ctx, req, mode == "newest")
	if err != nil {
		return Output{}, err
	}

	// Group in Go: the key needs URL parsing that SQL can't do reliably, and
	// the per-link aggregate it runs over is already small (one row per link).
	byKey := map[string]*ContentGroup{}
	members := map[string][]*linkInfo{}
	var order []string
	// A previous-only link still belongs to a group that has current clicks.
	live := map[string]bool{}
	for _, li := range set.links {
		if li.clicks > 0 {
			live[li.contentKey] = true
		}
	}
	for _, li := range set.links {
		if mode != "newest" && !live[li.contentKey] {
			continue
		}
		members[li.contentKey] = append(members[li.contentKey], li)
		if byKey[li.contentKey] == nil {
			byKey[li.contentKey] = &ContentGroup{ContentKey: li.contentKey, Path: ContentPath(li.dest)}
			order = append(order, li.contentKey)
		}
	}
	groups := make([]ContentGroup, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		ms := members[k]
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].clicks != ms[j].clicks {
				return ms[i].clicks > ms[j].clicks
			}
			return ms[i].key < ms[j].key
		})
		var prev int64
		src := map[string]int64{}
		ti := make([]TitleInput, 0, len(ms))
		g.Links = []GroupLink{}
		for _, li := range ms {
			g.Clicks += li.clicks
			prev += li.prev
			for v, n := range li.bySource {
				src[v] += n
			}
			if li.created.After(g.newest) {
				g.newest = li.created
			}
			ti = append(ti, TitleInput{Title: li.title, UTMSource: li.utmSource})
			g.Links = append(g.Links, GroupLink{Key: li.key, Label: li.label, UTMSource: li.utmSource,
				UTMMedium: li.utmMedium, CampaignSlug: li.campaign, Clicks: li.clicks})
		}
		g.Title = ContentTitle(ti, ms[0].dest)
		g.PreviousClicks, g.ChangePct = prevPtr(prev, set.hasPrev), changePct(g.Clicks, prev, set.hasPrev)
		g.BySource = sourceCounts(src)
		groups = append(groups, *g)
	}
	sortGroups(groups, mode)
	total := len(groups)
	if len(groups) > limit {
		groups = groups[:limit]
	}
	data := TopContentData{Groups: groups, TotalGroups: total, Truncated: set.truncated}
	return Output{Highlights: topContentHighlights(data, req), Data: data}, nil
}

func sourceCounts(m map[string]int64) []SourceCount {
	out := make([]SourceCount, 0, len(m))
	for v, n := range m {
		if n > 0 {
			out = append(out, SourceCount{Value: v, Clicks: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Clicks != out[j].Clicks {
			return out[i].Clicks > out[j].Clicks
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// shareSentence names the biggest real source and its share of total clicks,
// e.g. "mastodon drove 55% of its clicks". It returns "" when no source is set.
func biggestShare(by []SourceCount, total int64) (SourceCount, int64, bool) {
	if total <= 0 {
		return SourceCount{}, 0, false
	}
	for _, s := range by { // by is sorted by clicks desc
		if s.Value != NoneValue {
			return s, pct(s.Clicks, total), true
		}
	}
	return SourceCount{}, 0, false
}

func truncNote(out []string, truncated bool) []string {
	if truncated {
		out = append(out, "Counts may be incomplete: the report hit its row limit and dropped the smallest entries.")
	}
	return out
}

func topContentHighlights(d TopContentData, req *Request) []string {
	cur := req.Windows.Current
	var out []string
	if len(d.Groups) == 0 {
		return truncNote([]string{fmt.Sprintf("No content had clicks between %s and %s.", cur.From, cur.To)}, d.Truncated)
	}
	g := d.Groups[0]
	s := fmt.Sprintf("Top content: %q with %s %s", g.Title, commas(g.Clicks), plural(g.Clicks, "click", "clicks"))
	if g.PreviousClicks != nil {
		s += ", " + delta(g.Clicks, *g.PreviousClicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
	}
	out = append(out, s+".")
	if sc, share, ok := biggestShare(g.BySource, g.Clicks); ok {
		out = append(out, fmt.Sprintf("%s drove the biggest share of its clicks across all of its links: %d%% (%s).", sc.Value, share, commas(sc.Clicks)))
	}
	if len(g.Links) > 1 {
		out = append(out, fmt.Sprintf("It is shared as %d links.", len(g.Links)))
	}
	out = append(out, fmt.Sprintf("%d content %s in total.", d.TotalGroups, plural(int64(d.TotalGroups), "group", "groups")))
	return truncNote(out, d.Truncated)
}

func runTopLinks(ctx context.Context, req *Request) (Output, error) {
	mode, err := req.EnumParam("sort", "clicks", "clicks", "change", "newest")
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultTopLinksLimit)
	set, err := loadLinks(ctx, req, mode == "newest")
	if err != nil {
		return Output{}, err
	}

	// Content titles come from the whole group, so group the members first.
	titles := map[string][]TitleInput{}
	dests := map[string]string{}
	for _, li := range set.links {
		titles[li.contentKey] = append(titles[li.contentKey], TitleInput{Title: li.title, UTMSource: li.utmSource})
		dests[li.contentKey] = li.dest
	}
	cache := map[string]string{}

	rows := make([]LinkRow, 0, len(set.links))
	srcTotals := map[string]int64{}
	var totalClicks int64
	for _, li := range set.links {
		if mode != "newest" && li.clicks == 0 {
			continue
		}
		ct, ok := cache[li.contentKey]
		if !ok {
			ct = ContentTitle(titles[li.contentKey], dests[li.contentKey])
			cache[li.contentKey] = ct
		}
		for v, n := range li.bySource {
			srcTotals[v] += n
		}
		totalClicks += li.clicks
		rows = append(rows, LinkRow{
			Key: li.key, ShortURL: qr.ShortURL(req.BaseURL, li.key), Title: li.title, Label: li.label,
			ContentKey: li.contentKey, ContentTitle: ct, Destination: li.dest, CampaignSlug: li.campaign,
			UTMSource: li.utmSource, UTMMedium: li.utmMedium, UTMCampaign: li.utmCampaign,
			UTMContent: li.utmContent, UTMTerm: li.utmTerm, Placement: li.placement, Active: li.active,
			CreatedAt: li.created, Clicks: li.clicks,
			PreviousClicks: prevPtr(li.prev, set.hasPrev), ChangePct: changePct(li.clicks, li.prev, set.hasPrev),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch mode {
		case "change":
			da, db := a.Clicks, b.Clicks
			if a.PreviousClicks != nil {
				da -= *a.PreviousClicks
			}
			if b.PreviousClicks != nil {
				db -= *b.PreviousClicks
			}
			if da != db {
				return da > db
			}
		case "newest":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
		}
		if a.Clicks != b.Clicks {
			return a.Clicks > b.Clicks
		}
		return a.Key < b.Key
	})
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	data := TopLinksData{Links: rows, TotalLinks: total, Truncated: set.truncated}
	return Output{Highlights: topLinksHighlights(data, sourceCounts(srcTotals), totalClicks, req), Data: data}, nil
}

func topLinksHighlights(d TopLinksData, by []SourceCount, total int64, req *Request) []string {
	cur := req.Windows.Current
	if len(d.Links) == 0 {
		return truncNote([]string{fmt.Sprintf("No links had clicks between %s and %s.", cur.From, cur.To)}, d.Truncated)
	}
	l := d.Links[0]
	name := l.Title
	if name == "" {
		name = l.Key
	}
	s := fmt.Sprintf("Top link: %q (/u/%s) with %s %s", name, l.Key, commas(l.Clicks), plural(l.Clicks, "click", "clicks"))
	if l.PreviousClicks != nil {
		s += ", " + delta(l.Clicks, *l.PreviousClicks) + fmt.Sprintf(" the previous %d days", req.Windows.Previous.Days())
	}
	out := []string{s + "."}
	if sc, share, ok := biggestShare(by, total); ok {
		out = append(out, fmt.Sprintf("%s drove the biggest share of clicks across all matching links, not only the listed rows: %d%% (%s).", sc.Value, share, commas(sc.Clicks)))
	}
	out = append(out, fmt.Sprintf("%d %s in total.", d.TotalLinks, plural(int64(d.TotalLinks), "link", "links")))
	return truncNote(out, d.Truncated)
}
