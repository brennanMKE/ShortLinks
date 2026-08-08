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

// TestDeleteCampaign_SetsLinksCampaignIDNullAndKeepsLinks mirrors
// campaigns.Store's TestDeleteCampaign_SetsLinksCampaignIDNullAndKeepsLinks:
// dev mode must reproduce migration 000011's ON DELETE SET NULL by hand,
// since there is no real FK backing it in memory. Deleting a campaign with
// assigned links must succeed, null every one of their CampaignID fields
// (read back via a fresh GetLink, not the pre-delete value), and delete no
// link row.
func TestDeleteCampaign_SetsLinksCampaignIDNullAndKeepsLinks(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()
	const userID = int64(1) // seeded admin, owns "wiki" and "gh"

	c, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	wiki, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink wiki: %v", err)
	}
	gh, err := s.GetLink(ctx, userID, "gh")
	if err != nil {
		t.Fatalf("GetLink gh: %v", err)
	}
	if err := s.AssignLinkToCampaign(ctx, userID, c.ID, wiki.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("assign wiki: %v", err)
	}
	if err := s.AssignLinkToCampaign(ctx, userID, c.ID, gh.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("assign gh: %v", err)
	}

	if err := s.DeleteCampaign(ctx, userID, c.Slug, nil, audit0Entry()); err != nil {
		t.Fatalf("DeleteCampaign with links assigned: %v", err)
	}

	gotWiki, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink wiki after delete: %v", err)
	}
	if gotWiki.CampaignID != nil {
		t.Errorf("wiki campaign_id = %v, want nil (not a dangling deleted-campaign id)", *gotWiki.CampaignID)
	}
	if gotWiki.CampaignName != "" || gotWiki.CampaignSlug != "" {
		t.Errorf("wiki campaign_name=%q campaign_slug=%q, want both empty", gotWiki.CampaignName, gotWiki.CampaignSlug)
	}
	gotGH, err := s.GetLink(ctx, userID, "gh")
	if err != nil {
		t.Fatalf("GetLink gh after delete: %v", err)
	}
	if gotGH.CampaignID != nil {
		t.Errorf("gh campaign_id = %v, want nil", *gotGH.CampaignID)
	}

	// ListLinksForCampaign against the now-deleted id must not still surface
	// either link — the exact regression a copy-paste "just delete the
	// campaign row" implementation produces.
	stillIn, err := s.ListLinksForCampaign(ctx, userID, c.ID)
	if err != nil {
		t.Fatalf("ListLinksForCampaign: %v", err)
	}
	if len(stillIn) != 0 {
		t.Errorf("ListLinksForCampaign(deleted id %d) = %+v, want empty", c.ID, stillIn)
	}
}

// ── #0099: link membership ──────────────────────────────────────────────────

// TestGetCampaignByID_RoundTrip asserts GetCampaignByID finds a created
// campaign by id and reports ErrCampaignNotFound for another user.
func TestGetCampaignByID_RoundTrip(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const alice, bob = int64(1), int64(2)

	created, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: alice, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	got, err := s.GetCampaignByID(ctx, alice, created.ID)
	if err != nil {
		t.Fatalf("GetCampaignByID: %v", err)
	}
	if got.Slug != created.Slug {
		t.Errorf("slug = %q, want %q", got.Slug, created.Slug)
	}

	if _, err := s.GetCampaignByID(ctx, bob, created.ID); !errors.Is(err, campaigns.ErrCampaignNotFound) {
		t.Errorf("bob GetCampaignByID err = %v, want ErrCampaignNotFound", err)
	}
}

