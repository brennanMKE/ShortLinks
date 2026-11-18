package reports

import (
	"time"
)

const dayLayout = "2006-01-02"

// Window is a resolved reporting window: inclusive civil days From..To in TZ,
// plus the half-open UTC instants [Start, End) that bound the SQL query.
type Window struct {
	From, To string
	TZName   string
	Start    time.Time
	End      time.Time
	// PartialLastDay is true when To is the current day in TZ, i.e. the final
	// bucket is still filling.
	PartialLastDay bool
}

// Days returns the window length in civil days.
func (w Window) Days() int {
	f, _ := parseDay(w.From)
	t, _ := parseDay(w.To)
	return int(t.Sub(f).Hours()/24+0.5) + 1
}

// DayList returns every civil date From..To as YYYY-MM-DD.
func (w Window) DayList() []string {
	f, _ := parseDay(w.From)
	n := w.Days()
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = f.AddDate(0, 0, i).Format(dayLayout)
	}
	return out
}

// ResolvedWindows is the current window and, when comparison applies, the
// same-length window immediately before it.
type ResolvedWindows struct {
	Current  Window
	Previous *Window // nil for compare=none and for range=all
}

// ResolveWindows computes the windows for p at instant now. earliest is the
// instant of the user's first click, used only for range=all (nil when the
// user has no clicks, which yields a single-day window of today).
//
// Days are civil days in p.TZ. Day arithmetic goes through the calendar
// (time.Date), never through 24h durations, so a daylight-saving day of 23 or
// 25 hours still starts and ends at local midnight.
func ResolveWindows(p Params, now time.Time, earliest *time.Time) ResolvedWindows {
	today := civil(now.In(p.TZ))
	var from, to time.Time // civil dates as UTC midnights
	to = today

	switch {
	case p.From != "":
		from, _ = parseDay(p.From)
		if p.To != "" {
			to, _ = parseDay(p.To)
		}
		if to.After(today) {
			to = today
		}
		if from.After(to) {
			from = to
		}
	case p.Range == RangeAll:
		from = today
		if earliest != nil {
			from = civil(earliest.In(p.TZ))
		}
	default:
		days := rangeDays[p.Range]
		if days == 0 {
			days = rangeDays[DefaultRange]
		}
		from = today.AddDate(0, 0, -(days - 1))
	}

	cur := makeWindow(from, to, p.TZ, p.TZName, today)
	rw := ResolvedWindows{Current: cur}
	if p.Compare == ComparePrevious && (p.From != "" || p.Range != RangeAll) {
		n := cur.Days()
		pto := from.AddDate(0, 0, -1)
		pfrom := from.AddDate(0, 0, -n)
		prev := makeWindow(pfrom, pto, p.TZ, p.TZName, today)
		rw.Previous = &prev
	}
	return rw
}

// civil returns t's calendar date in its own location as a UTC-midnight time.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func makeWindow(from, to time.Time, loc *time.Location, tzName string, today time.Time) Window {
	return Window{
		From:           from.Format(dayLayout),
		To:             to.Format(dayLayout),
		TZName:         tzName,
		Start:          time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc),
		End:            time.Date(to.Year(), to.Month(), to.Day()+1, 0, 0, 0, 0, loc),
		PartialLastDay: to.Equal(today),
	}
}
