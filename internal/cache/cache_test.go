package cache

import (
	"testing"
	"time"
)

func newTestCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	c, err := New(1000, ttl)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestSetThenGetReturnsValue(t *testing.T) {
	c := newTestCache(t, DefaultTTL)
	expires := time.Now().Add(time.Hour)
	want := &CachedLink{
		DestinationURL: "https://example.com/landing",
		Active:         true,
		ExpiresAt:      &expires,
		DeniedReason:   0,
	}

	c.Set("abc123", want)
	c.Wait() // Set is asynchronous; ensure the write is applied before Get.

	got, found := c.Get("abc123")
	if !found {
		t.Fatal("Get: expected cache hit, got miss")
	}
	if got.Negative {
		t.Error("Get: expected positive entry, got negative")
	}
	if got.DestinationURL != want.DestinationURL {
		t.Errorf("DestinationURL = %q, want %q", got.DestinationURL, want.DestinationURL)
	}
	if got.Active != want.Active {
		t.Errorf("Active = %v, want %v", got.Active, want.Active)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
	}
}

func TestGetUnknownKeyReturnsMiss(t *testing.T) {
	c := newTestCache(t, DefaultTTL)

	got, found := c.Get("does-not-exist")
	if found {
		t.Errorf("Get: expected miss for unknown key, got hit with %+v", got)
	}
}

func TestSetNegativeRetrievableAndMarkedNegative(t *testing.T) {
	c := newTestCache(t, DefaultTTL)

	c.SetNegative("ghost")
	c.Wait()

	got, found := c.Get("ghost")
	if !found {
		t.Fatal("Get: expected negative entry to be retrievable, got miss")
	}
	if !got.Negative {
		t.Errorf("expected Negative=true, got %+v", got)
	}
	if got.DestinationURL != "" {
		t.Errorf("negative entry should have empty DestinationURL, got %q", got.DestinationURL)
	}
}

func TestDeleteRemovesEntry(t *testing.T) {
	c := newTestCache(t, DefaultTTL)

	c.Set("temp", &CachedLink{DestinationURL: "https://example.com", Active: true})
	c.Wait()
	if _, found := c.Get("temp"); !found {
		t.Fatal("precondition: expected entry to be present before delete")
	}

	c.Delete("temp")
	c.Wait()

	if got, found := c.Get("temp"); found {
		t.Errorf("Get: expected miss after delete, got hit with %+v", got)
	}
}

func TestPositiveEntryExpiresAfterTTL(t *testing.T) {
	// Short TTL so the test stays fast while still exercising expiry.
	c := newTestCache(t, 50*time.Millisecond)

	c.Set("ephemeral", &CachedLink{DestinationURL: "https://example.com", Active: true})
	c.Wait()
	if _, found := c.Get("ephemeral"); !found {
		t.Fatal("precondition: expected entry to be present before TTL elapses")
	}

	time.Sleep(150 * time.Millisecond)

	if got, found := c.Get("ephemeral"); found {
		t.Errorf("Get: expected entry to expire after TTL, got hit with %+v", got)
	}
}

func TestNegativeEntryUsesShorterTTL(t *testing.T) {
	// A long positive TTL must not extend negative entries, which use the
	// fixed NegativeTTL. Confirm the constant is the shorter of the two.
	if NegativeTTL >= DefaultTTL {
		t.Fatalf("NegativeTTL (%v) should be shorter than DefaultTTL (%v)", NegativeTTL, DefaultTTL)
	}
}
