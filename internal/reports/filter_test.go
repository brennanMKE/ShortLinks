package reports

import (
	"reflect"
	"strings"
	"testing"
)

func TestPredicate(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		next     int
		skip     []string
		wantSQL  string
		wantArgs []any
	}{
		{"no filters excludes bots", "", 5, nil, "k.is_bot = FALSE", nil},
		{"include bots and nothing else is TRUE", "include_bots=true", 5, nil, "TRUE", nil},
		{"skip bots", "", 5, []string{SkipBots}, "TRUE", nil},
		{"campaign none", "campaign=none", 5, nil, "k.is_bot = FALSE AND k.campaign_id IS NULL", nil},
		{"campaign slug", "campaign=guides", 5, nil,
			"k.is_bot = FALSE AND k.campaign_id IN (SELECT c.id FROM campaigns c WHERE c.user_id = $1 AND c.slug = $5)", []any{"guides"}},
		{"campaign skipped", "campaign=guides", 5, []string{SkipCampaign}, "k.is_bot = FALSE", nil},
		{"OR within a dimension", "source=a,b", 5, nil,
			"k.is_bot = FALSE AND (NULLIF(btrim(k.utm_source), '') = ANY($5::text[]))", []any{[]string{"a", "b"}}},
		{"none only", "medium=~none", 5, nil,
			"k.is_bot = FALSE AND (NULLIF(btrim(k.utm_medium), '') IS NULL)", nil},
		{"none or value", "medium=~none,email", 5, nil,
			"k.is_bot = FALSE AND (NULLIF(btrim(k.utm_medium), '') IS NULL OR NULLIF(btrim(k.utm_medium), '') = ANY($5::text[]))", []any{[]string{"email"}}},
		{"AND across dimensions with numbering", "campaign=g&source=a&content=x,y", 10, nil,
			"k.is_bot = FALSE AND k.campaign_id IN (SELECT c.id FROM campaigns c WHERE c.user_id = $1 AND c.slug = $10)" +
				" AND (NULLIF(btrim(k.utm_source), '') = ANY($11::text[])) AND (NULLIF(btrim(k.utm_content), '') = ANY($12::text[]))",
			[]any{"g", []string{"a"}, []string{"x", "y"}}},
		{"skip own dimension", "source=a&medium=m", 5, []string{"source"},
			"k.is_bot = FALSE AND (NULLIF(btrim(k.utm_medium), '') = ANY($5::text[]))", []any{[]string{"m"}}},
		{"placement normalised", "placement=Poster%20A,~none", 5, nil,
			"k.is_bot = FALSE AND (NULLIF(lower(btrim(l.placement)), '') IS NULL OR NULLIF(lower(btrim(l.placement)), '') = ANY($5::text[]))", []any{[]string{"poster a"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParams(t, tc.query, "")
			sql, args := p.Predicate(tc.next, tc.skip...)
			if sql != tc.wantSQL {
				t.Errorf("sql =\n %s\nwant\n %s", sql, tc.wantSQL)
			}
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("args = %#v, want %#v", args, tc.wantArgs)
			}
		})
	}
}

// TestPredicate_NoInlinedValues guards against SQL injection: user-supplied
// text must only ever appear as bound arguments.
func TestPredicate_NoInlinedValues(t *testing.T) {
	p := mustParams(t, "source=x'%3BDROP%20TABLE%20clicks%3B--&campaign=a'b", "")
	sql, args := p.Predicate(5)
	if strings.Contains(sql, "DROP") || strings.Contains(sql, "a'b") {
		t.Fatalf("value inlined into SQL: %s", sql)
	}
	if len(args) != 2 {
		t.Errorf("args = %#v", args)
	}
}

func TestDimensionExpr(t *testing.T) {
	for _, d := range Dimensions {
		if e, ok := DimensionExpr(d); !ok || e == "" {
			t.Errorf("no expression for %s", d)
		}
	}
	if _, ok := DimensionExpr("nope"); ok {
		t.Error("unknown dimension accepted")
	}
}
