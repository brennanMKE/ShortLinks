package config

import (
	"strings"
	"testing"
)

const testPortikoKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// With the switch unset or false, no PORTIKO_* value is read or validated, so
// an instance that has not opted in cannot be stopped from booting by one.
func TestLoad_PortikoOffByDefault(t *testing.T) {
	setRequired(t)
	t.Setenv("PORTIKO_CRYPTO_KEY", "not a key")
	t.Setenv("PORTIKO_TRUSTED_PROXIES", "garbage")
	for _, v := range []string{"", "false", "0"} {
		t.Setenv("PORTIKO_ENABLED", v)
		cfg, err := loadFromFile(noEnvFile)
		if err != nil {
			t.Fatalf("PORTIKO_ENABLED=%q: %v", v, err)
		}
		if cfg.PortikoEnabled || cfg.PortikoClientSecret != "" || cfg.PortikoCryptoKey != [32]byte{} {
			t.Errorf("PORTIKO_ENABLED=%q: Portiko settings were read: %+v", v, cfg)
		}
	}
}

func TestLoad_PortikoEnabled(t *testing.T) {
	setRequired(t)
	t.Setenv("PORTIKO_ENABLED", "true")
	t.Setenv("PORTIKO_CRYPTO_KEY", testPortikoKey)
	t.Setenv("PORTIKO_CLIENT_SECRET", "0123456789abcdef0123")
	t.Setenv("PORTIKO_TRUSTED_PROXIES", "127.0.0.1, ::1, 10.0.0.0/8")
	cfg, err := loadFromFile(noEnvFile)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.PortikoEnabled || cfg.PortikoCryptoKey[0] != 0x00 || cfg.PortikoCryptoKey[31] != 0xff {
		t.Errorf("unexpected config: %+v", cfg)
	}
	if len(cfg.PortikoTrustedProxies) != 3 || cfg.PortikoTrustedProxies[0].String() != "127.0.0.1/32" {
		t.Errorf("trusted proxies = %v", cfg.PortikoTrustedProxies)
	}
	if cfg.PortikoDatabaseURL != "" {
		t.Errorf("PortikoDatabaseURL should stay empty so the default is derived: %q", cfg.PortikoDatabaseURL)
	}
}

func TestLoad_PortikoEnabledNeedsKeyAndSecret(t *testing.T) {
	setRequired(t)
	t.Setenv("PORTIKO_ENABLED", "true")
	_, err := loadFromFile(noEnvFile)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"PORTIKO_CRYPTO_KEY", "PORTIKO_CLIENT_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	t.Setenv("PORTIKO_CRYPTO_KEY", "abcd")
	t.Setenv("PORTIKO_CLIENT_SECRET", "0123456789abcdef0123")
	if _, err := loadFromFile(noEnvFile); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Errorf("short key: %v", err)
	}
	t.Setenv("PORTIKO_ENABLED", "yes")
	if _, err := loadFromFile(noEnvFile); err == nil || !strings.Contains(err.Error(), "PORTIKO_ENABLED") {
		t.Errorf("bad boolean: %v", err)
	}
}

func TestLoad_PortikoRefusedInDevMode(t *testing.T) {
	setRequired(t)
	t.Setenv("STORAGE", "json")
	t.Setenv("PORTIKO_ENABLED", "true")
	t.Setenv("PORTIKO_CRYPTO_KEY", testPortikoKey)
	t.Setenv("PORTIKO_CLIENT_SECRET", "0123456789abcdef0123")
	if _, err := loadFromFile(noEnvFile); err == nil || !strings.Contains(err.Error(), "STORAGE=json") {
		t.Errorf("dev mode: %v", err)
	}
}

func TestDecodePortikoKeyBase64(t *testing.T) {
	k, err := decodePortikoKey("ABEiM0RVZneImaq7zN3u/wARIjNEVWZ3iJmqu8zd7v8=")
	if err != nil || k[0] != 0x00 || k[1] != 0x11 || k[31] != 0xff {
		t.Errorf("base64 key = %x, %v", k, err)
	}
}
