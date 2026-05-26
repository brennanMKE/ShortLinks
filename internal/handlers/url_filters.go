package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// filterRuleStore is the behavior the URL-filters admin handler needs from the
// data layer. *filters.Store satisfies it; depending on an interface keeps the
// handler unit-testable with a fake and documents the exact contract.
type filterRuleStore interface {
	List(ctx context.Context) ([]filters.FilterRule, error)
	Create(ctx context.Context, in filters.NewRule) (filters.FilterRule, error)
	Get(ctx context.Context, id int64) (filters.FilterRule, error)
	Update(ctx context.Context, id int64, upd filters.RuleUpdate) (filters.FilterRule, error)
	Delete(ctx context.Context, id int64) error
	// LoadActive backs the /admin/url-filters/test endpoint, which evaluates a URL
	// against the current active rules (compiled).
	LoadActive(ctx context.Context) ([]filters.Rule, error)
}

// ruleCacheInvalidator is the slice of the rule cache the admin handler needs:
// the ability to drop the cached rule snapshot so a create/update/delete takes
// effect immediately rather than after the 60s TTL. *cache.RuleCache satisfies
// it via Invalidate. Optional (nil → no-op) so the handler is testable without a
// real cache.
type ruleCacheInvalidator interface {
	Invalidate()
}

// URLFiltersHandler serves the admin-only URL filter rule routes:
//
//	GET    /admin/url-filters       — list all rules
//	POST   /admin/url-filters       — create a rule (validates regex), 201
//	PATCH  /admin/url-filters/{id}  — partial update
//	DELETE /admin/url-filters/{id}  — delete a rule
//	POST   /admin/url-filters/test  — evaluate a URL against active rules
//
// All routes MUST be mounted behind RequireSession then RequireAdmin (#0017).
// Every mutation invalidates the rule cache and leaves the #0025 audit seam
// (url_filter.created/updated/deleted).
type URLFiltersHandler struct {
	store filterRuleStore
	cache ruleCacheInvalidator
	// auditor records the url_filter.created/updated/deleted audit entries
	// (#0025). May be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
	now     func() time.Time
}

// NewURLFiltersHandler constructs a URLFiltersHandler. Pass a nil cache to
// disable invalidation and a nil auditor to disable audit writes (tests that do
// not exercise those paths).
func NewURLFiltersHandler(store filterRuleStore, ruleCache ruleCacheInvalidator, auditor *audit.Logger) *URLFiltersHandler {
	return &URLFiltersHandler{store: store, cache: ruleCache, auditor: auditor, now: time.Now}
}

