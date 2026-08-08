package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// campaignStore is the behavior the campaigns handler needs from the data
// layer. Depending on an interface (rather than the concrete
// *campaigns.Store) keeps the handler unit-testable with a fake and mirrors
// linkStore in links.go. Every method takes the authenticated user id so the
// store scopes each query to the caller's own rows — the handler never
// trusts a client-supplied owner.
//
// AssignLinkToCampaign/UnassignLinkFromCampaign (#0099) are Campaign-prefixed
// per #0098's downstream constraint: devstore.Store is a single Go type that
// must also satisfy filterRuleStore's bare Create/Update/Delete/Get, so a
// bare AssignLink/UnassignLink here would be structurally impossible for
// dev mode to implement.
type campaignStore interface {
	CreateCampaign(ctx context.Context, in campaigns.NewCampaign, auditor *audit.Logger, entry audit.Entry) (campaigns.Campaign, error)
	UpdateCampaign(ctx context.Context, userID int64, slug string, upd campaigns.CampaignUpdate, auditor *audit.Logger, entry audit.Entry) (campaigns.Campaign, error)
	DeleteCampaign(ctx context.Context, userID int64, slug string, auditor *audit.Logger, entry audit.Entry) error
	ListCampaignsForUser(ctx context.Context, userID int64) ([]campaigns.Campaign, error)
	GetCampaignBySlug(ctx context.Context, userID int64, slug string) (campaigns.Campaign, error)
	AssignLinkToCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
	UnassignLinkFromCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
}

// campaignLinksProvider is the slice of the links data layer the campaigns
// handler needs (#0099) for the three link-membership endpoints: resolving a
// client-supplied key to a link the CALLER owns (the ownership gate for
// assign — a key belonging to another user 404s exactly like a nonexistent
// one) and listing every link currently assigned to a campaign.
// *links.Store satisfies this via GetLink/ListLinksForCampaign.
type campaignLinksProvider interface {
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	ListLinksForCampaign(ctx context.Context, userID, campaignID int64) ([]links.Link, error)
}

// maxCampaignNameLength bounds the campaign name so it (and the slug derived
// from it, which is never longer) cannot grow large enough to overflow a
// PostgreSQL btree index entry (the UNIQUE(user_id, slug) index): an
// unbounded name produces a 500 ("index row size exceeds btree version 4
// maximum") instead of a clean validation error. Chosen generously relative
// to that ~2704-byte limit — 255 mirrors the conventional bound used
// elsewhere (e.g. links.key's VARCHAR(12), scaled up for a free-text name).
// Counted in RUNES (utf8.RuneCountInString), not bytes: len() would reject a
// 100-character CJK name (300 bytes) as "over 255 characters", which is both
// wrong and a confusing error for #0103's form. 255 runes is still at most
// ~1020 bytes (4 bytes/rune), far under the btree limit this bound protects.
const maxCampaignNameLength = 255

// CampaignsHandler serves the authenticated campaign CRUD + link-membership
// API:
//
//	GET    /api/campaigns              — list the caller's campaigns
//	POST   /api/campaigns              — create a campaign
//	GET    /api/campaigns/{slug}       — campaign metadata
//	PATCH  /api/campaigns/{slug}       — update name/description/dates/archived/defaults
//	DELETE /api/campaigns/{slug}       — delete a campaign
//	GET    /api/campaigns/{slug}/links — links in the campaign (#0099)
//	POST   /api/campaigns/{slug}/links — assign existing links by key (#0099)
//	DELETE /api/campaigns/{slug}/links/{key} — unassign one link (#0099)
//
// All eight routes MUST be mounted behind middleware.RequireSession; each
// handler reads the authenticated user from the request context and scopes
// every store call to that user, so a request can only see or mutate its own
// campaigns and links. This mirrors LinksHandler.
type CampaignsHandler struct {
	store campaignStore
	// links resolves/lists the links a campaign's membership endpoints
	// operate on (#0099). May be nil only in tests that never exercise those
	// three routes — List/AssignLinks/UnassignLink would panic on a nil
	// dereference otherwise, so every real wiring (main.go, campaignsMux)
	// must supply a non-nil value.
	links campaignLinksProvider
	// auditor records the campaign.created/updated/deleted/link_assigned/
	// link_unassigned audit entries in-band with the mutation
	// (audit.Logger.WriteTx, called from inside the store's transaction). May
	// be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
}

