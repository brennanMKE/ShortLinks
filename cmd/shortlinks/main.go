package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"golang.org/x/time/rate"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
	"github.com/brennanMKE/ShortLinks/internal/events"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/web"
)

const version = "0.3.0"

func main() {
	// Subcommand routing: `shortlinks serve` starts the HTTP server;
	// `shortlinks seed` bootstraps the admin user and a test link; anything
	// else (including no argument or `version`) prints the version.
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		if err := serve(); err != nil {
			log.Fatalf("shortlinks: %v", err)
		}
	case "seed":
		if err := seed(); err != nil {
			log.Fatalf("shortlinks: %v", err)
		}
	default:
		fmt.Printf("shortlinks %s\n", version)
	}
}

// serve loads configuration, connects the database pool (Postgres path) or
// constructs the in-memory dev store (dev path), mounts the routes, and listens
// on the configured port until the process is terminated.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// ── Backend selection ────────────────────────────────────────────────────
	// Dev mode is engaged ONLY by an explicit STORAGE=json — never by an empty
	// DATABASE_URL — so production can never silently fall back to the in-memory
	// store and lose data. Without STORAGE=json the Postgres path always runs.
	if cfg.DevMode() {
		return serveDevMode(cfg)
	}
	return servePostgres(cfg)
}

