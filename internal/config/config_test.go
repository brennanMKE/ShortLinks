package config

import (
	"strings"
	"testing"
)

// noEnvFile is a path that is guaranteed not to exist, so loadFromFile reads
// purely from the environment set via t.Setenv (no real .env interference).
const noEnvFile = "testdata/does-not-exist.env"

// validRequired holds a valid value for each required variable. setRequired
// walks requiredFields (the list Load uses) and looks each name up here, so a
// newly required variable fails the tests with a message naming it instead of
// silently drifting.
var validRequired = map[string]string{
	"BASE_URL":           "https://go.sstools.co",
	"DATABASE_URL":       "postgres://u:p@localhost:5432/db?sslmode=disable",
	"WEBAUTHN_RP_ID":     "go.sstools.co",
	"WEBAUTHN_RP_ORIGIN": "https://go.sstools.co",
	"SESSION_SECRET":     "deadbeef",
	"ADMIN_EMAIL":        "admin@example.com",
}

// setRequired sets every variable Load requires to a valid value via t.Setenv
// so a test can start from a known-good state and then mutate individual vars.
func setRequired(t *testing.T) {
	t.Helper()
	for _, f := range requiredFields(&Config{}) {
		v, ok := validRequired[f.name]
		if !ok {
			t.Fatalf("Load now requires %s: add a valid value to validRequired in config_test.go", f.name)
		}
		t.Setenv(f.name, v)
	}
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
	type tc struct {
		name      string
		unset     string // required var to leave empty
		wantInErr string // substring expected in the error
	}
	var tests []tc
	for _, f := range requiredFields(&Config{}) {
		tests = append(tests, tc{"missing " + f.name, f.name, f.name})
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

// TestLoad_DefaultTimezone covers DEFAULT_TIMEZONE (#0139): unset and empty
// default to UTC, a valid IANA name is kept, an unknown name fails Load.
// It sets BASE_URL itself so it does not depend on the shared fixture.
func TestLoad_DefaultTimezone(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    string
		wantErr bool
	}{
		{"unset", "", "UTC", false},
		{"valid", "America/Los_Angeles", "America/Los_Angeles", false},
		{"padded", "  Europe/Berlin ", "Europe/Berlin", false},
		{"invalid", "Mars/Olympus", "", true},
		{"Local rejected", "Local", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("BASE_URL", "https://example.com")
			t.Setenv("DEFAULT_TIMEZONE", tc.env)
			cfg, err := loadFromFile(noEnvFile)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "DEFAULT_TIMEZONE") {
					t.Fatalf("err = %v, want DEFAULT_TIMEZONE error", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.DefaultTimezone != tc.want {
				t.Errorf("DefaultTimezone = %q, want %q", cfg.DefaultTimezone, tc.want)
			}
		})
	}
}
