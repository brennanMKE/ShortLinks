package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// Caller is the authenticated identity behind one MCP request, plus what it
// takes to act as that identity in-process. Tools receive it; they never see
// the raw token.
type Caller struct {
	UserID    int64
	Email     string
	TokenID   int64
	TokenName string
	// ReadOnly is true for a read-only token: write tools are hidden from
	// tools/list and refused when called.
	ReadOnly bool
	// BaseURL is the instance's public base URL.
	BaseURL string

	// What an inner request needs to be authenticated again by requireAPI.
	authorization string
	remoteAddr    string
	forwardedFor  string
	host          string
	dispatch      http.Handler
}

func (h *Handler) newCaller(r *http.Request, u *middleware.AuthUser) *Caller {
	return &Caller{
		UserID:        u.ID,
		Email:         u.Email,
		TokenID:       u.APITokenID,
		TokenName:     u.APITokenName,
		ReadOnly:      u.ReadOnly,
		BaseURL:       h.opts.BaseURL,
		authorization: r.Header.Get("Authorization"),
		remoteAddr:    r.RemoteAddr,
		forwardedFor:  r.Header.Get("X-Forwarded-For"),
		host:          r.Host,
		dispatch:      h.dispatcher(),
	}
}

// call serves one REST request in-process as the caller and returns the
// status and body. The request carries the caller's own Authorization header,
// so it is re-authenticated by requireAPI on the way in: a revoked token, a
// read-only write and every other REST rule apply exactly as for a network
// call. It always targets an /api path, never /mcp, so it cannot recurse.
//
// The context is marked as an internal dispatch (the per-token rate limit was
// already charged once, for the outer POST /mcp) and as "mcp" for the audit
// log. headers is for per-call values such as Idempotency-Key.
func (c *Caller) call(ctx context.Context, method, path string, query url.Values, body any, headers map[string]string) (int, []byte) {
	if c.dispatch == nil {
		return http.StatusInternalServerError, errorBody("internal_error", "MCP dispatcher is not configured")
	}
	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return http.StatusBadRequest, errorBody("invalid_request", "could not encode request body: "+err.Error())
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	ctx = audit.WithVia(middleware.WithInternalDispatch(ctx), "mcp")
	req, err := http.NewRequestWithContext(ctx, method, "http://mcp.internal"+target, rd)
	if err != nil {
		return http.StatusBadRequest, errorBody("invalid_request", err.Error())
	}
	req.Host = c.host
	req.RemoteAddr = c.remoteAddr
	req.Header.Set("Authorization", c.authorization)
	if c.forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", c.forwardedFor)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	if !serveRecovering(c.dispatch, rec, req) {
		return http.StatusInternalServerError, errorBody("internal_error", "internal server error")
	}
	if rec.Code >= 300 && rec.Code < 400 {
		// A redirect means the path was not the intended route (for example
		// a key of "." or ".."); never surface its body to the agent.
		return http.StatusBadRequest, errorBody("invalid_request", "the request did not match an API route")
	}
	return rec.Code, rec.Body.Bytes()
}

// serveRecovering serves req and reports false if the handler panicked. The
// SDK runs tool handlers in bare goroutines, so an unrecovered panic would
// end the whole process.
func serveRecovering(h http.Handler, w http.ResponseWriter, r *http.Request) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("mcp: panic in dispatched request", "panic", p, "stack", string(debug.Stack()))
			ok = false
		}
	}()
	h.ServeHTTP(w, r)
	return true
}

func errorBody(code, msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": code, "message": msg})
	return b
}

// restError renders a non-2xx REST response as the text of a tool error:
// the HTTP status, the error code or message the API sent, and any extra
// fields (for example reason and label on 422 destination_denied) so the
// agent can correct itself.
func restError(status int, body []byte) (string, map[string]any) {
	fields := map[string]any{}
	_ = json.Unmarshal(body, &fields)
	structured := map[string]any{"status": status}
	var extra []string
	code, _ := fields["error"].(string)
	msg, _ := fields["message"].(string)
	if code != "" {
		structured["error"] = code
	}
	if msg != "" {
		structured["message"] = msg
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if k != "error" && k != "message" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		structured[k] = fields[k]
		extra = append(extra, fmt.Sprintf("%s=%v", k, fields[k]))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "ShortLinks API error: HTTP %d", status)
	if code != "" {
		sb.WriteString(" " + code)
	}
	if msg != "" {
		sb.WriteString(": " + msg)
	}
	if len(extra) > 0 {
		sb.WriteString(" (" + strings.Join(extra, ", ") + ")")
	}
	if code == "" && msg == "" && len(body) > 0 {
		sb.WriteString(": " + strings.TrimSpace(string(body)))
	}
	return sb.String(), structured
}