// servePostgres is the production path: connects to Postgres, constructs the
// real stores, and serves. This is the original serve() logic, unchanged.
func servePostgres(cfg *config.Config) error {
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	wa, err := auth.NewWebAuthn(cfg)
	if err != nil {
		return err
	}

	// Choose the mailer: real SES transport when SMTP credentials are present,
	// otherwise a stdout NoOpMailer for local development.
	var mailer auth.Mailer = auth.NoOpMailer{BaseURL: cfg.BaseURL}
	if cfg.SESSmtpUsername != "" && cfg.SESSmtpPassword != "" {
		mailer = auth.NewSESMailer(cfg)
	}

	// Append-only audit log writer (#0025), shared by every service/handler that
	// records an action. Auth ceremonies write their entries inside their own
	// transaction (WriteTx); API/admin handlers log-and-continue (Record) since
	// their action has already committed.
	auditLogger := audit.New(pool)

	store := auth.NewStore(pool)
	regSvc := auth.NewRegistrationService(store, wa, mailer, auditLogger, cfg)
	loginSvc := auth.NewLoginService(store, wa, mailer, auditLogger, slog.Default())
	recoverSvc := auth.NewRecoveryService(store, wa, mailer, auditLogger)
	authH := handlers.NewAuthHandler(regSvc, loginSvc, recoverSvc, slog.Default())
	credsH := handlers.NewCredentialsHandler(store, auditLogger)
	settingsH := handlers.NewSettingsHandler(store, auditLogger)
	// Admin user management (#0028): list/detail/deactivate/reactivate. The
	// deactivate/reactivate paths write their account.deactivated/reactivated audit
	// row inside the store's transaction (WriteTx) so it commits atomically with
	// the active flip and session deletion.
	adminUsersH := handlers.NewAdminUsersHandler(store, auditLogger)
	// Admin audit log view (#0029): paginated, newest-first, optional ?user_id=
	// filter. Reads through audit.Reader over the same shared pool as the writer.
	adminAuditH := handlers.NewAdminAuditHandler(audit.NewReader(pool))

	// URL filtering (#0024): the rule store + a 60s-TTL cache of the active,
	// compiled rules. The cache loads from the DB on a miss/expiry and is
	// invalidated immediately by the admin CRUD handler on any mutation. The
	// loader compiles the rules once (uncompilable patterns are skipped + logged).
	filterStore := filters.NewStore(pool)
	ruleCache := cache.NewRuleCache(func(ctx context.Context) ([]filters.Rule, error) {
		rules, err := filterStore.LoadActive(ctx)
		if err != nil {
			return nil, err
		}
		return filters.CompileRules(rules, slog.Default()), nil
	})
	urlFiltersH := handlers.NewURLFiltersHandler(filterStore, ruleCache, auditLogger)

	// SSE event broker (#0026): the in-memory pub/sub singleton shared by the
	// links handler (publisher) and the events handler (subscriber). A successful
	// POST /api/links insert/reactivation publishes a link.created event that the
	// broker fans out to every GET /api/events stream open for that user.
	broker := events.NewBroker()
	eventsH := handlers.NewEventsHandler(broker)

	// Redirect path (#0007 cache / #0009 redirect / #0030 click recording). The
	// redirect cache fronts the hot GET /u/{key} lookup; the resolver checks it
	// then falls back to the link store (caching positive hits and short-TTL
	// negative entries for absent keys). The clicks recorder persists each click
	// best-effort off the redirect goroutine, and the stats store backs the #0030
	// UTM analytics on the link-detail endpoint.
	linkStore := links.NewStore(pool)
	redirectCache, err := cache.New(int64(cfg.CacheMaxCost), time.Duration(cfg.CacheTTLSeconds)*time.Second)
	if err != nil {
		return err
	}
	defer redirectCache.Close()
	resolver := links.NewResolver(redirectCache, linkStore)
	clickRecorder := clicks.NewRecorder(pool, slog.Default())
	statsStore := clicks.NewStatsStore(pool)
	redirectH := handlers.NewRedirectHandler(resolver, handlers.NewClickRecorder(clickRecorder))

	// Campaign store (#0098), constructed before linksH so it can be wired
	// into both handlers: linksH resolves an optional campaign_id/slug on
	// POST /api/links (#0099), and campaignsH performs the CRUD +
	// link-membership mutations. campaign.created/updated/deleted/
	// link_assigned/link_unassigned audit entries are written by the store
	// INSIDE the same transaction as the mutation (audit.WriteTx), unlike the
	// links handler's fire-and-forget Record — see campaigns.Store's doc
	// comments.
	campaignStore := campaigns.NewStore(pool)

	// Link CRUD API (#0022). The links store reuses the shared pgx pool. The
	// redirect cache constructed above is now wired as the cache-evictor so a
	// PATCH/DELETE drops the key and the next redirect re-reads the DB. The rule
	// cache is wired so the #0024 URL filter check runs at the top of Create, the
	// broker so a successful create broadcasts the #0026 link.created SSE event,
	// the stats store so GET /api/links/{key} returns the #0030 utm_stats, and
	// the campaign store so POST /api/links can resolve an optional
	// campaign_id/campaign_slug (#0099).
	linksH := handlers.NewLinksHandler(linkStore, redirectCache, ruleCache, auditLogger, broker, statsStore, campaignStore)

	// Campaign CRUD + link-membership + batch-create API (#0098, #0099,
	// #0105). Link membership (assign/unassign/list) and batch-create both
	// need the links store to resolve/list/insert the links they operate on,
	// scoped to the caller. The stats store (constructed above for linksH's
	// utm_stats field) doubles as the campaign-scoped rollup provider
	// (#0102): *clicks.StatsStore satisfies campaignStatsProvider via
	// CampaignSummary and CampaignRollup — the two COMPOSITE methods, each of
	// which reads its whole payload from a single REPEATABLE READ snapshot.
	// The interface is deliberately narrowed to those two rather than the
	// four per-fragment methods, so a handler cannot assemble one response
	// from reads taken at different instants. ruleCache (constructed above
	// for linksH's #0024 filter check) is wired again here so
	// BatchCreateLinks (#0105) runs the SAME filter check single-create does,
	// before inserting anything.
	campaignsH := handlers.NewCampaignsHandler(campaignStore, linkStore, auditLogger, statsStore, ruleCache)

	// Current user profile (#0027): GET /api/me returns {id, email, is_admin}
	// read straight off the RequireSession-attached context, so the Svelte SPA
	// can gate the admin view. Stateless — no data-layer dependency.
	meH := handlers.NewMeHandler()

	// requireSession guards the authenticated account-management routes; the
	// store satisfies middleware.SessionResolver via ResolveSession.
	requireSession := middleware.RequireSession(store)
	// requireAdmin composes the session guard with the admin check; admin-only
	// routes wrap their handler with requireAdmin(...). RequireSession runs
	// first (attaching the user / answering 401), then RequireAdmin (403 for a
	// non-admin), per #0017.
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}

	return mountAndServe(cfg, pool,
		authH, credsH, settingsH, adminUsersH, adminAuditH,
		urlFiltersH, eventsH, redirectH, linksH, campaignsH, meH,
		requireSession, requireAdmin, nil /* no outer middleware in production */)
}

