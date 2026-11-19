package links

import (
	"sort"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// Group is one content group (#0189): every matching link that shares a
// reports.ContentKey, with the group's totals. Links keep the order they were
// listed in, so the list's sort decides the order inside a group.
type Group struct {
	ContentKey string
	Title      string // reports.ContentTitle of the members
	Path       string // reports.ContentPath of the first member's destination
	Links      []Link
	Clicks     int64 // all-time non-bot clicks over the members
	// ClicksWindow is the members' window clicks (0 without a window).
	ClicksWindow int64
}

// GroupLinks buckets rows (already filtered and ordered by the list sort) into
// content groups using the same grouping as the reports (reports.ContentKey,
// reports.ContentTitle), then orders the groups by sort. It never splits a
// content item: every row of a key lands in the one group.
//
//	created, "" : the group holding the newest link first
//	-created    : the group holding the oldest link first
//	clicks      : group all-time clicks, most first
//	clicks_window: group window clicks, most first (then all-time)
//	title       : group title A to Z, untitled last
//
// Ties keep the order in which the groups first appeared in rows.
func GroupLinks(rows []Link, sortBy string) []Group {
	idx := map[string]int{}
	var groups []Group
	for _, l := range rows {
		k := reports.ContentKey(l.DestinationURL)
		i, ok := idx[k]
		if !ok {
			i = len(groups)
			idx[k] = i
			groups = append(groups, Group{ContentKey: k, Path: reports.ContentPath(l.DestinationURL)})
		}
		g := &groups[i]
		g.Links = append(g.Links, l)
		g.Clicks += l.ClickCount
		g.ClicksWindow += l.ClicksWindow
	}
	for i := range groups {
		in := make([]reports.TitleInput, 0, len(groups[i].Links))
		for _, l := range groups[i].Links {
			in = append(in, reports.TitleInput{Title: l.Title, UTMSource: l.UTMSource})
		}
		groups[i].Title = reports.ContentTitle(in, groups[i].Links[0].DestinationURL)
	}
	switch sortBy {
	case SortClicks:
		sort.SliceStable(groups, func(a, b int) bool { return groups[a].Clicks > groups[b].Clicks })
	case SortClicksWindow:
		sort.SliceStable(groups, func(a, b int) bool {
			if groups[a].ClicksWindow != groups[b].ClicksWindow {
				return groups[a].ClicksWindow > groups[b].ClicksWindow
			}
			return groups[a].Clicks > groups[b].Clicks
		})
	case SortTitle:
		sort.SliceStable(groups, func(a, b int) bool {
			ta, tb := strings.TrimSpace(groups[a].Title), strings.TrimSpace(groups[b].Title)
			if (ta == "") != (tb == "") {
				return tb == "" // untitled groups last
			}
			return strings.ToLower(ta) < strings.ToLower(tb)
		})
	}
	if groups == nil {
		groups = []Group{}
	}
	return groups
}
