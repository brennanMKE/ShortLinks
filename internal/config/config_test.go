package config

import (
	"strings"
	"testing"
)

// noEnvFile is a path that is guaranteed not to exist, so loadFromFile reads
// purely from the environment set via t.Setenv (no real .env interference).
const noEnvFile = "testdata/does-not-exist.env"

// setRequired sets every required variable to a valid value via t.Setenv so a
// test can start from a known-good state and then mutate individual vars.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("WEBAUTHN_RP_ID", "go.sstools.co")
	t.Setenv("WEBAUTHN_RP_ORIGIN", "https://go.sstools.co")
	t.Setenv("SESSION_SECRET", "deadbeef")
	t.Setenv("ADMIN_EMAIL", "admin@example.com")
}

func TestLoad_AllRequiredPresent(t *testing.T) {
	setRequired(t)
	// Set every optional/typed field explicitly to verify parsing.
	t.Setenv("PORT", "9090")
	t.Setenv("BASE_URL", "https://example.com")
	t.Setenv("SES_SMTP_HOST", "smtp.example.com")
	t.Setenv("SES_SMTP_PORT", "2525")
	t.Setenv("SES_SMTP_USERNAME", "user")
	t.Setenv("SES_SMTP_PASSWORD", "secret")
	t.Setenv("EMAIL_FROM", "ShortLinks <noreply@example.com>")
	t.Setenv("CACHE_MAX_COST", "50000")
	t.Setenv("CACHE_TTL_SECONDS", "600")

	cfg, err := loadFromFile(noEnvFile)
	if err != nil {
		t.Fatalf("loadFromFile returned error: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Port", cfg.Port, 9090},
		{"BaseURL", cfg.BaseURL, "https://example.com"},
		{"DatabaseURL", cfg.DatabaseURL, "postgres://u:p@localhost:5432/db?sslmode=disable"},
		{"WebAuthnRPID", cfg.WebAuthnRPID, "go.sstools.co"},
		{"WebAuthnRPOrigin", cfg.WebAuthnRPOrigin, "https://go.sstools.co"},
		{"SessionSecret", cfg.SessionSecret, "deadbeef"},
		{"SESSmtpHost", cfg.SESSmtpHost, "smtp.example.com"},
		{"SESSmtpPort", cfg.SESSmtpPort, 2525},
		{"SESSmtpUsername", cfg.SESSmtpUsername, "user"},
		{"SESSmtpPassword", cfg.SESSmtpPassword, "secret"},
		{"EmailFrom", cfg.EmailFrom, "ShortLinks <noreply@example.com>"},
		{"CacheMaxCost", cfg.CacheMaxCost, 50000},
		{"CacheTTLSeconds", cfg.CacheTTLSeconds, 600},
		{"AdminEmail", cfg.AdminEmail, "admin@example.com"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	setRequired(t)
	// Leave PORT, SES_SMTP_PORT, CACHE_MAX_COST, CACHE_TTL_SECONDS unset.

	cfg, err := loadFromFile(noEnvFile)
	if err != nil {
		t.Fatalf("loadFromFile returned error: %v", err)
	}

	if cfg.Port != defaultPort {
		t.Errorf("Port = %d, want default %d", cfg.Port, defaultPort)
	}
	if cfg.SESSmtpPort != defaultSESSmtpPort {
		t.Errorf("SESSmtpPort = %d, want default %d", cfg.SESSmtpPort, defaultSESSmtpPort)
	}
	if cfg.CacheMaxCost != defaultCacheMaxCost {
		t.Errorf("CacheMaxCost = %d, want default %d", cfg.CacheMaxCost, defaultCacheMaxCost)
	}
	if cfg.CacheTTLSeconds != defaultCacheTTLSeconds {
		t.Errorf("CacheTTLSeconds = %d, want default %d", cfg.CacheTTLSeconds, defaultCacheTTLSeconds)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	tests := []struct {
		name      string
		unset     string // required var to leave empty
		wantInErr string // substring expected in the error
	}{
		{"missing DATABASE_URL", "DATABASE_URL", "DATABASE_URL"},
		{"missing WEBAUTHN_RP_ID", "WEBAUTHN_RP_ID", "WEBAUTHN_RP_ID"},
		{"missing WEBAUTHN_RP_ORIGIN", "WEBAUTHN_RP_ORIGIN", "WEBAUTHN_RP_ORIGIN"},
		{"missing SESSION_SECRET", "SESSION_SECRET", "SESSION_SECRET"},
		{"missing ADMIN_EMAIL", "ADMIN_EMAIL", "ADMIN_EMAIL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tt.unset, "")

			cfg, err := loadFromFile(noEnvFile)
			if err == nil {
				t.Fatalf("expected error for missing %s, got nil (cfg=%+v)", tt.unset, cfg)
			}
			if !strings.Contains(err.Error(), tt.wantInErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantInErr)
			}
		})
	}
}

func TestLoad_MissingRequiredReportsAll(t *testing.T) {
	setRequired(t)
	// Clear two required vars; the error must mention both.
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SESSION_SECRET", "")

	_, err := loadFromFile(noEnvFile)
	if err == nil {
		t.Fatal("expected error when multiple required vars missing, got nil")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") || !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Errorf("error = %q, want both DATABASE_URL and SESSION_SECRET listed", err.Error())
	}
}

func TestLoad_InvalidInteger(t *testing.T) {
	tests := []struct {
		name string
		envK string
	}{
		{"invalid PORT", "PORT"},
		{"invalid SES_SMTP_PORT", "SES_SMTP_PORT"},
		{"invalid CACHE_MAX_COST", "CACHE_MAX_COST"},
		{"invalid CACHE_TTL_SECONDS", "CACHE_TTL_SECONDS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tt.envK, "not-a-number")

			_, err := loadFromFile(noEnvFile)
			if err == nil {
				t.Fatalf("expected error for invalid %s, got nil", tt.envK)
			}
			if !strings.Contains(err.Error(), tt.envK) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.envK)
			}
		})
	}
}
