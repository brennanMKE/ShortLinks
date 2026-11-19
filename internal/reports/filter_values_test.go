package reports

import (
	"net/url"
	"reflect"
	"testing"
)

// #0187: a filter value may contain a comma.
func TestFilterValueSplitting(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"legacy comma list", "content=a,b", []string{"a", "b"}},
		{"repeated is not split", "content=a%2C+b&content=c", []string{"a, b", "c"}},
		{"escaped comma in lone value", `content=spring\,+2026`, []string{"spring, 2026"}},
		{"escaped backslash", `content=a\\,b`, []string{`a\`, "b"}},
		{"other backslash kept", `content=a\b`, []string{`a\b`}},
		{"repeated dedupes and trims", "content=+a+&content=a&content=", []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParams(t, tc.query, "")
			if got := p.Filters["content"]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("content = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAddFilterValues_RoundTrip(t *testing.T) {
	for _, vals := range [][]string{{"a, b"}, {`x\y, z`}, {"a, b", "c"}, {"a", "b"}, {"~none"}} {
		v := url.Values{}
		AddFilterValues(v, "content", vals)
		p, err := ParseParams(v, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Filters["content"]; !reflect.DeepEqual(got, vals) {
			t.Errorf("%q round-tripped to %q (query %s)", vals, got, v.Encode())
		}
	}
}
