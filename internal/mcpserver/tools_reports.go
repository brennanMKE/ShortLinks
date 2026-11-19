package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// ReportRunner is the slice of *reports.Service the report tools use. The
// tools call it directly: reports are read-only, so no HTTP dispatch is needed.
type ReportRunner interface {
	Run(ctx context.Context, userID int64, name string, raw url.Values) (*reports.Envelope, error)
	Catalogue() reports.Catalogue
}

// reportCommon is every report's shared parameters (#0139).
type reportCommon struct {
	Range       string   `json:"range,omitempty" jsonschema:"Window ending today (today is a partial day). Default 30d"`
	From        string   `json:"from,omitempty" jsonschema:"First day of a custom window, YYYY-MM-DD in tz, inclusive. Overrides range"`
	To          string   `json:"to,omitempty" jsonschema:"Last day of a custom window, YYYY-MM-DD in tz, inclusive. Defaults to today; requires from"`
	TZ          string   `json:"tz,omitempty" jsonschema:"IANA time zone used to bucket days, for example America/Los_Angeles. Defaults to the instance's DEFAULT_TIMEZONE"`
	Compare     string   `json:"compare,omitempty" jsonschema:"previous adds the same-length window immediately before for comparison (the default); none turns it off"`
	Campaign    string   `json:"campaign,omitempty" jsonschema:"Only clicks on links in this campaign slug, or none for clicks with no campaign. Default all"`
	Source      []string `json:"source,omitempty" jsonschema:"Only clicks with these utm_source values; ~none matches empty"`
	Medium      []string `json:"medium,omitempty" jsonschema:"Only clicks with these utm_medium values; ~none matches empty"`
	UTMCampaign []string `json:"utm_campaign,omitempty" jsonschema:"Only clicks with these utm_campaign values; ~none matches empty"`
	Content     []string `json:"content,omitempty" jsonschema:"Only clicks with these utm_content values; ~none matches empty"`
	Term        []string `json:"term,omitempty" jsonschema:"Only clicks with these utm_term values; ~none matches empty"`
	Placement   []string `json:"placement,omitempty" jsonschema:"Only links with these placement values (case-insensitive); ~none matches empty"`
	IncludeBots bool     `json:"include_bots,omitempty" jsonschema:"Count clicks classified as bots. Default false"`
}

// reportRanked adds limit, for the reports that return ranked lists.
type reportRanked struct {
	reportCommon
	Limit int `json:"limit,omitempty" jsonschema:"Maximum rows, 1 to 500. Each report has its own default"`
}

type overviewIn struct {
	reportRanked
}

type topIn struct {
	reportRanked
	Sort string `json:"sort,omitempty" jsonschema:"clicks ranks by clicks (default); change by growth over the previous window; newest by creation date, including items with no clicks"`
}

type breakdownIn struct {
	reportRanked
	Dimension string `json:"dimension" jsonschema:"What to group clicks by. referer is the referrer hostname; campaign values are slugs; link values are link keys; content_group groups links to the same destination"`
}

type timeseriesIn struct {
	reportRanked
	Granularity string `json:"granularity,omitempty" jsonschema:"Bucket size: day (default) or week (weeks start on Monday in tz)"`
	SplitBy     string `json:"split_by,omitempty" jsonschema:"Split into one series per value of this dimension. Default: one series named all"`
	SeriesLimit int    `json:"series_limit,omitempty" jsonschema:"With split_by, the most series to return, 1 to 50; the rest are summed into an other series. Default 5"`
}

type moversIn struct {
	reportRanked
	Unit      string `json:"unit,omitempty" jsonschema:"content groups links to the same destination (default); link compares individual links"`
	MinClicks *int   `json:"min_clicks,omitempty" jsonschema:"Ignore items whose larger period has fewer clicks than this. Default 5"`
}

type idleIn struct {
	reportRanked
	MinAgeDays *int `json:"min_age_days,omitempty" jsonschema:"Only links created at least this many days ago, so brand-new links are not flagged, 0 to 3650. Default 7"`
}

type campaignComparisonIn struct {
	reportRanked
}

