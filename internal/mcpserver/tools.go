package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Spec describes one tool.
type Spec struct {
	// Name is the tool name an agent calls (snake_case).
	Name string
	// Title is a short human-readable name.
	Title string
	// Description tells the model what the tool does and when to use it:
	// one or two sentences.
	Description string
	// Write marks a tool that changes data. Write tools are hidden from a
	// read-only token's tools/list and refused if called.
	Write bool
	// Destructive and Idempotent set the matching MCP annotations; they only
	// matter for a Write tool. A read tool is annotated read-only.
	Destructive bool
	Idempotent  bool
	// Routes lists the REST routes the tool covers, as registered on the mux
	// ("GET /api/links/{key}"). The parity test requires every /api route to
	// appear here or on Excluded. A report tool, which reads internal/reports
	// directly, names the report route it replaces.
	Routes []string
	// Refine, when set, adjusts the input schema inferred from the In struct
	// (jsonschema struct tags cannot express enums or formats). It receives
	// the schema's properties and must only modify them.
	Refine func(props map[string]*jsonschema.Schema)
}

// ToolInfo is a registered tool as the parity test and docs see it.
type ToolInfo struct {
	Name   string
	Write  bool
	Routes []string
}

// Builder is what a Registrar receives: the server being built for one
// caller, and that caller.
type Builder struct {
	Caller *Caller

	h      *Handler
	server *mcp.Server
	infos  []ToolInfo
	hidden map[string]bool
}

// A Registrar adds tools to a Builder. Link and campaign tools register in
// registerLinkTools and registerCampaignTools; #0150 appends the report tools
// to registrars. A registrar runs for every request, so it must only build
// schemas and closures, never touch the database.
type Registrar func(b *Builder)

// registrars lists every tool group, in registration order.
var registrars = []Registrar{
	registerIdentityTools,
	registerLinkTools,
	registerCampaignTools,
	registerReportTools,
	registerPrompts,
}

// AddTool registers a typed tool. In is the input struct; its json tags name
// the arguments and its jsonschema tags describe them (every field needs a
// description, and a field without omitempty is required). fn builds the
// result and should return an error result via failure rather than a Go
// error; a Go error is also surfaced to the agent as a tool error.
func AddTool[In any](b *Builder, s Spec, fn func(ctx context.Context, c *Caller, in In) (*mcp.CallToolResult, error)) {
	destructive := s.Destructive
	open := false
	t := &mcp.Tool{
		Name:        s.Name,
		Title:       s.Title,
		Description: s.Description,
		Annotations: &mcp.ToolAnnotations{
			Title:           s.Title,
			ReadOnlyHint:    !s.Write,
			DestructiveHint: &destructive,
			IdempotentHint:  s.Idempotent,
			OpenWorldHint:   &open,
		},
	}
	if s.Refine != nil {
		// Built once per tool and decoded fresh per request, because the SDK
		// resolves the schema it is given and a shared one would race.
		raw, ok := b.h.schemas.Load(s.Name)
		if !ok {
			sch, err := jsonschema.For[In](nil)
			if err != nil {
				panic("mcpserver: schema for " + s.Name + ": " + err.Error())
			}
			s.Refine(sch.Properties)
			enc, err := json.Marshal(sch)
			if err != nil {
				panic("mcpserver: schema for " + s.Name + ": " + err.Error())
			}
			raw, _ = b.h.schemas.LoadOrStore(s.Name, enc)
		}
		sch := &jsonschema.Schema{}
		if err := json.Unmarshal(raw.([]byte), sch); err != nil {
			panic("mcpserver: schema for " + s.Name + ": " + err.Error())
		}
		t.InputSchema = sch
	}
	b.infos = append(b.infos, ToolInfo{Name: s.Name, Write: s.Write, Routes: s.Routes})
	if s.Write && b.Caller.ReadOnly {
		b.hidden[s.Name] = true
	}
	guard := !b.h.skipToolGuard
	mcp.AddTool(b.server, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		if s.Write && b.Caller.ReadOnly && guard {
			return failure(0, "this API token is read-only, so write tools such as "+s.Name+" are not available; use a token without read-only"), nil, nil
		}
		res, err := recovering(s.Name, func() (*mcp.CallToolResult, error) { return fn(ctx, b.Caller, in) })
		return res, nil, err
	})
}

// recovering runs fn and turns a panic into an internal_error tool error, so
// one bad call cannot crash the process (the SDK gives handlers no recover).
func recovering(tool string, fn func() (*mcp.CallToolResult, error)) (res *mcp.CallToolResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("mcp: panic in tool", "tool", tool, "panic", p, "stack", string(debug.Stack()))
			res, err = failure(http.StatusInternalServerError, "internal_error: the tool failed unexpectedly"), nil
		}
	}()
	return fn()
}

