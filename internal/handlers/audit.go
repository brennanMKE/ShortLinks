package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// Audit pagination bounds. per_page defaults to 50 (per the issue AC) and is
// capped at maxPerPage so a client cannot ask for an unbounded page; page is
// 1-based and clamped to a minimum of 1.
const (
	defaultAuditPerPage = 50
	maxAuditPerPage     = 200
)

// auditReader is the behavior the admin audit handler needs from the data
// layer. *audit.Reader satisfies it. Depending on an interface keeps the
// handler unit-testable with a fake and documents the exact contract.
type auditReader interface {
	ListAuditLog(ctx context.Context, userID *int64, limit, offset int) ([]audit.Record, int64, error)
}

// AdminAuditHandler serves the admin-only audit log route:
//
//	GET /admin/audit  — paginated audit log, newest-first, optional ?user_id=
//
// It MUST be mounted behind middleware.RequireSession then
// middleware.RequireAdmin (#0017): RequireSession answers 401 for an
// absent/invalid session, RequireAdmin answers 403 for a non-admin. The handler
// itself does not re-check auth.
type AdminAuditHandler struct {
	reader auditReader
}

// NewAdminAuditHandler constructs an AdminAuditHandler over the audit read
// layer.
func NewAdminAuditHandler(reader auditReader) *AdminAuditHandler {
	return &AdminAuditHandler{reader: reader}
}

// auditRecordView is the JSON shape for one audit_log row. Nullable columns are
// pointers so they serialize as JSON null (not 0 / "") when unset. metadata is
// json.RawMessage so it round-trips as a JSON object, never a quoted string;
// when the column is SQL NULL it is nil and serializes as JSON null.
type auditRecordView struct {
	ID         int64           `json:"id"`
	ActorID    *int64          `json:"actor_id"`
	UserID     *int64          `json:"user_id"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type"`
	TargetID   *int64          `json:"target_id"`
	Metadata   json.RawMessage `json:"metadata"`
	IPAddress  *string         `json:"ip_address"`
	CreatedAt  time.Time       `json:"created_at"`
}

func toAuditRecordView(r audit.Record) auditRecordView {
	return auditRecordView{
		ID:         r.ID,
		ActorID:    r.ActorID,
		UserID:     r.UserID,
		Action:     r.Action,
		TargetType: r.TargetType,
		TargetID:   r.TargetID,
		Metadata:   r.Metadata,
		IPAddress:  r.IP,
		CreatedAt:  r.CreatedAt,
	}
}

// auditResponse is the GET /admin/audit body. The list is always present (never
// null) so the client gets a stable shape; total/page/per_page describe the
// pagination so the client can compute the number of pages.
type auditResponse struct {
	AuditLog []auditRecordView `json:"audit_log"`
	Total    int64             `json:"total"`
	Page     int               `json:"page"`
	PerPage  int               `json:"per_page"`
}

// List handles GET /admin/audit. It returns the audit log newest-first,
// paginated via ?page= and ?per_page= (default 50, per_page capped at
// maxAuditPerPage), and optionally filtered to a single user via ?user_id=. A
// non-integer user_id, page, or per_page is rejected with 400. Admin-only via
// the middleware chain.
func (h *AdminAuditHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	page, ok := parsePositiveQueryInt(w, q.Get("page"), 1, "page")
	if !ok {
		return
	}
	perPage, ok := parsePositiveQueryInt(w, q.Get("per_page"), defaultAuditPerPage, "per_page")
	if !ok {
		return
	}
	// Cap the page size so a client cannot request an unbounded slice.
	if perPage > maxAuditPerPage {
		perPage = maxAuditPerPage
	}

	// Optional ?user_id= filter. Present-but-unparseable is a client error (400);
	// absent means "no filter".
	var userID *int64
	if raw := q.Get("user_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid user_id")
			return
		}
		userID = &id
	}

	offset := (page - 1) * perPage
	records, total, err := h.reader.ListAuditLog(r.Context(), userID, perPage, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	views := make([]auditRecordView, 0, len(records))
	for _, rec := range records {
		views = append(views, toAuditRecordView(rec))
	}
	writeJSON(w, http.StatusOK, auditResponse{
		AuditLog: views,
		Total:    total,
		Page:     page,
		PerPage:  perPage,
	})
}

// parsePositiveQueryInt parses a positive integer query parameter, returning
// def when raw is empty. It writes a 400 and returns ok=false when raw is
// present but not a positive integer.
func parsePositiveQueryInt(w http.ResponseWriter, raw string, def int, name string) (int, bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "invalid "+name)
		return 0, false
	}
	return n, true
}
