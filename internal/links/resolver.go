package links

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brennanMKE/ShortLinks/internal/cache"
)

// Resolution is the minimal redirect-relevant view of a link, resolved by key
// across ALL users (the redirect path is public and not user-scoped). It carries
// only what the redirect handler needs to decide active/expired and where to
// send the visitor.
type Resolution struct {
	DestinationURL string
	Active         bool
	ExpiresAt      *time.Time
	DeniedReason   int16
}

// ResolveByKey looks up the redirect-relevant fields for a key across all users.
// Unlike GetLink it is NOT user-scoped — the redirect endpoint (GET /u/{key}) is
// public. ErrLinkNotFound is returned when no link uses the key, which the
// resolver maps to a negative cache entry.
func (s *Store) ResolveByKey(ctx context.Context, key string) (Resolution, error) {
	var r Resolution
	var expiresAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT destination_url, active, expires_at, denied_reason
		   FROM links
		  WHERE key = $1`,
		key,
	).Scan(&r.DestinationURL, &r.Active, &expiresAt, &r.DeniedReason)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Resolution{}, ErrLinkNotFound
	case err != nil:
		return Resolution{}, fmt.Errorf("links: resolving key: %w", err)
	}
	r.ExpiresAt = expiresAt
	return r, nil
}

// keyResolver is the slice of *cache.Cache the Resolver uses: read-through with a
// negative-entry path. *cache.Cache satisfies it. Taking an interface keeps the
// resolver unit-testable without a real Ristretto cache, but production wires the
// real cache.
type keyResolver interface {
	Get(key string) (*cache.CachedLink, bool)
	Set(key string, link *cache.CachedLink)
	SetNegative(key string)
}

// keyLookup is the slice of *Store the Resolver uses to fall back to the DB on a
// cache miss. *Store satisfies it via ResolveByKey.
type keyLookup interface {
	ResolveByKey(ctx context.Context, key string) (Resolution, error)
}

// Resolver implements the redirect handler's LinkResolver: it checks the redirect
// cache first and, on a miss, falls back to the database, populating the cache
// (a positive entry for a found link, a short-TTL negative entry for an absent
// key per the PRD's Redirect Cache design). The handler depends only on the
// LinkResolver interface (it returns cache.CachedLink), so this lives here in the
// links package where both the Store and cache are available.
type Resolver struct {
	cache keyResolver
	store keyLookup
}

// NewResolver constructs a Resolver over the redirect cache and the link store.
// The cache may be nil, in which case every resolve goes straight to the DB and
// nothing is cached (useful in tests that want to exercise the DB path directly).
func NewResolver(c *cache.Cache, store *Store) *Resolver {
	// Pass the concrete cache through the interface so a nil *cache.Cache is a
	// genuine nil (no caching), not a non-nil interface wrapping a nil pointer.
	var kc keyResolver
	if c != nil {
		kc = c
	}
	return &Resolver{cache: kc, store: store}
}

// Resolve returns the cached/DB-backed link for key. The boolean reports whether
// a usable POSITIVE entry was found: a negative cache hit (known-absent key) and
// a genuine DB miss both return found=false with no error, so the handler treats
// them uniformly as 404. A non-nil error signals an internal lookup failure
// (e.g. a DB error) distinct from "not found".
//
// Returned CachedLink for a found link carries Negative=false; the resolver never
// returns a negative entry to the handler (it converts it to found=false), but it
// DOES store negative entries in the cache to shield the DB from repeated lookups
// of an invalid key.
func (r *Resolver) Resolve(ctx context.Context, key string) (cache.CachedLink, bool, error) {
	if r.cache != nil {
		if entry, ok := r.cache.Get(key); ok && entry != nil {
			if entry.Negative {
				return cache.CachedLink{}, false, nil
			}
			return *entry, true, nil
		}
	}

	res, err := r.store.ResolveByKey(ctx, key)
	switch {
	case errors.Is(err, ErrLinkNotFound):
		// Cache the absence with a short TTL so a burst of bad lookups does not
		// hammer the DB, then report not-found to the handler.
		if r.cache != nil {
			r.cache.SetNegative(key)
		}
		return cache.CachedLink{}, false, nil
	case err != nil:
		return cache.CachedLink{}, false, err
	}

	entry := cache.CachedLink{
		DestinationURL: res.DestinationURL,
		Active:         res.Active,
		ExpiresAt:      res.ExpiresAt,
		DeniedReason:   res.DeniedReason,
	}
	if r.cache != nil {
		e := entry
		r.cache.Set(key, &e)
	}
	return entry, true, nil
}
