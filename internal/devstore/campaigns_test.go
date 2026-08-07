package devstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
)

// TestCreateCampaign_RoundTrip asserts a created campaign round-trips through
// GetCampaignBySlug and that default_utm_campaign defaults to the generated
// slug, matching internal/campaigns.Store's behavior.
func TestCreateCampaign_RoundTrip(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()
	const userID = int64(1)

	created, err := s.CreateCampaign(ctx, campaigns.NewCampaign{
		UserID: userID,
		Name:   "Summer Fair",
	}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if created.Slug != "summer-fair" {
		t.Errorf("slug = %q, want %q", created.Slug, "summer-fair")
	}
	if created.DefaultUTMCampaign != created.Slug {
		t.Errorf("default_utm_campaign = %q, want %q (the slug)", created.DefaultUTMCampaign, created.Slug)
	}

	got, err := s.GetCampaignBySlug(ctx, userID, created.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlug: %v", err)
	}
	if got.ID != created.ID || got.Name != "Summer Fair" {
		t.Errorf("got = %+v, want id=%d name=Summer Fair", got, created.ID)
	}
}

// TestCreateCampaign_SlugCollisionGetsSuffix asserts two campaigns with the
// same name for the same user get suffixed slugs, matching the real store's
// deterministic-suffix behavior (never an error).
func TestCreateCampaign_SlugCollisionGetsSuffix(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	first, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign first: %v", err)
	}
	second, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign second: %v", err)
	}
	if first.Slug != "summer-fair" {
		t.Errorf("first slug = %q, want %q", first.Slug, "summer-fair")
	}
	if second.Slug != "summer-fair-2" {
		t.Errorf("second slug = %q, want %q", second.Slug, "summer-fair-2")
	}
}

// TestListCampaignsForUser_OrderedMostRecentFirst asserts the in-memory list
// is scoped to the caller and ordered most-recently-created first, matching
// campaigns.Store.ListCampaignsForUser's documented order.
func TestListCampaignsForUser_OrderedMostRecentFirst(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const alice, bob = int64(1), int64(2)

	if _, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: alice, Name: "Alice One"}, nil, audit0Entry()); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: alice, Name: "Alice Two"}, nil, audit0Entry()); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: bob, Name: "Bob One"}, nil, audit0Entry()); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	got, err := s.ListCampaignsForUser(ctx, alice)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, c := range got {
		if c.UserID != alice {
			t.Errorf("campaign %q belongs to user %d, want %d", c.Slug, c.UserID, alice)
		}
	}
	if got[0].Name != "Alice Two" || got[1].Name != "Alice One" {
		t.Errorf("order = [%q, %q], want [Alice Two, Alice One] (most recently created first)", got[0].Name, got[1].Name)
	}
}

