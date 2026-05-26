package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/filters"
)

// TestRuleCache_LoadsOnceWithinTTL asserts the loader is hit once on the first
// Rules call and not again while the snapshot is within the TTL.
func TestRuleCache_LoadsOnceWithinTTL(t *testing.T) {
	calls := 0
	rc := NewRuleCache(func(context.Context) ([]filters.Rule, error) {
		calls++
		return []filters.Rule{{ID: 1, Pattern: `x`, ReasonCode: 1}}, nil
	})

	for i := 0; i < 3; i++ {
		rules, err := rc.Rules(context.Background())
		if err != nil {
			t.Fatalf("Rules: %v", err)
		}
		if len(rules) != 1 {
			t.Fatalf("len = %d, want 1", len(rules))
		}
	}
	if calls != 1 {
		t.Errorf("loader calls = %d, want 1 (cached within TTL)", calls)
	}
}

// TestRuleCache_InvalidateForcesReload asserts Invalidate drops the snapshot so
// the next Rules call reloads from the loader immediately.
func TestRuleCache_InvalidateForcesReload(t *testing.T) {
	calls := 0
	rc := NewRuleCache(func(context.Context) ([]filters.Rule, error) {
		calls++
		return nil, nil
	})

	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if calls != 1 {
		t.Fatalf("after two reads within TTL, calls = %d, want 1", calls)
	}

	rc.Invalidate()
	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("Rules after invalidate: %v", err)
	}
	if calls != 2 {
		t.Errorf("after invalidate + read, calls = %d, want 2", calls)
	}
}

// TestRuleCache_ReloadsAfterTTL asserts the snapshot is refreshed once it is
// older than the TTL, using an injected clock so the test stays fast.
func TestRuleCache_ReloadsAfterTTL(t *testing.T) {
	calls := 0
	rc := NewRuleCache(func(context.Context) ([]filters.Rule, error) {
		calls++
		return nil, nil
	})
	now := time.Now()
	rc.now = func() time.Time { return now }

	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	// Advance past the TTL.
	now = now.Add(FilterRuleTTL + time.Second)
	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if calls != 2 {
		t.Errorf("after TTL expiry, calls = %d, want 2", calls)
	}
}

// TestRuleCache_LoaderErrorKeepsStale asserts a loader error returns the stale
// snapshot rather than wiping it, so a transient DB blip does not clear the
// cache.
func TestRuleCache_LoaderErrorKeepsStale(t *testing.T) {
	fail := false
	rc := NewRuleCache(func(context.Context) ([]filters.Rule, error) {
		if fail {
			return nil, errors.New("db down")
		}
		return []filters.Rule{{ID: 7, Pattern: `y`, ReasonCode: 2}}, nil
	})
	now := time.Now()
	rc.now = func() time.Time { return now }

	if _, err := rc.Rules(context.Background()); err != nil {
		t.Fatalf("initial Rules: %v", err)
	}

	// Force a refresh that fails.
	now = now.Add(FilterRuleTTL + time.Second)
	fail = true
	rules, err := rc.Rules(context.Background())
	if err == nil {
		t.Fatal("expected loader error to surface")
	}
	if len(rules) != 1 || rules[0].ID != 7 {
		t.Errorf("stale snapshot = %+v, want the previously loaded rule", rules)
	}
}