// serveDevMode boots the app without PostgreSQL using the in-memory dev store.
// All handler interfaces are satisfied by *devstore.Store or its companion
// types. The Postgres connect, pgxpool, and migration paths are entirely skipped.
func serveDevMode(cfg *config.Config) error {
	log.Printf("shortlinks: STORAGE=json — starting with in-memory dev store (no Postgres)")

	ds := devstore.New(cfg.AdminEmail)

	// WebAuthn is still needed for the config but auth routes are stubbed.
	// In dev mode WebAuthnRPID/Origin may be empty; provide localhost defaults.
	if cfg.WebAuthnRPID == "" {
		cfg.WebAuthnRPID = "localhost"
	}
	if cfg.WebAuthnRPOrigin == "" {
		cfg.WebAuthnRPOrigin = fmt.Sprintf("http://localhost:%d", cfg.Port)
	}

	// Auth handler — registration/login/recovery are all stubs in dev mode.
	// Logout still works (deletes the dev session cookie).
	authH := handlers.NewAuthHandler(
		devstore.DevRegistrar{},
		devstore.NewDevLoginService(ds),
		devstore.DevRecoverer{},
		slog.Default(),
	)

	// Credential, settings, user-management, and audit handlers use the dev store.
	credsH := handlers.NewCredentialsHandler(ds, nil /* no audit in dev */)
	settingsH := handlers.NewSettingsHandler(ds, nil)
	adminUsersH := handlers.NewAdminUsersHandler(ds, nil)
	adminAuditH := handlers.NewAdminAuditHandler(ds)

	// URL filters: dev store satisfies both filterRuleStore and ruleCacheInvalidator.
	urlFiltersH := handlers.NewURLFiltersHandler(ds, ds, nil)

	// SSE broker.
	broker := events.NewBroker()
	eventsH := handlers.NewEventsHandler(broker)

	// Redirect path: the dev store implements handlers.LinkResolver directly
	// (via its Resolve method), so we bypass links.NewResolver which requires
	// a concrete *links.Store. The NoCacheEvictor no-ops cache eviction since
	// we're using the store's own in-memory lookup with no Ristretto cache.
	redirectH := handlers.NewRedirectHandler(ds, handlers.NewClickRecorder(ds))

	// Links handler: dev store satisfies linkStore, cacheEvictor (no-op via
	// NoCacheEvictor), ruleProvider (ds.Rules), statsProvider, and (#0099)
	// campaignLookup (GetCampaignByID/GetCampaignBySlug) — all on the same
	// *devstore.Store.
	linksH := handlers.NewLinksHandler(ds, devstore.NoCacheEvictor{}, ds, nil, broker, ds, ds)

	meH := handlers.NewMeHandler()

	// Session middleware backed by the dev store.
	requireSession := middleware.RequireSession(ds)
	requireAdmin := func(next http.Handler) http.Handler {
		return requireSession(middleware.RequireAdmin(next))
	}

	// Dev-only auto-login middleware: on every request that has no session cookie,
	// mint a dev session for the seeded mock admin and inject it into the request
	// so RequireSession accepts it immediately — no passkey ceremony needed.
	// The hard guardrail (cfg.DevMode() check) is enforced inside DevAutoLogin.
	devAutoLogin := middleware.DevAutoLogin(ds, cfg.DevMode())

	// Campaign CRUD + link-membership + batch-create API (#0098, #0099,
	// #0105): devstore.Store now implements campaignStore in-memory
	// (CreateCampaign/UpdateCampaign/DeleteCampaign/ListCampaignsForUser/
	// GetCampaignBySlug/AssignLinkToCampaign/UnassignLinkFromCampaign) AND
	// campaignLinksProvider (GetLink/ListLinksForCampaign/CreateLinksBatch),
	// so dev mode gets working routes rather than falling through to the SPA
	// catch-all with a misleading 200 text/html — a real
	// 404-on-unmounted-route problem the review caught, since #0103's UI work
	// runs against ./scripts/dev.sh (STORAGE=json) and needs genuine JSON
	// responses to build against. ds also satisfies ruleProvider (already
	// wired into linksH above), reused here so BatchCreateLinks runs the same
	// (empty, in dev) filter check.
	campaignsH := handlers.NewCampaignsHandler(ds, ds, nil, ds, ds)

	return mountAndServe(cfg, ds,
		authH, credsH, settingsH, adminUsersH, adminAuditH,
		urlFiltersH, eventsH, redirectH, linksH, campaignsH, meH,
		requireSession, requireAdmin, devAutoLogin)
}

