package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Default values applied when an optional variable is unset.
const (
	defaultPort             = 8080
	defaultSESSmtpPort      = 587
	defaultCacheMaxCost     = 10000
	defaultCacheTTLSeconds  = 300
	defaultTimezone         = "UTC"
	defaultRedirectRate     = 120
	defaultRedirectMissRate = 20
)

// Config holds all runtime configuration loaded from the environment.
type Config struct {
	// Server
	Port int
	// BaseURL is the deployment's public base URL (BASE_URL), required
	// unconditionally by Load. It is the single source for every externally
	// visible URL the app produces: the short links the SPA displays and
	// copies (served to it via GET /api/me), the payload internal/qr encodes
	// into QR codes, the short-URL column of the campaign CSV export, and the
	// verification/recovery links in outgoing email (#0117). Callers should
	// tolerate a trailing slash; the handlers that hand it out or build on it
	// trim one.
	BaseURL string

	// Database
	DatabaseURL string

	// Storage backend selector. ONLY the explicit value "json" selects the
	// in-memory dev backend (see DevMode); anything else — including the default
	// empty value — uses Postgres. An empty DATABASE_URL does NOT engage dev mode,
	// so production can't accidentally fall back to the in-memory store.
	Storage string

	// WebAuthn
	WebAuthnRPID     string
	WebAuthnRPOrigin string

	// Sessions. SessionSecret also derives the click-IP hashing key (#0121).
	SessionSecret string

	// Email (AWS SES SMTP)
	SESSmtpHost     string
	SESSmtpPort     int
	SESSmtpUsername string
	SESSmtpPassword string
	EmailFrom       string

	// Cache
	CacheMaxCost    int
	CacheTTLSeconds int

	// Bootstrap
	AdminEmail string

	// DefaultTimezone (DEFAULT_TIMEZONE) is the IANA zone the reports API uses
	// to bucket days when a request carries no tz parameter (#0139). Optional,
	// validated at load, "UTC" when unset.
	DefaultTimezone string

	// Portiko (#0130): the embedded identity provider served under /id. Every
	// field is inert unless PortikoEnabled is true, which is the default
	// (PORTIKO_ENABLED unset or "false"), so a binary built with this code
	// behaves exactly as before on an instance that has not opted in.
	PortikoEnabled bool
	// PortikoCryptoKey (PORTIKO_CRYPTO_KEY) seals the provider's signing keys
	// and authorization codes: 32 bytes as 64 hex characters or base64.
	// Required when enabled. It must be stable across restarts.
	PortikoCryptoKey [32]byte
	// PortikoClientSecret (PORTIKO_CLIENT_SECRET) is the secret ShortLinks
	// itself signs in with, registered at every start. Required when enabled.
	PortikoClientSecret string
	// PortikoDatabaseURL (PORTIKO_DATABASE_URL) defaults to DATABASE_URL with
	// search_path=portiko, so the provider's tables live in their own schema.
	PortikoDatabaseURL string
	// PortikoTrustedProxies (PORTIKO_TRUSTED_PROXIES, comma-separated IPs or
	// CIDRs) are the proxies whose X-Forwarded-For the provider believes, so its
	// rate limits see real client addresses. Behind Apache on the same host this
	// is "127.0.0.1,::1".
	PortikoTrustedProxies []netip.Prefix

	// RedirectRatePerMin / RedirectMissRatePerMin (#0178) cap GET /u/{key}
	// per client IP: all requests, and 404/410 responses only.
	RedirectRatePerMin     int
	RedirectMissRatePerMin int
}

// DevMode reports whether the dev store should be used instead of Postgres.
// It is true only when STORAGE=json is explicitly set. DATABASE_URL remains
// required in non-dev mode; set STORAGE=json to skip the DB entirely.
func (c *Config) DevMode() bool {
	return c.Storage == "json"
}

