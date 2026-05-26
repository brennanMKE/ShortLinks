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
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
	"github.com/brennanMKE/ShortLinks/internal/events"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/handlers"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

const version = "0.1.0"

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

// serve loads configuration, connects the database pool, mounts the routes, and
// listens on the configured port until the process is terminated.
func serve() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

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
	loginSvc := auth.NewLoginService(store, wa, auditLogger, slog.Default())
	recoverSvc := auth.NewRecoveryService(store, wa, mailer, auditLogger)
	authH := handlers.NewAuthHandler(regSvc, loginSvc, recoverSvc)
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

	// Link CRUD API (#0022). The links store reuses the shared pgx pool. The
	// redirect cache constructed above is now wired as the cache-evictor so a
	// PATCH/DELETE drops the key and the next redirect re-reads the DB. The rule
	// cache is wired so the #0024 URL filter check runs at the top of Create, the
	// broker so a successful create broadcasts the #0026 link.created SSE event,
	// and the stats store so GET /api/links/{key} returns the #0030 utm_stats.
	linksH := handlers.NewLinksHandler(linkStore, redirectCache, ruleCache, auditLogger, broker, statsStore)

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

	// Per-IP rate limiters for the abuse-prone public auth endpoints (PRD Phase
	// 2). Burst equals the per-window allowance so a fresh IP gets its full
	// quota immediately, then refills at the sustained rate.
	registerLimiter := middleware.NewRateLimiter(rate.Every(time.Hour/3), 3)  // 3 / hour / IP
	loginLimiter := middleware.NewRateLimiter(rate.Every(time.Minute/10), 10) // 10 / minute / IP
	recoverLimiter := middleware.NewRateLimiter(rate.Every(time.Hour/3), 3)   // 3 / hour / IP

	mux := http.NewServeMux()
	mux.Handle("GET /health", handlers.NewHealthHandler(pool))

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

	// Current user profile (#0027) — behind RequireSession; returns the caller's
	// {id, email, is_admin} for the SPA to gate the admin view.
	mux.Handle("GET /api/me", requireSession(http.HandlerFunc(meH.Me)))

	// SSE stream (#0026) — behind RequireSession; pushes link.created events to
	// the authenticated user's connected dashboard clients.
	mux.Handle("GET /api/events", requireSession(http.HandlerFunc(eventsH.Stream)))

	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("shortlinks %s listening on %s", version, addr)
	return http.ListenAndServe(addr, mux)
}
