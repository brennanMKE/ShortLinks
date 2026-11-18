package reports

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
)

func TestParseParams_Defaults(t *testing.T) {
	p, err := ParseParams(url.Values{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Range != "30d" || p.Compare != "previous" || p.TZName != "UTC" || p.IncludeBots || p.Limit != 0 || p.Campaign != "" || len(p.Filters) != 0 {
		t.Errorf("defaults wrong: %+v", p)
	}
}

func TestParseParams_DefaultTZ(t *testing.T) {
	p, err := ParseParams(url.Values{}, "America/Los_Angeles")
	if err != nil || p.TZName != "America/Los_Angeles" {
		t.Fatalf("p.TZName = %q, err %v", p.TZName, err)
	}
	p, err = ParseParams(url.Values{"tz": {"Europe/Berlin"}}, "America/Los_Angeles")
	if err != nil || p.TZName != "Europe/Berlin" {
		t.Fatalf("explicit tz should win: %q, %v", p.TZName, err)
	}
}

func TestParseParams_Valid(t *testing.T) {
	tests := []struct {
		name  string
		query string
		check func(Params) bool
	}{
		{"range 7d", "range=7d", func(p Params) bool { return p.Range == "7d" }},
		{"range all", "range=all", func(p Params) bool { return p.Range == "all" }},
		{"custom window", "from=2026-10-01&to=2026-10-31", func(p Params) bool { return p.From == "2026-10-01" && p.To == "2026-10-31" }},
		{"from only", "from=2026-10-01", func(p Params) bool { return p.From == "2026-10-01" && p.To == "" }},
		{"from equals to", "from=2026-10-01&to=2026-10-01", func(p Params) bool { return p.To == "2026-10-01" }},
		{"compare none", "compare=none", func(p Params) bool { return p.Compare == "none" }},
		{"campaign slug", "campaign=guides", func(p Params) bool { return p.Campaign == "guides" }},
		{"campaign none", "campaign=none", func(p Params) bool { return p.Campaign == "none" }},
		{"csv values", "source=a,b", func(p Params) bool { return reflect.DeepEqual(p.Filters["source"], []string{"a", "b"}) }},
		{"repeated and padded", "source=a&source=%20b%20,a", func(p Params) bool { return reflect.DeepEqual(p.Filters["source"], []string{"a", "b"}) }},
		{"none token", "medium=~none,social", func(p Params) bool { return reflect.DeepEqual(p.Filters["medium"], []string{"~none", "social"}) }},
		{"empty filter ignored", "term=", func(p Params) bool { _, ok := p.Filters["term"]; return !ok }},
		{"include_bots", "include_bots=true", func(p Params) bool { return p.IncludeBots }},
		{"limit", "limit=500", func(p Params) bool { return p.Limit == 500 && p.LimitOr(10) == 500 }},
		{"limit default", "", func(p Params) bool { return p.LimitOr(10) == 10 }},
		{"unknown ignored", "frobnicate=1", func(p Params) bool { return true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, _ := url.ParseQuery(tc.query)
			p, err := ParseParams(v, "")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.check(p) {
				t.Errorf("check failed: %+v", p)
			}
		})
	}
}

func TestParseParams_Errors(t *testing.T) {
	tests := []struct {
		query, code, field string
	}{
		{"range=14d", "invalid_range", "range"},
		{"range=ALL", "invalid_range", "range"},
		{"tz=Mars/Olympus", "invalid_tz", "tz"},
		{"tz=Local", "invalid_tz", "tz"},
		{"from=2026-13-01", "invalid_date", "from"},
		{"from=10/01/2026", "invalid_date", "from"},
		{"from=2026-10-01&to=nope", "invalid_date", "to"},
		{"to=2026-10-01", "invalid_date", "to"},
		{"from=2026-10-02&to=2026-10-01", "invalid_date", "from"},
		{"compare=next", "invalid_compare", "compare"},
		{"include_bots=yes", "invalid_include_bots", "include_bots"},
		{"include_bots=1", "invalid_include_bots", "include_bots"},
		{"limit=0", "invalid_limit", "limit"},
		{"limit=501", "invalid_limit", "limit"},
		{"limit=abc", "invalid_limit", "limit"},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			v, _ := url.ParseQuery(tc.query)
			_, err := ParseParams(v, "")
			var pe *ParamError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want *ParamError", err)
			}
			if pe.Code != tc.code || pe.Field != tc.field {
				t.Errorf("got %s/%s, want %s/%s", pe.Code, pe.Field, tc.code, tc.field)
			}
		})
	}
}