// mountAndServe registers all routes on a new ServeMux and starts listening.
// This is shared between the Postgres and dev paths to avoid code duplication.
// The `db` parameter satisfies handlers.Pinger for GET /health.
// outerMiddleware, when non-nil, wraps the entire mux as the outermost handler.
// It is used in dev mode only (serveDevMode) to apply the auto-login middleware;
// the production path always passes nil.
// campaignsH may be nil (serveDevMode has no dev-store campaigns backing yet),
// in which case the /api/campaigns routes are simply not mounted rather than
// registered against a nil handler.
func mountAndServe(
	cfg *config.Config,
	pinger handlers.Pinger,
	authH *handlers.AuthHandler,
	credsH *handlers.CredentialsHandler,
	settingsH *handlers.SettingsHandler,
	adminUsersH *handlers.AdminUsersHandler,
	adminAuditH *handlers.AdminAuditHandler,
	urlFiltersH *handlers.URLFiltersHandler,
	eventsH *handlers.EventsHandler,
	redirectH *handlers.RedirectHandler,
	linksH *handlers.LinksHandler,
	campaignsH *handlers.CampaignsHandler,
	meH *handlers.MeHandler,
	requireSession func(http.Handler) http.Handler,
	requireAdmin func(http.Handler) http.Handler,
	outerMiddleware func(http.Handler) http.Handler,
) error {
	// Per-IP rate limiters for the abuse-prone public auth endpoints (PRD Phase
	// 2). Burst equals the per-window allowance so a fresh IP gets its full
	// quota immediately, then refills at the sustained rate.
	registerLimiter := middleware.NewRateLimiter(rate.Every(time.Hour/3), 3)  // 3 / hour / IP
	loginLimiter := middleware.NewRateLimiter(rate.Every(time.Minute/10), 10) // 10 / minute / IP
	recoverLimiter := middleware.NewRateLimiter(rate.Every(time.Hour/3), 3)   // 3 / hour / IP

	mux := http.NewServeMux()
	mux.Handle("GET /health", handlers.NewHealthHandler(pinger))

	// Public redirect path (#0009): resolve key → 302 to destination with inbound
	// UTM merged, recording the click asynchronously (#0030). No session required.
	mux.Handle("GET /u/{key}", redirectH)
	mux.Handle("POST /auth/register/start", registerLimiter.Middleware(http.HandlerFunc(authH.RegisterStart)))
	mux.HandleFunc("GET /auth/register/verify", authH.RegisterVerify)
	mux.HandleFunc("POST /auth/register/finish", authH.RegisterFinish)
	// The PRD lists login/start as the rate-limited login endpoint; it is
	// registered here as GET, so the 10/min limiter is attached to that route.
	mux.Handle("GET /auth/login/start", loginLimiter.Middleware(http.HandlerFunc(authH.LoginStart)))
	mux.HandleFunc("POST /auth/login/finish", authH.LoginFinish)
	mux.HandleFunc("POST /auth/logout", authH.Logout)
	// "Sign out everywhere" (#0094) — session-guarded: revokes every session
	// for the authenticated account (including this one), never touches
	// passkey_credentials or users. Complements DELETE /account/credentials/{id}
	// (#0019), the credential-level lever.
	mux.Handle("POST /auth/logout/all", requireSession(http.HandlerFunc(authH.LogoutAll)))
	mux.Handle("POST /auth/recover", recoverLimiter.Middleware(http.HandlerFunc(authH.RecoverStart)))
	mux.HandleFunc("GET /auth/recover/verify", authH.RecoverVerify)
	mux.HandleFunc("POST /auth/recover/finish", authH.RecoverFinish)

	// Passkey credential management — authenticated, operates only on the
	// caller's own credentials (#0019).
	mux.Handle("GET /account/credentials", requireSession(http.HandlerFunc(credsH.List)))
	mux.Handle("PATCH /account/credentials/{id}", requireSession(http.HandlerFunc(credsH.Rename)))
	mux.Handle("DELETE /account/credentials/{id}", requireSession(http.HandlerFunc(credsH.Revoke)))

	// Admin-only runtime settings (#0021): read all settings and update one.
	// Both behind RequireSession + RequireAdmin. The registration gate
	// (registrations_enabled) is read fresh from the DB on each
	// POST /auth/register/start, so a PATCH here takes effect immediately.
	mux.Handle("GET /admin/settings", requireAdmin(http.HandlerFunc(settingsH.List)))
	mux.Handle("PATCH /admin/settings", requireAdmin(http.HandlerFunc(settingsH.Patch)))

	// Admin-only user management (#0028): list users (status + last login), user
	// detail (+ link/passkey counts), deactivate a non-admin user (sets
	// active=false, deletes all their sessions, audits account.deactivated), and
	// reactivate (sets active=true, audits account.reactivated). All behind
	// RequireSession + RequireAdmin.
	mux.Handle("GET /admin/users", requireAdmin(http.HandlerFunc(adminUsersH.List)))
	mux.Handle("GET /admin/users/{id}", requireAdmin(http.HandlerFunc(adminUsersH.Get)))
	mux.Handle("POST /admin/users/{id}/deactivate", requireAdmin(http.HandlerFunc(adminUsersH.Deactivate)))
	mux.Handle("POST /admin/users/{id}/reactivate", requireAdmin(http.HandlerFunc(adminUsersH.Reactivate)))

	// Admin-only audit log (#0029): GET /admin/audit returns the append-only
	// audit_log newest-first, paginated via ?page=&per_page= (default 50, capped
	// at 200), with an optional ?user_id= filter scoped to one user's rows. Behind
	// RequireSession + RequireAdmin.
	mux.Handle("GET /admin/audit", requireAdmin(http.HandlerFunc(adminAuditH.List)))

	// Admin-only URL filter rules (#0024): CRUD + a dry-run test endpoint. All
	// behind RequireSession + RequireAdmin. Every mutation invalidates the 60s
	// rule cache so the change takes effect on the next link creation at once.
	mux.Handle("GET /admin/url-filters", requireAdmin(http.HandlerFunc(urlFiltersH.List)))
	mux.Handle("POST /admin/url-filters", requireAdmin(http.HandlerFunc(urlFiltersH.Create)))
	mux.Handle("POST /admin/url-filters/test", requireAdmin(http.HandlerFunc(urlFiltersH.Test)))
	mux.Handle("PATCH /admin/url-filters/{id}", requireAdmin(http.HandlerFunc(urlFiltersH.Patch)))
	mux.Handle("DELETE /admin/url-filters/{id}", requireAdmin(http.HandlerFunc(urlFiltersH.Delete)))

	// Link CRUD API (#0022) — all behind RequireSession and scoped to the
	// authenticated user in the store. Dedup (#0023), URL filtering (#0024), audit
	// (#0025), and the #0026 SSE broadcast all layer onto the create path.
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(linksH.Create)))
	mux.Handle("GET /api/links", requireSession(http.HandlerFunc(linksH.List)))
	mux.Handle("GET /api/links/{key}", requireSession(http.HandlerFunc(linksH.Get)))
	mux.Handle("PATCH /api/links/{key}", requireSession(http.HandlerFunc(linksH.Patch)))
	mux.Handle("DELETE /api/links/{key}", requireSession(http.HandlerFunc(linksH.Delete)))
	// QR codes (#0106): vector SVG and print-resolution PNG, each encoding
	// the link's SHORT URL (never the destination) so a scan is recorded as
	// a click like any other visit — see internal/qr's package doc comment.
	mux.Handle("GET /api/links/{key}/qr.svg", requireSession(http.HandlerFunc(linksH.QRSVG)))
	mux.Handle("GET /api/links/{key}/qr.png", requireSession(http.HandlerFunc(linksH.QRPNG)))

	// Campaign CRUD + link-membership + stats API (#0098, #0099, #0102) — all
	// behind RequireSession and scoped to the authenticated user in the
	// store. Not mounted when campaignsH is nil (dev mode has no dev-store
	// backing for campaigns).
	if campaignsH != nil {
		mux.Handle("GET /api/campaigns", requireSession(http.HandlerFunc(campaignsH.List)))
		mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(campaignsH.Create)))
		mux.Handle("GET /api/campaigns/{slug}", requireSession(http.HandlerFunc(campaignsH.Get)))
		mux.Handle("PATCH /api/campaigns/{slug}", requireSession(http.HandlerFunc(campaignsH.Patch)))
		mux.Handle("DELETE /api/campaigns/{slug}", requireSession(http.HandlerFunc(campaignsH.Delete)))
		// Campaign rollups (#0102): total/over-time/per-link/channel stats,
		// optionally windowed via ?from=/?to=.
		mux.Handle("GET /api/campaigns/{slug}/stats", requireSession(http.HandlerFunc(campaignsH.Stats)))
		// Link membership (#0099): list, assign, and unassign the links that
		// belong to a campaign.
		mux.Handle("GET /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(campaignsH.ListLinks)))
		mux.Handle("POST /api/campaigns/{slug}/links", requireSession(http.HandlerFunc(campaignsH.AssignLinks)))
		mux.Handle("DELETE /api/campaigns/{slug}/links/{key}", requireSession(http.HandlerFunc(campaignsH.UnassignLink)))
		// Batch create (#0105): one destination URL, a row per channel, one
		// short link per non-blank row, all assigned to this campaign in a
		// single atomic request.
		mux.Handle("POST /api/campaigns/{slug}/links/batch", requireSession(http.HandlerFunc(campaignsH.BatchCreateLinks)))
		// Bulk QR download (#0106): a zip archive of every assigned link's
		// SVG + PNG QR codes, named so printed sheets can be matched back to
		// their placements.
		mux.Handle("GET /api/campaigns/{slug}/qr.zip", requireSession(http.HandlerFunc(campaignsH.QRZip)))
		// Per-link CSV export (#0107): the campaign's rollup as a downloadable
		// CSV, over the same optional ?from=/?to= window and defaults as
		// /stats above, reusing that same clicks.StatsStore.CampaignRollup
		// call so the two can never disagree.
		mux.Handle("GET /api/campaigns/{slug}/export.csv", requireSession(http.HandlerFunc(campaignsH.Export)))
	}

	// Current user profile (#0027) — behind RequireSession; returns the caller's
	// {id, email, is_admin} for the SPA to gate the admin view.
	mux.Handle("GET /api/me", requireSession(http.HandlerFunc(meH.Me)))

	// SSE stream (#0026) — behind RequireSession; pushes link.created events to
	// the authenticated user's connected dashboard clients.
	mux.Handle("GET /api/events", requireSession(http.HandlerFunc(eventsH.Stream)))

	// Svelte SPA (#0038) — the catch-all served LAST. Under the Go 1.22 mux this
	// "GET /" pattern is the least specific, so every explicit route above wins
	// over it. It serves hashed assets from the embedded web/dist directly and
	// falls back to index.html for any other path, so SPA deep links survive a
	// hard refresh.
	mux.Handle("GET /", handlers.NewSPAHandler(web.DistFS()))

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("shortlinks %s listening on %s", version, addr)

	// Apply the outer middleware (dev auto-login) when provided. This must never
	// be non-nil on the production path — servePostgres always passes nil.
	var handler http.Handler = mux
	if outerMiddleware != nil {
		handler = outerMiddleware(mux)
	}
	return http.ListenAndServe(addr, handler)
}
