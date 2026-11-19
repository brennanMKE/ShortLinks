package links

import "testing"

func TestGroupLinks(t *testing.T) {
	mk := func(key, dest, title string, clicks, win int64) Link {
		return Link{Key: key, DestinationURL: dest, Title: title, ClickCount: clicks, ClicksWindow: win}
	}
	rows := []Link{
		mk("a1", "https://Ex.com/g/a?utm_source=m", "Guide A - Mastodon post 1", 1, 1),
		mk("b1", "https://ex.com/g/b", "Guide B", 5, 0),
		mk("a2", "https://ex.com/g/a/", "Guide A - Bluesky post 2", 2, 0),
	}
	gs := GroupLinks(rows, "")
	if len(gs) != 2 || gs[0].ContentKey != "ex.com/g/a" || len(gs[0].Links) != 2 || gs[0].Title != "Guide A" || gs[0].Path != "/g/a" {
		t.Fatalf("groups = %+v", gs)
	}
	if gs[0].Clicks != 3 || gs[0].ClicksWindow != 1 {
		t.Errorf("totals = %d/%d", gs[0].Clicks, gs[0].ClicksWindow)
	}
	if gs := GroupLinks(rows, SortClicks); gs[0].ContentKey != "ex.com/g/b" {
		t.Errorf("sort clicks first = %s", gs[0].ContentKey)
	}
	if gs := GroupLinks(rows, SortClicksWindow); gs[0].ContentKey != "ex.com/g/a" {
		t.Errorf("sort window first = %s", gs[0].ContentKey)
	}
	if gs := GroupLinks(rows, SortTitle); gs[1].Title != "Guide B" {
		t.Errorf("sort title = %+v", gs)
	}
	// An untitled group (empty destination, no title) sorts last under title.
	un := append([]Link{mk("u", "", "", 0, 0)}, rows...)
	if gs := GroupLinks(un, SortTitle); gs[len(gs)-1].ContentKey != "" {
		t.Errorf("untitled not last: %+v", gs)
	}
	if gs := GroupLinks(nil, ""); gs == nil || len(gs) != 0 {
		t.Errorf("empty = %#v", gs)
	}
	if got := Page(GroupLinks(rows, ""), 1, 1); len(got) != 1 || got[0].ContentKey != "ex.com/g/b" {
		t.Errorf("page = %+v", got)
	}
	if got := Page(GroupLinks(rows, ""), 1, 5); got == nil || len(got) != 0 {
		t.Errorf("past end = %#v", got)
	}
}
