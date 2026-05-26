package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// settingStore is the behavior the settings handler needs from the data layer.
// Depending on an interface (rather than the concrete *auth.Store) keeps the
// handler unit-testable with a fake, mirroring the AuthHandler/CredentialsHandler
// pattern. *auth.Store satisfies it via ListSettings and UpdateSetting.
type settingStore interface {
	ListSettings(ctx context.Context) ([]auth.Setting, error)
	UpdateSetting(ctx context.Context, key, value string, now time.Time) (oldValue string, err error)
}

// SettingsHandler serves the admin-only runtime settings routes:
//
//	GET   /admin/settings  — list every row in the settings table
//	PATCH /admin/settings  — update one existing setting key
//
// Both routes MUST be mounted behind middleware.RequireSession then
// middleware.RequireAdmin (#0017): RequireSession attaches the AuthUser and
// answers 401 for an absent/invalid session, and RequireAdmin answers 403 for a
// non-admin. The handler itself re-reads the user from the context only so it
// can attribute the (future #0025) audit entry; it does not re-check admin.
type SettingsHandler struct {
	store settingStore
	// auditor records the settings.updated audit entry (#0025). May be nil in
	// unit tests that do not assert audit rows.
	auditor *audit.Logger
	// now is injectable so updated_at and the audit timestamp are deterministic
	// in tests; defaults to time.Now.
	now func() time.Time
}

// NewSettingsHandler constructs a SettingsHandler over the data layer. A nil
// auditor disables audit writes.
func NewSettingsHandler(store settingStore, auditor *audit.Logger) *SettingsHandler {
	return &SettingsHandler{store: store, auditor: auditor, now: time.Now}
}

// settingView is the JSON shape for one settings row. updated_at is omitted when
// NULL so a never-touched seed row does not emit a zero timestamp.
type settingView struct {
	Key       string     `json:"key"`
	Value     string     `json:"value"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// settingsResponse is the GET /admin/settings body: {"settings":[{...}]}. The
// list is always present (never null) so the client gets a stable shape.
type settingsResponse struct {
	Settings []settingView `json:"settings"`
}

// List handles GET /admin/settings. It returns every row in the settings table
// as {"settings":[{"key":...,"value":...,"updated_at":...}]}. Admin-only by the
// middleware chain it is mounted behind.
func (h *SettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.ListSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

// patchSettingRequest is the PATCH /admin/settings body. Per the issue's
// acceptance criteria it carries a single key/value pair, e.g.
// {"key":"registrations_enabled","value":"true"}.
type patchSettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Patch handles PATCH /admin/settings. It updates one EXISTING setting key,
// validates the value for known keys, and returns the full updated settings
// list. An unknown key yields 400 (no arbitrary key creation, per the issue),
// and an invalid value for a validated key yields 400 with the row unchanged.
func (h *SettingsHandler) Patch(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		// Should be unreachable behind RequireSession+RequireAdmin, but guard so
		// the handler never panics if mounted without the chain.
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req patchSettingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	if !validSettingValue(req.Key, req.Value) {
		writeError(w, http.StatusBadRequest, "invalid value")
		return
	}

	oldValue, err := h.store.UpdateSetting(r.Context(), req.Key, req.Value, h.now())
	switch {
	case err == nil:
		// fall through to write the audit entry and return the new state.
	case errors.Is(err, auth.ErrSettingNotFound):
		writeError(w, http.StatusBadRequest, "unknown setting key")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// #0025 audit: settings.updated attributed to the admin (actor_id = u.ID)
	// with {key, old_value, new_value}. target_type is "settings"; settings rows
	// are keyed by a TEXT primary key, not a bigint, so target_id is left NULL.
	// The setting is already updated, so this is fire-and-forget.
	if h.auditor != nil {
		actor := u.ID
		h.auditor.Record(r.Context(), audit.Entry{
			ActorID:    &actor,
			Action:     audit.ActionSettingsUpdated,
			TargetType: audit.TargetSettings,
			Metadata: map[string]any{
				"key":       req.Key,
				"old_value": oldValue,
				"new_value": req.Value,
			},
			IP: clientIP(r),
		})
	}

	// Return the authoritative updated settings list so the client refreshes its
	// full view in one round trip.
	settings, err := h.store.ListSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(settings))
}

// toSettingsResponse maps store rows to the JSON response, always emitting a
// non-nil slice.
func toSettingsResponse(settings []auth.Setting) settingsResponse {
	views := make([]settingView, 0, len(settings))
	for _, s := range settings {
		views = append(views, settingView{
			Key:       s.Key,
			Value:     s.Value,
			UpdatedAt: s.UpdatedAt,
		})
	}
	return settingsResponse{Settings: views}
}

// validSettingValue enforces per-key value constraints. registrations_enabled is
// a boolean toggle and must be exactly "true" or "false"; any other validated
// key would be added here. Unrecognized keys pass this check and are rejected
// later by the store (ErrSettingNotFound → 400) because they do not exist —
// this function only guards the shape of values for known keys.
func validSettingValue(key, value string) bool {
	switch key {
	case "registrations_enabled":
		return value == "true" || value == "false"
	default:
		return true
	}
}
