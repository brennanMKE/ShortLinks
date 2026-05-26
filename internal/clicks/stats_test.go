package clicks

import (
	"context"
	"testing"
)

// recordN records n clicks for key with the given UTM triple (empty strings →
// NULL, exercising the "(none)" bucket).
func recordN(t *testing.T, rec *Recorder, key, source, medium, campaign string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := rec.Record(context.Background(), Click{
			Key:         key,
			UTMSource:   source,
			UTMMedium:   medium,
			UTMCampaign: campaign,
		})
		if err != nil {
			t.Fatalf("record click: %v", err)
		}
	}
}

// findBucket returns the count for value in the slice, or -1 if absent.
func findBucket(buckets []Bucket, value string) int64 {
	for _, b := range buckets {
		if b.Value == value {
			return b.Count
		}
	}
	return -1
}

// TestUTMStatsForLink_Breakdown seeds a mix of UTM combinations and asserts the
// total count plus the per-dimension grouped counts, including the NULL → "(none)"
// bucket and the count-desc ordering.
func TestUTMStatsForLink_Breakdown(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com")

	// 4× email/newsletter/launch, 2× social/cpc/launch, 1× with no UTM at all.
	recordN(t, rec, "abc123", "email", "newsletter", "launch", 4)
	recordN(t, rec, "abc123", "social", "cpc", "launch", 2)
	recordN(t, rec, "abc123", "", "", "", 1)

	got, err := stats.UTMStatsForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}

	if got.ClickCount != 7 {
		t.Errorf("click_count = %d, want 7", got.ClickCount)
	}

	// by_source: email=4, social=2, (none)=1 — ordered desc.
	if len(got.BySource) != 3 {
		t.Fatalf("by_source len = %d, want 3: %+v", len(got.BySource), got.BySource)
	}
	if got.BySource[0].Value != "email" || got.BySource[0].Count != 4 {
		t.Errorf("by_source[0] = %+v, want email=4", got.BySource[0])
	}
	if c := findBucket(got.BySource, "social"); c != 2 {
		t.Errorf("by_source social = %d, want 2", c)
	}
	if c := findBucket(got.BySource, NoneBucket); c != 1 {
		t.Errorf("by_source %s = %d, want 1", NoneBucket, c)
	}

	// by_medium: newsletter=4, cpc=2, (none)=1.
	if c := findBucket(got.ByMedium, "newsletter"); c != 4 {
		t.Errorf("by_medium newsletter = %d, want 4", c)
	}
	if c := findBucket(got.ByMedium, "cpc"); c != 2 {
		t.Errorf("by_medium cpc = %d, want 2", c)
	}
	if c := findBucket(got.ByMedium, NoneBucket); c != 1 {
		t.Errorf("by_medium %s = %d, want 1", NoneBucket, c)
	}

	// by_campaign: launch=6, (none)=1.
	if c := findBucket(got.ByCampaign, "launch"); c != 6 {
		t.Errorf("by_campaign launch = %d, want 6", c)
	}
	if c := findBucket(got.ByCampaign, NoneBucket); c != 1 {
		t.Errorf("by_campaign %s = %d, want 1", NoneBucket, c)
	}
	if got.ByCampaign[0].Value != "launch" {
		t.Errorf("by_campaign[0] = %q, want launch (count desc)", got.ByCampaign[0].Value)
	}
}

// TestUTMStatsForLink_NoClicks asserts a link with no clicks returns a zero count
// and empty (non-nil) breakdown slices, so the JSON encodes [] not null.
func TestUTMStatsForLink_NoClicks(t *testing.T) {
	pool := testPool(t)
	stats := NewStatsStore(pool)

	uid := seedUser(t, pool, "bob@example.com")
	linkID := seedLink(t, pool, uid, "empty1", "https://example.com")

	got, err := stats.UTMStatsForLink(context.Background(), linkID)
	if err != nil {
		t.Fatalf("UTMStatsForLink: %v", err)
	}
	if got.ClickCount != 0 {
		t.Errorf("click_count = %d, want 0", got.ClickCount)
	}
	if got.BySource == nil || len(got.BySource) != 0 {
		t.Errorf("by_source = %+v, want empty non-nil", got.BySource)
	}
	if got.ByMedium == nil || len(got.ByMedium) != 0 {
		t.Errorf("by_medium = %+v, want empty non-nil", got.ByMedium)
	}
	if got.ByCampaign == nil || len(got.ByCampaign) != 0 {
		t.Errorf("by_campaign = %+v, want empty non-nil", got.ByCampaign)
	}
}
