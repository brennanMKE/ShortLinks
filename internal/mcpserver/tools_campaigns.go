package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listCampaignsIn struct {
	Sort string `json:"sort,omitempty" jsonschema:"One of activity, created or name"`
	TZ   string `json:"tz,omitempty" jsonschema:"IANA time zone for the 30-day click counts. Defaults to the instance's zone"`
}

type slugIn struct {
	Slug string `json:"slug" jsonschema:"The campaign's slug"`
}

type createCampaignIn struct {
	Name               string `json:"name" jsonschema:"The campaign's name. The slug is generated from it"`
	Description        string `json:"description,omitempty" jsonschema:"What the campaign is for"`
	StartsAt           string `json:"starts_at,omitempty" jsonschema:"When the campaign starts, as an RFC 3339 timestamp"`
	EndsAt             string `json:"ends_at,omitempty" jsonschema:"When the campaign ends, as an RFC 3339 timestamp"`
	DefaultUTMSource   string `json:"default_utm_source,omitempty" jsonschema:"Default utm_source for links created in the campaign"`
	DefaultUTMMedium   string `json:"default_utm_medium,omitempty" jsonschema:"Default utm_medium for links created in the campaign"`
	DefaultUTMCampaign string `json:"default_utm_campaign,omitempty" jsonschema:"Default utm_campaign for links created in the campaign"`
	DefaultUTMTerm     string `json:"default_utm_term,omitempty" jsonschema:"Default utm_term for links created in the campaign"`
	DefaultUTMContent  string `json:"default_utm_content,omitempty" jsonschema:"Default utm_content for links created in the campaign"`
	IdempotencyKey     string `json:"idempotency_key,omitempty" jsonschema:"A unique string for this create. Retrying with the same value returns the first result instead of creating a duplicate"`
}

type updateCampaignIn struct {
	Slug               string  `json:"slug" jsonschema:"The slug of the campaign to change"`
	Name               *string `json:"name,omitempty" jsonschema:"New name"`
	Description        *string `json:"description,omitempty" jsonschema:"New description"`
	StartsAt           *string `json:"starts_at,omitempty" jsonschema:"New start as an RFC 3339 timestamp"`
	EndsAt             *string `json:"ends_at,omitempty" jsonschema:"New end as an RFC 3339 timestamp"`
	Archived           *bool   `json:"archived,omitempty" jsonschema:"True to archive the campaign, false to restore it"`
	DefaultUTMSource   *string `json:"default_utm_source,omitempty" jsonschema:"New default utm_source"`
	DefaultUTMMedium   *string `json:"default_utm_medium,omitempty" jsonschema:"New default utm_medium"`
	DefaultUTMCampaign *string `json:"default_utm_campaign,omitempty" jsonschema:"New default utm_campaign"`
	DefaultUTMTerm     *string `json:"default_utm_term,omitempty" jsonschema:"New default utm_term"`
	DefaultUTMContent  *string `json:"default_utm_content,omitempty" jsonschema:"New default utm_content"`
}

type batchRowIn struct {
	DestinationURL string `json:"destination_url,omitempty" jsonschema:"The URL this row's link redirects to. A row without one is skipped as blank"`
	Title          string `json:"title,omitempty" jsonschema:"Title for this row's link"`
	UTMSource      string `json:"utm_source,omitempty" jsonschema:"utm_source for this row, usually the channel"`
	UTMMedium      string `json:"utm_medium,omitempty" jsonschema:"utm_medium for this row"`
	UTMCampaign    string `json:"utm_campaign,omitempty" jsonschema:"utm_campaign for this row"`
	UTMTerm        string `json:"utm_term,omitempty" jsonschema:"utm_term for this row"`
	UTMContent     string `json:"utm_content,omitempty" jsonschema:"utm_content for this row, identifying the content"`
	Placement      string `json:"placement,omitempty" jsonschema:"Placement label for this row, such as a poster location"`
	ExternalID     string `json:"external_id,omitempty" jsonschema:"Your own id for this row's link, 1 to 128 printable ASCII characters, unique among your links and within the batch"`
}

