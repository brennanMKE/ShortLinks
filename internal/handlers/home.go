package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// homeSettingsReader is what the public home-info handler needs from the data
// layer. *auth.Store and *devstore.Store satisfy it.
type homeSettingsReader interface {
	RegistrationsEnabled(ctx context.Context) (bool, error)
	GetSetting(ctx context.Context, key string) (string, error)
}

// HomeHandler serves GET /api/public/home (#0125). It is UNAUTHENTICATED by
// design: the SPA needs it before sign-in. It exposes a fixed set of keys and no
// other setting.
type HomeHandler struct{ store homeSettingsReader }

// NewHomeHandler constructs a HomeHandler.
func NewHomeHandler(store homeSettingsReader) *HomeHandler { return &HomeHandler{store: store} }

// homeResponse is the public body. The key set is pinned by a test so nothing
// else from settings can leak. Hero is filled by #0176 and Activity by #0175
// (present only in activity mode); until then hero is null and activity absent.
type homeResponse struct {
	RedirectURL          string    `json:"redirect_url"`
	RegistrationsEnabled bool      `json:"registrations_enabled"`
	Mode                 string    `json:"mode"`
	Title                string    `json:"title"`
	About                string    `json:"about"`
	Hero                 *heroInfo `json:"hero"`
}

// heroInfo describes the hero image (#0176).
type heroInfo struct {
	URL    string `json:"url"`
	Alt    string `json:"alt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// settingOrEmpty reads a setting, treating a missing row (database not yet
// migrated) as unset.
func (h *HomeHandler) settingOrEmpty(ctx context.Context, key string) (string, error) {
	v, err := h.store.GetSetting(ctx, key)
	if errors.Is(err, auth.ErrSettingNotFound) {
		return "", nil
	}
	return v, err
}

// ServeHTTP reads every value fresh (no cache). Missing rows are treated as
// unset, not an error; an unknown stored mode falls back to default.
func (h *HomeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.store.RegistrationsEnabled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := homeResponse{RegistrationsEnabled: enabled}
	for _, f := range []struct {
		key string
		dst *string
	}{
		{settingHomeRedirectURL, &resp.RedirectURL},
		{settingHomeMode, &resp.Mode},
		{settingHomeTitle, &resp.Title},
		{settingHomeAbout, &resp.About},
	} {
		v, err := h.settingOrEmpty(r.Context(), f.key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		*f.dst = v
	}
	if validHomeMode(resp.Mode) != nil {
		resp.Mode = homeModeDefault
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
