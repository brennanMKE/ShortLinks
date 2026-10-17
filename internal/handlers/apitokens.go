package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// apiTokenStore is the behavior the API-token handler needs from the data
// layer. *auth.Store and *devstore.Store satisfy it. Every method is scoped to
// the authenticated user id; the handler never trusts a client-supplied owner.
type apiTokenStore interface {
	CreateAPIToken(ctx context.Context, in auth.NewAPIToken, auditor *audit.Logger, entry audit.Entry) (auth.APIToken, string, error)
	ListAPITokensForUser(ctx context.Context, userID int64) ([]auth.APIToken, error)
	RevokeAPIToken(ctx context.Context, userID, id int64, now time.Time, auditor *audit.Logger, entry audit.Entry) (auth.APIToken, error)
}

// APITokensHandler serves API-token management for the signed-in user:
//
//	GET    /account/api-tokens       — list the caller's tokens (never secrets)
//	POST   /account/api-tokens       — mint a token; the secret is returned once
//	DELETE /account/api-tokens/{id}  — revoke one of the caller's tokens
//
// All three MUST be mounted behind middleware.RequireCookieSession, so they can
// only be driven from the browser UI — never by an API token (which could
// otherwise mint more of itself) and never by a bearer session.
type APITokensHandler struct {
	store   apiTokenStore
	auditor *audit.Logger // nil disables audit writes (dev mode, unit tests)
}

// NewAPITokensHandler constructs an APITokensHandler.
func NewAPITokensHandler(store apiTokenStore, auditor *audit.Logger) *APITokensHandler {
	return &APITokensHandler{store: store, auditor: auditor}
}

// apiTokenView is the JSON shape for one token. It never carries the secret or
// its hash; prefix is the short display stub ("slk_AbCdEfGh").
type apiTokenView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	ReadOnly   bool       `json:"read_only"`
	Status     string     `json:"status"` // active | expired | revoked
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

func toAPITokenView(t auth.APIToken, now time.Time) apiTokenView {
	status := "active"
	switch {
	case t.RevokedAt != nil:
		status = "revoked"
	case t.ExpiresAt != nil && !t.ExpiresAt.After(now):
		status = "expired"
	}
	return apiTokenView{
		ID: t.ID, Name: t.Name, Prefix: t.Prefix, ReadOnly: t.ReadOnly, Status: status,
		CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt,
		LastUsedIP: t.LastUsedIP, RevokedAt: t.RevokedAt,
	}
}

// List handles GET /account/api-tokens.
func (h *APITokensHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	tokens, err := h.store.ListAPITokensForUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	now := time.Now()
	views := make([]apiTokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, toAPITokenView(t, now))
	}
	writeJSON(w, http.StatusOK, views)
}

// createAPITokenRequest is the POST body. expires_at is optional: omitted or
// null means the token never expires.
type createAPITokenRequest struct {
	Name      string     `json:"name"`
	ReadOnly  bool       `json:"read_only"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// createAPITokenResponse carries the plaintext token — the only time it is
// ever sent.
type createAPITokenResponse struct {
	Token  string       `json:"token"`
	APIToken apiTokenView `json:"api_token"`
}

// Create handles POST /account/api-tokens. It returns 201 with the token's
// metadata and the plaintext secret, marked Cache-Control: no-store so no
// browser or proxy cache keeps a copy. A duplicate live name is 409.
func (h *APITokensHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createAPITokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > auth.MaxAPITokenNameLen {
		writeError(w, http.StatusBadRequest, "name is too long")
		return
	}
	now := time.Now()
	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		writeError(w, http.StatusBadRequest, "expires_at must be in the future")
		return
	}

	actor := u.ID
	meta := map[string]any{"name": name, "read_only": req.ReadOnly, "expires_at": req.ExpiresAt}
	t, secret, err := h.store.CreateAPIToken(r.Context(),
		auth.NewAPIToken{UserID: u.ID, Name: name, ReadOnly: req.ReadOnly, ExpiresAt: req.ExpiresAt},
		h.auditor,
		audit.Entry{
			ActorID: &actor, UserID: &actor,
			Action: audit.ActionAPITokenCreated, TargetType: audit.TargetAPIToken,
			Metadata: meta, IP: clientIP(r),
		})
	switch {
	case err == nil:
	case errors.Is(err, auth.ErrAPITokenNameTaken):
		writeError(w, http.StatusConflict, "api_token_name_taken")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusCreated, createAPITokenResponse{Token: secret, APIToken: toAPITokenView(t, now)})
}

// Revoke handles DELETE /account/api-tokens/{id}. A token that is not the
// caller's, does not exist, or is already revoked yields 404.
func (h *APITokensHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid api token id")
		return
	}

	actor := u.ID
	now := time.Now()
	t, err := h.store.RevokeAPIToken(r.Context(), u.ID, id, now, h.auditor, audit.Entry{
		ActorID: &actor, UserID: &actor,
		Action: audit.ActionAPITokenRevoked, TargetType: audit.TargetAPIToken,
		IP: clientIP(r),
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toAPITokenView(t, now))
	case errors.Is(err, auth.ErrAPITokenNotFound):
		writeError(w, http.StatusNotFound, "api token not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