type batchCreateIn struct {
	Slug           string       `json:"slug" jsonschema:"The campaign to add the new links to"`
	Rows           []batchRowIn `json:"rows" jsonschema:"One entry per link to create, usually one per channel. All are created together or none are"`
	KeyLength      int          `json:"key_length,omitempty" jsonschema:"Length of each generated key, 6 to 32 (default 6). Use 12 or more for links that must not be guessable"`
	IdempotencyKey string       `json:"idempotency_key,omitempty" jsonschema:"A unique string for this batch. Retrying with the same value returns the first result instead of creating the links again"`
}

type assignIn struct {
	Slug string   `json:"slug" jsonschema:"The campaign to add links to"`
	Keys []string `json:"keys" jsonschema:"Keys of existing links to put in the campaign, at most 50"`
}

type unassignIn struct {
	Slug string `json:"slug" jsonschema:"The campaign to remove the link from"`
	Key  string `json:"key" jsonschema:"The key of the link to remove. The link itself is kept"`
}

func campaignLine(m map[string]any) string {
	line := fmt.Sprintf("%s (%s)", str(m, "name"), str(m, "slug"))
	if a, _ := m["archived"].(bool); a {
		line += " archived"
	}
	return line
}

func summarizeCampaign(verb string) func(map[string]any) string {
	return func(m map[string]any) string { return verb + " campaign " + campaignLine(m) }
}

