package reports

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func mustParams(t *testing.T, query, defaultTZ string) Params {
	t.Helper()
	v, _ := url.ParseQuery(query)
	p, err := ParseParams(v, defaultTZ)
	if err != nil {
		t.Fatalf("ParseParams(%q): %v", query, err)
	}
	return p
}

func TestResolveWindows(t *testing.T) {
	// 2026-11-18 20:41 UTC is 12:41 PST on the 18th.
	now := time.Date(2026, 11, 18, 20, 41, 0, 0, time.UTC)
	first := time.Date(2026, 9, 3, 5, 0, 0, 0, time.UTC) // Sep 2 22:00 PDT

	tests := []struct {
		name             string
		query            string
		earliest         *time.Time
		from, to         string
		prevFrom, prevTo string
		partial          bool
		wantPrev         bool
		days             int
	}{
		{"default 30d UTC", "", nil, "2026-10-20", "2026-11-18", "2026-09-20", "2026-10-19", true, true, 30},
		{"7d", "range=7d", nil, "2026-11-12", "2026-11-18", "2026-11-05", "2026-11-11", true, true, 7},
		{"365d", "range=365d", nil, "2025-11-19", "2026-11-18", "2024-11-19", "2025-11-18", true, true, 365},
		{"compare none", "compare=none", nil, "2026-10-20", "2026-11-18", "", "", true, false, 30},
		{"custom inclusive", "from=2026-10-01&to=2026-10-10", nil, "2026-10-01", "2026-10-10", "2026-09-21", "2026-09-30", false, true, 10},
		{"custom to clamped to today", "from=2026-11-10&to=2027-01-01", nil, "2026-11-10", "2026-11-18", "2026-11-01", "2026-11-09", true, true, 9},
		{"all with clicks (tz LA)", "range=all&tz=America/Los_Angeles", &first, "2026-09-02", "2026-11-18", "", "", true, false, 78},
		{"all without clicks", "range=all", nil, "2026-11-18", "2026-11-18", "", "", true, false, 1},
		{"tz shifts today", "range=7d&tz=Pacific/Auckland", nil, "2026-11-13", "2026-11-19", "2026-11-06", "2026-11-12", true, true, 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParams(t, tc.query, "")
			rw := ResolveWindows(p, now, tc.earliest)
			c := rw.Current
			if c.From != tc.from || c.To != tc.to || c.PartialLastDay != tc.partial || c.Days() != tc.days {
				t.Errorf("current = %s..%s partial=%v days=%d; want %s..%s partial=%v days=%d",
					c.From, c.To, c.PartialLastDay, c.Days(), tc.from, tc.to, tc.partial, tc.days)
			}
			if (rw.Previous != nil) != tc.wantPrev {
				t.Fatalf("previous present = %v, want %v", rw.Previous != nil, tc.wantPrev)
			}
			if rw.Previous != nil {
				if rw.Previous.From != tc.prevFrom || rw.Previous.To != tc.prevTo || rw.Previous.PartialLastDay {
					t.Errorf("previous = %s..%s partial=%v; want %s..%s", rw.Previous.From, rw.Previous.To, rw.Previous.PartialLastDay, tc.prevFrom, tc.prevTo)
				}
				if !rw.Previous.End.Equal(c.Start) {
					t.Errorf("previous.End %v != current.Start %v: windows must abut", rw.Previous.End, c.Start)
				}
				if rw.Previous.Days() != c.Days() {
					t.Errorf("previous days %d != current days %d", rw.Previous.Days(), c.Days())
				}
			}
		})
	}
}

