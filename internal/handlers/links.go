package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// defaultPerPage is the page size used when ?per_page= is absent, per the issue
// (#0022) acceptance criteria.
const defaultPerPage = 20

// maxPerPage caps a client-supplied ?per_page= so a single request cannot ask
// for an unbounded result set.
const maxPerPage = 100

// linkStore is the behavior the links handler needs from the data layer.
// Depending on an interface (rather than the concrete *links.Store) keeps the
// handler unit-testable with a fake and documents the exact contract. Every
// mutating/reading method takes the authenticated user id so the store scopes
// each query to the caller's own rows — the handler never trusts a
// client-supplied owner.
type linkStore interface {
	KeyExists(ctx context.Context, key string) (bool, error)
	CreateLink(ctx context.Context, in links.NewLink) (links.Link, error)
	ListLinks(ctx context.Context, userID int64, limit, offset int) ([]links.Link, error)
	CountLinks(ctx context.Context, userID int64) (int64, error)
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	UpdateLink(ctx context.Context, userID int64, key string, upd links.LinkUpdate) (links.Link, error)
	DeactivateLink(ctx context.Context, userID int64, key string) error
}

// cacheEvictor is the slice of the redirect cache the links handler needs: the
// ability to drop a key so the next redirect re-reads the DB. *cache.Cache
// satisfies it via Delete. It is optional — if no cache is wired in (nil), the
// handler simply skips eviction (the redirect path will repopulate naturally
// from the DB once the cached entry's TTL lapses). Taking an interface keeps the
// handler testable without a real Ristretto cache.
type cacheEvictor interface {
	Delete(key string)
}

// LinksHandler serves the authenticated link CRUD API:
//
//	POST   /api/links        — create a short link
//	GET    /api/links        — list the caller's links (paginated)
//	GET    /api/links/{key}  — link detail + click stats
//	PATCH  /api/links/{key}  — update title/destination/expiry
//	DELETE /api/links/{key}  — deactivate (soft delete)
//
// All five routes MUST be mounted behind middleware.RequireSession (#0017);
// each handler reads the authenticated user from the request context and scopes
// every store call to that user, so a request can only see or mutate its own
// links.
type LinksHandler struct {
	store linkStore
	// cache is the redirect cache to evict from on update/deactivate; may be nil
	// (no cache wired) in which case eviction is skipped.
	cache cacheEvictor
}

// NewLinksHandler constructs a LinksHandler over the data layer and the redirect
// cache. Pass a nil cache to disable eviction (e.g. in unit tests that do not
// exercise the cache); the handler then skips the Delete calls.
func NewLinksHandler(store linkStore, redirectCache cacheEvictor) *LinksHandler {
	return &LinksHandler{store: store, cache: redirectCache}
}

// linkView is the JSON shape for a single link, shared by every endpoint. The
// field set matches the issue's acceptance criteria. duplicate is part of the
// create response shape (always false until #0023 populates it); it is omitted
// from list/detail responses where it is not meaningful.
type linkView struct {
	ID             int64      `json:"id"`
	Key            string     `json:"key"`
	DestinationURL string     `json:"destination_url"`
	Title          string     `json:"title"`
	Active         bool       `json:"active"`
	DeniedReason   int16      `json:"denied_reason"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at"`
	ClickCount     int64      `json:"click_count"`
	Duplicate      *bool      `json:"duplicate,omitempty"`
}

// toLinkView maps a domain Link to its JSON shape without the duplicate field
// (used by list and detail responses).
func toLinkView(l links.Link) linkView {
	return linkView{
		ID:             l.ID,
		Key:            l.Key,
		DestinationURL: l.DestinationURL,
		Title:          l.Title,
		Active:         l.Active,
		DeniedReason:   l.DeniedReason,
		CreatedAt:      l.CreatedAt,
		ExpiresAt:      l.ExpiresAt,
		ClickCount:     l.ClickCount,
	}
}

// createLinkRequest is the POST /api/links body. custom_key and alias are
// accepted as synonyms for a user-supplied key so the client may send either
// (the PRD/issue use both "key" and "alias" terminology); key is the canonical
// field. expires_at is RFC 3339.
type createLinkRequest struct {
	DestinationURL string     `json:"destination_url"`
	Title          string     `json:"title"`
	Key            string     `json:"key"`
	CustomKey      string     `json:"custom_key"`
	Alias          string     `json:"alias"`
	ExpiresAt      *time.Time `json:"expires_at"`
}

// customKey returns the user-supplied alias from whichever field carried it, or
// "" when none was provided (the server then generates a key).
func (r createLinkRequest) customKey() string {
	switch {
	case r.Key != "":
		return r.Key
	case r.CustomKey != "":
		return r.CustomKey
	default:
		return r.Alias
	}
}

// listLinksResponse is the GET /api/links body: the page of links plus
// pagination metadata. links is always a non-nil array.
type listLinksResponse struct {
	Links   []linkView `json:"links"`
	Page    int        `json:"page"`
	PerPage int        `json:"per_page"`
	Total   int64      `json:"total"`
}