// Load reads configuration from the environment. In development it first loads
// a .env file (via godotenv) if one is present in the current working
// directory; a missing .env file is not an error, since the variables may
// already be set in the process environment (e.g. by systemd in production).
//
// Defaults are applied for unset optional variables, integer fields are parsed,
// and required fields are validated. If any required field is missing or any
// integer field fails to parse, Load returns an error describing every problem
// it found rather than just the first.
func Load() (*Config, error) {
	return loadFromFile(".env")
}

// loadFromFile is the internal implementation of Load with an explicit .env
// path, kept unexported so tests can avoid touching the real working directory.
func loadFromFile(path string) (*Config, error) {
	// Load .env if present. A missing file is not an error; any other error
	// (e.g. malformed file) is surfaced to the caller.
	if _, statErr := os.Stat(path); statErr == nil {
		if err := godotenv.Load(path); err != nil {
			return nil, fmt.Errorf("config: loading %s: %w", path, err)
		}
	}

	var errs []string

	cfg := &Config{
		Port:             getInt("PORT", defaultPort, &errs),
		BaseURL:          os.Getenv("BASE_URL"),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		Storage:          os.Getenv("STORAGE"),
		WebAuthnRPID:     os.Getenv("WEBAUTHN_RP_ID"),
		WebAuthnRPOrigin: os.Getenv("WEBAUTHN_RP_ORIGIN"),
		SessionSecret:    os.Getenv("SESSION_SECRET"),
		SESSmtpHost:      os.Getenv("SES_SMTP_HOST"),
		SESSmtpPort:      getInt("SES_SMTP_PORT", defaultSESSmtpPort, &errs),
		SESSmtpUsername:  os.Getenv("SES_SMTP_USERNAME"),
		SESSmtpPassword:  os.Getenv("SES_SMTP_PASSWORD"),
		EmailFrom:        os.Getenv("EMAIL_FROM"),
		CacheMaxCost:     getInt("CACHE_MAX_COST", defaultCacheMaxCost, &errs),
		CacheTTLSeconds:  getInt("CACHE_TTL_SECONDS", defaultCacheTTLSeconds, &errs),
		AdminEmail:       os.Getenv("ADMIN_EMAIL"),
		DefaultTimezone:  defaultTimezone,

		RedirectRatePerMin:     getInt("REDIRECT_RATE_PER_MIN", defaultRedirectRate, &errs),
		RedirectMissRatePerMin: getInt("REDIRECT_MISS_RATE_PER_MIN", defaultRedirectMissRate, &errs),
	}
	if cfg.RedirectRatePerMin < 1 {
		errs = append(errs, "REDIRECT_RATE_PER_MIN must be at least 1")
	}
	if cfg.RedirectMissRatePerMin > cfg.RedirectRatePerMin {
		errs = append(errs, "REDIRECT_MISS_RATE_PER_MIN must not exceed REDIRECT_RATE_PER_MIN")
	}
	if cfg.RedirectMissRatePerMin < 1 {
		errs = append(errs, "REDIRECT_MISS_RATE_PER_MIN must be at least 1")
	}

	if tz := strings.TrimSpace(os.Getenv("DEFAULT_TIMEZONE")); tz != "" {
		if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
			errs = append(errs, fmt.Sprintf("invalid IANA time zone for DEFAULT_TIMEZONE: %q", tz))
		} else {
			cfg.DefaultTimezone = tz
		}
	}

	// Validate required fields. When STORAGE=json (dev mode) DATABASE_URL is
	// not required; in all other cases every variable below must be present.
	// Collect every missing required variable so the error is comprehensive.
	devMode := cfg.DevMode()
	required := requiredFields(cfg)
	for _, r := range required {
		if r.value == "" && !(r.skipInDev && devMode) {
			errs = append(errs, fmt.Sprintf("missing required variable %s", r.name))
		}
	}

	loadPortiko(cfg, &errs)

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}

	return cfg, nil
}

