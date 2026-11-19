package reports

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestPublicSummary_TopLinksExcludesExternalIDLinks covers #0182: a link with
// an external_id was created by a client (an invitation, say) and is never
// listed publicly, even when it is active, in a campaign, non-expiring and
// busy. Its clicks still count in the totals, like any other link's.
func TestPublicSummary_TopLinksExcludesExternalIDLinks(t *testing.T) {
	f := newFixture(t)
	u := f.user("a@example.com")
	camp := f.campaign(u, "Guides", "guides")
	at := daysAgo(1, 18)

	shown := f.pubLink(u, "shown1", "Public guide", "https://example.com/guide", "", true, 0, nil, camp)
	hidden := f.pubLink(u, "hide01", "Family invite", "https://example.com/invite", "", true, 0, nil, camp)
	if _, err := f.pool.Exec(context.Background(), `UPDATE links SET external_id = 'invite-9' WHERE id = $1`, hidden); err != nil {
		t.Fatalf("set external_id: %v", err)
	}
	f.clicks(3, click{link: shown, at: at})
	f.clicks(50, click{link: hidden, at: at})

	a := f.summary(10)
	if len(a.TopLinks) != 1 || a.TopLinks[0].Title != "Public guide" {
		t.Fatalf("top links = %+v, want only the link without an external_id", a.TopLinks)
	}
	if a.TotalClicks != 53 {
		t.Errorf("total = %d, want 53 (the hidden link's clicks still count)", a.TotalClicks)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), "hide01") || strings.Contains(string(raw), "invite") {
		t.Errorf("response leaks the hidden link: %s", raw)
	}
}
