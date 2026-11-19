package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// Options configures the MCP handler.
type Options struct {
	// BaseURL is the instance's public base URL (config BASE_URL). Every
	// link a tool returns carries a short_url built from it.
	BaseURL string
	// Reports runs the analytics reports for the report tools (#0150). Nil
	// (dev mode, which has no clicks table) makes those tools answer with an
	// error.
	Reports ReportRunner
	// DefaultTimezone is the instance's DEFAULT_TIMEZONE, advertised as the
	// default of every report tool's tz input.
	DefaultTimezone string
	// Version is reported to clients as the server version.
	Version string
}

// Handler serves /mcp. Construct it with New, then give it the mux its tools
// dispatch to with SetDispatcher once the routes are registered.
type Handler struct {
	opts   Options
	stream *mcp.StreamableHTTPHandler
	// guarded is stream behind Go's cross-origin protection (Go 1.25+).
	guarded http.Handler
	cache   *mcp.SchemaCache
	// schemas caches each refined tool input schema, as JSON, by tool name.
	schemas sync.Map

	mu       sync.RWMutex
	dispatch http.Handler

	// skipToolGuard turns off the per-tool read-only check so tests can prove
	// the REST layer refuses a write by itself. Never set outside tests.
	skipToolGuard bool
}

// New returns a Handler. It must be mounted behind the API-token guard; it
// answers 403 api_token_required itself if no token identity is present.
func New(opts Options) *Handler {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	h := &Handler{opts: opts, cache: mcp.NewSchemaCache()}
	h.stream = mcp.NewStreamableHTTPHandler(h.serverFor, &mcp.StreamableHTTPOptions{
		// Each POST stands alone: authenticated, then served by a server
		// built for that caller's token. GET and DELETE answer 405.
		Stateless:    true,
		JSONResponse: true,
		// Kept off deliberately. The listener is loopback behind Apache, so
		// every request arrives "via localhost" carrying the public Host
		// header, which the SDK's DNS-rebinding check would reject. The
		// check guards against a browser being tricked into sending
		// ambient credentials; /mcp is bearer-token only (a cookie session
		// is refused), and no CORS headers are sent, so a page cannot
		// read a response or supply the token. Cross-origin browser POSTs
		// are additionally refused by http.CrossOriginProtection below.
		DisableLocalhostProtection: true,
		MaxRequestBodyBytes:        MaxBodyBytes,
	})
	h.guarded = http.NewCrossOriginProtection().Handler(h.stream)
	return h
}

// SetDispatcher sets the handler the tools call in-process: the API mux. It
// must route /api/* through requireAPI and must not route /mcp back here.
func (h *Handler) SetDispatcher(d http.Handler) {
	h.mu.Lock()
	h.dispatch = d
	h.mu.Unlock()
}

func (h *Handler) dispatcher() http.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.dispatch
}

// MaxBodyBytes caps a /mcp request body (256 KiB).
const MaxBodyBytes = 256 << 10

type ctxKey struct{}

// perRequest memoises the server for one HTTP request: the SDK asks for it
// twice (protocol-version check, then serving).
type perRequest struct {
	once   sync.Once
	caller *Caller
	server *mcp.Server
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || !u.ViaAPIToken() {
		writeJSONError(w, http.StatusForbidden, "api_token_required")
		return
	}
	// Read the body under a hard cap and refuse JSON-RPC batches. The SDK
	// accepts a batch array when the protocol-version header is absent or
	// 2025-03-26; one POST would then run many tools for a single charge
	// against the per-token rate limit.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid_request")
		}
		return
	}
	if t := bytes.TrimLeft(body, " \t\r\n"); len(t) > 0 && t[0] == '[' {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"JSON-RPC batches are not supported; send one request per POST"}}` + "\n"))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	pr := &perRequest{caller: h.newCaller(r, u)}
	h.guarded.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, pr)))
}

func (h *Handler) serverFor(r *http.Request) *mcp.Server {
	pr, ok := r.Context().Value(ctxKey{}).(*perRequest)
	if !ok {
		return nil
	}
	pr.once.Do(func() { pr.server, _ = h.buildServer(pr.caller) })
	return pr.server
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}` + "\n"))
}
