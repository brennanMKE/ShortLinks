package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

type noInput struct{}

func registerIdentityTools(b *Builder) {
	AddTool(b, Spec{
		Name:  "whoami",
		Title: "Who am I",
		Description: "Return the account this token belongs to, the instance's base URL and whether the token is read-only. " +
			"Use it to confirm the connection before other calls.",
		Routes: []string{"GET /api/me"},
	}, func(ctx context.Context, c *Caller, _ noInput) (*mcp.CallToolResult, error) {
		return proxy(ctx, c, http.MethodGet, "/api/me", nil, nil, nil, func(m map[string]any) string {
			s := "Signed in as " + str(m, "email")
			if t, ok := m["api_token"].(map[string]any); ok {
				s += fmt.Sprintf(" with token %q", str(t, "name"))
				if ro, _ := t["read_only"].(bool); ro {
					s += " (read-only)"
				}
			}
			return s
		})
	})
}

type listLinksIn struct {
	Q          string `json:"q,omitempty" jsonschema:"Search text matched against title, key and destination URL"`
	Campaign   string `json:"campaign,omitempty" jsonschema:"Only links in these campaign slugs (comma-separated), or ~none for links in no campaign"`
	Source     string `json:"source,omitempty" jsonschema:"Only links with this utm_source (comma-separated for several, ~none for none)"`
	Medium     string `json:"medium,omitempty" jsonschema:"Only links with this utm_medium (comma-separated for several, ~none for none)"`
	ContentKey string `json:"content_key,omitempty" jsonschema:"Only links that share this content key, which groups the per-channel links of one piece of content"`
	Status     string `json:"status,omitempty" jsonschema:"One of active, inactive, expired or all. Omit it to include every status"`
	Sort       string `json:"sort,omitempty" jsonschema:"One of created (newest first, the default), -created, clicks, clicks_window or title. clicks_window needs window"`
	Window     string `json:"window,omitempty" jsonschema:"7d, 30d or 90d. Adds a clicks_window count to each link over that window"`
	TZ         string `json:"tz,omitempty" jsonschema:"IANA time zone for the window, such as America/Los_Angeles. Defaults to the instance's zone"`
	Page       int    `json:"page,omitempty" jsonschema:"Page number, starting at 1"`
	PerPage    int    `json:"per_page,omitempty" jsonschema:"Links per page, up to 100"`
}

type keyIn struct {
	Key string `json:"key" jsonschema:"The link's short key, the last part of its short URL"`
}

