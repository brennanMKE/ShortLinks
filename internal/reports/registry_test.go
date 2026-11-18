package reports

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	run := func(context.Context, *Request) (Output, error) { return Output{}, nil }
	r.Register(Report{Name: "b", Description: "B", Run: run})
	r.Register(Report{Name: "a", Description: "A", Params: []ParamSpec{{Name: "x"}}, Run: run})

	if _, ok := r.Get("a"); !ok {
		t.Error("a not found")
	}
	if _, ok := r.Get("zzz"); ok {
		t.Error("unknown report found")
	}
	c := r.Catalogue()
	if len(c.Reports) != 2 || c.Reports[0].Name != "b" || c.Reports[1].Path != "/api/reports/a" {
		t.Errorf("catalogue = %+v", c.Reports)
	}
	if c.Reports[0].Params == nil {
		t.Error("params must be [] not null")
	}
	if len(c.CommonParams) == 0 {
		t.Error("common params missing")
	}

	mustPanic := func(name string, f func()) {
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	mustPanic("duplicate", func() { r.Register(Report{Name: "a", Run: run}) })
	mustPanic("no run", func() { r.Register(Report{Name: "c"}) })
	mustPanic("no name", func() { r.Register(Report{Run: run}) })
}

func TestDefaultRegistryHasOverview(t *testing.T) {
	if _, ok := NewDefaultRegistry().Get("overview"); !ok {
		t.Fatal("overview not registered")
	}
}

func TestCommonParamsCoverParser(t *testing.T) {
	have := map[string]bool{}
	for _, s := range CommonParams {
		have[s.Name] = true
	}
	for _, n := range append([]string{"range", "from", "to", "tz", "compare", "campaign", "include_bots", "limit"}, Dimensions...) {
		if !have[n] {
			t.Errorf("CommonParams missing %s", n)
		}
	}
}

func TestHighlightHelpers(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1284: "1,284", 1234567: "1,234,567"} {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %s", in, got)
		}
	}
	deltas := []struct {
		cur, prev int64
		want      string
	}{{1284, 1088, "up 18% on"}, {90, 100, "down 10% on"}, {5, 5, "flat on"}, {0, 0, "flat on"}, {3, 0, "up from zero in"}}
	for _, d := range deltas {
		if got := delta(d.cur, d.prev); got != d.want {
			t.Errorf("delta(%d,%d) = %q, want %q", d.cur, d.prev, got, d.want)
		}
	}
}

func TestRequestHelpers(t *testing.T) {
	req := func(q string) *Request {
		v, _ := url.ParseQuery(q)
		return &Request{Raw: v}
	}
	code := func(err error) string {
		var pe *ParamError
		if errors.As(err, &pe) {
			return pe.Code
		}
		return ""
	}

	if s, err := req("slug=a").RequiredString("slug"); err != nil || s != "a" {
		t.Errorf("RequiredString present = %q, %v", s, err)
	}
	for _, q := range []string{"", "slug=", "slug=%20"} {
		if _, err := req(q).RequiredString("slug"); code(err) != "missing_slug" {
			t.Errorf("RequiredString(%q) err = %v", q, err)
		}
	}

	enum := []struct {
		q, want, code string
	}{
		{"", "views", ""},
		{"by=clicks", "clicks", ""},
		{"by=%20clicks%20", "clicks", ""},
		{"by=nope", "", "invalid_by"},
		{"by=CLICKS", "", "invalid_by"},
	}
	for _, tc := range enum {
		got, err := req(tc.q).EnumParam("by", "views", "views", "clicks")
		if got != tc.want || code(err) != tc.code {
			t.Errorf("EnumParam(%q) = %q, %v; want %q, %q", tc.q, got, err, tc.want, tc.code)
		}
	}

	if n, err := req("n=5").IntParam("n", 1, 1, 10); n != 5 || err != nil {
		t.Errorf("IntParam = %d, %v", n, err)
	}
	if _, err := req("n=11").IntParam("n", 1, 1, 10); code(err) != "invalid_n" {
		t.Errorf("IntParam out of range err = %v", err)
	}
}

func TestCatalogueShowsRequiredAndDefaultLimit(t *testing.T) {
	r := NewRegistry()
	r.Register(Report{
		Name: "x", Description: "X", DefaultLimit: 25,
		Params: []ParamSpec{{Name: "slug", Type: "string", Required: true}, {Name: "by", Type: "enum", Values: []string{"a"}}},
		Run:    func(context.Context, *Request) (Output, error) { return Output{}, nil },
	})
	b, _ := json.Marshal(r.Catalogue().Reports[0])
	for _, want := range []string{`"required":true`, `"default_limit":25`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("catalogue %s lacks %s", b, want)
		}
	}
	if strings.Count(string(b), `"required"`) != 1 {
		t.Errorf("required should be omitted when false: %s", b)
	}
	ov, _ := NewDefaultRegistry().Get("overview")
	if ov.DefaultLimit != 50 {
		t.Errorf("overview DefaultLimit = %d", ov.DefaultLimit)
	}
}

func TestScopedQuery(t *testing.T) {
	p := mustParams(t, "source=a&campaign=g", "America/Los_Angeles")
	w := ResolveWindows(p, time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC), nil).Current
	sc := p.ScopedQuery(7, w, ScopeOpts{Joins: "LEFT JOIN campaigns c ON c.id = k.campaign_id", Skip: []string{SkipBots, "source"}, Where: "k.is_bot = TRUE"})
	wantSQL := " FROM clicks k JOIN links l ON l.id = k.link_id LEFT JOIN campaigns c ON c.id = k.campaign_id" +
		" WHERE l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3 AND $4::text IS NOT NULL AND (" +
		"k.campaign_id IN (SELECT c.id FROM campaigns c WHERE c.user_id = $1 AND c.slug = $5)) AND (k.is_bot = TRUE)"
	if sc.SQL != wantSQL {
		t.Errorf("SQL =\n %s\nwant\n %s", sc.SQL, wantSQL)
	}
	if len(sc.Args) != 5 || sc.Args[0] != int64(7) || sc.Args[3] != "America/Los_Angeles" || sc.Args[4] != "g" || sc.Next != 6 {
		t.Errorf("args = %#v next = %d", sc.Args, sc.Next)
	}
	plain := mustParams(t, "", "").ScopedQuery(1, w, ScopeOpts{})
	if plain.Next != 5 || len(plain.Args) != 4 || !strings.Contains(plain.SQL, "(k.is_bot = FALSE)") {
		t.Errorf("plain = %+v", plain)
	}
}
