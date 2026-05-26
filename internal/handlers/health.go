package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthPingTimeout bounds the database connectivity check so /health stays
// fast and predictable for monitoring tools and the systemd watchdog. The PRD's
// acceptance criteria fix this at 2 seconds.
const healthPingTimeout = 2 * time.Second

// Pinger reports whether a dependency (the database pool) is reachable.
//
// The health handler depends only on this interface so it is fully
// unit-testable with a fake; *pgxpool.Pool satisfies it via its Ping method.
type Pinger interface {
	Ping(ctx context.Context) error
}

// healthResponse is the JSON body returned by GET /health.
//
// On success: {"status":"ok","db":"ok"}. On a failed database ping:
// {"status":"degraded","db":"error","error":"..."}. The Error field is omitted
// when empty so the healthy response stays minimal.
type healthResponse struct {
	Status string `json:"status"`
	DB     string `json:"db"`
	Error  string `json:"error,omitempty"`
}

// HealthHandler serves GET /health: a public, unauthenticated check returning
// the overall service status and database connectivity for monitoring tools and
// the systemd watchdog.
type HealthHandler struct {
	db Pinger
}

// NewHealthHandler constructs a HealthHandler from a Pinger (the DB pool).
func NewHealthHandler(db Pinger) *HealthHandler {
	return &HealthHandler{db: db}
}

// ServeHTTP implements http.Handler. It pings the database under a 2-second
// timeout and returns 200 with {"status":"ok","db":"ok"} when reachable, or 503
// with {"status":"degraded","db":"error","error":"..."} when the ping fails.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), healthPingTimeout)
	defer cancel()

	resp := healthResponse{Status: "ok", DB: "ok"}
	status := http.StatusOK
	if err := h.db.Ping(ctx); err != nil {
		resp = healthResponse{Status: "degraded", DB: "error", Error: err.Error()}
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encoding to a fixed-shape struct cannot fail; ignore the error so we don't
	// attempt to write a second header after WriteHeader.
	_ = json.NewEncoder(w).Encode(resp)
}
