// Package idp embeds the Portiko identity provider in the ShortLinks binary,
// served under /id on the instance's own host (#0126, #0130).
//
// Everything here is behind PORTIKO_ENABLED, which is off by default. cmd/
// shortlinks calls Mount once; with the switch off Mount returns the handler it
// was given untouched, so an instance that has not opted in behaves exactly as
// before. With it on, ShortLinks signs in through the provider as a relying party (#0132, portal.go).
package idp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Kolonnade/Portiko/provider"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
)

// Prefix is where the provider is mounted, relative to BASE_URL.
const Prefix = "/id"

// ClientID is the OAuth client_id ShortLinks registers for itself.
const ClientID = "shortlinks"

// Schema is the Postgres schema the provider's tables live in unless
// PORTIKO_DATABASE_URL says otherwise. Portiko has its own users and
// schema_migrations tables, so it must not share public with ShortLinks.
const Schema = "portiko"

// StartupTimeout bounds creating the schema, migrating it, building the
// provider and registering the client, so a slow or wedged database cannot hold
// the instance's own startup.
const StartupTimeout = 30 * time.Second

// Mount starts the provider and returns next with /id and /id/... routed to it.
// With cfg.PortikoEnabled false it returns next unchanged, a no-op closer and
// no error, and touches neither the database nor the network.
//
// A provider that fails to start never takes the instance down: the error is
// logged and /id and /id/... answer 503, while every other route is served by
// next exactly as before. The returned error is therefore always nil; it stays
// in the signature so a caller that wants to treat it as fatal can.
//
// The returned closer releases the provider's connection pool.
func Mount(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, next http.Handler) (http.Handler, func(), error) {
	if !cfg.PortikoEnabled {
		return next, func() {}, nil
	}
	return MountPortal(ctx, cfg, pool, next, NewPortal(cfg, pool))
}

// MountPortal is Mount with the Portal (#0132) that the route table's guards
// were built from. It starts the provider, binds the Portal's relying party to
// it, and serves the Portiko sign-in routes in front of next. With the switch
// off it returns next untouched.
func MountPortal(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, next http.Handler, portal *Portal) (http.Handler, func(), error) {
	if !cfg.PortikoEnabled {
		return next, func() {}, nil
	}
	next = portal.Wrap(next)
	startCtx, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	p, err := start(startCtx, cfg, pool)
	if err != nil {
		slog.Error("portiko provider failed to start; serving ShortLinks without it, and /id answers 503", "err", err)
		return mountAt(unavailable(), next), func() {}, nil
	}
	go p.Sweep(ctx)
	if err := portal.bind(startCtx, p.Handler()); err != nil {
		// The provider is up but signing in through it is not: the guards answer 503.
		slog.Error("portiko sign-in failed to start; protected routes answer 503", "err", err)
	} else if st := portal.state.Load(); st != nil {
		go st.sessions.RunPruner(ctx, time.Hour)
	}
	return mountAt(p.Handler(), next), p.Close, nil
}

// unavailable answers every /id request with 503.
func unavailable() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "identity provider unavailable", http.StatusServiceUnavailable)
	})
}

