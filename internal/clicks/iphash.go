package clicks

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"net/netip"
	"strings"
	"time"
)

const (
	ipKeyLabel  = "shortlinks click-ip key v1"
	ipSaltLabel = "shortlinks click-ip salt v1\x00"
)

// IPHasher turns a visitor IP into a keyed, daily-rotating one-way hash (#0121).
// The same IP hashes identically within one UTC day (so repeat clicks can be
// counted) and differently on every other day, so hashes cannot be linked across
// days or reversed by enumerating the IPv4 space without the key.
type IPHasher struct {
	key []byte
}

// NewIPHasher derives the hashing key from secret (SESSION_SECRET) as
// HMAC-SHA256(secret, "shortlinks click-ip key v1"), so it is independent of
// the cookie-signing use of the same secret. An empty secret is an error.
func NewIPHasher(secret string) (*IPHasher, error) {
	if secret == "" {
		return nil, errors.New("clicks: IP hash secret must not be empty")
	}
	return &IPHasher{key: hmacSHA256([]byte(secret), []byte(ipKeyLabel))}, nil
}

// HashAt returns the 32-byte hash of ip using the salt for at's UTC day, or nil
// when ip is empty or not a bare IP address. IPv4-mapped IPv6 addresses hash as
// their IPv4 form and zones are ignored.
func (h *IPHasher) HashAt(ip string, at time.Time) []byte {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return nil
	}
	addr = addr.Unmap().WithZone("")
	day := at.UTC().Format("2006-01-02")
	salt := hmacSHA256(h.key, []byte(ipSaltLabel+day))
	return hmacSHA256(salt, []byte(addr.String()))
}

// Hash is HashAt for the current time.
func (h *IPHasher) Hash(ip string) []byte {
	return h.HashAt(ip, time.Now())
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}