type campaignPerformanceIn struct {
	reportRanked
	Slug    string `json:"slug" jsonschema:"The campaign's slug"`
	Columns string `json:"columns,omitempty" jsonschema:"What the content-by-channel matrix columns are: source (default), medium or placement"`
}

type linkPerformanceIn struct {
	reportRanked
	Key string `json:"key" jsonschema:"The link's key"`
}

type listReportsIn struct{}

// values turns the shared fields into the query the reports parse.
func (c reportCommon) values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s = strings.TrimSpace(s); s != "" {
			v.Set(k, s)
		}
	}
	set("range", c.Range)
	set("from", c.From)
	set("to", c.To)
	set("tz", c.TZ)
	set("compare", c.Compare)
	set("campaign", c.Campaign)
	for k, list := range map[string][]string{
		"source": c.Source, "medium": c.Medium, "utm_campaign": c.UTMCampaign,
		"content": c.Content, "term": c.Term, "placement": c.Placement,
	} {
		reports.AddFilterValues(v, k, list)
	}
	if c.IncludeBots {
		v.Set("include_bots", "true")
	}
	return v
}

func (r reportRanked) values() url.Values {
	v := r.reportCommon.values()
	if r.Limit != 0 {
		v.Set("limit", strconv.Itoa(r.Limit))
	}
	return v
}

// catalogue is the built-in report catalogue, read once to keep every enum in
// a tool's schema in step with the report that defines it.
var catalogue = reports.NewDefaultRegistry().Catalogue()

// enumOf returns the allowed values of a report's extra parameter, or of a
// shared one when report is "".
func enumOf(report, param string) []string {
	specs := reports.CommonParams
	if report != "" {
		specs = nil
		for _, r := range catalogue.Reports {
			if r.Name == report {
				specs = r.Params
			}
		}
	}
	for _, p := range specs {
		if p.Name == param {
			return p.Values
		}
	}
	panic("mcpserver: no enum " + param + " on report " + report)
}

func enumSchema(props map[string]*jsonschema.Schema, name string, values []string) {
	p := props[name]
	if p == nil {
		panic("mcpserver: schema has no property " + name)
	}
	p.Enum = nil
	for _, v := range values {
		p.Enum = append(p.Enum, v)
	}
}

// refineReport returns the Refine hook for a report tool: the shared enums,
// the date formats, the tz default and the report's own enums.
func (h *Handler) refineReport(report string, extra map[string]string) func(map[string]*jsonschema.Schema) {
	tz := h.opts.DefaultTimezone
	return func(props map[string]*jsonschema.Schema) {
		enumSchema(props, "range", enumOf("", "range"))
		enumSchema(props, "compare", enumOf("", "compare"))
		props["from"].Format = "date"
		props["to"].Format = "date"
		if p := props["limit"]; p != nil {
			one, max := 1.0, float64(reports.MaxLimit)
			p.Minimum, p.Maximum = &one, &max
		}
		if tz != "" {
			if b, err := json.Marshal(tz); err == nil {
				props["tz"].Default = b
			}
		}
		for param, own := range extra {
			if own == "" {
				own = report
			}
			enumSchema(props, param, enumOf(own, param))
		}
	}
}

// reportTool describes one report tool.
type reportTool struct {
	tool, report, title, description string
	enums                            map[string]string // param -> report whose enum applies ("" = this one)
}