func registerCampaignTools(b *Builder) {
	AddTool(b, Spec{
		Name:        "list_campaigns",
		Title:       "List campaigns",
		Description: "List the account's campaigns with link counts, total clicks, the last 30 days' clicks and a status.",
		Routes:      []string{"GET /api/campaigns"},
	}, func(ctx context.Context, c *Caller, in listCampaignsIn) (*mcp.CallToolResult, error) {
		q := url.Values{}
		if in.Sort != "" {
			q.Set("sort", in.Sort)
		}
		if in.TZ != "" {
			q.Set("tz", in.TZ)
		}
		return proxy(ctx, c, http.MethodGet, "/api/campaigns", q, nil, nil, func(m map[string]any) string {
			items, _ := m["campaigns"].([]any)
			s := fmt.Sprintf("%d campaigns", len(items))
			for _, it := range items {
				if im, ok := it.(map[string]any); ok {
					s += "\n" + campaignLine(im)
				}
			}
			return s
		})
	})

	AddTool(b, Spec{
		Name:        "get_campaign",
		Title:       "Get a campaign",
		Description: "Get one campaign with its settings, assigned links and click statistics for its default window.",
		Routes:      []string{"GET /api/campaigns/{slug}"},
	}, func(ctx context.Context, c *Caller, in slugIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodGet, "/api/campaigns/"+pathSeg(in.Slug), nil, nil, nil, func(m map[string]any) string {
			links, _ := m["links"].([]any)
			return fmt.Sprintf("Campaign %s: %d links", campaignLine(m), len(links))
		})
	})

	AddTool(b, Spec{
		Name:        "list_campaign_links",
		Title:       "List a campaign's links",
		Description: "List the links assigned to a campaign.",
		Routes:      []string{"GET /api/campaigns/{slug}/links"},
	}, func(ctx context.Context, c *Caller, in slugIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodGet, "/api/campaigns/"+pathSeg(in.Slug)+"/links", nil, nil, nil, linksSummary("Campaign "+in.Slug))
	})

	AddTool(b, Spec{
		Name:        "create_campaign",
		Title:       "Create a campaign",
		Description: "Create a campaign to group links, with optional dates and default UTM labels. Pass idempotency_key so a retry cannot create a duplicate.",
		Write:       true,
		Routes:      []string{"POST /api/campaigns"},
	}, func(ctx context.Context, c *Caller, in createCampaignIn) (*mcp.CallToolResult, error) {
		if r := required("name", in.Name); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPost, "/api/campaigns", nil, bodyOf(in, "idempotency_key"), idemHeader(in.IdempotencyKey), summarizeCampaign("Created"))
	})

	AddTool(b, Spec{
		Name:        "update_campaign",
		Title:       "Update a campaign",
		Description: "Change a campaign's name, description, dates, default UTM labels or archived state. Only the fields you pass change.",
		Write:       true, Idempotent: true,
		Routes: []string{"PATCH /api/campaigns/{slug}"},
	}, func(ctx context.Context, c *Caller, in updateCampaignIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPatch, "/api/campaigns/"+pathSeg(in.Slug), nil, bodyOf(in, "slug"), nil, summarizeCampaign("Updated"))
	})

	AddTool(b, Spec{
		Name:        "delete_campaign",
		Title:       "Delete a campaign",
		Description: "Permanently delete a campaign. Its links are kept but no longer belong to it. To hide a campaign without deleting it, archive it with update_campaign.",
		Write:       true, Destructive: true,
		Routes: []string{"DELETE /api/campaigns/{slug}"},
	}, func(ctx context.Context, c *Caller, in slugIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodDelete, "/api/campaigns/"+pathSeg(in.Slug), nil, nil, nil, func(map[string]any) string {
			return "Deleted campaign " + in.Slug
		})
	})

	AddTool(b, Spec{
		Name:  "batch_create_campaign_links",
		Title: "Create many links in a campaign",
		Description: "Create a short link per row and put them all in a campaign in one atomic request, for example one link per channel for the same destination. " +
			"Pass idempotency_key so a retry cannot create the links twice.",
		Write:  true,
		Routes: []string{"POST /api/campaigns/{slug}/links/batch"},
	}, func(ctx context.Context, c *Caller, in batchCreateIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		body := map[string]any{"rows": in.Rows}
		if in.KeyLength != 0 {
			body["key_length"] = in.KeyLength
		}
		return proxy(ctx, c, http.MethodPost, "/api/campaigns/"+pathSeg(in.Slug)+"/links/batch", nil,
			body, idemHeader(in.IdempotencyKey), func(m map[string]any) string {
				items, _ := m["links"].([]any)
				s := fmt.Sprintf("Created %d links in campaign %s", len(items), in.Slug)
				for _, it := range items {
					if im, ok := it.(map[string]any); ok {
						s += "\n" + linkLine(im)
					}
				}
				return s
			})
	})

	AddTool(b, Spec{
		Name:        "assign_links_to_campaign",
		Title:       "Add links to a campaign",
		Description: "Put existing links into a campaign, a link at most one campaign at a time.",
		Write:       true, Idempotent: true,
		Routes: []string{"POST /api/campaigns/{slug}/links"},
	}, func(ctx context.Context, c *Caller, in assignIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPost, "/api/campaigns/"+pathSeg(in.Slug)+"/links", nil,
			map[string]any{"keys": in.Keys}, nil, linksSummary("Campaign "+in.Slug))
	})

	AddTool(b, Spec{
		Name:        "remove_link_from_campaign",
		Title:       "Remove a link from a campaign",
		Description: "Take a link out of a campaign. The link and its clicks are kept.",
		Write:       true, Idempotent: true,
		Routes: []string{"DELETE /api/campaigns/{slug}/links/{key}"},
	}, func(ctx context.Context, c *Caller, in unassignIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("slug", in.Slug); r != nil {
			return r, nil
		}
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodDelete, "/api/campaigns/"+pathSeg(in.Slug)+"/links/"+pathSeg(in.Key), nil, nil, nil, func(map[string]any) string {
			return fmt.Sprintf("Removed link %s from campaign %s", in.Key, in.Slug)
		})
	})
}
