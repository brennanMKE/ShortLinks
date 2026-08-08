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

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/events"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/qr"
)

// timeseriesDays is the default look-back window supplied to ClicksOverTime
// from the link-detail handler. Zero values tell the stats store to apply its
// own defaults (currently 30 days).
var zeroTime time.Time

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
	// CreateOrReactivateLink is the generated-key create path with per-user URL
	// deduplication (#0023): it runs the dedup lookup + decision + any write
	// atomically and reports which branch it took via the CreateOutcome.
	CreateOrReactivateLink(ctx context.Context, in links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, links.CreateOutcome, error)
	// CreateDeniedLink inserts a denied link row (active=false,
	// denied_reason=code) for the #0024 URL-filter check. It mints a unique
	// generated key via genKey so the denied row does not collide with the global
	// UNIQUE(key) constraint.
	CreateDeniedLink(ctx context.Context, in links.NewLink, reasonCode int16, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, error)
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

// ruleProvider is the slice of the filter-rule cache the links handler needs:
// the active, compiled URL filter rules (refreshed from the DB on a 60s TTL).
// *cache.RuleCache satisfies it via Rules. It is optional — a nil provider (no
// filter cache wired) disables URL filtering, so the handler falls through to
// the normal dedup/insert path. Taking an interface keeps the handler testable
// with a fake rule set and no DB.
type ruleProvider interface {
	Rules(ctx context.Context) ([]filters.Rule, error)
}

// statsProvider is the slice of the click-analytics store the links handler
// needs: the per-link UTM breakdown (#0030) and the clicks-over-time series
// (#0049). *clicks.StatsStore satisfies both methods. It is optional — a nil
// provider (no stats store wired) omits the utm_stats and timeseries fields from
// the link-detail response, so the handler stays testable without a DB and
// degrades gracefully if analytics are not wired.
type statsProvider interface {
	UTMStatsForLink(ctx context.Context, linkID int64) (clicks.UTMStats, error)
	ClicksOverTime(ctx context.Context, linkID int64, from, to time.Time) (clicks.TimeseriesResult, error)
}

// campaignLookup is the slice of the campaigns data layer the links handler
// needs (#0099): resolving an optional client-supplied campaign_id or
// campaign_slug on POST /api/links to a campaign the CALLER owns. Both
// methods are already user-scoped and report campaigns.ErrCampaignNotFound
// indistinguishably for "does not exist" and "belongs to another user" — the
// same 404-hiding contract linkStore's GetLink uses — so a request that
// tries campaign_id/campaign_slug against another user's campaign is
// rejected exactly like a request for a nonexistent one, which is what makes
// "assign a link into user B's campaign" impossible from user A's requests.
// *campaigns.Store satisfies this via GetCampaignByID/GetCampaignBySlug. May
// be nil (e.g. in unit tests that never send campaign_id/campaign_slug), in
// which case Create rejects a request that supplies either with a 400 rather
// than silently ignoring it.
type campaignLookup interface {
	GetCampaignByID(ctx context.Context, userID, id int64) (campaigns.Campaign, error)
	GetCampaignBySlug(ctx context.Context, userID int64, slug string) (campaigns.Campaign, error)
}

// eventPublisher is the slice of the events broker the links handler needs: the
// ability to broadcast a link.created event to a user's connected SSE clients
// (#0026). *events.Broker satisfies it via Publish. It is optional — a nil
// publisher (no broker wired) disables the SSE broadcast, so the create path
// simply skips the publish. Taking an interface keeps the handler testable with
// a fake broker and no real SSE plumbing.
type eventPublisher interface {
	Publish(userID int64, event events.Event)
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
	// rules is the active-URL-filter-rule provider used by the #0024 filter check
	// at the top of Create; may be nil (no filter cache wired) in which case URL
	// filtering is skipped and every URL proceeds to the dedup/insert path.
	rules ruleProvider
	// auditor records the link.created/reactivated/denied/deactivated audit
	// entries (#0025). May be nil in unit tests that do not assert audit rows.
	auditor *audit.Logger
	// broker broadcasts the #0026 link.created SSE event to the user's connected
	// dashboard clients on a successful insert/reactivation. May be nil (no broker
	// wired) in which case the broadcast is skipped.
	broker eventPublisher
	// stats provides the per-link UTM analytics breakdown enriching the
	// link-detail response (#0030). May be nil (no stats store wired) in which
	// case the utm_stats field is omitted.
	stats statsProvider
	// campaigns resolves an optional POST /api/links campaign_id/campaign_slug
	// to an owned campaign (#0099). May be nil, in which case a request that
	// supplies either field is rejected with a 400.
	campaigns campaignLookup
}