// ruleView is the JSON shape for one url_filter_rules row.
type ruleView struct {
	ID          int64     `json:"id"`
	Pattern     string    `json:"pattern"`
	ReasonCode  int16     `json:"reason_code"`
	ReasonLabel string    `json:"reason_label"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	CreatedBy   *int64    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

func toRuleView(r filters.FilterRule) ruleView {
	return ruleView{
		ID:          r.ID,
		Pattern:     r.Pattern,
		ReasonCode:  r.ReasonCode,
		ReasonLabel: filters.ReasonLabel(int(r.ReasonCode)),
		Description: r.Description,
		Active:      r.Active,
		CreatedBy:   r.CreatedBy,
		CreatedAt:   r.CreatedAt,
	}
}

// rulesResponse is the GET /admin/url-filters body: {"rules":[{...}]}.
type rulesResponse struct {
	Rules []ruleView `json:"rules"`
}

// List handles GET /admin/url-filters. Admin-only via the middleware chain.
func (h *URLFiltersHandler) List(w http.ResponseWriter, r *http.Request) {
	rules, err := h.store.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	views := make([]ruleView, 0, len(rules))
	for _, ru := range rules {
		views = append(views, toRuleView(ru))
	}
	writeJSON(w, http.StatusOK, rulesResponse{Rules: views})
}

// createRuleRequest is the POST /admin/url-filters body.
type createRuleRequest struct {
	Pattern     string `json:"pattern"`
	ReasonCode  int16  `json:"reason_code"`
	Description string `json:"description"`
}

// Create handles POST /admin/url-filters. It validates that pattern compiles as
// a Go regex and that reason_code is in range (1..6), inserts the rule
// (attributed to the admin), invalidates the rule cache, leaves the
// url_filter.created audit seam, and returns 201 with the new rule.
func (h *URLFiltersHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Pattern == "" {
		writeError(w, http.StatusBadRequest, "pattern is required")
		return
	}
	if _, err := regexp.Compile(req.Pattern); err != nil {
		writeError(w, http.StatusBadRequest, "pattern must be a valid Go regular expression")
		return
	}
	if !filters.ValidReasonCode(int(req.ReasonCode)) {
		writeError(w, http.StatusBadRequest, "reason_code must be between 1 and 6")
		return
	}

	rule, err := h.store.Create(r.Context(), filters.NewRule{
		Pattern:     req.Pattern,
		ReasonCode:  req.ReasonCode,
		Description: req.Description,
		CreatedBy:   u.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// A new rule changes evaluation immediately — drop the cached snapshot.
	h.invalidate()

	// #0025 audit: url_filter.created attributed to the admin (u.ID) with
	// {pattern, reason_code, description}. The rule is already committed, so this
	// is fire-and-forget.
	if h.auditor != nil {
		actor := u.ID
		rid := rule.ID
		h.auditor.Record(r.Context(), audit.Entry{
			ActorID:    &actor,
			Action:     audit.ActionURLFilterCreated,
			TargetType: audit.TargetURLFilter,
			TargetID:   &rid,
			Metadata: map[string]any{
				"pattern":     rule.Pattern,
				"reason_code": rule.ReasonCode,
				"description": rule.Description,
			},
			IP: clientIP(r),
		})
	}

	writeJSON(w, http.StatusCreated, toRuleView(rule))
}

// patchRuleRequest is the PATCH /admin/url-filters/{id} body. Every field is a
// pointer so absent ("leave unchanged") is distinguished from present ("set").
type patchRuleRequest struct {
	Pattern     *string `json:"pattern"`
	ReasonCode  *int16  `json:"reason_code"`
	Description *string `json:"description"`
	Active      *bool   `json:"active"`
}

// Patch handles PATCH /admin/url-filters/{id}. Partial update: validates a new
// pattern compiles and a new reason_code is in range, applies the change,
// invalidates the rule cache, leaves the url_filter.updated audit seam.
func (h *URLFiltersHandler) Patch(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id, ok := parseRuleID(w, r)
	if !ok {
		return
	}

	var req patchRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Pattern != nil {
		if *req.Pattern == "" {
			writeError(w, http.StatusBadRequest, "pattern must not be empty")
			return
		}
		if _, err := regexp.Compile(*req.Pattern); err != nil {
			writeError(w, http.StatusBadRequest, "pattern must be a valid Go regular expression")
			return
		}
	}
	if req.ReasonCode != nil && !filters.ValidReasonCode(int(*req.ReasonCode)) {
		writeError(w, http.StatusBadRequest, "reason_code must be between 1 and 6")
		return
	}

	// Read the pre-update rule so the audit metadata can carry the old
	// pattern/reason_code. A miss only weakens the audit entry; the update below
	// still determines the response status.
	before, beforeErr := h.store.Get(r.Context(), id)

	rule, err := h.store.Update(r.Context(), id, filters.RuleUpdate{
		Pattern:     req.Pattern,
		ReasonCode:  req.ReasonCode,
		Description: req.Description,
		Active:      req.Active,
	})
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, filters.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, "rule not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	h.invalidate()

	// #0025 audit: url_filter.updated for the admin (u.ID) with
	// {old_pattern, new_pattern, old_reason_code, new_reason_code}. The update is
	// already committed, so this is fire-and-forget.
	if h.auditor != nil && beforeErr == nil {
		actor := u.ID
		rid := rule.ID
		h.auditor.Record(r.Context(), audit.Entry{
			ActorID:    &actor,
			Action:     audit.ActionURLFilterUpdated,
			TargetType: audit.TargetURLFilter,
			TargetID:   &rid,
			Metadata: map[string]any{
				"old_pattern":     before.Pattern,
				"new_pattern":     rule.Pattern,
				"old_reason_code": before.ReasonCode,
				"new_reason_code": rule.ReasonCode,
			},
			IP: clientIP(r),
		})
	}

	writeJSON(w, http.StatusOK, toRuleView(rule))
}

// Delete handles DELETE /admin/url-filters/{id}. Deletes the rule, invalidates
// the rule cache, leaves the url_filter.deleted audit seam.
func (h *URLFiltersHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	id, ok := parseRuleID(w, r)
	if !ok {
		return
	}

	// Read the rule before deleting so the audit metadata can carry its
	// pattern/reason_code/description (the row is gone afterward). A miss only
	// weakens the audit entry; the delete below still determines the status.
	deleted, deletedErr := h.store.Get(r.Context(), id)

	err := h.store.Delete(r.Context(), id)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, filters.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, "rule not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	h.invalidate()

	// #0025 audit: url_filter.deleted for the admin (u.ID) with
	// {pattern, reason_code, description}. The rule is already deleted, so this is
	// fire-and-forget.
	if h.auditor != nil && deletedErr == nil {
		actor := u.ID
		rid := id
		h.auditor.Record(r.Context(), audit.Entry{
			ActorID:    &actor,
			Action:     audit.ActionURLFilterDeleted,
			TargetType: audit.TargetURLFilter,
			TargetID:   &rid,
			Metadata: map[string]any{
				"pattern":     deleted.Pattern,
				"reason_code": deleted.ReasonCode,
				"description": deleted.Description,
			},
			IP: clientIP(r),
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Rule deleted"})
}

// testRuleRequest is the POST /admin/url-filters/test body.
type testRuleRequest struct {
	URL string `json:"url"`
}

// testRuleResponse mirrors the AC: {"matched":true,"reason_code":2,"rule_id":5}
// on a match, or {"matched":false} on no match (the optional fields are omitted
// when unmatched).
type testRuleResponse struct {
	Matched    bool   `json:"matched"`
	ReasonCode *int   `json:"reason_code,omitempty"`
	RuleID     *int64 `json:"rule_id,omitempty"`
}

// Test handles POST /admin/url-filters/test. It evaluates the given URL against
// the current ACTIVE rules (loaded fresh from the DB, compiled) and reports the
// first match. This is a dry-run: it never inserts a denied link.
func (h *URLFiltersHandler) Test(w http.ResponseWriter, r *http.Request) {
	var req testRuleRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}

	rules, err := h.store.LoadActive(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	compiled := filters.CompileRules(rules, nil)
	code, ruleID, matched := filters.Evaluate(compiled, req.URL)
	if !matched {
		writeJSON(w, http.StatusOK, testRuleResponse{Matched: false})
		return
	}
	writeJSON(w, http.StatusOK, testRuleResponse{Matched: true, ReasonCode: &code, RuleID: &ruleID})
}

// invalidate drops the cached rule snapshot when a cache is wired (no-op when
// nil), so a mutation is observed on the next link creation immediately.
func (h *URLFiltersHandler) invalidate() {
	if h.cache != nil {
		h.cache.Invalidate()
	}
}

// parseRuleID reads and validates the {id} path value, writing a 400 and
// returning ok=false on a missing/invalid id.
func parseRuleID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid rule id")
		return 0, false
	}
	return id, true
}
