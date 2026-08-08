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
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/filters"
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
	// ListCampaignsForUser/GetCampaignBySlugWithCounts (#0102) return each
	// campaign paired with its link_count/total_clicks summary — the fields
	// #0098 stubbed as 0. GetCampaignBySlug (bare) is kept for the internal
	// callers (Patch/Delete/AssignLinks/UnassignLink) that only need
	// metadata and would otherwise pay for the two extra correlated
	// subqueries on every mutation.
	ListCampaignsForUser(ctx context.Context, userID int64) ([]campaigns.CampaignWithCounts, error)
	GetCampaignBySlug(ctx context.Context, userID int64, slug string) (campaigns.Campaign, error)
	GetCampaignBySlugWithCounts(ctx context.Context, userID int64, slug string) (campaigns.CampaignWithCounts, error)
	AssignLinkToCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
	UnassignLinkFromCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
}

// campaignStatsProvider is the slice of the click-analytics store the
// campaigns handler needs (#0102): the campaign-scoped rollups backing GET
// /api/campaigns/{slug}/stats and the stats/timeseries fields folded
// additively into GET /api/campaigns/{slug}. *clicks.StatsStore satisfies
// this. It is optional — a nil provider (no stats store wired) omits the
// stats/timeseries/by_link/series_by_link fields from campaign detail and
// causes GET /api/campaigns/{slug}/stats to report 500, mirroring
// statsProvider's nil-degradation contract in links.go.
//
// Deliberately CampaignSummary/CampaignRollup, not the four individual
// clicks.StatsStore methods (CampaignStats/CampaignClicksOverTime/
// CampaignClicksByLink/CampaignSeriesByLink) those two are built from. A
// prior version of this handler called the four methods directly, once
// each, and combined their results into one response — which meant each
// piece was read against its own snapshot (its own transaction or none at
// all). Under concurrent recording that let `timeseries.days` sum to a
// different number than `stats.click_count` in the SAME JSON body: #0101's
// "Total clicks: 5" over "No click data yet" defect, reproduced one level
// up, on exactly the pair #0104 renders side by side. Depending on
// CampaignSummary/CampaignRollup instead makes that structurally
// impossible from the handler's side: the whole payload comes back from
// one call that already read everything from one transaction (see
// clicks.CampaignRollup's doc comment).
type campaignStatsProvider interface {
	CampaignSummary(ctx context.Context, campaignID int64, from, to time.Time) (clicks.CampaignSummary, error)
	CampaignRollup(ctx context.Context, campaignID int64, from, to time.Time) (clicks.CampaignRollup, error)
}

