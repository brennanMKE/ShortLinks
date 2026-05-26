package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// deactivationReasons is the set of allowed account.deactivated reasons, mirroring
// the PRD's Deactivation reasons dropdown. A reason outside this set is rejected
// with 400. The "other" reason additionally requires a non-empty note.
var deactivationReasons = map[string]struct{}{
	"malware_distribution": {},
	"phishing":             {},
	"spam":                 {},
	"harassment":           {},
	"terms_violation":      {},
	"other":                {},
}

// userStore is the behavior the admin user-management handler needs from the data
// layer. *auth.Store satisfies it. Depending on an interface keeps the handler
// unit-testable with a fake and documents the exact contract. The deactivate /
// reactivate methods take the auditor + a prepared audit.Entry so the audit row is
// written inside the store's transaction (WriteTx), committing atomically with the
// account change.
type userStore interface {
	ListUsers(ctx context.Context) ([]auth.ManagedUser, error)
	GetUser(ctx context.Context, id int64) (auth.UserDetail, error)
	DeactivateUser(ctx context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error)
	ReactivateUser(ctx context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error)
}

// AdminUsersHandler serves the admin-only user-management routes:
//
//	GET  /admin/users                  — list all users (status + last login)
//	GET  /admin/users/{id}             — user detail + link/passkey counts
//	POST /admin/users/{id}/deactivate  — deactivate a non-admin user
//	POST /admin/users/{id}/reactivate  — reactivate a user
//
// All routes MUST be mounted behind middleware.RequireSession then
// middleware.RequireAdmin (#0017): RequireSession attaches the AuthUser and
// answers 401 for an absent/invalid session, RequireAdmin answers 403 for a
// non-admin. The handler re-reads the user from the context only to attribute the
// audit entry (actor = acting admin) and to refuse self-deactivation; it does not
// re-check admin.
type AdminUsersHandler struct {
	store userStore
	// auditor records account.deactivated / account.reactivated entries (#0025).
	// Passed through to the store so the row is written inside its transaction.
	// May be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
	now     func() time.Time
}

// NewAdminUsersHandler constructs an AdminUsersHandler over the data layer. A nil
// auditor disables audit writes.
func NewAdminUsersHandler(store userStore, auditor *audit.Logger) *AdminUsersHandler {
	return &AdminUsersHandler{store: store, auditor: auditor, now: time.Now}
}

// userView is the JSON shape for one users row in the list and as the body of a
// deactivate/reactivate response. last_login_at is omitted when the account has
// never logged in.
type userView struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	IsAdmin     bool       `json:"is_admin"`
	Active      bool       `json:"active"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

func toUserView(u auth.ManagedUser) userView {
	return userView{
		ID:          u.ID,
		Email:       u.Email,
		IsAdmin:     u.IsAdmin,
		Active:      u.Active,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// userDetailView extends userView with the per-account counts on the detail view.
type userDetailView struct {
	userView
	LinkCount    int64 `json:"link_count"`
	PasskeyCount int64 `json:"passkey_count"`
}

// usersResponse is the GET /admin/users body: {"users":[{...}]}. The list is
// always present (never null) so the client gets a stable shape.
type usersResponse struct {
	Users []userView `json:"users"`
}

// List handles GET /admin/users. It returns every account with its status
// (active) and last-login column. Admin-only via the middleware chain.
func (h *AdminUsersHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, toUserView(u))
	}
	writeJSON(w, http.StatusOK, usersResponse{Users: views})
}

// Get handles GET /admin/users/{id}. It returns the account detail plus its link
// and passkey counts, or 404 when no such account exists.
func (h *AdminUsersHandler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}
	d, err := h.store.GetUser(r.Context(), id)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, userDetailView{
			userView:     toUserView(d.ManagedUser),
			LinkCount:    d.LinkCount,
			PasskeyCount: d.PasskeyCount,
		})
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// deactivateRequest is the POST /admin/users/{id}/deactivate body: a required
// reason (one of the six defined values) and an optional note (required when
// reason == "other").
type deactivateRequest struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

// Deactivate handles POST /admin/users/{id}/deactivate. It validates the reason
// (and note for "other"), refuses to deactivate an admin or the acting admin
// themselves, then atomically sets active=false, deletes all of the target's
// sessions, and writes the account.deactivated audit entry. Returns the updated
// user with 200.
func (h *AdminUsersHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	actor, ok := middleware.UserFromContext(r.Context())
	if !ok {
		// Unreachable behind RequireSession+RequireAdmin, but guard so the handler
		// never panics if mounted without the chain.
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}

	var req deactivateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, valid := deactivationReasons[req.Reason]; !valid {
		writeError(w, http.StatusBadRequest, "invalid reason")
		return
	}
	if req.Reason == "other" && req.Note == "" {
		writeError(w, http.StatusBadRequest, "note is required when reason is other")
		return
	}
	// Refuse self-deactivation: an admin must not lock themselves out, and they
	// are an admin anyway (which the store would also refuse).
	if id == actor.ID {
		writeError(w, http.StatusForbidden, "cannot deactivate yourself")
		return
	}

	// #0025 audit: account.deactivated attributed to the acting admin (actor_id),
	// affecting the target (user_id / target_id), with {reason, note} metadata. The
	// store writes this in-band (WriteTx) so it commits with the change.
	actorID := actor.ID
	targetID := id
	entry := audit.Entry{
		ActorID:    &actorID,
		UserID:     &targetID,
		Action:     audit.ActionAccountDeactivated,
		TargetType: audit.TargetUser,
		TargetID:   &targetID,
		Metadata: map[string]any{
			"reason": req.Reason,
			"note":   req.Note,
		},
		IP: clientIP(r),
	}

	u, err := h.store.DeactivateUser(r.Context(), id, h.auditor, entry)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toUserView(u))
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, auth.ErrUserIsAdmin):
		writeError(w, http.StatusForbidden, "cannot deactivate an admin user")
	case errors.Is(err, auth.ErrUserAlreadyInactive):
		writeError(w, http.StatusConflict, "user already inactive")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// reactivateRequest is the POST /admin/users/{id}/reactivate body: an optional
// note recorded in the account.reactivated audit metadata.
type reactivateRequest struct {
	Note string `json:"note"`
}

// Reactivate handles POST /admin/users/{id}/reactivate. It sets active=true and
// writes the account.reactivated audit entry ({note}). Sessions are NOT restored —
// the user must log in again. Returns the updated user with 200.
func (h *AdminUsersHandler) Reactivate(w http.ResponseWriter, r *http.Request) {
	actor, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id, ok := parseUserID(w, r)
	if !ok {
		return
	}

	var req reactivateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	actorID := actor.ID
	targetID := id
	entry := audit.Entry{
		ActorID:    &actorID,
		UserID:     &targetID,
		Action:     audit.ActionAccountReactivated,
		TargetType: audit.TargetUser,
		TargetID:   &targetID,
		Metadata: map[string]any{
			"note": req.Note,
		},
		IP: clientIP(r),
	}

	u, err := h.store.ReactivateUser(r.Context(), id, h.auditor, entry)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toUserView(u))
	case errors.Is(err, auth.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, auth.ErrUserAlreadyActive):
		writeError(w, http.StatusConflict, "user already active")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// parseUserID reads and validates the {id} path value, writing a 400 and
// returning ok=false on a missing/invalid id.
func parseUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return 0, false
	}
	return id, true
}
