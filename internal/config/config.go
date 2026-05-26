package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Default values applied when an optional variable is unset.
const (
	defaultPort            = 8080
	defaultSESSmtpPort     = 587
	defaultCacheMaxCost    = 10000
	defaultCacheTTLSeconds = 300
)

// Config holds all runtime configuration loaded from the environment.
type Config struct {
	// Server
	Port    int
	BaseURL string

	// Database
	DatabaseURL string

	// WebAuthn
	WebAuthnRPID     string
	WebAuthnRPOrigin string

	// Sessions
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
	}

	// Validate required fields. Collect every missing one.
	required := []struct {
		name  string
		value string
	}{
		{"BASE_URL", cfg.BaseURL},
		{"DATABASE_URL", cfg.DatabaseURL},
		{"WEBAUTHN_RP_ID", cfg.WebAuthnRPID},
		{"WEBAUTHN_RP_ORIGIN", cfg.WebAuthnRPOrigin},
		{"SESSION_SECRET", cfg.SessionSecret},
		{"ADMIN_EMAIL", cfg.AdminEmail},
	}
	for _, r := range required {
		if r.value == "" {
			errs = append(errs, fmt.Sprintf("missing required variable %s", r.name))
		}
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}

	return cfg, nil
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