// buildServer returns a server exposing the tools this caller may use.
func (h *Handler) buildServer(c *Caller) (*mcp.Server, []ToolInfo) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "shortlinks", Title: "ShortLinks", Version: h.opts.Version},
		&mcp.ServerOptions{
			SchemaCache: h.cache,
			Instructions: "ShortLinks creates and manages short links and campaigns. " +
				"Short URLs in results are complete and ready to share. " +
				"Pass idempotency_key on create tools so a retry after a timeout cannot make a duplicate. " +
				"The get_* report tools answer questions about clicks; start with list_reports to see what each one covers.",
		})
	b := &Builder{Caller: c, h: h, server: srv, hidden: map[string]bool{}}
	for _, reg := range registrars {
		reg(b)
	}
	if len(b.hidden) > 0 {
		hidden := b.hidden
		srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				res, err := next(ctx, method, req)
				if err != nil || method != "tools/list" {
					return res, err
				}
				if lr, ok := res.(*mcp.ListToolsResult); ok {
					kept := make([]*mcp.Tool, 0, len(lr.Tools))
					for _, t := range lr.Tools {
						if !hidden[t.Name] {
							kept = append(kept, t)
						}
					}
					lr.Tools = kept
				}
				return res, nil
			}
		})
	}
	return srv, b.infos
}

// Describe returns every tool the server registers for a full-access caller.
// The parity test uses it to learn which REST routes the tools cover.
func Describe() []ToolInfo {
	h := New(Options{})
	_, infos := h.buildServer(&Caller{})
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// ---- results --------------------------------------------------------------

// success builds a tool result from a 2xx REST body: a one-line summary, then
// the JSON itself as text (clients that ignore structured content still see
// the data), plus the JSON as structured content. Link objects already carry
// short_url from the REST API (#0181).
func success(c *Caller, summary func(m map[string]any) string, body []byte) *mcp.CallToolResult {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		// Not an object (no REST route returns one): wrap it.
		var v any
		_ = json.Unmarshal(body, &v)
		m = map[string]any{"result": v}
		body, _ = json.Marshal(m)
	}
	text := ""
	if summary != nil {
		text = summary(m)
	}
	content := []mcp.Content{}
	if text != "" {
		content = append(content, &mcp.TextContent{Text: text})
	}
	// The JSON appears twice on purpose: as structured content and as a text
	// block. Most clients show the model only `content`, so without the
	// text block the model would see the summary line but not the data.
	content = append(content, &mcp.TextContent{Text: string(body)})
	return &mcp.CallToolResult{Content: content, StructuredContent: json.RawMessage(body)}
}

// failure builds a tool error. status 0 means the error did not come from REST.
func failure(status int, text string) *mcp.CallToolResult {
	res := &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	if status != 0 {
		res.StructuredContent = map[string]any{"status": status, "error": text}
	}
	return res
}

// proxy dispatches one REST call and converts the outcome: a 2xx body into a
// success result, anything else into a tool error carrying the REST status,
// error code and message.
func proxy(ctx context.Context, c *Caller, method, path string, query url.Values, body any, headers map[string]string, summary func(m map[string]any) string) (*mcp.CallToolResult, error) {
	status, resp := c.call(ctx, method, path, query, body, headers)
	if status < 200 || status > 299 {
		text, structured := restError(status, resp)
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: structured}, nil
	}
	if len(bytes.TrimSpace(resp)) == 0 {
		resp = []byte(`{"ok":true}`)
	}
	return success(c, summary, resp), nil
}

// bodyOf turns an input struct into a request body, dropping the fields that
// belong in the path or a header instead.
func bodyOf(in any, drop ...string) map[string]any {
	b, _ := json.Marshal(in)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	for _, k := range drop {
		delete(m, k)
	}
	return m
}

func idemHeader(key string) map[string]string {
	return map[string]string{"Idempotency-Key": key}
}

func pathSeg(s string) string { return url.PathEscape(s) }

// required returns a tool error naming the missing argument, or nil.
func required(name, val string) *mcp.CallToolResult {
	if strings.TrimSpace(val) == "" {
		return failure(0, name+" is required")
	}
	return nil
}

// requiredSeg is required for a value used as one path segment (a link key or
// campaign slug).
func requiredSeg(name, val string) *mcp.CallToolResult {
	if r := required(name, val); r != nil {
		return r
	}
	// "." and ".." are cleaned out of the path by the mux (a redirect), and a
	// slash would change the route; no key or slug can contain either.
	if val == "." || val == ".." || strings.Contains(val, "/") {
		return failure(0, name+" is invalid: it must not be \".\", \"..\" or contain \"/\"")
	}
	return nil
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func linkLine(m map[string]any) string {
	short := str(m, "short_url")
	dest := str(m, "destination_url")
	line := short + " → " + dest
	if a, ok := m["active"].(bool); ok && !a {
		line += " (inactive)"
	}
	return line
}

func summarizeLink(verb string) func(map[string]any) string {
	return func(m map[string]any) string {
		if d, ok := m["duplicate"].(bool); ok && d {
			return "Existing link returned (same destination already shortened): " + linkLine(m)
		}
		return verb + " " + linkLine(m)
	}
}

func linksSummary(what string) func(map[string]any) string {
	return func(m map[string]any) string {
		items, _ := m["links"].([]any)
		var sb strings.Builder
		if total, ok := m["total"].(float64); ok {
			fmt.Fprintf(&sb, "%s: %d of %d links (page %v)", what, len(items), int(total), m["page"])
		} else {
			fmt.Fprintf(&sb, "%s: %d links", what, len(items))
		}
		for i, it := range items {
			if i >= 10 {
				fmt.Fprintf(&sb, "\n… and %d more", len(items)-10)
				break
			}
			if im, ok := it.(map[string]any); ok {
				sb.WriteString("\n" + linkLine(im))
			}
		}
		return sb.String()
	}
}
