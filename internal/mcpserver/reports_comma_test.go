package mcpserver

import (
	"reflect"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// A filter value with a comma must reach the reports whole (#0187).
func TestReportCommonValues_CommaInFilterValue(t *testing.T) {
	for _, vals := range [][]string{{"spring, 2026"}, {"spring, 2026", "x"}} {
		p, err := reports.ParseParams(reportCommon{Content: vals}.values(), "")
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Filters["content"]; !reflect.DeepEqual(got, vals) {
			t.Errorf("content %q reached the reports as %q", vals, got)
		}
	}
}
