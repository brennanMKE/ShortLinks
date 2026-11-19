package reports

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must resolve on hosts without a system tzdata
)

// Range values accepted by the range parameter.
const (
	RangeAll  = "all"
	Range7d   = "7d"
	Range30d  = "30d"
	Range90d  = "90d"
	Range365d = "365d"

	DefaultRange = Range30d
)

// Compare values.
const (
	ComparePrevious = "previous"
	CompareNone     = "none"
)

// NoneValue is the filter token (and facet value) for "empty / NULL".
const NoneValue = "~none"

// CampaignNone is the campaign filter value for clicks with no campaign.
const CampaignNone = "none"

// MaxLimit is the upper bound of the limit parameter.
const MaxLimit = 500

// Dimensions lists the filterable UTM-style dimensions in the order they are
// reported. The names are the query-parameter names.
var Dimensions = []string{"source", "medium", "utm_campaign", "content", "term", "placement"}

// rangeDays maps a range value to its length in days.
var rangeDays = map[string]int{Range7d: 7, Range30d: 30, Range90d: 90, Range365d: 365}

// ParamError is a parameter validation failure. Code is the stable machine
// code the API returns in {"error": code}; Field names the offending
// parameter.
type ParamError struct {
	Field   string
	Code    string
	Message string
}

func (e *ParamError) Error() string { return e.Code + ": " + e.Message }

func paramErr(field, code, msg string) *ParamError {
	return &ParamError{Field: field, Code: code, Message: msg}
}

// Params is the parsed, validated common parameter set shared by every report.
type Params struct {
	// Range is one of 7d/30d/90d/365d/all. Ignored when From is set.
	Range string
	// RangeExplicit is true when the caller supplied range, from or to, so a
	// report with its own default window (e.g. a campaign's dates) knows
	// whether to keep it.
	RangeExplicit bool
	// From and To are inclusive YYYY-MM-DD days in TZ. From is empty unless a
	// custom window was requested; To then defaults to today when omitted.
	From, To string
	TZ       *time.Location
	TZName   string
	// Compare is "previous" or "none".
	Compare string
	// Campaign is a campaign slug, CampaignNone, or "" for all.
	Campaign string
	// Filters maps a Dimensions name to its OR-ed values (NoneValue allowed).
	Filters     map[string][]string
	IncludeBots bool
	// Limit is 0 when not supplied; use LimitOr for the report default.
	Limit int
}

// LimitOr returns the requested limit, or def when none was supplied.
func (p Params) LimitOr(def int) int {
	if p.Limit == 0 {
		return def
	}
	return p.Limit
}