// Create handles POST /api/links. It validates the destination URL, resolves a
// key (a user-supplied custom alias if present and free, otherwise a freshly
// generated unique 6-char key), inserts the link, and returns 201 with the full
// link object carrying "duplicate": false.
//
// SEAMS — the layered issues slot into this method in the following ORDER,
// between request validation and the insert. None are implemented here:
//
//	── #0024 URL FILTER CHECK (runs FIRST) ───────────────────────────────────
//	Load active filter rules (cache/DB) and test req.DestinationURL. On a match:
//	insert a denied link (active=false, denied_reason=<code>), write a
//	link.denied audit entry (#0025), and return 422
//	{error:"url_denied", reason:<code>, label:<label>}. Only runs for the
//	GENERATED-key path; the PRD evaluates the filter before dedup so a blocked
//	URL is always re-evaluated.
//
//	── #0023 DEDUP CHECK (runs AFTER the filter, before insert) ──────────────
//	Only for the generated-key path (custom aliases are NOT deduplicated):
//	SELECT an existing non-denied link for (user_id, destination_url). If an
//	active one exists → return it with duplicate=true (no insert, no SSE). If an
//	inactive one exists → reactivate (active=true) and return it with
//	duplicate=true (+ SSE). Otherwise fall through to the insert below. This is
//	where the &duplicateFalse below becomes &true.
//
// After a successful create/reactivate (the seam-marked points below):
//
//	── #0025 AUDIT ── write a link.created entry attributed to u.ID with
//	   metadata {key, destination_url, title, duplicate}.
//	── #0026 SSE ──── broker.Publish(u.ID, Event{Name:"link.created", Payload:<link JSON>}).
func (h *LinksHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createLinkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	dest := strings.TrimSpace(req.DestinationURL)
	if !validDestinationURL(dest) {
		writeError(w, http.StatusBadRequest, "destination_url must be a valid absolute http(s) URL")
		return
	}

	// ───────────────────────────────────────────────────────────────────────
	// SEAM #0024 (URL filter check — runs FIRST): evaluate dest against the
	// active filter rules here, BEFORE dedup and BEFORE the insert. On a match,
	// insert a denied link, write link.denied audit (#0025), and return 422.
	// Not implemented in #0022.
	// ───────────────────────────────────────────────────────────────────────

	customKey := strings.TrimSpace(req.customKey())

	// Track whether this create is a dedup hit. #0023 flips this to true on an
	// existing/reactivated match; for #0022 it is always false.
	duplicate := false

	var key string
	if customKey != "" {
		// Custom aliases are NOT deduplicated (PRD) — attempt the insert with the
		// supplied key directly; a clash surfaces as ErrKeyTaken → 409 below.
		if !validKey(customKey) {
			writeError(w, http.StatusBadRequest, "custom key must be 1-12 url-safe characters")
			return
		}
		key = customKey
	} else {
		// ───────────────────────────────────────────────────────────────────
		// SEAM #0023 (dedup check — runs AFTER the filter, before insert): for
		// the generated-key path only, look up an existing non-denied link for
		// (u.ID, dest). Return it (active) or reactivate it (inactive) with
		// duplicate=true and skip the GenerateUniqueKey + insert below. Not
		// implemented in #0022.
		// ───────────────────────────────────────────────────────────────────

		generated, err := links.GenerateUniqueKey(func(candidate string) (bool, error) {
			return h.store.KeyExists(r.Context(), candidate)
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate a unique key")
			return
		}
		key = generated
	}

	created, err := h.store.CreateLink(r.Context(), links.NewLink{
		UserID:         u.ID,
		Key:            key,
		DestinationURL: dest,
		Title:          strings.TrimSpace(req.Title),
		ExpiresAt:      req.ExpiresAt,
	})
	switch {
	case err == nil:
		// success — fall through.
	case errors.Is(err, links.ErrKeyTaken):
		writeError(w, http.StatusConflict, "key already taken")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// ───────────────────────────────────────────────────────────────────────
	// SEAM #0025 (audit): write a link.created entry for u.ID with metadata
	// {key, destination_url, title, duplicate}. Not implemented in #0022.
	// SEAM #0026 (SSE): broker.Publish(u.ID, Event{Name:"link.created",
	// Payload:<created link JSON>}). Not implemented in #0022.
	// ───────────────────────────────────────────────────────────────────────
	_ = u // retained for the #0025/#0026 seams above.

	view := toLinkView(created)
	view.Duplicate = &duplicate
	writeJSON(w, http.StatusCreated, view)
}

// List handles GET /api/links. It returns the caller's links most-recent-first,
// paginated via ?page= (1-based, default 1) and ?per_page= (default 20, capped
// at maxPerPage). Each item includes its click_count. Scoped to the caller in
// the store.
func (h *LinksHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	page, perPage := parsePagination(r)
	offset := (page - 1) * perPage

	rows, err := h.store.ListLinks(r.Context(), u.ID, perPage, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	total, err := h.store.CountLinks(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	views := make([]linkView, 0, len(rows))
	for _, l := range rows {
		views = append(views, toLinkView(l))
	}
	writeJSON(w, http.StatusOK, listLinksResponse{
		Links:   views,
		Page:    page,
		PerPage: perPage,
		Total:   total,
	})
}

// Get handles GET /api/links/{key}. It returns the caller's link detail with its
// click stats. A key that does not exist OR belongs to another user yields 404 —
// the same response in both cases so the endpoint never reveals another user's
// link.
func (h *LinksHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	link, err := h.store.GetLink(r.Context(), u.ID, key)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toLinkView(link))
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// patchLinkRequest is the PATCH /api/links/{key} body. Every field is a pointer
// so the handler can distinguish "absent" (leave unchanged) from "present"
// (set). Only title, destination_url, and expires_at are updatable per #0022.
type patchLinkRequest struct {
	Title          *string    `json:"title"`
	DestinationURL *string    `json:"destination_url"`
	ExpiresAt      *time.Time `json:"expires_at"`
	// expiresAtPresent records whether the JSON contained the expires_at key at
	// all, so sending `"expires_at": null` clears it while omitting the field
	// leaves it unchanged. Populated by UnmarshalJSON.
	expiresAtPresent bool
}

// UnmarshalJSON decodes the patch body while recording whether expires_at was
// present (even if null), so the handler can tell "clear expiry" (key present,
// value null) from "leave expiry unchanged" (key absent).
func (p *patchLinkRequest) UnmarshalJSON(data []byte) error {
	// Probe which keys are present without losing the typed decode.
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	type alias patchLinkRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = patchLinkRequest(a)
	_, p.expiresAtPresent = probe["expires_at"]
	return nil
}

// Patch handles PATCH /api/links/{key}. It updates the provided subset of
// {title, destination_url, expires_at} on the caller's own link and returns the
// updated link. destination_url is validated when present. A key not owned by
// the caller yields 404. On success the redirect cache entry for the key is
// evicted (if a cache is wired) so the next redirect reflects the change.
func (h *LinksHandler) Patch(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	var req patchLinkRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	upd := links.LinkUpdate{}
	if req.Title != nil {
		t := strings.TrimSpace(*req.Title)
		upd.Title = &t
	}
	if req.DestinationURL != nil {
		dest := strings.TrimSpace(*req.DestinationURL)
		if !validDestinationURL(dest) {
			writeError(w, http.StatusBadRequest, "destination_url must be a valid absolute http(s) URL")
			return
		}
		upd.DestinationURL = &dest
	}
	if req.expiresAtPresent {
		// Present (possibly null): set expires_at to the given value (req.ExpiresAt
		// is nil when null, which clears the column).
		exp := req.ExpiresAt
		upd.ExpiresAt = &exp
	}

	link, err := h.store.UpdateLink(r.Context(), u.ID, key, upd)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Evict the redirect cache so a changed destination/expiry/active state is
	// observed immediately rather than after the cached entry's TTL. The PRD only
	// mandates eviction on DELETE, but a destination/expiry change must not be
	// served stale either; if no cache is wired the call is skipped.
	h.evict(key)

	writeJSON(w, http.StatusOK, toLinkView(link))
}

// Delete handles DELETE /api/links/{key}. Per the PRD, DELETE deactivates the
// link (soft delete: active=false) rather than removing the row, then evicts the
// key from the redirect cache so subsequent redirects 404. A key not owned by
// the caller yields 404.
//
// SEAM #0025 (audit): write a link.deactivated entry for u.ID with metadata
// {key, destination_url}. Not implemented in #0022.
func (h *LinksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	err := h.store.DeactivateLink(r.Context(), u.ID, key)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// PRD: explicit cache key deletion on DELETE /api/links/{key} so the next
	// redirect re-reads the (now inactive) DB row and 404s. Skipped when no cache
	// is wired.
	h.evict(key)

	// SEAM #0025 (audit): link.deactivated for u.ID with {key, destination_url}.
	_ = u

	writeJSON(w, http.StatusOK, map[string]string{"message": "Link deactivated"})
}

// evict drops the key from the redirect cache when one is wired. It is a no-op
// when h.cache is nil, so callers need not branch.
func (h *LinksHandler) evict(key string) {
	if h.cache != nil {
		h.cache.Delete(key)
	}
}

// parsePagination reads ?page= (1-based) and ?per_page= from the request,
// applying defaults and bounds. Invalid or out-of-range values fall back to the
// defaults rather than erroring, so a malformed pager query still returns the
// first page.
func parsePagination(r *http.Request) (page, perPage int) {
	page = 1
	perPage = defaultPerPage
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			perPage = n
		}
	}
	if perPage > maxPerPage {
		perPage = maxPerPage
	}
	return page, perPage
}

// validDestinationURL reports whether s is a syntactically valid absolute URL
// with an http or https scheme and a host. This is purely a syntactic check;
// URL filtering (#0024) is a separate, policy-driven step.
func validDestinationURL(s string) bool {
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

// validKey reports whether a user-supplied custom alias is acceptable: 1–12
// characters drawn from the same url-safe alphabet as generated keys plus a few
// conventional separators. The 12-char ceiling matches the links.key column
// (VARCHAR(12)).
func validKey(s string) bool {
	if len(s) == 0 || len(s) > 12 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}
