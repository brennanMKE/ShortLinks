package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// Prompt names. Each returns one user message telling the model which tools to
// call and what to write; nothing runs until the model follows it.
const (
	PromptWeeklySummary  = "weekly_summary"
	PromptCampaignReport = "campaign_report"
	PromptWhatsWorking   = "whats_working"
)

func promptText(text string) *mcp.GetPromptResult {
	return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}}}
}

// registerPrompts adds the prompts. They only produce text, so a read-only
// token may use them like any other caller.
func registerPrompts(b *Builder) {
	s := b.server
	tzNote := func(tz string) string {
		if tz = strings.TrimSpace(tz); tz != "" {
			return fmt.Sprintf(" Pass tz=%q to every call.", tz)
		}
		return ""
	}

	s.AddPrompt(&mcp.Prompt{
		Name: PromptWeeklySummary, Title: "Weekly summary",
		Description: "A short summary of the last 7 days of link activity compared with the week before.",
		Arguments:   []*mcp.PromptArgument{{Name: "tz", Description: "IANA time zone for the days, such as America/Los_Angeles. Defaults to the instance's zone"}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		tz := req.Params.Arguments["tz"]
		if err := checkTZ(tz); err != nil {
			return nil, err
		}
		return promptText("Write a short weekly summary of my ShortLinks activity." + tzNote(tz) + `

1. Call get_overview with range=7d and compare=previous. Report total clicks against the previous 7 days, with the change.
2. Call get_top_content with range=7d and limit=3. Name the top three pieces of content with their clicks.
3. Call get_movers with range=7d. Say what is rising, what is new, and what has gone quiet.

Keep it to a few short paragraphs or bullets, lead with the headline number, and use only numbers the tools returned. If a call fails, say so instead of guessing.`), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name: PromptCampaignReport, Title: "Campaign report",
		Description: "Summarise how one campaign performed: which content won on which channel, and which links sat idle.",
		Arguments:   []*mcp.PromptArgument{{Name: "slug", Description: "The campaign's slug", Required: true}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		slug := strings.TrimSpace(req.Params.Arguments["slug"])
		if slug == "" {
			return nil, fmt.Errorf("the slug argument is required")
		}
		return promptText(fmt.Sprintf(`Write a report on the ShortLinks campaign with slug %q.

1. Call get_campaign_performance with slug=%q. It defaults to the campaign's own dates; keep that unless I say otherwise.
2. Summarise total clicks and how they compare with the previous period when there is one.
3. Say which content won on which channel, using the matrix of content by source.
4. List the campaign's idle links, which got no clicks, and suggest what to do with them.

If the tool reports campaign_not_found, say the slug was not found and offer to call list_campaigns. Use only numbers the tool returned.`, slug, slug)), nil
	})

	s.AddPrompt(&mcp.Prompt{
		Name: PromptWhatsWorking, Title: "What's working",
		Description: "Find the content and channels getting the most clicks and recommend where to post next.",
		Arguments:   []*mcp.PromptArgument{{Name: "range", Description: "Window ending today: 7d, 30d (default), 90d, 365d or all"}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		rng := strings.TrimSpace(req.Params.Arguments["range"])
		if rng == "" {
			rng = reports.DefaultRange
		}
		if !slices.Contains(enumOf("", "range"), rng) {
			return nil, fmt.Errorf("range must be one of %s", strings.Join(enumOf("", "range"), ", "))
		}
		return promptText(fmt.Sprintf(`Tell me what is working in my ShortLinks and where to post next, over range=%s.

1. Call get_top_content with range=%s to see which content gets the most clicks.
2. Call get_breakdown with dimension=source and range=%s to see which channels bring those clicks.
3. Recommend where to post next: which channels to lean into, which content to repost where it has not been shared, and which channels are not paying off.

Back each recommendation with a number from the tools. Keep it short and actionable.`, rng, rng, rng)), nil
	})
}

func checkTZ(tz string) error {
	tz = strings.TrimSpace(tz)
	if tz == "" {
		return nil
	}
	if _, err := reports.ParseParams(map[string][]string{"tz": {tz}}, ""); err != nil {
		return fmt.Errorf("tz must be an IANA time zone such as America/Los_Angeles")
	}
	return nil
}
