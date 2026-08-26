package handlers

import (
	"net/http"
	"strings"

	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// MeHandler serves the authenticated current-user profile route:
//
//	GET /api/me — return the caller's {id, email, is_admin, base_url}
//
// The route MUST be mounted behind middleware.RequireSession (#0017): the guard
// validates the session cookie, answers 401 for an absent/invalid session, and
// attaches the AuthUser to the request context. This handler then reads that
// user straight off the context — no DB round-trip — because AuthUser already
// carries exactly the three fields the profile exposes (id, email, is_admin).
//
// The Svelte SPA calls this on load to decide whether to show the Admin tab
// (is_admin), per the PRD's Key HTTP Routes table. It is also how the SPA
// learns the deployment's public base URL (#0117): short links are only ever
// displayed to an authenticated user, so the profile call the SPA already
// makes on boot is the natural carrier — no extra endpoint, no extra round
// trip, and no domain compiled into the bundle.
type MeHandler struct {
	// baseURL is the configured BASE_URL (internal/config), which Load
	// requires unconditionally — so it is always non-empty in a real wiring.
	baseURL string
}

// NewMeHandler constructs a MeHandler over the configured BASE_URL. The user
// itself is supplied by RequireSession on the request context, so that is the
// handler's only dependency.
func NewMeHandler(baseURL string) *MeHandler { return &MeHandler{baseURL: baseURL} }

// meResponse is the GET /api/me body. Field names are snake_case to match the
// PRD baseline and the rest of the API (e.g. is_admin).
type meResponse struct {
	ID      int64  `json:"id"`
	Email   string `json:"email"`
	IsAdmin bool   `json:"is_admin"`
	// BaseURL is the deployment's public base URL (#0117). The SPA builds
	// every displayed short URL from it (web/src/lib/links.ts's shortUrl), so
	// it matches what internal/qr encodes into the QR codes for the same link.
	BaseURL string `json:"base_url"`
}

// Me handles GET /api/me. It returns 200 with the authenticated user's profile.
// It re-reads the user from the context and answers 401 if absent so the handler
// never panics when mounted without RequireSession; in normal operation the
// guard guarantees the user is present.
func (h *MeHandler) Me(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		ID:      u.ID,
		Email:   u.Email,
		IsAdmin: u.IsAdmin,
		BaseURL: strings.TrimRight(h.baseURL, "/"),
	})
}
