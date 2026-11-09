package clicks

import (
	"bytes"
	"testing"
	"time"
)

func mustHasher(t *testing.T, secret string) *IPHasher {
	t.Helper()
	h, err := NewIPHasher(secret)
	if err != nil {
		t.Fatalf("NewIPHasher: %v", err)
	}
	return h
}

func TestNewIPHasher_EmptySecretErrors(t *testing.T) {
	if _, err := NewIPHasher(""); err == nil {
		t.Fatal("want error for empty secret")
	}
}

func TestIPHasher_DeterministicAndLength(t *testing.T) {
	h := mustHasher(t, "s")
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	a, b := h.HashAt("203.0.113.7", at), h.HashAt("203.0.113.7", at.Add(time.Hour))
	if len(a) != 32 || !bytes.Equal(a, b) {
		t.Fatalf("want identical 32-byte hashes within a day, got %x vs %x", a, b)
	}
	if bytes.Equal(a, h.HashAt("203.0.113.8", at)) {
		t.Error("different IPs should hash differently")
	}
}

func TestIPHasher_DiffersAcrossDaysAndSecrets(t *testing.T) {
	h := mustHasher(t, "s")
	d1 := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if bytes.Equal(h.HashAt("203.0.113.7", d1), h.HashAt("203.0.113.7", d1.AddDate(0, 0, 1))) {
		t.Error("hash should differ across days")
	}
	if bytes.Equal(h.HashAt("203.0.113.7", d1), mustHasher(t, "other").HashAt("203.0.113.7", d1)) {
		t.Error("hash should differ across secrets")
	}
}

func TestIPHasher_UTCDayBoundary(t *testing.T) {
	h := mustHasher(t, "s")
	before := time.Date(2026, 11, 8, 23, 59, 59, 0, time.UTC)
	after := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	if bytes.Equal(h.HashAt("203.0.113.7", before), h.HashAt("203.0.113.7", after)) {
		t.Error("hash should change at UTC midnight")
	}
	// Same instant expressed in another zone must give the same hash.
	est := time.FixedZone("x", -5*3600)
	if !bytes.Equal(h.HashAt("203.0.113.7", before), h.HashAt("203.0.113.7", before.In(est))) {
		t.Error("hash must depend on the UTC day only")
	}
}

func TestIPHasher_Normalization(t *testing.T) {
	h := mustHasher(t, "s")
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	if !bytes.Equal(h.HashAt("::ffff:203.0.113.7", at), h.HashAt("203.0.113.7", at)) {
		t.Error("IPv4-mapped IPv6 should equal plain IPv4")
	}
	if !bytes.Equal(h.HashAt("fe80::1%eth0", at), h.HashAt("fe80::1", at)) {
		t.Error("zone should be stripped")
	}
}

func TestIPHasher_InvalidInputIsNil(t *testing.T) {
	h := mustHasher(t, "s")
	for _, in := range []string{"", "garbage", "203.0.113.7:1234"} {
		if got := h.Hash(in); got != nil {
			t.Errorf("Hash(%q) = %x, want nil", in, got)
		}
	}
}