// ParseParams validates the common report parameters. defaultTZ is the
// DEFAULT_TIMEZONE setting ("" means UTC). Unknown parameters are ignored so
// reports can read their own extras from the same url.Values.
func ParseParams(v url.Values, defaultTZ string) (Params, error) {
	p := Params{Range: DefaultRange, Compare: ComparePrevious, Filters: map[string][]string{}}

	// tz
	tzName := strings.TrimSpace(v.Get("tz"))
	if tzName == "" {
		tzName = strings.TrimSpace(defaultTZ)
	}
	if tzName == "" {
		tzName = "UTC"
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil || tzName == "Local" {
		return p, paramErr("tz", "invalid_tz", "tz must be an IANA time zone such as America/Los_Angeles")
	}
	p.TZ, p.TZName = loc, tzName

	// range
	if r := strings.TrimSpace(v.Get("range")); r != "" {
		if _, ok := rangeDays[r]; !ok && r != RangeAll {
			return p, paramErr("range", "invalid_range", "range must be one of 7d, 30d, 90d, 365d, all")
		}
		p.Range = r
	}

	p.RangeExplicit = strings.TrimSpace(v.Get("range")) != "" || strings.TrimSpace(v.Get("from")) != "" || strings.TrimSpace(v.Get("to")) != ""

	// from / to
	from, to := strings.TrimSpace(v.Get("from")), strings.TrimSpace(v.Get("to"))
	if from != "" {
		if _, err := parseDay(from); err != nil {
			return p, paramErr("from", "invalid_date", "from must be a YYYY-MM-DD date")
		}
	}
	if to != "" {
		if _, err := parseDay(to); err != nil {
			return p, paramErr("to", "invalid_date", "to must be a YYYY-MM-DD date")
		}
		if from == "" {
			return p, paramErr("to", "invalid_date", "to requires from")
		}
	}
	if from != "" && to != "" && from > to {
		return p, paramErr("from", "invalid_date", "from must not be after to")
	}
	p.From, p.To = from, to

	// compare
	if c := strings.TrimSpace(v.Get("compare")); c != "" {
		if c != ComparePrevious && c != CompareNone {
			return p, paramErr("compare", "invalid_compare", "compare must be previous or none")
		}
		p.Compare = c
	}

	// campaign
	p.Campaign = strings.TrimSpace(v.Get("campaign"))

	// dimension filters
	for _, d := range Dimensions {
		if vals := splitList(v[d]); len(vals) > 0 {
			p.Filters[d] = vals
		}
	}

	// include_bots
	if b := strings.TrimSpace(v.Get("include_bots")); b != "" {
		bv, err := strconv.ParseBool(b)
		if err != nil || (b != "true" && b != "false") {
			return p, paramErr("include_bots", "invalid_include_bots", "include_bots must be true or false")
		}
		p.IncludeBots = bv
	}

	// limit
	if l := strings.TrimSpace(v.Get("limit")); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > MaxLimit {
			return p, paramErr("limit", "invalid_limit", "limit must be an integer from 1 to 500")
		}
		p.Limit = n
	}
	return p, nil
}

// Validate checks the parameters that depend on the current time: a from in
// the future (in p.TZ) is invalid_date. A future to is allowed and clamped to
// today by ResolveWindows.
func (p Params) Validate(now time.Time) error {
	if p.From == "" {
		return nil
	}
	from, _ := parseDay(p.From)
	if from.After(civil(now.In(p.TZ))) {
		return paramErr("from", "invalid_date", "from must not be in the future")
	}
	return nil
}

// parseDay parses a strict YYYY-MM-DD civil date (as UTC midnight).
func parseDay(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

// splitList reads one filter parameter's raw values.
//
// A parameter given more than once (utm_content=a&utm_content=b) is a list of
// whole values: nothing is split, so a value may contain commas. A parameter
// given once is the older comma-separated form (utm_content=a,b), where a
// literal comma is written \, and a literal backslash \\ (see
// EscapeFilterValue). Values are trimmed, empties dropped and duplicates
// removed, keeping first-seen order.
func splitList(raw []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" || seen[part] {
			return
		}
		seen[part] = true
		out = append(out, part)
	}
	if len(raw) == 1 {
		for _, part := range splitEscaped(raw[0]) {
			add(part)
		}
		return out
	}
	for _, r := range raw {
		add(r)
	}
	return out
}

// SplitList is splitList for callers outside the package that read the same
// list-valued query parameters (the campaign QR zip's keys, #0191).
func SplitList(raw []string) []string { return splitList(raw) }

// splitEscaped splits on commas not preceded by a backslash and unescapes \,
// to , and \\ to \. Any other backslash is kept as written.
func splitEscaped(s string) []string {
	var parts []string
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (s[i+1] == ',' || s[i+1] == '\\'):
			b.WriteByte(s[i+1])
			i++
		case c == ',':
			parts = append(parts, b.String())
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	return append(parts, b.String())
}

// EscapeFilterValue escapes a value for the single-parameter comma form.
func EscapeFilterValue(v string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`).Replace(v)
}

// AddFilterValues sets a filter parameter from whole values so that each one
// matches exactly, commas included. Several values go as repeated parameters;
// a single value is escaped, because a lone parameter is still read as the
// comma-separated form.
func AddFilterValues(v url.Values, key string, vals []string) {
	if len(vals) == 1 {
		v.Add(key, EscapeFilterValue(vals[0]))
		return
	}
	for _, x := range vals {
		v.Add(key, x)
	}
}