// TestResolveWindows_DSTBoundaries pins window instants across the 2026
// America/Los_Angeles transitions: spring forward on Mar 8 (23-hour day) and
// fall back on Nov 1 (25-hour day). Day edges must stay at local midnight.
func TestResolveWindows_DSTBoundaries(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	tests := []struct {
		name       string
		now        time.Time
		query      string
		start, end time.Time
		span       time.Duration
		days       int
	}{
		{
			"spring forward day is 23h",
			time.Date(2026, 3, 8, 20, 0, 0, 0, time.UTC),
			"from=2026-03-08&to=2026-03-08&compare=none",
			time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), // 00:00 PST
			time.Date(2026, 3, 9, 7, 0, 0, 0, time.UTC), // 00:00 PDT
			23 * time.Hour, 1,
		},
		{
			"fall back day is 25h",
			time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC),
			"from=2026-11-01&to=2026-11-01&compare=none",
			time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), // 00:00 PDT
			time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC), // 00:00 PST
			25 * time.Hour, 1,
		},
		{
			"7d window spanning spring forward keeps 7 days",
			time.Date(2026, 3, 11, 20, 0, 0, 0, time.UTC),
			"range=7d&compare=none",
			time.Date(2026, 3, 5, 8, 0, 0, 0, time.UTC),  // Mar 5 00:00 PST
			time.Date(2026, 3, 12, 7, 0, 0, 0, time.UTC), // Mar 12 00:00 PDT
			7*24*time.Hour - time.Hour, 7,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParams(t, tc.query+"&tz=America/Los_Angeles", "")
			w := ResolveWindows(p, tc.now, nil).Current
			if !w.Start.Equal(tc.start) || !w.End.Equal(tc.end) {
				t.Errorf("instants = %v..%v, want %v..%v", w.Start.UTC(), w.End.UTC(), tc.start, tc.end)
			}
			if got := w.End.Sub(w.Start); got != tc.span {
				t.Errorf("span = %v, want %v", got, tc.span)
			}
			if w.Days() != tc.days || len(w.DayList()) != tc.days {
				t.Errorf("days = %d / %d, want %d", w.Days(), len(w.DayList()), tc.days)
			}
			if h, m, s := w.Start.In(la).Clock(); h+m+s != 0 {
				t.Errorf("start not local midnight: %v", w.Start.In(la))
			}
		})
	}
}

// TestResolveWindows_PreviousAcrossDST checks the previous window is the same
// number of civil days even when it contains a DST change.
func TestResolveWindows_PreviousAcrossDST(t *testing.T) {
	now := time.Date(2026, 3, 20, 20, 0, 0, 0, time.UTC)
	p := mustParams(t, "range=30d&tz=America/Los_Angeles", "")
	rw := ResolveWindows(p, now, nil)
	if rw.Previous == nil || rw.Previous.Days() != 30 || rw.Previous.To != "2026-02-18" || rw.Previous.From != "2026-01-20" {
		t.Fatalf("previous = %+v", rw.Previous)
	}
	if !rw.Previous.End.Equal(rw.Current.Start) {
		t.Errorf("windows do not abut")
	}
	// The current window crosses Mar 8: 30 civil days but 30*24-1 hours.
	if got := rw.Current.End.Sub(rw.Current.Start); got != 30*24*time.Hour-time.Hour {
		t.Errorf("current span = %v", got)
	}
}

func TestWindow_DayList(t *testing.T) {
	w := Window{From: "2026-02-27", To: "2026-03-02"}
	got := w.DayList()
	want := []string{"2026-02-27", "2026-02-28", "2026-03-01", "2026-03-02"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("day %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestParams_Validate(t *testing.T) {
	now := time.Date(2026, 11, 18, 20, 41, 0, 0, time.UTC) // Nov 18 UTC, Nov 18 12:41 in LA, Nov 19 in Auckland
	tests := []struct {
		query string
		bad   bool
	}{
		{"", false},
		{"from=2026-11-18", false},
		{"from=2026-11-19", true},
		{"from=2026-11-19&tz=Pacific/Auckland", false}, // already the 19th there
		{"from=2026-11-10&to=2027-01-01", false},       // future to is clamped, not rejected
	}
	for _, tc := range tests {
		err := mustParams(t, tc.query, "").Validate(now)
		var pe *ParamError
		if tc.bad {
			if !errors.As(err, &pe) || pe.Code != "invalid_date" || pe.Field != "from" {
				t.Errorf("%q: err = %v, want invalid_date on from", tc.query, err)
			}
		} else if err != nil {
			t.Errorf("%q: unexpected %v", tc.query, err)
		}
	}
}

func TestParseParams_RangeExplicit(t *testing.T) {
	for q, want := range map[string]bool{"": false, "tz=UTC&compare=none": false, "range=30d": true, "from=2026-10-01": true, "from=2026-10-01&to=2026-10-02": true} {
		if got := mustParams(t, q, "").RangeExplicit; got != want {
			t.Errorf("%q: RangeExplicit = %v, want %v", q, got, want)
		}
	}
}