// NewCampaignsHandler constructs a CampaignsHandler over the data layer, the
// links lookup for the membership endpoints (#0099), and the audit logger.
// Pass a nil auditor to disable audit writes (e.g. in unit tests that do not
// exercise that path).
func NewCampaignsHandler(store campaignStore, linkLookup campaignLinksProvider, auditor *audit.Logger) *CampaignsHandler {
	return &CampaignsHandler{store: store, links: linkLookup, auditor: auditor}
}

// campaignView is the JSON shape for a single campaign, shared by every
// endpoint that returns one campaign.
type campaignView struct {
	ID                 int64      `json:"id"`
	Name               string     `json:"name"`
	Slug               string     `json:"slug"`
	Description        string     `json:"description"`
	StartsAt           *time.Time `json:"starts_at"`
	EndsAt             *time.Time `json:"ends_at"`
	Archived           bool       `json:"archived"`
	DefaultUTMSource   string     `json:"default_utm_source"`
	DefaultUTMMedium   string     `json:"default_utm_medium"`
	DefaultUTMCampaign string     `json:"default_utm_campaign"`
	DefaultUTMTerm     string     `json:"default_utm_term"`
	DefaultUTMContent  string     `json:"default_utm_content"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// toCampaignView maps a domain Campaign to its JSON shape.
func toCampaignView(c campaigns.Campaign) campaignView {
	return campaignView{
		ID:                 c.ID,
		Name:               c.Name,
		Slug:               c.Slug,
		Description:        c.Description,
		StartsAt:           c.StartsAt,
		EndsAt:             c.EndsAt,
		Archived:           c.Archived,
		DefaultUTMSource:   c.DefaultUTMSource,
		DefaultUTMMedium:   c.DefaultUTMMedium,
		DefaultUTMCampaign: c.DefaultUTMCampaign,
		DefaultUTMTerm:     c.DefaultUTMTerm,
		DefaultUTMContent:  c.DefaultUTMContent,
		CreatedAt:          c.CreatedAt,
		UpdatedAt:          c.UpdatedAt,
	}
}

// campaignListItemView is the shape of each entry in GET /api/campaigns.
// link_count and total_clicks are DELIBERATELY always 0 in this issue — #0099
// adds link membership and #0102 adds the rollup queries that would populate
// them. This is not a stub to be quietly filled in; it is the issue's
// explicit scope boundary.
type campaignListItemView struct {
	campaignView
	LinkCount   int64 `json:"link_count"`
	TotalClicks int64 `json:"total_clicks"`
}

// listCampaignsResponse is the GET /api/campaigns body. campaigns is always a
// non-nil array so it encodes as [] rather than null when empty.
type listCampaignsResponse struct {
	Campaigns []campaignListItemView `json:"campaigns"`
}

// List handles GET /api/campaigns. It returns the caller's campaigns, most
// recently created first, scoped to the caller in the store.
func (h *CampaignsHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	rows, err := h.store.ListCampaignsForUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	views := make([]campaignListItemView, 0, len(rows))
	for _, c := range rows {
		views = append(views, campaignListItemView{
			campaignView: toCampaignView(c),
			LinkCount:    0,
			TotalClicks:  0,
		})
	}
	writeJSON(w, http.StatusOK, listCampaignsResponse{Campaigns: views})
}

// createCampaignRequest is the POST /api/campaigns body. Only name is
// required; the slug is always server-generated from it (see
// campaigns.Store.CreateCampaign) and is never accepted from the client.
type createCampaignRequest struct {
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	StartsAt           *time.Time `json:"starts_at"`
	EndsAt             *time.Time `json:"ends_at"`
	DefaultUTMSource   string     `json:"default_utm_source"`
	DefaultUTMMedium   string     `json:"default_utm_medium"`
	DefaultUTMCampaign string     `json:"default_utm_campaign"`
	DefaultUTMTerm     string     `json:"default_utm_term"`
	DefaultUTMContent  string     `json:"default_utm_content"`
}

// Create handles POST /api/campaigns. It validates the name, resolves a
// unique per-user slug from it, inserts the campaign, and returns 201 with
// the full campaign object.
//
// #0025-style audit: a campaign.created entry attributed to u.ID is written
// by the store INSIDE the same transaction as the insert (audit.WriteTx),
// unlike links.created which is a fire-and-forget Record after commit — see
// campaigns.Store.CreateCampaign's doc comment for why this issue requires the
// stricter in-band write.
func (h *CampaignsHandler) Create(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	var req createCampaignRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(name) > maxCampaignNameLength {
		writeError(w, http.StatusBadRequest, "name must be at most 255 characters")
		return
	}
	if req.StartsAt != nil && req.EndsAt != nil && req.EndsAt.Before(*req.StartsAt) {
		writeError(w, http.StatusBadRequest, "ends_at must not be before starts_at")
		return
	}

	in := campaigns.NewCampaign{
		UserID:             u.ID,
		Name:               name,
		Description:        strings.TrimSpace(req.Description),
		StartsAt:           req.StartsAt,
		EndsAt:             req.EndsAt,
		DefaultUTMSource:   strings.TrimSpace(req.DefaultUTMSource),
		DefaultUTMMedium:   strings.TrimSpace(req.DefaultUTMMedium),
		DefaultUTMCampaign: strings.TrimSpace(req.DefaultUTMCampaign),
		DefaultUTMTerm:     strings.TrimSpace(req.DefaultUTMTerm),
		DefaultUTMContent:  strings.TrimSpace(req.DefaultUTMContent),
	}

	actor := u.ID
	entry := audit.Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     audit.ActionCampaignCreated,
		TargetType: audit.TargetCampaign,
		Metadata:   map[string]any{"name": name},
		IP:         clientIP(r),
	}

	created, err := h.store.CreateCampaign(r.Context(), in, h.auditor, entry)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, toCampaignView(created))
}

// Get handles GET /api/campaigns/{slug}. It returns the caller's campaign
// metadata. A slug that does not exist OR belongs to another user yields 404
// — the same response in both cases so the endpoint never reveals another
// user's campaign.
func (h *CampaignsHandler) Get(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	c, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toCampaignView(c))
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// patchCampaignRequest is the PATCH /api/campaigns/{slug} body. Every field
// is a pointer so the handler can distinguish "absent" (leave unchanged) from
// "present" (set), mirroring patchLinkRequest. starts_at/ends_at additionally
// track whether the key was present at all (even if null), the same trick
// patchLinkRequest uses for expires_at, so `"starts_at": null` clears the
// date while omitting the field leaves it unchanged. The slug itself is not
// patchable — it is fixed at creation.
type patchCampaignRequest struct {
	Name               *string    `json:"name"`
	Description        *string    `json:"description"`
	StartsAt           *time.Time `json:"starts_at"`
	EndsAt             *time.Time `json:"ends_at"`
	Archived           *bool      `json:"archived"`
	DefaultUTMSource   *string    `json:"default_utm_source"`
	DefaultUTMMedium   *string    `json:"default_utm_medium"`
	DefaultUTMCampaign *string    `json:"default_utm_campaign"`
	DefaultUTMTerm     *string    `json:"default_utm_term"`
	DefaultUTMContent  *string    `json:"default_utm_content"`

	startsAtPresent bool
	endsAtPresent   bool
}

// UnmarshalJSON decodes the patch body while recording whether starts_at/
// ends_at were present (even if null), mirroring patchLinkRequest's handling
// of expires_at.
func (p *patchCampaignRequest) UnmarshalJSON(data []byte) error {
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	type alias patchCampaignRequest
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = patchCampaignRequest(a)
	_, p.startsAtPresent = probe["starts_at"]
	_, p.endsAtPresent = probe["ends_at"]
	return nil
}

// Patch handles PATCH /api/campaigns/{slug}. It updates the provided subset
// of {name, description, starts_at, ends_at, archived, default_utm_*} on the
// caller's own campaign and returns the updated campaign. A slug not owned by
// the caller yields 404.
//
// The campaign.updated audit entry is written by the store inside the same
// transaction as the field update (audit.WriteTx via Store.UpdateCampaign),
// so a committed change always carries its audit row.
func (h *CampaignsHandler) Patch(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	var req patchCampaignRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Read the current row first so a starts_at/ends_at check can be evaluated
	// against the EFFECTIVE resulting window, not just whichever of the two
	// dates happens to be present in this particular PATCH body (#0102 uses
	// starts_at/ends_at as the default chart window; a PATCH that only moves
	// one of the two past the other, with the other left unchanged, would
	// otherwise silently produce an inverted, always-empty window). A miss here
	// (not found / not owned) is reported the same way UpdateCampaign itself
	// would report it, so this adds no new information a non-owner could use.
	current, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	if err != nil {
		if errors.Is(err, campaigns.ErrCampaignNotFound) {
			writeError(w, http.StatusNotFound, "campaign not found")
		} else {
			writeError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	upd := campaigns.CampaignUpdate{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name must not be empty")
			return
		}
		if utf8.RuneCountInString(name) > maxCampaignNameLength {
			writeError(w, http.StatusBadRequest, "name must be at most 255 characters")
			return
		}
		upd.Name = &name
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		upd.Description = &d
	}

	effectiveStartsAt := current.StartsAt
	if req.startsAtPresent {
		effectiveStartsAt = req.StartsAt
		upd.StartsAt = &req.StartsAt
	}
	effectiveEndsAt := current.EndsAt
	if req.endsAtPresent {
		effectiveEndsAt = req.EndsAt
		upd.EndsAt = &req.EndsAt
	}
	if effectiveStartsAt != nil && effectiveEndsAt != nil && effectiveEndsAt.Before(*effectiveStartsAt) {
		writeError(w, http.StatusBadRequest, "ends_at must not be before starts_at")
		return
	}

	if req.Archived != nil {
		upd.Archived = req.Archived
	}
	if req.DefaultUTMSource != nil {
		v := strings.TrimSpace(*req.DefaultUTMSource)
		upd.DefaultUTMSource = &v
	}
	if req.DefaultUTMMedium != nil {
		v := strings.TrimSpace(*req.DefaultUTMMedium)
		upd.DefaultUTMMedium = &v
	}
	if req.DefaultUTMCampaign != nil {
		v := strings.TrimSpace(*req.DefaultUTMCampaign)
		upd.DefaultUTMCampaign = &v
	}
	if req.DefaultUTMTerm != nil {
		v := strings.TrimSpace(*req.DefaultUTMTerm)
		upd.DefaultUTMTerm = &v
	}
	if req.DefaultUTMContent != nil {
		v := strings.TrimSpace(*req.DefaultUTMContent)
		upd.DefaultUTMContent = &v
	}

	actor := u.ID
	entry := audit.Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     audit.ActionCampaignUpdated,
		TargetType: audit.TargetCampaign,
		Metadata:   map[string]any{"slug": slug},
		IP:         clientIP(r),
	}

	updated, err := h.store.UpdateCampaign(r.Context(), u.ID, slug, upd, h.auditor, entry)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, toCampaignView(updated))
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// Delete handles DELETE /api/campaigns/{slug}. Unlike links (soft delete),
// this permanently removes the row — see campaigns.Store.DeleteCampaign's doc
// comment. It does not cascade to anything in this issue's scope. A slug not
// owned by the caller yields 404.
//
// The campaign.deleted audit entry is written by the store inside the same
// transaction as the delete.
func (h *CampaignsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	// Read the campaign (scoped to the caller) BEFORE deleting so the audit
	// metadata carries its name. A miss here only weakens the audit entry; the
	// delete below still runs and returns the right status.
	existing, existingErr := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)

	actor := u.ID
	meta := map[string]any{"slug": slug}
	if existingErr == nil {
		meta["name"] = existing.Name
	}
	entry := audit.Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     audit.ActionCampaignDeleted,
		TargetType: audit.TargetCampaign,
		Metadata:   meta,
		IP:         clientIP(r),
	}

	err := h.store.DeleteCampaign(r.Context(), u.ID, slug, h.auditor, entry)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"message": "Campaign deleted"})
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// campaignLinksResponse is the GET/POST /api/campaigns/{slug}/links body:
// links is always a non-nil array so it encodes as [] rather than null when
// the campaign has no links.
type campaignLinksResponse struct {
	Links []linkView `json:"links"`
}

// ListLinks handles GET /api/campaigns/{slug}/links. It returns every link
// currently assigned to the caller's own campaign, most-recently-created
// first. A slug that does not exist OR belongs to another user yields 404 —
// the same indistinguishable-404 contract every other campaign endpoint uses.
func (h *CampaignsHandler) ListLinks(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	c, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	rows, err := h.links.ListLinksForCampaign(r.Context(), u.ID, c.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	views := make([]linkView, 0, len(rows))
	for _, l := range rows {
		views = append(views, toLinkView(l))
	}
	writeJSON(w, http.StatusOK, campaignLinksResponse{Links: views})
}

// assignLinksRequest is the POST /api/campaigns/{slug}/links body. keys is
// the canonical field (plural, since assigning several existing links to a
// campaign at once is the common "tag my whole promotion" workflow); key is
// accepted as a convenience singular alias for assigning exactly one.
type assignLinksRequest struct {
	Keys []string `json:"keys"`
	Key  string   `json:"key"`
}

// maxAssignLinksKeys caps how many keys a single POST
// /api/campaigns/{slug}/links request may name (review item 8). Each key
// costs two round trips to the DB (GetLink, then the assign UPDATE/audit
// insert) plus a re-read, all sequential and non-atomic across keys — see
// AssignLinks' doc comment. Without a cap, an N-key request is 2N+1 queries
// with no upper bound, and a client-supplied array size drives it directly.
// 50 is generous for the "tag a batch of existing links" workflow this
// endpoint exists for while keeping a single request's cost bounded.
const maxAssignLinksKeys = 50

// requestedKeys returns the de-duplicated, non-empty set of keys the request
// named, merging the singular key alias into keys.
func (r assignLinksRequest) requestedKeys() []string {
	seen := make(map[string]bool, len(r.Keys)+1)
	out := make([]string, 0, len(r.Keys)+1)
	add := func(k string) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			return
		}
		seen[k] = true
		out = append(out, k)
	}
	add(r.Key)
	for _, k := range r.Keys {
		add(k)
	}
	return out
}

// AssignLinks handles POST /api/campaigns/{slug}/links: assigns one or more
// of the caller's OWN existing links (by key) to the caller's own campaign.
//
// Ownership is enforced twice, independently, before anything is written:
// GetCampaignBySlug resolves {slug} only if it belongs to the caller (404
// otherwise — user A can never assign into user B's campaign), and each key
// is resolved via links.Store.GetLink, ALSO scoped to the caller (404
// otherwise — user A can never assign user B's link, even into A's own
// campaign, by guessing B's key).
//
// DECISION (acceptance criterion): assigning an already-assigned link MOVES
// it rather than being rejected — a link belongs to at most one campaign,
// enforced by the links.campaign_id column, so "assign" always just
// overwrites whatever was there. See
// TestCampaignsAssignLinks_MovesAlreadyAssignedLink.
//
// A request naming multiple keys is NOT atomic across keys: each is resolved
// and assigned independently, in the order given, and the first key that
// fails ownership (404) stops the request without rolling back any keys
// already assigned in this same call — the response only ever reports 200 on
// full success, so a partial failure is visible as a 404 with none of the
// later keys applied, but earlier ones in the same request remain assigned.
func (h *CampaignsHandler) AssignLinks(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	var req assignLinksRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	keys := req.requestedKeys()
	if len(keys) == 0 {
		writeError(w, http.StatusBadRequest, "at least one key is required")
		return
	}
	if len(keys) > maxAssignLinksKeys {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d keys per request", maxAssignLinksKeys))
		return
	}

	c, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	assigned := make([]links.Link, 0, len(keys))
	for _, key := range keys {
		link, err := h.links.GetLink(r.Context(), u.ID, key)
		switch {
		case err == nil:
			// fall through.
		case errors.Is(err, links.ErrLinkNotFound):
			writeError(w, http.StatusNotFound, "link not found: "+key)
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		actor := u.ID
		linkID := link.ID
		entry := audit.Entry{
			ActorID:    &actor,
			UserID:     &actor,
			Action:     audit.ActionCampaignLinkAssigned,
			TargetType: audit.TargetLink,
			TargetID:   &linkID,
			Metadata:   map[string]any{"key": link.Key, "campaign_slug": c.Slug, "campaign_id": c.ID},
			IP:         clientIP(r),
		}
		if err := h.store.AssignLinkToCampaign(r.Context(), u.ID, c.ID, link.ID, h.auditor, entry); err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		// Re-read so the response reflects the post-assignment state
		// (campaign_id/campaign_name/campaign_slug now set).
		updated, err := h.links.GetLink(r.Context(), u.ID, key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		assigned = append(assigned, updated)
	}

	views := make([]linkView, 0, len(assigned))
	for _, l := range assigned {
		views = append(views, toLinkView(l))
	}
	writeJSON(w, http.StatusOK, campaignLinksResponse{Links: views})
}

// UnassignLink handles DELETE /api/campaigns/{slug}/links/{key}: clears the
// caller's own link's campaign_id, but only when the link is currently
// assigned to THIS campaign. Ownership is enforced the same way as
// AssignLinks (both the campaign and the link must belong to the caller); a
// link that exists, belongs to the caller, but is assigned to a DIFFERENT
// campaign (or none) also 404s via UnassignLinkFromCampaign's ErrLinkNotFound
// — "unassign from a campaign you're not even in" is not a meaningful
// success.
func (h *CampaignsHandler) UnassignLink(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	key := r.PathValue("key")
	if slug == "" || key == "" {
		writeError(w, http.StatusBadRequest, "slug and key are required")
		return
	}

	c, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	link, err := h.links.GetLink(r.Context(), u.ID, key)
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

	actor := u.ID
	linkID := link.ID
	entry := audit.Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     audit.ActionCampaignLinkUnassigned,
		TargetType: audit.TargetLink,
		TargetID:   &linkID,
		Metadata:   map[string]any{"key": link.Key, "campaign_slug": c.Slug, "campaign_id": c.ID},
		IP:         clientIP(r),
	}

	err = h.store.UnassignLinkFromCampaign(r.Context(), u.ID, c.ID, link.ID, h.auditor, entry)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"message": "Link unassigned"})
	case errors.Is(err, campaigns.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link is not assigned to this campaign")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}
