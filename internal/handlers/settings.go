package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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
	// baseURL is the instance's own BASE_URL, used to reject a home_redirect_url
	// that points back at this instance (#0125).
	baseURL string
}

// NewSettingsHandler constructs a SettingsHandler over the data layer. A nil
// auditor disables audit writes.
func NewSettingsHandler(store settingStore, auditor *audit.Logger, baseURL string) *SettingsHandler {
	return &SettingsHandler{store: store, auditor: auditor, now: time.Now, baseURL: baseURL}
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
	req.Value = strings.TrimSpace(req.Value)
	if err := validateSettingValue(req.Key, req.Value, h.baseURL); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

// settingHomeRedirectURL is the key of the signed-out home page's primary URL
// (#0125). Empty means unset: there is deliberately no default.
const settingHomeRedirectURL = "home_redirect_url"

// maxHomeRedirectURLLen bounds the stored primary URL.
const maxHomeRedirectURLLen = 2048

// validateSettingValue enforces per-key value constraints and returns an error
// whose message is safe to show to the admin. registrations_enabled is a boolean
// toggle and must be exactly "true" or "false"; home_redirect_url is empty or an
// absolute http(s) URL that is not on this instance's own host (baseURL).
// Unrecognized keys pass and are rejected later by the store
// (ErrSettingNotFound -> 400) because they do not exist.
func validateSettingValue(key, value, baseURL string) error {
	switch key {
	case "registrations_enabled":
		if value == "true" || value == "false" {
			return nil
		}
		return errors.New("invalid value")
	case settingHomeRedirectURL:
		return validHomeRedirectURL(value, baseURL)
	default:
		return nil
	}
}

// validHomeRedirectURL checks the primary URL rules (#0125). Empty clears it.
func validHomeRedirectURL(value, baseURL string) error {
	if value == "" {
		return nil
	}
	errAbsolute := fmt.Errorf("%s must be an absolute http(s) URL", settingHomeRedirectURL)
	if len(value) > maxHomeRedirectURLLen || strings.ContainsAny(value, " \t\r\n") {
		return errAbsolute
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return errAbsolute
		}
	}
	u, err := url.Parse(value)
	if err != nil {
		return errAbsolute
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errAbsolute
	}
	// Never loop back to this instance's own home page. Hostname only: anything
	// on the instance's host is pointless as a primary site.
	if base, perr := url.Parse(baseURL); perr == nil && base.Hostname() != "" &&
		strings.EqualFold(u.Hostname(), base.Hostname()) {
		return fmt.Errorf("%s must not point at this instance", settingHomeRedirectURL)
	}
	return nil
}