// NewLinksHandler constructs a LinksHandler over the data layer, the redirect
// cache, the URL-filter rule cache, the audit logger, the SSE event broker, the
// click-analytics store, and the campaign lookup (#0099). Pass a nil
// redirectCache to disable eviction, a nil ruleProvider to disable URL
// filtering, a nil auditor to disable audit writes, a nil broker to disable
// the #0026 SSE broadcast, a nil stats provider to omit the #0030 utm_stats
// field, and a nil campaignLookup to reject campaign_id/campaign_slug on
// create (e.g. in unit tests that do not exercise those paths); the handler
// then skips the respective steps.
func NewLinksHandler(store linkStore, redirectCache cacheEvictor, rules ruleProvider, auditor *audit.Logger, broker eventPublisher, stats statsProvider, campLookup campaignLookup) *LinksHandler {
	return &LinksHandler{store: store, cache: redirectCache, rules: rules, auditor: auditor, broker: broker, stats: stats, campaigns: campLookup}
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
	// CampaignID..Placement are the #0099 discrete UTM/campaign fields, present
	// on every linkView (not just the detail response) since they are already
	// known synchronously from the domain Link with no extra query.
	CampaignID  *int64 `json:"campaign_id"`
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMTerm     string `json:"utm_term"`
	UTMContent  string `json:"utm_content"`
	Placement   string `json:"placement"`
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
		CampaignID:     l.CampaignID,
		UTMSource:      l.UTMSource,
		UTMMedium:      l.UTMMedium,
		UTMCampaign:    l.UTMCampaign,
		UTMTerm:        l.UTMTerm,
		UTMContent:     l.UTMContent,
		Placement:      l.Placement,
	}
}