// loadPortiko reads the PORTIKO_* settings (#0130). With PORTIKO_ENABLED unset
// or false it reads nothing else and reports no error, so a stray or malformed
// PORTIKO_* value cannot stop an instance that has not opted in from booting.
func loadPortiko(cfg *Config, errs *[]string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PORTIKO_ENABLED"))) {
	case "", "false", "0":
		return
	case "true", "1":
		cfg.PortikoEnabled = true
	default:
		*errs = append(*errs, fmt.Sprintf("invalid boolean for PORTIKO_ENABLED: %q (use true or false)", os.Getenv("PORTIKO_ENABLED")))
		return
	}
	if cfg.DevMode() {
		*errs = append(*errs, "PORTIKO_ENABLED=true needs Postgres; it cannot be combined with STORAGE=json")
	}
	key, err := decodePortikoKey(os.Getenv("PORTIKO_CRYPTO_KEY"))
	if err != nil {
		*errs = append(*errs, "PORTIKO_CRYPTO_KEY "+err.Error()+" (generate one with: openssl rand -hex 32)")
	}
	cfg.PortikoCryptoKey = key
	cfg.PortikoClientSecret = os.Getenv("PORTIKO_CLIENT_SECRET")
	if len(cfg.PortikoClientSecret) < 16 {
		*errs = append(*errs, "PORTIKO_CLIENT_SECRET is required when PORTIKO_ENABLED=true and must be at least 16 characters (generate one with: openssl rand -hex 32)")
	}
	cfg.PortikoDatabaseURL = os.Getenv("PORTIKO_DATABASE_URL")
	for _, p := range strings.Split(os.Getenv("PORTIKO_TRUSTED_PROXIES"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(p); err == nil {
			cfg.PortikoTrustedProxies = append(cfg.PortikoTrustedProxies, prefix)
		} else if addr, err := netip.ParseAddr(p); err == nil {
			cfg.PortikoTrustedProxies = append(cfg.PortikoTrustedProxies, netip.PrefixFrom(addr, addr.BitLen()))
		} else {
			*errs = append(*errs, fmt.Sprintf("PORTIKO_TRUSTED_PROXIES: %q is not an IP address or CIDR", p))
		}
	}
}

// decodePortikoKey accepts 32 bytes as 64 hex characters or as base64.
func decodePortikoKey(v string) ([32]byte, error) {
	var key [32]byte
	v = strings.TrimSpace(v)
	if v == "" {
		return key, fmt.Errorf("is required when PORTIKO_ENABLED=true")
	}
	if b, err := hex.DecodeString(v); err == nil && len(b) == 32 {
		copy(key[:], b)
		return key, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(v); err == nil && len(b) == 32 {
			copy(key[:], b)
			return key, nil
		}
	}
	return key, fmt.Errorf("must be 32 bytes as 64 hex characters or base64")
}

// requiredField describes one environment variable Load insists on.
type requiredField struct {
	name      string
	value     string
	skipInDev bool // when true, skip this check in dev mode
}

// requiredFields is the single source of truth for which variables Load
// requires. Tests derive their fixtures from it so they cannot drift.
func requiredFields(cfg *Config) []requiredField {
	return []requiredField{
		{"BASE_URL", cfg.BaseURL, false},
		{"DATABASE_URL", cfg.DatabaseURL, true}, // not required when STORAGE=json
		{"WEBAUTHN_RP_ID", cfg.WebAuthnRPID, false},
		{"WEBAUTHN_RP_ORIGIN", cfg.WebAuthnRPOrigin, false},
		{"SESSION_SECRET", cfg.SessionSecret, false},
		{"ADMIN_EMAIL", cfg.AdminEmail, false},
	}
}

// getInt reads an integer environment variable, returning def when the variable
// is unset or empty. If the variable is set but cannot be parsed as an integer,
// an error message is appended to errs and def is returned.
func getInt(name string, def int, errs *[]string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("invalid integer for %s: %q", name, raw))
		return def
	}
	return v
}