// campaignLinksProvider is the slice of the links data layer the campaigns
// handler needs (#0099/#0105) for the link-membership + batch-create
// endpoints: resolving a client-supplied key to a link the CALLER owns (the
// ownership gate for assign — a key belonging to another user 404s exactly
// like a nonexistent one), listing every link currently assigned to a
// campaign, and inserting N new links in one transaction (#0105's batch
// create, which deliberately bypasses CreateOrReactivateLink's dedup — see
// links.Store.CreateLinksBatch's doc comment for why). *links.Store satisfies
// this via GetLink/ListLinksForCampaign/CreateLinksBatch.
type campaignLinksProvider interface {
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	ListLinksForCampaign(ctx context.Context, userID, campaignID int64) ([]links.Link, error)
	CreateLinksBatch(ctx context.Context, rows []links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) ([]links.Link, error)
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
//	POST   /api/campaigns/{slug}/links/batch — create N new links at once (#0105)
//
// All nine routes MUST be mounted behind middleware.RequireSession; each
// handler reads the authenticated user from the request context and scopes
// every store call to that user, so a request can only see or mutate its own
// campaigns and links. This mirrors LinksHandler.
type CampaignsHandler struct {
	store campaignStore
	// links resolves/lists the links a campaign's membership endpoints
	// operate on (#0099), and inserts the links a batch-create request
	// generates (#0105). May be nil only in tests that never exercise those
	// four routes — List/AssignLinks/UnassignLink/BatchCreateLinks would
	// panic on a nil dereference otherwise, so every real wiring (main.go,
	// campaignsMux) must supply a non-nil value.
	links campaignLinksProvider
	// auditor records the campaign.created/updated/deleted/link_assigned/
	// link_unassigned audit entries in-band with the mutation
	// (audit.Logger.WriteTx, called from inside the store's transaction), and
	// (#0105) a link.created entry per batch-created link, fire-and-forget
	// after the batch commits — the same convention LinksHandler.Create
	// already uses for single-link creates, since a batch-created link is
	// otherwise indistinguishable from one created singly with campaign_id
	// set. May be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
	// stats provides the campaign-scoped click rollups (#0102) enriching GET
	// /api/campaigns/{slug} and backing GET /api/campaigns/{slug}/stats. May
	// be nil (no stats store wired), in which case the detail response omits
	// those fields and the dedicated stats endpoint reports 500 — mirroring
	// LinksHandler's nil-provider degradation.
	stats campaignStatsProvider
	// rules is the active-URL-filter-rule provider (#0024) BatchCreateLinks
	// consults before inserting anything (#0105) — the same optional
	// dependency LinksHandler.Create takes. May be nil (no filter cache
	// wired), in which case the filter check is skipped entirely, matching
	// LinksHandler's degradation.
	rules ruleProvider
}

// NewCampaignsHandler constructs a CampaignsHandler over the data layer, the
// links lookup for the membership + batch-create endpoints (#0099/#0105),
// the audit logger, the campaign-scoped stats provider (#0102), and the
// active-URL-filter-rule provider BatchCreateLinks consults (#0105). Pass a
// nil auditor to disable audit writes, a nil statsProvider to omit the
// stats/timeseries fields, and a nil ruleProvider to disable the batch-create
// filter check (e.g. in unit tests that do not exercise those paths).
func NewCampaignsHandler(store campaignStore, linkLookup campaignLinksProvider, auditor *audit.Logger, statsProvider campaignStatsProvider, rules ruleProvider) *CampaignsHandler {
	return &CampaignsHandler{store: store, links: linkLookup, auditor: auditor, stats: statsProvider, rules: rules}
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
// link_count/total_clicks (#0102) are real aggregates now — #0098 stubbed
// them as 0 pending link membership (#0099) and the rollup queries
// (#0102) this issue adds.
type campaignListItemView struct {
	campaignView
	LinkCount   int64 `json:"link_count"`
	TotalClicks int64 `json:"total_clicks"`
}

// toCampaignListItemView maps a domain CampaignWithCounts to its JSON shape.
func toCampaignListItemView(c campaigns.CampaignWithCounts) campaignListItemView {
	return campaignListItemView{
		campaignView: toCampaignView(c.Campaign),
		LinkCount:    c.LinkCount,
		TotalClicks:  c.TotalClicks,
	}
}

// listCampaignsResponse is the GET /api/campaigns body. campaigns is always a
// non-nil array so it encodes as [] rather than null when empty.
type listCampaignsResponse struct {
	Campaigns []campaignListItemView `json:"campaigns"`
}

// List handles GET /api/campaigns. It returns the caller's campaigns, most
// recently created first, scoped to the caller in the store, each carrying
// its real link_count/total_clicks (#0102). A campaign with no links, or
// whose every click is bot-flagged, still appears with 0/0 — the store's
// correlated-subquery counts have no JOIN/GROUP BY to collapse a row out of
// (see campaigns.Store's campaignCountColumns doc comment).
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
		views = append(views, toCampaignListItemView(c))
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

// campaignDetailView is the GET /api/campaigns/{slug} body: campaign
// metadata, its link_count/total_clicks (#0102), every link currently
// assigned to it, and (when a stats provider is wired) its stats +
// clicks-over-time series — additively extending campaignView (#0098's
// downstream constraint 8: "GET /api/campaigns/{slug} should extend with a
// campaignDetailView wrapper mirroring linkDetailView, which is purely
// additive") rather than changing campaignView itself, and mirroring how GET
// /api/links/{key} already combines utm_stats + timeseries via
// linkDetailView. Links is always a non-nil array so it encodes as []
// rather than null; Stats/Timeseries are omitted (nil, via omitempty) only
// when no stats provider is wired, matching statsProvider's nil-degradation
// contract in links.go — this is safe specifically because Stats/Timeseries
// are POINTERS, so omitempty only triggers on nil, never on an empty-but-set
// struct (unlike a slice, where omitempty would also hide a genuine empty
// result).
//
// LINK_COUNT/TOTAL_CLICKS ARE ALL-TIME; STATS.CLICK_COUNT IS WINDOWED — this
// is deliberate, not an oversight, but it means the two can legitimately
// disagree in the same response (e.g. a campaign with 5 clicks from 60 days
// ago and no starts_at/ends_at set: total_clicks=5, stats.click_count=0,
// timeseries.days=[], because the default window only looks back 30 days).
// total_clicks stays all-time because that is the right number for a list/
// summary figure — the same reason campaignListItemView (GET
// /api/campaigns) reports it all-time with no window at all, and the two
// endpoints would disagree with EACH OTHER if this one windowed it instead.
// stats.click_count stays windowed because it is the number the chart next
// to it was computed from, and the two must never drift apart from each
// other (see CampaignRollup's doc comment) even though they are allowed to
// drift from total_clicks. #0103/#0104 must not assume total_clicks and
// stats.click_count are interchangeable; see
// TestCampaignsGet_TotalClicksIsAllTimeStatsClickCountIsWindowed, which pins
// exactly the scenario above.
type campaignDetailView struct {
	campaignView
	LinkCount   int64                    `json:"link_count"`
	TotalClicks int64                    `json:"total_clicks"`
	Links       []linkView               `json:"links"`
	Stats       *clicks.CampaignStats    `json:"stats,omitempty"`
	Timeseries  *clicks.TimeseriesResult `json:"timeseries,omitempty"`
}

// Get handles GET /api/campaigns/{slug}. It returns the caller's campaign
// metadata, its real (all-time) link_count/total_clicks (#0102), every link
// currently assigned to it, and — when a stats provider is wired — its
// CampaignStats and clicks-over-time series over the DEFAULT window
// (campaignWindow in clicks/stats.go: the campaign's own starts_at/ends_at
// when both are set, otherwise 30 days) — see campaignDetailView's doc
// comment for why that windowed click_count can legitimately differ from
// the all-time total_clicks above it. Use GET /api/campaigns/{slug}/stats
// directly for an explicit ?from=/?to= window. A slug that does not exist OR
// belongs to another user yields 404 — the same response in both cases so
// the endpoint never reveals another user's campaign.
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

	c, err := h.store.GetCampaignBySlugWithCounts(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		// fall through to build the detail view.
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	linkRows, err := h.links.ListLinksForCampaign(r.Context(), u.ID, c.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	linkViews := make([]linkView, 0, len(linkRows))
	for _, l := range linkRows {
		linkViews = append(linkViews, toLinkView(l))
	}

	detail := campaignDetailView{
		campaignView: toCampaignView(c.Campaign),
		LinkCount:    c.LinkCount,
		TotalClicks:  c.TotalClicks,
		Links:        linkViews,
	}

	// #0102: enrich with campaign stats and the clicks-over-time series when
	// a stats store is wired. c.ID was resolved scoped to the caller above,
	// so passing it here cannot leak another user's data. CampaignSummary
	// reads both from ONE transaction (see its doc comment) so click_count
	// and the timeseries it sits beside can never disagree with EACH OTHER
	// (they may still legitimately disagree with the all-time total_clicks
	// above — see campaignDetailView's doc comment). Stats failures are
	// fatal to the detail response so the client never sees a partial or
	// inconsistent analytics payload (mirrors LinksHandler.Get).
	if h.stats != nil {
		summary, err := h.stats.CampaignSummary(r.Context(), c.ID, zeroTime, zeroTime)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		detail.Stats = &summary.Stats
		detail.Timeseries = &summary.Timeseries
	}

	writeJSON(w, http.StatusOK, detail)
}

// campaignStatsResponse is the GET /api/campaigns/{slug}/stats body:
// CampaignStats's totals + the four channel breakdowns (embedded, so
// click_count/excluded_bot_count/by_source/by_medium/by_content/by_referer
// sit at the top level), plus the clicks-over-time series, the per-link
// breakdown, and the capped-plus-"Other" per-link series — the plan
// document's three questions (how many/over time, which channel, which
// link) answered in one request, ALL read from the same
// clicks.CampaignRollup call and therefore the same transaction/snapshot —
// see CampaignRollup's doc comment for why that matters.
type campaignStatsResponse struct {
	clicks.CampaignStats
	Timeseries   clicks.TimeseriesResult `json:"timeseries"`
	ByLink       []clicks.LinkBucket     `json:"by_link"`
	SeriesByLink []clicks.LinkSeries     `json:"series_by_link"`
}

// parseStatsWindow parses the optional ?from=/?to= query parameters GET
// /api/campaigns/{slug}/stats accepts, each a "YYYY-MM-DD" date matching
// stats.go's DayBucket date format, interpreted as UTC midnight. Either or
// both may be absent (zero time.Time), letting the store apply its own
// default window (clicks.campaignWindow's doc comment: the campaign's own
// starts_at/ends_at when both are set, otherwise 30 days). An unparseable
// value, or a `to` before `from` (both present), writes a 400 directly and
// returns ok=false, so the caller can just check ok and return.
func parseStatsWindow(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from date, want YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to date, want YYYY-MM-DD")
			return time.Time{}, time.Time{}, false
		}
		to = t
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		writeError(w, http.StatusBadRequest, "to must not be before from")
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// Stats handles GET /api/campaigns/{slug}/stats. It returns the caller's
// campaign's full rollup — CampaignStats, the clicks-over-time series, the
// per-link breakdown, and the capped-plus-"Other" per-link series — over an
// optional ?from=/?to= window (each "YYYY-MM-DD"; see parseStatsWindow).
// Omitting both applies the default window. The parsed from/to are passed
// straight through to CampaignRollup, which resolves the effective window
// and reads every quarter of the payload from ONE transaction (see its doc
// comment) — TestCampaignsStats_ExplicitWindowReachesStore pins that the
// parsed values actually reach the store rather than being silently
// discarded in favor of the default. A slug that does not exist OR belongs
// to another user yields 404, matching every other campaign endpoint's
// indistinguishable-404 contract. Returns 500 if no stats provider is wired
// (this endpoint has no meaningful degraded response).
func (h *CampaignsHandler) Stats(w http.ResponseWriter, r *http.Request) {
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

	if h.stats == nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	from, to, ok := parseStatsWindow(w, r)
	if !ok {
		return // parseStatsWindow already wrote the 400.
	}

	rollup, err := h.stats.CampaignRollup(r.Context(), c.ID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, campaignStatsResponse{
		CampaignStats: rollup.Stats,
		Timeseries:    rollup.Timeseries,
		ByLink:        rollup.ByLink,
		SeriesByLink:  rollup.SeriesByLink,
	})
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

// ── Batch create (#0105) ────────────────────────────────────────────────
//
// One destination URL, then a row per channel (source, medium, content,
// placement, optional title) — every non-blank row becomes its own short
// link, assigned to this campaign, in ONE server call. See
// links.Store.CreateLinksBatch's doc comment for the central decision this
// issue exists to make: dedup is bypassed ENTIRELY for this path, because
// #0099's forward-merge does not (and cannot) fix the trap where two rows
// differing only in placement compose to a byte-identical destination_url —
// see CreateOrReactivateLink's and CreateLinksBatch's doc comments for the
// full history.

// maxBatchCreateRows caps how many rows a single POST
// /api/campaigns/{slug}/links/batch request may submit, mirroring
// maxAssignLinksKeys' rationale: each row costs one key-generation exists
// check plus one INSERT inside the batch transaction, all sequential.
// Generous for the "channels in one promotion" workflow (the issue's own
// worked example tops out at eight rows) while keeping one request's cost
// and the transaction's duration bounded.
const maxBatchCreateRows = 50

// batchCreateLinkRow is one row of the POST /api/campaigns/{slug}/links/batch
// body: the per-channel fields that vary row to row. Deliberately narrower
// than createLinkRequest — no key/custom_key (batch rows always use
// generated keys; a custom alias would need to be unique across the whole
// batch AND the table, which is out of this issue's scope), no campaign_id/
// campaign_slug (the campaign is the URL's {slug}, not per-row), and no
// expires_at (not part of the issue's deliverable list). destination_url is
// the row's FULLY COMPOSED URL — the client bakes each row's UTM values into
// it via the SAME composeUtmUrl helper single-create uses (issue note:
// "resist writing a second composition path"), so this handler does not
// recompose it server-side; it only validates that what arrived is a valid
// absolute URL, exactly like createLinkRequest's destination_url.
type batchCreateLinkRow struct {
	DestinationURL string `json:"destination_url"`
	Title          string `json:"title"`
	UTMSource      string `json:"utm_source"`
	UTMMedium      string `json:"utm_medium"`
	UTMCampaign    string `json:"utm_campaign"`
	UTMTerm        string `json:"utm_term"`
	UTMContent     string `json:"utm_content"`
	Placement      string `json:"placement"`
}

// trimmed returns a copy of r with every field trimmed — the shape every
// validation/insertion step below actually operates on, computed once per
// row rather than re-trimming at each call site.
func (r batchCreateLinkRow) trimmed() batchCreateLinkRow {
	return batchCreateLinkRow{
		DestinationURL: strings.TrimSpace(r.DestinationURL),
		Title:          strings.TrimSpace(r.Title),
		UTMSource:      strings.TrimSpace(r.UTMSource),
		UTMMedium:      strings.TrimSpace(r.UTMMedium),
		UTMCampaign:    strings.TrimSpace(r.UTMCampaign),
		UTMTerm:        strings.TrimSpace(r.UTMTerm),
		UTMContent:     strings.TrimSpace(r.UTMContent),
		Placement:      strings.TrimSpace(r.Placement),
	}
}

// isBlank reports whether a row was added but left entirely empty — every
// field EXCEPT destination_url/utm_campaign/utm_term is blank. destination_url
// is excluded because the client always sends the shared base URL on every
// row regardless of whether the row itself was touched (composing an
// all-blank row's params onto the base returns the base unchanged — see
// composeUtmUrl), so it cannot distinguish a filled-in row from an empty one.
// utm_campaign/utm_term are excluded for the same reason: the batch form
// shares ONE campaign/term pair across every row (issue deliverable list:
// only source, medium, content, placement, and title vary per row), usually
// prefilled from the campaign's own default_utm_campaign/default_utm_term,
// so their mere non-blank presence must not by itself make an otherwise
// completely untouched row count as "filled in". Per the issue's acceptance
// criterion ("a row left blank is skipped rather than creating a link with
// empty UTM values"), a blank row is dropped before validation/insertion —
// it is not an error.
func (r batchCreateLinkRow) isBlank() bool {
	return r.UTMSource == "" && r.UTMMedium == "" && r.UTMContent == "" &&
		r.Placement == "" && r.Title == ""
}

// duplicateKey is the tuple this handler treats as "the same row" for the
// in-batch duplicate check below: (source, medium, content, placement).
//
// #0105 DECISION: the issue's acceptance-criterion shorthand says duplicate
// rows are "same source+medium+content" — but that definition, applied
// literally, would reject the exact "two rows differing only in placement"
// case this issue exists to make work (see links.Store.CreateLinksBatch's
// doc comment). placement is deliberately ADDED to the tuple here so the two
// concerns stay separable: rows that differ ONLY in placement are legitimate
// (two poster locations for the same channel) and must both be created;
// rows identical even in placement are a genuine accidental double-entry
// (the same channel typed twice) and are rejected. Comparison is exact and
// case-sensitive — this handler does not normalize casing (see
// docs/utm.md's "Canonical casing" section); silently folding case here
// would let two rows with different casing collide even though
// composeUtmUrl would bake them as different values, which is a worse
// surprise than leaving the check exact.
func (r batchCreateLinkRow) duplicateKey() string {
	return r.UTMSource + "\x00" + r.UTMMedium + "\x00" + r.UTMContent + "\x00" + r.Placement
}

// batchCreateLinksRequest is the POST /api/campaigns/{slug}/links/batch body.
type batchCreateLinksRequest struct {
	Rows []batchCreateLinkRow `json:"rows"`
}

// indexedBatchRow pairs a trimmed, non-blank batchCreateLinkRow with
// origIndex — its 1-based position in the ORIGINALLY SUBMITTED req.Rows,
// before blank rows were filtered out. BatchCreateLinks uses origIndex
// (never the row's position in the filtered slice) in every "row N" error
// message, so the number reported always matches the row the client-side
// form labeled Row N (review finding 5).
type indexedBatchRow struct {
	batchCreateLinkRow
	origIndex int
}

// batchCreateLinksResponse is the POST /api/campaigns/{slug}/links/batch
// success body. links is always non-nil so it encodes as [] rather than
// null. skipped_blank_rows reports how many submitted rows were dropped as
// blank (see batchCreateLinkRow.isBlank) — surfaced rather than silent so a
// user who left a row empty on purpose (or by mistake) sees that it was
// skipped, not silently ignored.
type batchCreateLinksResponse struct {
	Links            []linkView `json:"links"`
	SkippedBlankRows int        `json:"skipped_blank_rows"`
}

// BatchCreateLinks handles POST /api/campaigns/{slug}/links/batch: creates
// one new short link per non-blank row, all assigned to the caller's own
// campaign, in a SINGLE server call (issue: "prefer one server call — a
// partial failure halfway through a client-side loop leaves the campaign in
// a state the user did not ask for and cannot easily undo").
//
// THE WHOLE REQUEST IS ATOMIC. Every row is validated BEFORE any link is
// inserted — cap, blank-row filtering, URL syntax, in-batch duplicates, and
// (when a rule cache is wired) URL-filter denial all run first, over the
// full row set, and a failure at any of those steps writes an error
// response with NOTHING created. Once validation passes,
// links.Store.CreateLinksBatch inserts every row inside one transaction:
// either all of them commit, or (on an unexpected DB error) none do. See
// links.Store.CreateLinksBatch's doc comment for the full atomicity
// rationale and CreateLinksBatch's dedup-bypass decision — the reason this
// issue exists.
//
// A slug that does not exist OR belongs to another user yields 404, matching
// every other campaign endpoint's indistinguishable-404 contract.
//
// DOUBLE-SUBMIT IS NOT DEDUPLICATED — a CONTRACT CHANGE relative to
// single-create. Because this endpoint's dedup is bypassed entirely (see
// links.Store.CreateLinksBatch), submitting the identical N-row batch twice
// (e.g. a lost response followed by a retry) creates 2N links, not N —
// unlike POST /api/links, where "create the same link twice" reliably
// yields one link back via CreateOrReactivateLink's dedup lookup. The UI
// mitigates the common paths (submit button disabled while the request is
// in flight; the form resets on success, so there is nothing left to
// resubmit by accident), leaving only the genuine lost-response-then-retry
// window — judged an acceptable trade against the complexity of an
// idempotency key, but worth stating plainly since #0106 builds QR sheets
// on top of these links and a doubled batch means doubled sheets.
func (h *CampaignsHandler) BatchCreateLinks(w http.ResponseWriter, r *http.Request) {
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

	var req batchCreateLinksRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
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

	// Trim every row up front; every check below operates on the trimmed
	// shape. Blank rows (issue AC: "a row left blank is skipped rather than
	// creating a link with empty UTM values") are dropped here, BEFORE the
	// cap check, so a form with a trailing empty row the user never removed
	// does not itself push a legitimate batch over the cap.
	//
	// Each surviving row carries its ORIGINAL 1-based position in req.Rows
	// (origIndex), not its position in the post-filter slice (review finding
	// 5). Every error message below reports origIndex so "row N" always
	// matches the row the UI labeled Row N — before this, a request shaped
	// [filled, blank, blank, dup-of-1] returned "row 2 duplicates row 1"
	// (positions in the filtered slice) while the form showed the duplicate
	// as Row 4, sending the user hunting the wrong row.
	trimmedAll := make([]batchCreateLinkRow, 0, len(req.Rows))
	for _, row := range req.Rows {
		trimmedAll = append(trimmedAll, row.trimmed())
	}
	rows := make([]indexedBatchRow, 0, len(trimmedAll))
	skipped := 0
	for i, row := range trimmedAll {
		if row.isBlank() {
			skipped++
			continue
		}
		rows = append(rows, indexedBatchRow{batchCreateLinkRow: row, origIndex: i + 1})
	}

	if len(rows) == 0 {
		writeError(w, http.StatusBadRequest, "at least one non-blank row is required")
		return
	}
	if len(rows) > maxBatchCreateRows {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d rows per request", maxBatchCreateRows))
		return
	}

	// Duplicate-row check (issue AC: "duplicate rows are either rejected or
	// allowed deliberately — decide, state, test"). DECISION: rejected, atomically
	// (the whole request fails, nothing is created) — see duplicateKey's doc
	// comment for why placement is part of the tuple and two rows differing
	// only in placement are NOT flagged here.
	seen := make(map[string]int, len(rows)) // duplicateKey -> first row's ORIGINAL 1-based position (origIndex)
	for _, row := range rows {
		key := row.duplicateKey()
		if first, ok := seen[key]; ok {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("row %d duplicates row %d (same source, medium, content, and placement)", row.origIndex, first))
			return
		}
		seen[key] = row.origIndex
	}

	// URL syntax validation — every row, before any insert, so a single
	// malformed row fails the whole batch rather than creating the rows
	// before it and silently dropping the rest.
	for _, row := range rows {
		if !validDestinationURL(row.DestinationURL) {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("row %d: destination_url must be a valid absolute http(s) URL", row.origIndex))
			return
		}
	}

	// #0024 URL FILTER CHECK — every row's composed URL, before any insert,
	// mirroring LinksHandler.Create's single-URL check but applied to the
	// whole batch atomically: ANY row matching an active filter rule fails
	// the ENTIRE request with 422, and nothing is created. Skipped entirely
	// when no rule cache is wired (h.rules == nil), matching LinksHandler's
	// degradation.
	//
	// This handler makes TWO SEPARATE decisions about what LinksHandler.Create
	// does on a match, and they do not share a rationale:
	//
	//  1. NO DENIED LINK ROW. LinksHandler.Create calls CreateDeniedLink to
	//     persist an inactive row (active=false, denied_reason=code) it can
	//     attribute the denial to. A batch has no single row to attribute
	//     it to before the caller fixes the offending row and resubmits, and
	//     recording N-1 good rows as denied alongside the one that matched
	//     would misrepresent what was actually blocked. This part is
	//     deliberate and unchanged.
	//
	//  2. AN AUDIT ENTRY IS STILL RECORDED, unlike an earlier version of this
	//     handler which skipped it too — that was an oversight, not a
	//     decision: recording "no denied row" has a real reason (there is no
	//     row), but recording "no audit entry" had none (an audit.Entry
	//     carries the campaign, the denied URL, and the row index directly —
	//     it needs no link row to attribute to). Without it, POST /api/links
	//     surfaced a link.denied audit row for `http://evil.com/x` while the
	//     identical URL submitted as a batch row surfaced NOTHING, making the
	//     batch endpoint a filter-probing route with zero audit visibility —
	//     defeating #0025's coverage of #0024 for exactly this path (review
	//     finding 1). TargetID is nil (no link row exists to point at);
	//     Metadata carries batch:true and the row's origIndex so a reviewer
	//     can tell a batch denial apart from a single-create one and match it
	//     back to the submitted row.
	if h.rules != nil {
		activeRules, err := h.rules.Rules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		for _, row := range rows {
			if code, ruleID, matched := filters.Evaluate(activeRules, row.DestinationURL); matched {
				label := filters.ReasonLabel(code)

				if h.auditor != nil {
					actor := u.ID
					h.auditor.Record(r.Context(), audit.Entry{
						ActorID:    &actor,
						UserID:     &actor,
						Action:     audit.ActionLinkDenied,
						TargetType: audit.TargetLink,
						TargetID:   nil,
						Metadata: map[string]any{
							"destination_url": row.DestinationURL,
							"reason_code":     code,
							"reason_label":    label,
							"matched_rule_id": ruleID,
							"campaign_slug":   c.Slug,
							"campaign_id":     c.ID,
							"batch":           true,
							"row":             row.origIndex,
						},
						IP: clientIP(r),
					})
				}

				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error":  "url_denied",
					"reason": code,
					"label":  label,
					"row":    row.origIndex,
				})
				return
			}
		}
	}

	campaignID := c.ID
	newLinks := make([]links.NewLink, 0, len(rows))
	for _, row := range rows {
		newLinks = append(newLinks, links.NewLink{
			UserID:         u.ID,
			DestinationURL: row.DestinationURL,
			Title:          row.Title,
			CampaignID:     &campaignID,
			UTMSource:      row.UTMSource,
			UTMMedium:      row.UTMMedium,
			UTMCampaign:    row.UTMCampaign,
			UTMTerm:        row.UTMTerm,
			UTMContent:     row.UTMContent,
			Placement:      row.Placement,
		})
	}

	created, err := h.links.CreateLinksBatch(r.Context(), newLinks, links.GenerateUniqueKey)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, links.ErrKeyTaken):
		// Unreachable in practice — batch rows never carry a custom alias,
		// and generated-key collisions retry inside the same transaction
		// (see links.Store.CreateLinksBatch) — but mapped defensively rather
		// than falling into the generic 500 below.
		writeError(w, http.StatusConflict, "key already taken")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// #0025 audit: one link.created entry per batch-created link,
	// fire-and-forget after the batch's transaction has already committed —
	// the same convention LinksHandler.Create uses for a single insert. Each
	// entry additionally records batch=true and the campaign so a reviewer
	// of the audit log can tell a batch-created link apart from one created
	// singly with campaign_id set.
	if h.auditor != nil {
		actor := u.ID
		for _, l := range created {
			lid := l.ID
			h.auditor.Record(r.Context(), audit.Entry{
				ActorID:    &actor,
				UserID:     &actor,
				Action:     audit.ActionLinkCreated,
				TargetType: audit.TargetLink,
				TargetID:   &lid,
				Metadata: map[string]any{
					"key":             l.Key,
					"destination_url": l.DestinationURL,
					"title":           l.Title,
					"duplicate":       false,
					"batch":           true,
					"campaign_slug":   c.Slug,
					"campaign_id":     c.ID,
				},
				IP: clientIP(r),
			})
		}
	}

	views := make([]linkView, 0, len(created))
	for _, l := range created {
		views = append(views, toLinkView(l))
	}
	writeJSON(w, http.StatusCreated, batchCreateLinksResponse{Links: views, SkippedBlankRows: skipped})
}