// start builds the provider; ctx carries StartupTimeout.
func start(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool) (*provider.Provider, error) {
	dbURL := cfg.PortikoDatabaseURL
	if dbURL == "" {
		var err error
		if dbURL, err = DeriveDatabaseURL(cfg.DatabaseURL); err != nil {
			return nil, err
		}
	}
	if err := ensureSchema(ctx, dbURL); err != nil {
		return nil, err
	}
	if _, err := provider.Migrate(ctx, dbURL); err != nil {
		return nil, fmt.Errorf("idp: migrating the portiko schema: %w", err)
	}

	base := strings.TrimSuffix(cfg.BaseURL, "/")
	pcfg := &provider.Config{
		Issuer:            base + Prefix,
		DatabaseURL:       dbURL,
		ServiceName:       "ShortLinks",
		WebAuthnRPID:      cfg.WebAuthnRPID,
		WebAuthnRPOrigins: []string{cfg.WebAuthnRPOrigin},
		CryptoKey:         cfg.PortikoCryptoKey,
		TrustedProxies:    cfg.PortikoTrustedProxies,
		// An http:// BASE_URL is local development; Portiko's own Load refuses
		// this flag for an https issuer, so production never runs with it.
		DevInsecure: strings.HasPrefix(base, "http://"),
	}

	opts := provider.Options{
		RegistrationPolicy: registrationPolicy(auth.NewStore(pool)),
	}
	// With SES credentials, mail leaves through the same account as the rest of
	// ShortLinks' email. Without them Portiko's stdout mailer is the default.
	if cfg.SESSmtpUsername != "" && cfg.SESSmtpPassword != "" {
		opts.Mailer = NewMailer(auth.NewSESMailer(cfg), "ShortLinks")
	}

	p, err := provider.New(ctx, pcfg, opts)
	if err != nil {
		return nil, fmt.Errorf("idp: starting the provider: %w", err)
	}
	if err := p.RegisterClient(ctx, provider.Client{
		ID:                     ClientID,
		Secret:                 cfg.PortikoClientSecret,
		Audience:               base,
		DisplayName:            "ShortLinks",
		RedirectURIs:           []string{base + "/auth/portiko/callback"},
		PostLogoutRedirectURIs: []string{base + "/"},
		BackchannelLogoutURI:   base + "/auth/portiko/backchannel",
		EventsURI:              base + "/auth/portiko/events",
		// email is what #0132's OnSignIn maps a sign-in onto a users row with.
		Scopes:     []string{"openid", "profile", "email", "offline_access"},
		FirstParty: true,
	}); err != nil {
		p.Close()
		return nil, fmt.Errorf("idp: registering the ShortLinks client: %w", err)
	}
	slog.Info("portiko provider enabled", "issuer", pcfg.Issuer)
	return p, nil
}

// mountAt routes /id and /id/... to the provider and everything else to next.
// It compares the raw request path rather than using a ServeMux, so a path that
// is not the provider's reaches next byte for byte as it would have without the
// provider (a ServeMux would clean and redirect it). The provider's handler
// strips the prefix itself, so it must not be wrapped in StripPrefix.
func mountAt(prov, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == Prefix || strings.HasPrefix(r.URL.Path, Prefix+"/") {
			prov.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// registrationPolicy hands ShortLinks' runtime registrations_enabled toggle to
// the provider (#0129), read fresh on every call so an admin change applies at
// once.
func registrationPolicy(s *auth.Store) provider.RegistrationPolicy {
	return func(ctx context.Context, _ string) (bool, string, error) {
		on, err := s.RegistrationsEnabled(ctx)
		if err != nil {
			return false, "", err
		}
		return on, "Registration is currently closed.", nil
	}
}

// DeriveDatabaseURL returns databaseURL with search_path=portiko, so the
// provider's tables (including its own schema_migrations) are created in their
// own schema and never in public. It accepts a postgres:// URL or a key=value
// connection string, and replaces any search_path already present.
func DeriveDatabaseURL(databaseURL string) (string, error) {
	if databaseURL == "" {
		return "", fmt.Errorf("idp: DATABASE_URL is empty, so PORTIKO_DATABASE_URL must be set")
	}
	if strings.Contains(databaseURL, "://") {
		u, err := url.Parse(databaseURL)
		if err != nil {
			return "", fmt.Errorf("idp: DATABASE_URL is not a URL: %w", err)
		}
		// Append to the raw query rather than re-encoding it, so an existing
		// value keeps its exact bytes. A later search_path wins, but drop any
		// earlier one so the result is unambiguous.
		var keep []string
		for _, kv := range strings.Split(u.RawQuery, "&") {
			if kv != "" && !strings.HasPrefix(kv, "search_path=") {
				keep = append(keep, kv)
			}
		}
		u.RawQuery = strings.Join(append(keep, "search_path="+Schema), "&")
		return u.String(), nil
	}
	return databaseURL + " search_path=" + Schema, nil
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ensureSchema creates the schema named by the URL's search_path if it is
// missing. A URL with no search_path (a separate database) needs nothing.
func ensureSchema(ctx context.Context, databaseURL string) error {
	cc, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("idp: parsing the portiko database URL: %w", err)
	}
	schema := strings.TrimSpace(strings.Split(cc.RuntimeParams["search_path"], ",")[0])
	if schema == "" {
		return nil
	}
	if !identifier.MatchString(schema) {
		return fmt.Errorf("idp: search_path %q is not a plain lowercase schema name", schema)
	}
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return fmt.Errorf("idp: connecting to create the schema: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return fmt.Errorf("idp: creating schema %s: %w", schema, err)
	}
	return nil
}