func registerReportTools(b *Builder) {
	listSpec(b)

	reportToolOf(b, reportTool{"get_overview", "overview", "Overview",
		"Use for 'how are my links doing': total clicks, the daily trend, the previous period, campaigns and the busiest UTM values. Start here when the question is broad.", nil},
		func(in overviewIn) (url.Values, *mcp.CallToolResult) { return in.values(), nil })
	reportToolOf(b, reportTool{"get_top_content", "top-content", "Top content",
		"Use for 'what content is getting the most clicks'. Groups links that point to the same destination across platforms and campaigns, so one article posted three places counts once.",
		map[string]string{"sort": ""}},
		func(in topIn) (url.Values, *mcp.CallToolResult) { return withSort(in), nil })
	reportToolOf(b, reportTool{"get_top_links", "top-links", "Top links",
		"Use for 'which individual links are doing best'. Ranks single links, with their campaign and UTM fields, rather than grouping by destination.",
		map[string]string{"sort": "top-content"}},
		func(in topIn) (url.Values, *mcp.CallToolResult) { return withSort(in), nil })
	reportToolOf(b, reportTool{"get_breakdown", "breakdown", "Breakdown",
		"Use for 'where are clicks coming from' or any split by source, medium, referrer, campaign, link or content group, with share of total and the previous period.",
		map[string]string{"dimension": ""}},
		func(in breakdownIn) (url.Values, *mcp.CallToolResult) {
			if r := required("dimension", in.Dimension); r != nil {
				return nil, r
			}
			v := in.values()
			v.Set("dimension", in.Dimension)
			return v, nil
		})
	reportToolOf(b, reportTool{"get_timeseries", "timeseries", "Timeseries",
		"Use for 'how did clicks change over time' by day or week, optionally one series per source, campaign or other dimension.",
		map[string]string{"granularity": "", "split_by": ""}},
		func(in timeseriesIn) (url.Values, *mcp.CallToolResult) {
			v := in.values()
			setIf(v, "granularity", in.Granularity)
			setIf(v, "split_by", in.SplitBy)
			if in.SeriesLimit != 0 {
				v.Set("series_limit", strconv.Itoa(in.SeriesLimit))
			}
			return v, nil
		})
	reportToolOf(b, reportTool{"get_movers", "movers", "Movers",
		"Use for 'what's rising, what's falling, what went quiet'. Compares this window with the previous one and lists items that grew, shrank, are new or stopped getting clicks.",
		map[string]string{"unit": ""}},
		func(in moversIn) (url.Values, *mcp.CallToolResult) {
			v := in.values()
			setIf(v, "unit", in.Unit)
			if in.MinClicks != nil {
				v.Set("min_clicks", strconv.Itoa(*in.MinClicks))
			}
			return v, nil
		})
	reportToolOf(b, reportTool{"get_idle_links", "idle-links", "Idle links",
		"Use for 'which links get no clicks'. Lists active links with no clicks in the window, ignoring brand-new ones.", nil},
		func(in idleIn) (url.Values, *mcp.CallToolResult) {
			v := in.values()
			if in.MinAgeDays != nil {
				v.Set("min_age_days", strconv.Itoa(*in.MinAgeDays))
			}
			return v, nil
		})
	reportToolOf(b, reportTool{"compare_campaigns", "campaign-comparison", "Compare campaigns",
		"Use for 'which campaign did best' or to line campaigns up side by side on clicks and change from the previous period.", nil},
		func(in campaignComparisonIn) (url.Values, *mcp.CallToolResult) { return in.values(), nil })
	reportToolOf(b, reportTool{"get_campaign_performance", "campaign-performance", "Campaign performance",
		"Use for 'how did this campaign do': a content-by-channel matrix, the winners and the campaign's idle links. Defaults to the campaign's own dates.",
		map[string]string{"columns": ""}},
		func(in campaignPerformanceIn) (url.Values, *mcp.CallToolResult) {
			if r := requiredSeg("slug", in.Slug); r != nil {
				return nil, r
			}
			v := in.values()
			v.Set("slug", in.Slug)
			setIf(v, "columns", in.Columns)
			return v, nil
		})
	reportToolOf(b, reportTool{"get_link_performance", "link-performance", "Link performance",
		"Use for 'how is this one link doing': its clicks over time, sources, referrers and the previous period.", nil},
		func(in linkPerformanceIn) (url.Values, *mcp.CallToolResult) {
			if r := requiredSeg("key", in.Key); r != nil {
				return nil, r
			}
			v := in.values()
			v.Set("key", in.Key)
			return v, nil
		})
}

func setIf(v url.Values, k, s string) {
	if s = strings.TrimSpace(s); s != "" {
		v.Set(k, s)
	}
}

func withSort(in topIn) url.Values {
	v := in.values()
	setIf(v, "sort", in.Sort)
	return v
}

