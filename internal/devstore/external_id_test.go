package devstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/devstore"
	"github.com/brennanMKE/ShortLinks/internal/links"
)

// TestExternalID_DevStore mirrors the Postgres store's #0182 rules: an
// external_id create never revives an inactive link, and a denied link cannot
// take an id.
func TestExternalID_DevStore(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()
	gen := links.KeyGenerator(links.KeyLength)
	in := links.NewLink{UserID: 1, DestinationURL: "https://example.com/inv", ExternalID: "inv-1", MinKeyLength: links.KeyLength}

	l, outcome, err := s.CreateOrReactivateLink(ctx, in, gen)
	if err != nil || outcome != links.OutcomeInserted {
		t.Fatalf("create = %v %v", outcome, err)
	}
	if err := s.DeactivateLink(ctx, 1, l.Key); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	again, outcome, err := s.CreateOrReactivateLink(ctx, in, gen)
	if err != nil || outcome != links.OutcomeActiveDuplicate || again.Key != l.Key || again.Active {
		t.Errorf("recreate = %+v %v %v, want the same inactive link as a duplicate", again, outcome, err)
	}

	denied, err := s.CreateDeniedLink(ctx, links.NewLink{UserID: 1, DestinationURL: "https://evil.example/x", ExternalID: "x"}, 1, gen)
	if err != nil {
		t.Fatalf("denied: %v", err)
	}
	set := "x"
	if _, err := s.UpdateLink(ctx, 1, denied.Key, links.LinkUpdate{ExternalID: &set}); !errors.Is(err, links.ErrLinkDenied) {
		t.Errorf("set on denied link = %v, want ErrLinkDenied", err)
	}
	clear := ""
	if _, err := s.UpdateLink(ctx, 1, denied.Key, links.LinkUpdate{ExternalID: &clear}); err != nil {
		t.Errorf("clear on denied link = %v", err)
	}
}
