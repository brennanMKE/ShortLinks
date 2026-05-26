package cache

import (
	"time"

	"github.com/dgraph-io/ristretto/v2"
)

const (
	// DefaultMaxCost is the default maximum cache cost (entry count) when
	// CACHE_MAX_COST is unset. Matches the PRD default.
	DefaultMaxCost int64 = 10000

	// DefaultTTL is the default positive-entry TTL when CACHE_TTL_SECONDS is
	// unset. Matches the PRD default of 300 seconds.
	DefaultTTL = 300 * time.Second

	// NegativeTTL is the fixed, shorter TTL applied to negative entries (keys
	// not present in the database). A short TTL bounds how long an invalid key
	// is shielded from the DB while still absorbing bursts of bad lookups.
	NegativeTTL = 30 * time.Second

	// entryCost is the cost charged per cached entry. The cache is sized by
	// entry count (CACHE_MAX_COST), so every entry costs exactly one unit.
	entryCost int64 = 1
)

// CachedLink is the value stored for a short-link key. It is intentionally
// self-contained (no dependency on the links package) so that links can depend
// on cache and not the reverse.
//
// A negative entry — a key known to be absent from the database — is marked
// with Negative=true and carries no destination. Positive entries hold the
// resolved redirect data.
type CachedLink struct {
	// DestinationURL is the target the short link redirects to. Empty for a
	// negative entry.
	DestinationURL string

	// Active reports whether the link is currently active (not deactivated).
	Active bool

	// ExpiresAt is the link's expiry time, or nil if the link never expires.
	ExpiresAt *time.Time

	// DeniedReason is the non-zero denial reason code if the link was blocked
	// by URL filtering, or zero when the link is permitted.
	DeniedReason int16

	// Negative is true when this entry records the absence of a key in the
	// database (a negative cache hit), distinguishing it from a positive entry.
	Negative bool
}

// Cache is a Ristretto-backed cache mapping short-link keys to CachedLink
// values. It is safe for concurrent use.
type Cache struct {
	rc  *ristretto.Cache[string, *CachedLink]
	ttl time.Duration
}

// New constructs a Cache with the given maximum cost (entry count) and the TTL
// applied to positive entries. Negative entries always use NegativeTTL.
//
// If maxCost is non-positive, DefaultMaxCost is used. If ttl is non-positive,
// DefaultTTL is used.
func New(maxCost int64, ttl time.Duration) (*Cache, error) {
	if maxCost <= 0 {
		maxCost = DefaultMaxCost
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	rc, err := ristretto.NewCache(&ristretto.Config[string, *CachedLink]{
		// 10x MaxCost counters is the Ristretto-recommended ratio for good
		// eviction accuracy and hit ratios.
		NumCounters: maxCost * 10,
		MaxCost:     maxCost,
		BufferItems: 64,
	})
	if err != nil {
		return nil, err
	}

	return &Cache{rc: rc, ttl: ttl}, nil
}

// Get returns the cached entry for key and whether it was found. A returned
// entry may be negative (link.Negative == true); callers should treat a
// negative hit as "key does not exist" without consulting the database.
func (c *Cache) Get(key string) (*CachedLink, bool) {
	return c.rc.Get(key)
}

// Set stores a positive entry for key using the configured TTL.
func (c *Cache) Set(key string, link *CachedLink) {
	c.rc.SetWithTTL(key, link, entryCost, c.ttl)
}

// SetNegative stores a negative entry for key (a known-absent key) using the
// fixed NegativeTTL. The stored value has Negative=true and no destination.
func (c *Cache) SetNegative(key string) {
	c.rc.SetWithTTL(key, &CachedLink{Negative: true}, entryCost, NegativeTTL)
}

// Delete removes the entry for key. Call this when a link is deactivated,
// denied, or otherwise changed so the next lookup repopulates from the DB.
func (c *Cache) Delete(key string) {
	c.rc.Del(key)
}

// Wait blocks until all buffered writes have been applied. Ristretto's Set is
// asynchronous, so callers (and tests) that must observe a just-written entry
// via Get should call Wait first.
func (c *Cache) Wait() {
	c.rc.Wait()
}

// Close releases the resources held by the underlying cache.
func (c *Cache) Close() {
	c.rc.Close()
}