// reportToolOf registers one report tool: build turns the typed input into the
// report's query (or an early tool error), and the result carries the
// highlights as text, the JSON mirror, and the whole envelope as structured
// content.
func reportToolOf[In any](b *Builder, rt reportTool, build func(In) (url.Values, *mcp.CallToolResult)) {
	// The report's registry name is what Service.Run takes; the tool name is
	// the agent-facing one.
	AddTool(b, Spec{
		Name: rt.tool, Title: rt.title, Description: rt.description,
		Routes: []string{"GET /api/reports/{name}"},
		Refine: b.h.refineReport(rt.report, rt.enums),
	}, func(ctx context.Context, c *Caller, in In) (*mcp.CallToolResult, error) {
		run := b.h.opts.Reports
		if run == nil {
			return failure(0, "reports are not available on this server"), nil
		}
		v, early := build(in)
		if early != nil {
			return early, nil
		}
		env, err := run.Run(ctx, c.UserID, rt.report, v)
		if err != nil {
			return reportFailure(rt.tool, err), nil
		}
		return reportResult(env), nil
	})
}

func listSpec(b *Builder) {
	AddTool(b, Spec{
		Name: "list_reports", Title: "List reports",
		Description: "Use to see which reports exist, what each one answers and which parameters it takes. Every report is also its own get_* tool.",
		Routes:      []string{"GET /api/reports"},
	}, func(ctx context.Context, c *Caller, in listReportsIn) (*mcp.CallToolResult, error) {
		run := b.h.opts.Reports
		if run == nil {
			return failure(0, "reports are not available on this server"), nil
		}
		cat := run.Catalogue()
		var sb strings.Builder
		for _, r := range cat.Reports {
			fmt.Fprintf(&sb, "%s (%s): %s\n", r.Name, toolFor(r.Name), r.Description)
		}
		body, _ := json.Marshal(cat)
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(sb.String())}, &mcp.TextContent{Text: string(body)}},
			StructuredContent: json.RawMessage(body),
		}, nil
	})
}

// reportTools maps each report to its tool. The drift test compares it with
// reports.NewDefaultRegistry().
var reportTools = map[string]string{
	"overview": "get_overview", "top-content": "get_top_content", "top-links": "get_top_links",
	"breakdown": "get_breakdown", "timeseries": "get_timeseries", "movers": "get_movers",
	"idle-links": "get_idle_links", "campaign-comparison": "compare_campaigns",
	"campaign-performance": "get_campaign_performance", "link-performance": "get_link_performance",
}

func toolFor(report string) string { return reportTools[report] }

// reportResult is the text (the highlights, then the JSON), and the envelope
// as structured content.
func reportResult(env *reports.Envelope) *mcp.CallToolResult {
	body, err := json.Marshal(env)
	if err != nil {
		return failure(0, "internal_error: could not encode the report")
	}
	text := strings.Join(env.Highlights, "\n")
	if text == "" {
		text = "No highlights for " + env.Report + " in " + env.Window.From + " to " + env.Window.To + "."
	}
	return &mcp.CallToolResult{
		// The JSON appears twice on purpose, as in success(): most clients
		// show the model only `content`.
		Content:           []mcp.Content{&mcp.TextContent{Text: text}, &mcp.TextContent{Text: string(body)}},
		StructuredContent: json.RawMessage(body),
	}
}

// reportFailure maps a report error to a tool error carrying the code (and
// the field, for a bad parameter) so the agent can correct the call.
func reportFailure(tool string, err error) *mcp.CallToolResult {
	var pe *reports.ParamError
	var nf *reports.NotFoundError
	switch {
	case errors.Is(err, reports.ErrReportNotFound):
		return errResult("report_not_found", "", "no such report", 404)
	case errors.As(err, &nf):
		return errResult(nf.Code, "", nf.Message, 404)
	case errors.As(err, &pe):
		return errResult(pe.Code, pe.Field, pe.Message, 400)
	}
	slog.Error("mcp: report failed", "tool", tool, "err", err)
	return failure(500, "internal_error: the report failed unexpectedly")
}

func errResult(code, field, msg string, status int) *mcp.CallToolResult {
	text := code
	st := map[string]any{"status": status, "error": code, "message": msg}
	if field != "" {
		text += " (field " + field + ")"
		st["field"] = field
	}
	text += ": " + msg
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: st}
}