// TestListCampaignsForUser_EmptyForUserWithNoCampaigns asserts a user with no
// campaigns gets an empty (non-nil) slice, not an error.
func TestListCampaignsForUser_EmptyForUserWithNoCampaigns(t *testing.T) {
	s := devstore.New("")
	got, err := s.ListCampaignsForUser(context.Background(), 999)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// TestCampaignOwnership_NonOwnerCannotReadUpdateOrDelete is the
// security-relevant case: a non-owner's GetCampaignBySlug, UpdateCampaign,
// and DeleteCampaign all report campaigns.ErrCampaignNotFound (never leaking
// existence or succeeding), and the owner's row is undamaged after all three
// attempts — mirroring the ownership assertions in
// internal/campaigns/store_test.go for the Postgres-backed store.
func TestCampaignOwnership_NonOwnerCannotReadUpdateOrDelete(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const alice, bob = int64(1), int64(2)

	c, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: alice, Name: "Alice Campaign"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if _, err := s.GetCampaignBySlug(ctx, bob, c.Slug); !errors.Is(err, campaigns.ErrCampaignNotFound) {
		t.Errorf("bob GetCampaignBySlug err = %v, want ErrCampaignNotFound", err)
	}

	hijacked := "Hijacked"
	if _, err := s.UpdateCampaign(ctx, bob, c.Slug, campaigns.CampaignUpdate{Name: &hijacked}, nil, audit0Entry()); !errors.Is(err, campaigns.ErrCampaignNotFound) {
		t.Errorf("bob UpdateCampaign err = %v, want ErrCampaignNotFound", err)
	}

	if err := s.DeleteCampaign(ctx, bob, c.Slug, nil, audit0Entry()); !errors.Is(err, campaigns.ErrCampaignNotFound) {
		t.Errorf("bob DeleteCampaign err = %v, want ErrCampaignNotFound", err)
	}

	// Alice's row must be completely undamaged by all three attempts.
	got, err := s.GetCampaignBySlug(ctx, alice, c.Slug)
	if err != nil {
		t.Fatalf("alice GetCampaignBySlug after bob's attempts: %v", err)
	}
	if got.Name != "Alice Campaign" {
		t.Errorf("name = %q, want unchanged %q", got.Name, "Alice Campaign")
	}
}

// TestCampaign_StartsAtEndsAtNotAliased is the pointer-aliasing regression
// probe: it creates a campaign with a StartsAt pointer, mutates the
// *time.Time the CALLER passed in after the call returns, then mutates the
// StartsAt pointer on the RETURNED Campaign, and asserts neither mutation
// reaches a subsequent GetCampaignBySlug. Before the fix, mutating a returned
// pointer silently corrupted store state (confirmed by writing year 1999
// through it and observing GetCampaignBySlug echo 1999 back); this test fails
// the same way if that aliasing regresses.
func TestCampaign_StartsAtEndsAtNotAliased(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	starts := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	in := campaigns.NewCampaign{UserID: userID, Name: "Summer Fair", StartsAt: &starts}

	created, err := s.CreateCampaign(ctx, in, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	// Mutate the CALLER's original input pointer after the call returns. If
	// CreateCampaign stored this pointer directly, the store's copy would see
	// this mutation.
	starts = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)

	// Mutate the pointer on the value CreateCampaign RETURNED. If
	// CreateCampaign returned the same pointer it stored, the store's copy
	// would see this mutation too.
	if created.StartsAt == nil {
		t.Fatal("precondition: created.StartsAt is nil")
	}
	*created.StartsAt = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := s.GetCampaignBySlug(ctx, userID, created.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlug: %v", err)
	}
	if got.StartsAt == nil {
		t.Fatal("got.StartsAt is nil, want the original 2026-06-01")
	}
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.StartsAt.Equal(want) {
		t.Errorf("starts_at = %v, want %v (unaffected by mutating the caller's input pointer or the returned pointer)", got.StartsAt, want)
	}

	// Also confirm mutating the SECOND read's pointer doesn't reach a third.
	*got.StartsAt = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	got2, err := s.GetCampaignBySlug(ctx, userID, created.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlug (second read): %v", err)
	}
	if !got2.StartsAt.Equal(want) {
		t.Errorf("starts_at after mutating a previously-returned pointer = %v, want unaffected %v", got2.StartsAt, want)
	}
}

// TestUpdateCampaign_NoOpDoesNotBumpUpdatedAt asserts an UpdateCampaign call
// with no fields set leaves UpdatedAt untouched, matching
// campaigns.Store.UpdateCampaign's len(setClauses)==0 early-return behavior —
// a dev-built UI keying off updated_at must see the same behavior it would
// get from Postgres.
func TestUpdateCampaign_NoOpDoesNotBumpUpdatedAt(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	created, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	updated, err := s.UpdateCampaign(ctx, userID, created.Slug, campaigns.CampaignUpdate{}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("UpdateCampaign (no-op): %v", err)
	}
	if !updated.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("updated_at = %v, want unchanged %v (no fields were set)", updated.UpdatedAt, created.UpdatedAt)
	}
}

// TestDeleteCampaign_RemovesRow asserts a successful delete removes the row.
func TestDeleteCampaign_RemovesRow(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	created, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if err := s.DeleteCampaign(ctx, userID, created.Slug, nil, audit0Entry()); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if _, err := s.GetCampaignBySlug(ctx, userID, created.Slug); !errors.Is(err, campaigns.ErrCampaignNotFound) {
		t.Errorf("GetCampaignBySlug after delete err = %v, want ErrCampaignNotFound", err)
	}
}

// audit0Entry returns a minimal audit.Entry for tests that don't care about
// audit content — devstore's campaign methods accept but ignore it (dev mode
// wires a nil auditor; see the campaignStore section of devstore.go).
func audit0Entry() audit.Entry { return audit.Entry{} }