// TestAssignUnassignLinkToCampaign_RoundTrip exercises the full
// assign/unassign cycle against the seeded admin link "wiki" and asserts
// GetLink reflects campaign_id, campaign_name, and campaign_slug throughout
// — the same fields links.Store.GetLink's LEFT JOIN populates in production.
func TestAssignUnassignLinkToCampaign_RoundTrip(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()
	const userID = int64(1) // seeded admin, owns the "wiki" link

	c, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	before, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink before assign: %v", err)
	}
	if before.CampaignID != nil {
		t.Fatalf("precondition: wiki already has a campaign_id")
	}

	if err := s.AssignLinkToCampaign(ctx, userID, c.ID, before.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("AssignLinkToCampaign: %v", err)
	}
	assigned, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink after assign: %v", err)
	}
	if assigned.CampaignID == nil || *assigned.CampaignID != c.ID {
		t.Errorf("campaign_id = %v, want %d", assigned.CampaignID, c.ID)
	}
	if assigned.CampaignName != "Summer Fair" || assigned.CampaignSlug != c.Slug {
		t.Errorf("campaign_name=%q campaign_slug=%q, want %q/%q", assigned.CampaignName, assigned.CampaignSlug, "Summer Fair", c.Slug)
	}

	// ListLinksForCampaign should now include it.
	inCampaign, err := s.ListLinksForCampaign(ctx, userID, c.ID)
	if err != nil {
		t.Fatalf("ListLinksForCampaign: %v", err)
	}
	if len(inCampaign) != 1 || inCampaign[0].Key != "wiki" {
		t.Errorf("ListLinksForCampaign = %+v, want exactly [wiki]", inCampaign)
	}

	if err := s.UnassignLinkFromCampaign(ctx, userID, c.ID, before.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("UnassignLinkFromCampaign: %v", err)
	}
	unassigned, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink after unassign: %v", err)
	}
	if unassigned.CampaignID != nil {
		t.Errorf("campaign_id after unassign = %v, want nil", unassigned.CampaignID)
	}
	if unassigned.CampaignName != "" || unassigned.CampaignSlug != "" {
		t.Errorf("campaign_name=%q campaign_slug=%q after unassign, want both empty", unassigned.CampaignName, unassigned.CampaignSlug)
	}
}

// TestAssignLinkToCampaign_MovesAlreadyAssignedLink matches
// campaigns.Store's decided behavior in dev mode too: assigning an
// already-assigned link into a different campaign moves it.
func TestAssignLinkToCampaign_MovesAlreadyAssignedLink(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	a, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Campaign A"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign A: %v", err)
	}
	b, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Campaign B"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	link, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}

	if err := s.AssignLinkToCampaign(ctx, userID, a.ID, link.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("assign to A: %v", err)
	}
	if err := s.AssignLinkToCampaign(ctx, userID, b.ID, link.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("assign to B (already in A) returned an error, want success: %v", err)
	}
	got, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.CampaignID == nil || *got.CampaignID != b.ID {
		t.Errorf("campaign_id = %v, want %d (moved to B)", got.CampaignID, b.ID)
	}
}

// TestAssignLinkToCampaign_OwnershipEnforced asserts assigning a link
// belonging to a DIFFERENT user returns campaigns.ErrLinkNotFound.
func TestAssignLinkToCampaign_OwnershipEnforced(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const alice, bob = int64(1), int64(2)

	c, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: bob, Name: "Bob Campaign"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	aliceLink, err := s.GetLink(ctx, alice, "wiki") // seeded admin (id=1=alice here) owns "wiki"
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}

	// bob tries to assign alice's link into his own campaign.
	err = s.AssignLinkToCampaign(ctx, bob, c.ID, aliceLink.ID, nil, audit.Entry{})
	if !errors.Is(err, campaigns.ErrLinkNotFound) {
		t.Errorf("bob AssignLinkToCampaign(alice's link) err = %v, want ErrLinkNotFound", err)
	}
	got, err := s.GetLink(ctx, alice, "wiki")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.CampaignID != nil {
		t.Errorf("alice's link campaign_id = %v, want still nil", got.CampaignID)
	}
}

// TestLink_CampaignIDNotAliased is the pointer-aliasing regression probe for
// links.Link.CampaignID, mirroring TestCampaign_StartsAtEndsAtNotAliased:
// mutating a *int64 the caller passed into AssignLinkToCampaign, or a
// pointer on a value GetLink returned, must never reach the store's own
// state.
func TestLink_CampaignIDNotAliased(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()
	const userID = int64(1)

	c, err := s.CreateCampaign(ctx, campaigns.NewCampaign{UserID: userID, Name: "Summer Fair"}, nil, audit0Entry())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if err := s.AssignLinkToCampaign(ctx, userID, c.ID, link.ID, nil, audit.Entry{}); err != nil {
		t.Fatalf("AssignLinkToCampaign: %v", err)
	}

	got, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if got.CampaignID == nil {
		t.Fatal("precondition: campaign_id is nil")
	}
	// Mutate the pointer on the returned value.
	*got.CampaignID = 999999

	got2, err := s.GetLink(ctx, userID, "wiki")
	if err != nil {
		t.Fatalf("GetLink (second read): %v", err)
	}
	if got2.CampaignID == nil || *got2.CampaignID != c.ID {
		t.Errorf("campaign_id after mutating a previously-returned pointer = %v, want unaffected %d", got2.CampaignID, c.ID)
	}
}

// audit0Entry returns a minimal audit.Entry for tests that don't care about
// audit content — devstore's campaign methods accept but ignore it (dev mode
// wires a nil auditor; see the campaignStore section of devstore.go).
func audit0Entry() audit.Entry { return audit.Entry{} }