type createLinkIn struct {
	DestinationURL string `json:"destination_url" jsonschema:"The full http or https URL the short link redirects to"`
	Title          string `json:"title,omitempty" jsonschema:"A human-readable title for the link"`
	Key            string `json:"key,omitempty" jsonschema:"A custom short key. Omit it to get a generated one"`
	ExpiresAt      string `json:"expires_at,omitempty" jsonschema:"When the link stops redirecting, as an RFC 3339 timestamp such as 2026-12-31T23:59:59Z"`
	CampaignSlug   string `json:"campaign_slug,omitempty" jsonschema:"Slug of one of your campaigns to put the link in"`
	UTMSource      string `json:"utm_source,omitempty" jsonschema:"utm_source label recorded with the link, such as newsletter"`
	UTMMedium      string `json:"utm_medium,omitempty" jsonschema:"utm_medium label recorded with the link, such as email"`
	UTMCampaign    string `json:"utm_campaign,omitempty" jsonschema:"utm_campaign label recorded with the link"`
	UTMTerm        string `json:"utm_term,omitempty" jsonschema:"utm_term label recorded with the link"`
	UTMContent     string `json:"utm_content,omitempty" jsonschema:"utm_content label that identifies the piece of content"`
	Placement      string `json:"placement,omitempty" jsonschema:"Where the link will be placed, such as a poster location or channel"`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"A unique string for this create. Retrying with the same value returns the first result instead of creating a duplicate"`
}

type updateLinkIn struct {
	Key            string  `json:"key" jsonschema:"The key of the link to change"`
	Title          *string `json:"title,omitempty" jsonschema:"New title"`
	DestinationURL *string `json:"destination_url,omitempty" jsonschema:"New destination URL, http or https"`
	ExpiresAt      *string `json:"expires_at,omitempty" jsonschema:"New expiry as an RFC 3339 timestamp"`
	UTMSource      *string `json:"utm_source,omitempty" jsonschema:"New utm_source label"`
	UTMMedium      *string `json:"utm_medium,omitempty" jsonschema:"New utm_medium label"`
	UTMCampaign    *string `json:"utm_campaign,omitempty" jsonschema:"New utm_campaign label"`
	UTMTerm        *string `json:"utm_term,omitempty" jsonschema:"New utm_term label"`
	UTMContent     *string `json:"utm_content,omitempty" jsonschema:"New utm_content label"`
	Placement      *string `json:"placement,omitempty" jsonschema:"New placement label"`
}

func registerLinkTools(b *Builder) {
	AddTool(b, Spec{
		Name:  "list_links",
		Title: "List links",
		Description: "List or search the account's short links, newest first by default, with optional filters, sorting and a click window. " +
			"Use it to find a link by text, campaign, source or status.",
		Routes: []string{"GET /api/links"},
	}, func(ctx context.Context, c *Caller, in listLinksIn) (*mcp.CallToolResult, error) {
		q := url.Values{}
		for k, v := range map[string]string{"q": in.Q, "campaign": in.Campaign, "source": in.Source, "medium": in.Medium,
			"content_key": in.ContentKey, "status": in.Status, "sort": in.Sort, "window": in.Window, "tz": in.TZ} {
			if v != "" {
				q.Set(k, v)
			}
		}
		if in.Page > 0 {
			q.Set("page", strconv.Itoa(in.Page))
		}
		if in.PerPage > 0 {
			q.Set("per_page", strconv.Itoa(in.PerPage))
		}
		return proxy(ctx, c, http.MethodGet, "/api/links", q, nil, nil, linksSummary("Links"))
	})

	AddTool(b, Spec{
		Name:        "get_link",
		Title:       "Get a link",
		Description: "Get one link with its destination, status, campaign, UTM labels and click statistics.",
		Routes:      []string{"GET /api/links/{key}"},
	}, func(ctx context.Context, c *Caller, in keyIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodGet, "/api/links/"+pathSeg(in.Key), nil, nil, nil, summarizeLink("Link"))
	})

	AddTool(b, Spec{
		Name:        "get_link_qr",
		Title:       "Get a link's QR code",
		Description: "Return a print-resolution PNG QR code for a link. The code encodes the short URL, so scans are counted as clicks.",
		Routes:      []string{"GET /api/links/{key}/qr.png"},
	}, func(ctx context.Context, c *Caller, in keyIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		status, body := c.call(ctx, http.MethodGet, "/api/links/"+pathSeg(in.Key)+"/qr.png", nil, nil, nil)
		if status != http.StatusOK {
			text, structured := restError(status, body)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: structured}, nil
		}
		short := qr.ShortURL(c.BaseURL, in.Key)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "QR code for " + short},
				&mcp.ImageContent{Data: body, MIMEType: "image/png"},
			},
			StructuredContent: map[string]any{"key": in.Key, "short_url": short, "mime_type": "image/png"},
		}, nil
	})

	AddTool(b, Spec{
		Name:  "create_link",
		Title: "Create a link",
		Description: "Create a short link for a destination URL, optionally in a campaign and with UTM labels. " +
			"If the same destination was already shortened it returns that link. Pass idempotency_key so a retry cannot create a duplicate.",
		Write:  true,
		Routes: []string{"POST /api/links"},
	}, func(ctx context.Context, c *Caller, in createLinkIn) (*mcp.CallToolResult, error) {
		if r := required("destination_url", in.DestinationURL); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPost, "/api/links", nil, bodyOf(in, "idempotency_key"), idemHeader(in.IdempotencyKey), summarizeLink("Created"))
	})

	AddTool(b, Spec{
		Name:        "update_link",
		Title:       "Update a link",
		Description: "Change a link's title, destination, expiry, UTM labels or placement. Only the fields you pass change; to turn a link off or on use deactivate_link or reactivate_link.",
		Write:       true, Idempotent: true,
		Routes: []string{"PATCH /api/links/{key}"},
	}, func(ctx context.Context, c *Caller, in updateLinkIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPatch, "/api/links/"+pathSeg(in.Key), nil, bodyOf(in, "key"), nil, summarizeLink("Updated"))
	})

	AddTool(b, Spec{
		Name:        "deactivate_link",
		Title:       "Deactivate a link",
		Description: "Turn a link off so its short URL stops redirecting. Click history is kept, and reactivate_link turns it back on.",
		Write:       true, Destructive: true,
		Routes: []string{"PATCH /api/links/{key}"},
	}, func(ctx context.Context, c *Caller, in keyIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPatch, "/api/links/"+pathSeg(in.Key), nil, map[string]any{"active": false}, nil, summarizeLink("Deactivated"))
	})

	AddTool(b, Spec{
		Name:        "reactivate_link",
		Title:       "Reactivate a link",
		Description: "Turn a deactivated link back on. The destination is checked against the URL filter again, and a link denied at creation cannot be reactivated.",
		Write:       true, Idempotent: true,
		Routes: []string{"PATCH /api/links/{key}"},
	}, func(ctx context.Context, c *Caller, in keyIn) (*mcp.CallToolResult, error) {
		if r := requiredSeg("key", in.Key); r != nil {
			return r, nil
		}
		return proxy(ctx, c, http.MethodPatch, "/api/links/"+pathSeg(in.Key), nil, map[string]any{"active": true}, nil, summarizeLink("Reactivated"))
	})
}
