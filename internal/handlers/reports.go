package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// reportRunner is the slice of *reports.Service the handler needs.
type reportRunner interface {
	Run(ctx context.Context, userID int64, name string, raw url.Values) (*reports.Envelope, error)
	Catalogue() reports.Catalogue
}

// ReportsHandler serves the analytics reports (#0139):
//
//	GET /api/reports          — catalogue of registered reports
//	GET /api/reports/{name}   — run one report (overview, ...)
//
// Mount both behind requireAPI. Every report is scoped to the authenticated
// user's links; the user comes from the request context, never the query.
// Both routes are GET, so read-only API tokens may use them.
type ReportsHandler struct {
	svc reportRunner
}

// NewReportsHandler wraps a reports.Service.
func NewReportsHandler(svc *reports.Service) *ReportsHandler {
	return &ReportsHandler{svc: svc}
}

// List handles GET /api/reports.
func (h *ReportsHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := middleware.UserFromContext(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	writeJSON(w, http.StatusOK, h.svc.Catalogue())
}

// Get handles GET /api/reports/{name}.
func (h *ReportsHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	env, err := h.svc.Run(r.Context(), u.ID, r.PathValue("name"), r.URL.Query())
	if err != nil {
		var pe *reports.ParamError
		var nf *reports.NotFoundError
		switch {
		case errors.Is(err, reports.ErrReportNotFound):
			writeError(w, http.StatusNotFound, "report_not_found")
		case errors.As(err, &nf):
			writeError(w, http.StatusNotFound, nf.Code)
		case errors.As(err, &pe):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": pe.Code, "field": pe.Field, "message": pe.Message})
		default:
			slog.Error("reports: run failed", "report", r.PathValue("name"), "err", err)
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}
	writeJSON(w, http.StatusOK, env)
}
