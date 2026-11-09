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
// design: the SPA needs it before sign-in. It exposes exactly two facts and no
// other setting.
type HomeHandler struct{ store homeSettingsReader }

// NewHomeHandler constructs a HomeHandler.
func NewHomeHandler(store homeSettingsReader) *HomeHandler { return &HomeHandler{store: store} }

// homeResponse is the public body: exactly these two keys.
type homeResponse struct {
	RedirectURL          string `json:"redirect_url"`
	RegistrationsEnabled bool   `json:"registrations_enabled"`
}

// ServeHTTP reads both values fresh (no cache). A missing home_redirect_url row
// (database not yet migrated) is treated as unset, not an error.
func (h *HomeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.store.RegistrationsEnabled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	redirect, err := h.store.GetSetting(r.Context(), settingHomeRedirectURL)
	if err != nil {
		if !errors.Is(err, auth.ErrSettingNotFound) {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		redirect = ""
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, homeResponse{RedirectURL: redirect, RegistrationsEnabled: enabled})
}