// createLinkRequest is the POST /api/links body. custom_key and alias are
// accepted as synonyms for a user-supplied key so the client may send either
// (the PRD/issue use both "key" and "alias" terminology); key is the canonical
// field. expires_at is RFC 3339.
//
// CampaignID/CampaignSlug (#0099) are an optional way to assign the link to
// one of the caller's own campaigns at create time — an alternative to the
// dedicated POST /api/campaigns/{slug}/links endpoint for the common case of
// "create this link already in campaign X". When both are present CampaignID
// takes precedence (see Create). UTMSource..Placement are the five discrete
// UTM columns plus the operational placement label; the handler stores them
// verbatim alongside whatever the client baked into DestinationURL — see
// links.NewLink's doc comment for why the store does not itself verify the
// two agree.
type createLinkRequest struct {
	DestinationURL string     `json:"destination_url"`
	Title          string     `json:"title"`
	Key            string     `json:"key"`
	CustomKey      string     `json:"custom_key"`
	Alias          string     `json:"alias"`
	ExpiresAt      *time.Time `json:"expires_at"`
	CampaignID     *int64     `json:"campaign_id"`
	CampaignSlug   string     `json:"campaign_slug"`
	UTMSource      string     `json:"utm_source"`
	UTMMedium      string     `json:"utm_medium"`
	UTMCampaign    string     `json:"utm_campaign"`
	UTMTerm        string     `json:"utm_term"`
	UTMContent     string     `json:"utm_content"`
	Placement      string     `json:"placement"`
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
//	── #0024 URL FILTER CHECK (runs FIRST) — IMPLEMENTED ─────────────────────
//	Loads the active filter rules (cache→DB, 60s TTL) and tests
//	req.DestinationURL in BOTH the generated-key and custom-alias branches. On a
//	match it inserts a denied link (active=false, denied_reason=<code>), leaves
//	the link.denied audit seam (#0025), and returns 422
//	{error:"url_denied", reason:<code>, label:<label>}. The PRD evaluates the
//	filter before dedup so a blocked URL is always re-evaluated, and #0026 SSE
//	never fires for a denial.
//
//	── #0023 DEDUP CHECK (runs AFTER the filter, before insert) ──────────────
//	Implemented (#0023) for the generated-key path only — custom aliases are NOT
//	deduplicated. The store's CreateOrReactivateLink runs the dedup lookup +
//	decision + any write atomically (in a transaction) and reports which branch
//	it took: an active match is returned unchanged (duplicate=true, no write, no
//	SSE), an inactive match is reactivated (duplicate=true, fires SSE), and no
//	match inserts a fresh link (duplicate=false, fires SSE).
//
// After a successful create/reactivate (the seam-marked points below). The
// boolean `broadcast` distinguishes the cases per the PRD: an INSERT or a
// REACTIVATION fires #0026 SSE; a pure ACTIVE-duplicate does NOT.
//
//	── #0025 AUDIT ── write an audit entry attributed to u.ID: link.created with
//	   metadata {key, destination_url, title, duplicate} for inserts and active
//	   duplicates, link.reactivated for the reactivation branch.
//	── #0026 SSE ──── when broadcast, broker.Publish(u.ID, Event{Name:"link.created",
//	   Payload:<link JSON>}) (insert or reactivation only) — IMPLEMENTED.
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

	customKey := strings.TrimSpace(req.customKey())

	// ───────────────────────────────────────────────────────────────────────
	// #0099 CAMPAIGN RESOLUTION — an optional campaign_id or campaign_slug is
	// resolved to a campaign the CALLER owns before anything is inserted.
	// campaign_id takes precedence when both are present. GetCampaignByID/
	// GetCampaignBySlug are user-scoped and report campaigns.ErrCampaignNotFound
	// indistinguishably for "does not exist" and "belongs to another user", so
	// this is also the ownership gate: user A can never assign a link into
	// user B's campaign by guessing B's campaign id/slug — it 404s exactly like
	// a nonexistent one would. A request that supplies either field while no
	// campaign lookup is wired (h.campaigns == nil) is rejected with 400 rather
	// than silently creating an unassigned link.
	// ───────────────────────────────────────────────────────────────────────
	var campaignID *int64
	slug := strings.TrimSpace(req.CampaignSlug)
	if req.CampaignID != nil || slug != "" {
		if h.campaigns == nil {
			writeError(w, http.StatusBadRequest, "campaigns are not available")
			return
		}
		var (
			c   campaigns.Campaign
			err error
		)
		if req.CampaignID != nil {
			c, err = h.campaigns.GetCampaignByID(r.Context(), u.ID, *req.CampaignID)
		} else {
			c, err = h.campaigns.GetCampaignBySlug(r.Context(), u.ID, slug)
		}
		switch {
		case err == nil:
			id := c.ID
			campaignID = &id
		case errors.Is(err, campaigns.ErrCampaignNotFound):
			writeError(w, http.StatusNotFound, "campaign not found")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	in := links.NewLink{
		UserID:         u.ID,
		DestinationURL: dest,
		Title:          strings.TrimSpace(req.Title),
		ExpiresAt:      req.ExpiresAt,
		CampaignID:     campaignID,
		UTMSource:      strings.TrimSpace(req.UTMSource),
		UTMMedium:      strings.TrimSpace(req.UTMMedium),
		UTMCampaign:    strings.TrimSpace(req.UTMCampaign),
		UTMTerm:        strings.TrimSpace(req.UTMTerm),
		UTMContent:     strings.TrimSpace(req.UTMContent),
		Placement:      strings.TrimSpace(req.Placement),
	}

	// ───────────────────────────────────────────────────────────────────────
	// #0024 URL FILTER CHECK — runs FIRST, before dedup and before any insert,
	// in BOTH the generated-key and custom-alias branches: a denied URL is denied
	// regardless of alias. Load the active filter rules (cache → DB, 60s TTL) and
	// evaluate dest. On a match, record a denied link row (active=false,
	// denied_reason=code), leave the #0025 audit seam, and return 422 — the
	// dedup/insert path below never runs and #0026 SSE never fires for a denial.
	// When no rule cache is wired (h.rules == nil) the check is skipped.
	// ───────────────────────────────────────────────────────────────────────
	if h.rules != nil {
		rules, err := h.rules.Rules(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if code, ruleID, matched := filters.Evaluate(rules, dest); matched {
			denied, err := h.store.CreateDeniedLink(r.Context(), in, int16(code), links.GenerateUniqueKey)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			label := filters.ReasonLabel(code)

			// #0025 audit: link.denied attributed to u.ID with
			// {destination_url, reason_code, reason_label, matched_rule_id}. The
			// denied row is already committed, so this is fire-and-forget. #0026 SSE
			// is NOT fired for a denial — only successful inserts/reactivations
			// broadcast (see below).
			if h.auditor != nil {
				actor := u.ID
				lid := denied.ID
				h.auditor.Record(r.Context(), audit.Entry{
					ActorID:    &actor,
					UserID:     &actor,
					Action:     audit.ActionLinkDenied,
					TargetType: audit.TargetLink,
					TargetID:   &lid,
					Metadata: map[string]any{
						"destination_url": dest,
						"reason_code":     code,
						"reason_label":    label,
						"matched_rule_id": ruleID,
					},
					IP: clientIP(r),
				})
			}

			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error":  "url_denied",
				"reason": code,
				"label":  label,
			})
			return
		}
	}

	// duplicate is the create response's "duplicate" field: true for both the
	// active-found and reactivated dedup branches, false for a fresh insert.
	// broadcast records whether the post-create #0026 SSE seam should fire: an
	// INSERT or a REACTIVATION broadcasts; a pure active-duplicate does NOT.
	var (
		created     links.Link
		duplicate   bool
		broadcast   bool
		reactivated bool
	)

	if customKey != "" {
		// Custom aliases are NOT deduplicated (PRD) — attempt the insert with the
		// supplied key directly; a clash surfaces as ErrKeyTaken → 409 below.
		if !validKey(customKey) {
			writeError(w, http.StatusBadRequest, "custom key must be 1-12 url-safe characters")
			return
		}
		in.Key = customKey
		link, err := h.store.CreateLink(r.Context(), in)
		switch {
		case err == nil:
			created = link
			duplicate = false
			broadcast = true // a fresh custom-alias insert fires SSE/audit.
		case errors.Is(err, links.ErrKeyTaken):
			writeError(w, http.StatusConflict, "key already taken")
			return
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	} else {
		// ───────────────────────────────────────────────────────────────────
		// SEAM #0023 (dedup check — runs AFTER the filter, before insert):
		// generated-key path only. The store runs the dedup lookup + decision +
		// any write atomically and reports which branch it took. Key generation
		// happens inside that transaction (only on the no-match/insert branch).
		// ───────────────────────────────────────────────────────────────────
		link, outcome, err := h.store.CreateOrReactivateLink(r.Context(), in, links.GenerateUniqueKey)
		switch {
		case err == nil:
			created = link
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		switch outcome {
		case links.OutcomeInserted:
			duplicate = false
			broadcast = true
		case links.OutcomeReactivated:
			duplicate = true
			broadcast = true // reactivation re-publishes link.created.
			reactivated = true
		case links.OutcomeActiveDuplicate:
			duplicate = true
			broadcast = false // active duplicate: no insert, no SSE (PRD).
		}
	}

	// ───────────────────────────────────────────────────────────────────────
	// #0025 audit: link.reactivated for the reactivation branch (metadata
	// {key, destination_url}); link.created for inserts and active duplicates
	// (metadata {key, destination_url, title, duplicate}). The link is already
	// committed, so this is fire-and-forget.
	// ───────────────────────────────────────────────────────────────────────
	if h.auditor != nil {
		actor := u.ID
		lid := created.ID
		if reactivated {
			h.auditor.Record(r.Context(), audit.Entry{
				ActorID:    &actor,
				UserID:     &actor,
				Action:     audit.ActionLinkReactivated,
				TargetType: audit.TargetLink,
				TargetID:   &lid,
				Metadata: map[string]any{
					"key":             created.Key,
					"destination_url": created.DestinationURL,
				},
				IP: clientIP(r),
			})
		} else {
			h.auditor.Record(r.Context(), audit.Entry{
				ActorID:    &actor,
				UserID:     &actor,
				Action:     audit.ActionLinkCreated,
				TargetType: audit.TargetLink,
				TargetID:   &lid,
				Metadata: map[string]any{
					"key":             created.Key,
					"destination_url": created.DestinationURL,
					"title":           created.Title,
					"duplicate":       duplicate,
				},
				IP: clientIP(r),
			})
		}
	}
	view := toLinkView(created)
	view.Duplicate = &duplicate

	// #0026 SSE: broadcast link.created to the user's connected dashboard clients
	// on an insert or reactivation (broadcast == true), never on a pure
	// active-duplicate or a filter-denied create (those return earlier / set
	// broadcast == false). The payload is the SAME link object returned in the
	// response, marshaled here so a subscriber renders an identical row. Skipped
	// when no broker is wired (h.broker == nil). A marshal error (cannot occur for
	// this fixed-shape value) is swallowed so a successful create never fails on
	// the broadcast.
	if h.broker != nil && broadcast {
		if payload, err := json.Marshal(view); err == nil {
			h.broker.Publish(u.ID, events.Event{Name: "link.created", Payload: payload})
		}
	}

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

// linkDetailView is the GET /api/links/{key} body: a single link's fields plus
// the #0030 UTM analytics breakdown and the #0049 clicks-over-time series.
// It embeds linkView so the link fields keep their existing JSON shape.
// utm_stats and timeseries are omitted (nil) when no analytics store is wired.
type linkDetailView struct {
	linkView
	UTMStats   *clicks.UTMStats         `json:"utm_stats,omitempty"`
	Timeseries *clicks.TimeseriesResult `json:"timeseries,omitempty"`
	// CampaignName/CampaignSlug (#0099) are populated only when the link is
	// currently assigned to a campaign (links.Store.GetLink's LEFT JOIN);
	// both are "" for an unassigned link, matching CampaignID being nil.
	CampaignName string `json:"campaign_name,omitempty"`
	CampaignSlug string `json:"campaign_slug,omitempty"`
}

// Get handles GET /api/links/{key}. It returns the caller's link detail with its
// click stats, enriched (#0030) with a utm_stats breakdown aggregated by
// utm_source/medium/campaign. A key that does not exist OR belongs to another
// user yields 404 — the same response in both cases so the endpoint never reveals
// another user's link. Ownership is enforced by GetLink (user-scoped) before the
// analytics query runs against the resolved link id, so a non-owner can never see
// another user's breakdown.
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
		// fall through to build the detail view.
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	detail := linkDetailView{
		linkView:     toLinkView(link),
		CampaignName: link.CampaignName,
		CampaignSlug: link.CampaignSlug,
	}

	// #0030 / #0049: enrich with the UTM breakdown and clicks-over-time series
	// when an analytics store is wired. The link was resolved scoped to the caller
	// above, so passing link.ID here cannot leak another user's data. Stats
	// failures are fatal to the detail response so the client never sees a partial
	// or inconsistent analytics payload.
	if h.stats != nil {
		st, err := h.stats.UTMStatsForLink(r.Context(), link.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		detail.UTMStats = &st

		// #0049: default time window — zero values let the store apply its own
		// defaults (30-day look-back ending at the current UTC midnight).
		ts, err := h.stats.ClicksOverTime(r.Context(), link.ID, zeroTime, zeroTime)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		detail.Timeseries = &ts
	}

	writeJSON(w, http.StatusOK, detail)
}

// ── QR codes (#0106) ──────────────────────────────────────────────────────
//
// Both routes resolve {key} through the SAME user-scoped GetLink as Get
// above (404 for "does not exist" and "belongs to another user" alike), then
// hand link.Key — never link.DestinationURL — to qr.ShortURL. That choice is
// the acceptance criterion this feature exists to protect: encoding the
// destination would let a scan resolve without ever hitting GET /u/{key},
// silently bypassing click recording (#0030) and defeating the whole
// per-placement attribution this issue is for. See
// TestLinksHandler_QRSVG_EncodesShortURLNotDestination /
// TestLinksHandler_QRPNG_EncodesShortURLNotDestination, which decode the
// response body with an independent decoder and assert it equals the short
// URL and NOT the (deliberately different) destination URL.

// qrCacheControl is applied to every QR response. A link's key never
// changes once created, so its QR code is immutable content — safe to cache
// indefinitely client-side — but "immutable" still gets a max-age rather
// than relying on that keyword alone for older HTTP caches.
//
// CAVEAT (review finding): the IMAGE content this caches is genuinely
// immutable (it only ever encodes the key, which never changes), but the
// Content-Disposition filename below also bakes in the link's PLACEMENT,
// which IS editable. Editing a placement and re-downloading within the same
// browser cache window can therefore serve the OLD filename (stale
// placement slug) alongside fresh, correct image bytes for up to a year.
// Cosmetic — the encoded/scanned content is unaffected — but worth knowing
// before treating this cache header as "everything about this response is
// pinned to the key". Not fixed here: doing so would mean either dropping
// the aggressive cache lifetime (this response is requested rarely enough,
// and cheap enough to regenerate, that the caching is a nice-to-have, not
// load-bearing) or varying the URL on placement (which would need a cache
// key scheme this endpoint doesn't have reason to carry otherwise).
const qrCacheControl = "private, max-age=31536000, immutable"

// QRSVG handles GET /api/links/{key}/qr.svg: a vector QR code encoding this
// link's short URL, as a download (Content-Disposition: attachment) named
// via qr.Filename so it lands in a Downloads folder with a sensible name
// even when fetched on its own rather than through the bulk archive.
func (h *LinksHandler) QRSVG(w http.ResponseWriter, r *http.Request) {
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
		// fall through.
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	bitmap, err := qr.Matrix(qr.ShortURL(link.Key))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	svg := qr.RenderSVG(bitmap)

	filename := qr.Filename(link.Key, link.Placement, "svg")
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", qrCacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(svg)
}

// QRPNG handles GET /api/links/{key}/qr.png: a raster QR code at print
// resolution (qr.ModulePixelsPNG per module — see its doc comment for the
// DPI/pixel-size reasoning) encoding this link's short URL, as a download
// named via qr.Filename.
func (h *LinksHandler) QRPNG(w http.ResponseWriter, r *http.Request) {
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
		// fall through.
	case errors.Is(err, links.ErrLinkNotFound):
		writeError(w, http.StatusNotFound, "link not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	bitmap, err := qr.Matrix(qr.ShortURL(link.Key))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	pngBytes, err := qr.RenderPNG(bitmap)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	filename := qr.Filename(link.Key, link.Placement, "png")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", qrCacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}

// patchLinkRequest is the PATCH /api/links/{key} body. Every field is a pointer
// so the handler can distinguish "absent" (leave unchanged) from "present"
// (set). Only title, destination_url, and expires_at are updatable per #0022.
type patchLinkRequest struct {
	Title          *string    `json:"title"`
	DestinationURL *string    `json:"destination_url"`
	ExpiresAt      *time.Time `json:"expires_at"`
	// UTMSource..Placement (#0099) follow Title's convention, not
	// ExpiresAt's: a present-but-null/empty value clears the column (like
	// Title), so no *Present tracking is needed — the field being nil in the
	// decoded struct (absent OR JSON null) means "leave unchanged", matching
	// how Title already behaves. This lets an edit re-save the UTM builder
	// (repopulated from GetLink's response) and the discrete columns stay in
	// lockstep with a changed destination_url.
	UTMSource   *string `json:"utm_source"`
	UTMMedium   *string `json:"utm_medium"`
	UTMCampaign *string `json:"utm_campaign"`
	UTMTerm     *string `json:"utm_term"`
	UTMContent  *string `json:"utm_content"`
	Placement   *string `json:"placement"`
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
// {title, destination_url, expires_at, utm_source, utm_medium, utm_campaign,
// utm_term, utm_content, placement} on the caller's own link and returns the
// updated link — the five UTM fields and placement (#0099) let an edit save
// the builder (repopulated via utmParamsFromLink) back in lockstep with a
// changed destination_url; campaign_id is NOT patchable here, only via the
// dedicated assign/unassign endpoints. destination_url is validated when
// present. A key not owned by the caller yields 404. On success the redirect
// cache entry for the key is evicted (if a cache is wired) so the next
// redirect reflects the change.
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
	// #0099: the five discrete UTM fields plus placement, trimmed like every
	// other string field this handler accepts. Present-but-empty clears the
	// column (Title's convention), letting the edit form save a field the
	// user deliberately blanked out.
	setUTM := func(dst **string, src *string) {
		if src == nil {
			return
		}
		v := strings.TrimSpace(*src)
		*dst = &v
	}
	setUTM(&upd.UTMSource, req.UTMSource)
	setUTM(&upd.UTMMedium, req.UTMMedium)
	setUTM(&upd.UTMCampaign, req.UTMCampaign)
	setUTM(&upd.UTMTerm, req.UTMTerm)
	setUTM(&upd.UTMContent, req.UTMContent)
	setUTM(&upd.Placement, req.Placement)

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
// #0025 audit: a link.deactivated entry for u.ID with metadata
// {key, destination_url} is written after a successful deactivation.
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

	// Read the link (scoped to the caller) BEFORE deactivating so the audit
	// metadata carries its id and destination_url. A miss here only weakens the
	// audit entry; the deactivation below still runs and returns the right status.
	existing, existingErr := h.store.GetLink(r.Context(), u.ID, key)

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

	// #0025 audit: link.deactivated for u.ID with {key, destination_url}. The row
	// is already deactivated, so this is fire-and-forget.
	if h.auditor != nil && existingErr == nil {
		actor := u.ID
		lid := existing.ID
		h.auditor.Record(r.Context(), audit.Entry{
			ActorID:    &actor,
			UserID:     &actor,
			Action:     audit.ActionLinkDeactivated,
			TargetType: audit.TargetLink,
			TargetID:   &lid,
			Metadata: map[string]any{
				"key":             existing.Key,
				"destination_url": existing.DestinationURL,
			},
			IP: clientIP(r),
		})
	}

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
