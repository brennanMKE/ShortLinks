package cache

import (
	"context"
	"sync"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/filters"
)

// FilterRuleTTL is the lifetime of a cached snapshot of the active URL filter
// rules. The PRD mandates a 60-second TTL so every link creation does not hit
// the database; the cache is invalidated immediately on any rule
// create/update/delete via RuleCache.Invalidate.
const FilterRuleTTL = 60 * time.Second

// ruleLoader is the DB-backed source the RuleCache refreshes from. It returns
// the active rules already compiled (uncompilable patterns skipped). The links
// handler supplies a closure that loads + compiles via the filters package.
type ruleLoader func(ctx context.Context) ([]filters.Rule, error)

// RuleCache holds a single in-memory snapshot of the active, compiled URL filter
// rules with a 60-second TTL. Unlike the per-key redirect Cache, the filter
// rule set is small and shared, so it is cached as one whole snapshot guarded by
// a mutex rather than as Ristretto entries. The snapshot is loaded lazily on the
// first Rules call and refreshed when it is older than FilterRuleTTL.
//
// Invalidate drops the snapshot immediately so the next Rules call reloads from
// the DB — call it whenever a rule is created, updated, or deleted so a change
// takes effect at once rather than after the TTL lapses.
//
// RuleCache is safe for concurrent use.
type RuleCache struct {
	load ruleLoader
	now  func() time.Time

	mu       sync.Mutex
	rules    []filters.Rule
	loadedAt time.Time
	valid    bool
}

// NewRuleCache constructs a RuleCache that refreshes from load. load is invoked
// with the caller's context on a miss or expiry; it must return the active rules
// already compiled.
func NewRuleCache(load ruleLoader) *RuleCache {
	return &RuleCache{load: load, now: time.Now}
}

// Rules returns the cached active rules, refreshing from the loader when the
// snapshot is absent or older than FilterRuleTTL. A loader error is returned and
// the stale snapshot (if any) is left in place so a transient DB blip does not
// wipe the cache — the caller decides whether to fail the request or proceed.
func (rc *RuleCache) Rules(ctx context.Context) ([]filters.Rule, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.valid && rc.now().Sub(rc.loadedAt) < FilterRuleTTL {
		return rc.rules, nil
	}

	rules, err := rc.load(ctx)
	if err != nil {
		return rc.rules, err
	}
	rc.rules = rules
	rc.loadedAt = rc.now()
	rc.valid = true
	return rc.rules, nil
}

// Invalidate drops the cached snapshot so the next Rules call reloads from the
// DB. Call it immediately after any rule create/update/delete so the change is
// observed at once rather than after the TTL.
func (rc *RuleCache) Invalidate() {
	rc.mu.Lock()
	rc.valid = false
	rc.rules = nil
	rc.mu.Unlock()
}
