package campaigns

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// testPool connects to TEST_DATABASE_URL or skips, truncating campaigns/
// audit_log/users before and after each test.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`TRUNCATE campaigns, audit_log, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

func countCampaigns(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM campaigns`).Scan(&n); err != nil {
		t.Fatalf("count campaigns: %v", err)
	}
	return n
}

func countAuditRows(t *testing.T, pool *pgxpool.Pool, action string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = $1`, action,
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

func createEntry(actorID int64, action string) audit.Entry {
	actor := actorID
	return audit.Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     action,
		TargetType: audit.TargetCampaign,
	}
}

// TestCreateCampaign_DefaultUTMCampaignFromSlug asserts that when DefaultUTMCampaign
// is not supplied, CreateCampaign populates it from the resolved slug.
func TestCreateCampaign_DefaultUTMCampaignFromSlug(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{
		UserID: uid,
		Name:   "Summer Fair",
	}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if c.Slug != "summer-fair" {
		t.Fatalf("slug = %q, want %q", c.Slug, "summer-fair")
	}
	if c.DefaultUTMCampaign != c.Slug {
		t.Errorf("default_utm_campaign = %q, want %q (the slug)", c.DefaultUTMCampaign, c.Slug)
	}
}

// TestCreateCampaign_ExplicitDefaultUTMCampaignPreserved asserts a caller-supplied
// DefaultUTMCampaign is NOT overridden by the slug.
func TestCreateCampaign_ExplicitDefaultUTMCampaignPreserved(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{
		UserID:             uid,
		Name:               "Summer Fair",
		DefaultUTMCampaign: "custom-value",
	}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if c.DefaultUTMCampaign != "custom-value" {
		t.Errorf("default_utm_campaign = %q, want %q", c.DefaultUTMCampaign, "custom-value")
	}
}

// TestCreateCampaign_SlugCollisionWithinUserGetsSuffix asserts two campaigns with the
// same name for the SAME user get suffixed slugs, not an error.
func TestCreateCampaign_SlugCollisionWithinUserGetsSuffix(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	first, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign first: %v", err)
	}
	second, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
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

// TestCreateCampaign_SlugUniquePerUserNotGlobal asserts two DIFFERENT users can each
// hold a campaign with the same slug (unsuffixed).
func TestCreateCampaign_SlugUniquePerUserNotGlobal(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	a, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer Fair"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign alice: %v", err)
	}
	b, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: bob, Name: "Summer Fair"}, auditor, createEntry(bob, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign bob: %v", err)
	}
	if a.Slug != "summer-fair" || b.Slug != "summer-fair" {
		t.Errorf("slugs = %q, %q, want both %q (per-user scoping)", a.Slug, b.Slug, "summer-fair")
	}
}

// TestGetCampaignBySlug_OwnershipScoped asserts a campaign is only visible to
// its owner: another user's lookup by the same slug returns
// ErrCampaignNotFound.
func TestGetCampaignBySlug_OwnershipScoped(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer Fair"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if _, err := store.GetCampaignBySlug(context.Background(), bob, c.Slug); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("bob GetCampaignBySlug(%q) err = %v, want ErrCampaignNotFound", c.Slug, err)
	}
	got, err := store.GetCampaignBySlug(context.Background(), alice, c.Slug)
	if err != nil {
		t.Fatalf("alice GetCampaignBySlug: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("got id %d, want %d", got.ID, c.ID)
	}
}

// TestUpdateCampaign_PartialChangesOnlyProvidedFields asserts UpdateCampaign changes only the
// fields present in the CampaignUpdate and leaves the rest untouched.
func TestUpdateCampaign_PartialChangesOnlyProvidedFields(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{
		UserID:      uid,
		Name:        "Summer Fair",
		Description: "original description",
	}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	newName := "Winter Fair"
	updated, err := store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{
		Name: &newName,
	}, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if updated.Name != "Winter Fair" {
		t.Errorf("name = %q, want %q", updated.Name, "Winter Fair")
	}
	if updated.Description != "original description" {
		t.Errorf("description = %q, want unchanged %q", updated.Description, "original description")
	}
	if updated.Slug != c.Slug {
		t.Errorf("slug = %q, want unchanged %q (slug is immutable)", updated.Slug, c.Slug)
	}
}

// TestUpdateCampaign_UpdatedAtAdvances asserts UpdateCampaign actually sets
// updated_at = now() on a field change — a real, strict After() comparison,
// not merely "did not go backwards" (which a query that never touches
// updated_at would also satisfy, since CreatedAt == UpdatedAt would tie and
// pass a non-decreasing check). A short sleep guarantees the two timestamps,
// captured from separate transactions, are measurably distinct regardless of
// the database clock's resolution.
func TestUpdateCampaign_UpdatedAtAdvances(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	time.Sleep(10 * time.Millisecond)

	newName := "Winter Fair"
	updated, err := store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{
		Name: &newName,
	}, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if !updated.UpdatedAt.After(c.UpdatedAt) {
		t.Errorf("updated_at = %v, want strictly after create's updated_at %v", updated.UpdatedAt, c.UpdatedAt)
	}
}

// TestUpdateCampaign_ClearNullableFieldWithEmptyString asserts a non-nil pointer to
// "" clears a nullable string column.
func TestUpdateCampaign_ClearNullableFieldWithEmptyString(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{
		UserID:      uid,
		Name:        "Summer Fair",
		Description: "will be cleared",
	}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	empty := ""
	updated, err := store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{
		Description: &empty,
	}, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if updated.Description != "" {
		t.Errorf("description = %q, want cleared to empty", updated.Description)
	}
}

// TestUpdateCampaign_DatesClearedViaDoublePointer asserts the ExpiresAt-style
// double-pointer convention: a non-nil StartsAt pointing at a nil *time.Time
// clears the column.
func TestUpdateCampaign_DatesClearedViaDoublePointer(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	starts := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c, err := store.CreateCampaign(context.Background(), NewCampaign{
		UserID:   uid,
		Name:     "Summer Fair",
		StartsAt: &starts,
	}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if c.StartsAt == nil {
		t.Fatalf("precondition: starts_at not set")
	}

	var nilTime *time.Time
	updated, err := store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{
		StartsAt: &nilTime,
	}, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if updated.StartsAt != nil {
		t.Errorf("starts_at = %v, want cleared to nil", updated.StartsAt)
	}
}

// TestUpdateCampaign_OwnershipEnforced asserts one user cannot update another user's
// campaign: the update is scoped to userID, so it is reported not found.
func TestUpdateCampaign_OwnershipEnforced(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer Fair"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	newName := "Hijacked"
	_, err = store.UpdateCampaign(context.Background(), bob, c.Slug, CampaignUpdate{Name: &newName}, auditor, createEntry(bob, audit.ActionCampaignUpdated))
	if !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("bob UpdateCampaign err = %v, want ErrCampaignNotFound", err)
	}

	// Confirm alice's row is untouched.
	got, err := store.GetCampaignBySlug(context.Background(), alice, c.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlug: %v", err)
	}
	if got.Name != "Summer Fair" {
		t.Errorf("name = %q, want unchanged %q", got.Name, "Summer Fair")
	}
}

// TestArchiveCampaign_Reversible asserts ArchiveCampaign(true) then ArchiveCampaign(false) returns
// the campaign to its unarchived state — unlike Delete, which is permanent.
func TestArchiveCampaign_Reversible(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if c.Archived {
		t.Fatalf("precondition: campaign created archived")
	}

	archived, err := store.ArchiveCampaign(context.Background(), uid, c.Slug, true, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("ArchiveCampaign(true): %v", err)
	}
	if !archived.Archived {
		t.Fatalf("archived.Archived = false, want true")
	}

	unarchived, err := store.ArchiveCampaign(context.Background(), uid, c.Slug, false, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("ArchiveCampaign(false): %v", err)
	}
	if unarchived.Archived {
		t.Errorf("unarchived.Archived = true, want false (reversible)")
	}
}

// TestDeleteCampaign_RemovesRow asserts DeleteCampaign removes the row and a subsequent
// GetCampaignBySlug reports ErrCampaignNotFound.
func TestDeleteCampaign_RemovesRow(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if err := store.DeleteCampaign(context.Background(), uid, c.Slug, auditor, createEntry(uid, audit.ActionCampaignDeleted)); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if _, err := store.GetCampaignBySlug(context.Background(), uid, c.Slug); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("GetCampaignBySlug after delete err = %v, want ErrCampaignNotFound", err)
	}
	if n := countCampaigns(t, pool); n != 0 {
		t.Errorf("campaigns remaining = %d, want 0", n)
	}
}

// TestDeleteCampaign_OwnershipEnforced asserts one user cannot delete another user's
// campaign.
func TestDeleteCampaign_OwnershipEnforced(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer Fair"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if err := store.DeleteCampaign(context.Background(), bob, c.Slug, auditor, createEntry(bob, audit.ActionCampaignDeleted)); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("bob DeleteCampaign err = %v, want ErrCampaignNotFound", err)
	}
	if _, err := store.GetCampaignBySlug(context.Background(), alice, c.Slug); err != nil {
		t.Errorf("alice's campaign was deleted by bob's request: %v", err)
	}
}

// TestListCampaignsForUser_ScopedToOwnerAndOrderedMostRecentFirst asserts
// ListCampaignsForUser only returns the caller's own campaigns AND asserts
// the actual returned order is most-recently-created first — the doc comment
// on ListCampaignsForUser promises this, and #0103's list view depends on
// it, so this checks names in order rather than only checking length and
// ownership.
func TestListCampaignsForUser_ScopedToOwnerAndOrderedMostRecentFirst(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	if _, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Alice One"}, auditor, createEntry(alice, audit.ActionCampaignCreated)); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Alice Two"}, auditor, createEntry(alice, audit.ActionCampaignCreated)); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: bob, Name: "Bob One"}, auditor, createEntry(bob, audit.ActionCampaignCreated)); err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	got, err := store.ListCampaignsForUser(context.Background(), alice)
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
	// "Alice Two" was created after "Alice One", so it must come first — the
	// (created_at DESC, id DESC) tiebreaker makes this deterministic even when
	// two inserts land in the same timestamp tick.
	if got[0].Name != "Alice Two" || got[1].Name != "Alice One" {
		t.Errorf("order = [%q, %q], want [Alice Two, Alice One] (most recently created first)", got[0].Name, got[1].Name)
	}
}

// TestCreateCampaign_AuditInsertFailureRollsBackMutation proves the audit
// write for CreateCampaign shares the mutation's transaction — not merely
// that "commit failed rolls everything back" (which is also true of a
// fire-and-forget audit write issued AFTER commit, since that path never
// gets a chance to run once commit itself has failed and CreateCampaign has
// already returned). Instead this forces the audit INSERT ITSELF to fail
// inside the transaction, via a foreign-key violation on audit_log.actor_id
// (no user with this id exists), and asserts the campaign row does not
// survive either. A fire-and-forget Record-after-commit implementation would
// FAIL this test differently: the campaign would commit successfully (count
// == 1) while the broken audit write is merely logged and swallowed.
func TestCreateCampaign_AuditInsertFailureRollsBackMutation(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	ghost := int64(999999) // no such user exists; violates audit_log.actor_id's FK
	e := audit.Entry{
		ActorID:    &ghost,
		UserID:     &uid,
		Action:     audit.ActionCampaignCreated,
		TargetType: audit.TargetCampaign,
	}

	_, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, e)
	if err == nil {
		t.Fatal("CreateCampaign returned nil error despite an FK-violating audit actor_id")
	}
	if n := countCampaigns(t, pool); n != 0 {
		t.Errorf("campaigns after failed audit insert = %d, want 0 (mutation rolled back with the failed audit write)", n)
	}
}

// TestUpdateCampaign_AuditInsertFailureRollsBackMutation is the same proof as
// TestCreateCampaign_AuditInsertFailureRollsBackMutation, but for
// UpdateCampaign: the field change must not survive when the in-transaction
// audit insert itself fails.
func TestUpdateCampaign_AuditInsertFailureRollsBackMutation(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	ghost := int64(999999)
	e := audit.Entry{
		ActorID:    &ghost,
		UserID:     &uid,
		Action:     audit.ActionCampaignUpdated,
		TargetType: audit.TargetCampaign,
	}
	newName := "Hijacked By Failure"
	_, err = store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{Name: &newName}, auditor, e)
	if err == nil {
		t.Fatal("UpdateCampaign returned nil error despite an FK-violating audit actor_id")
	}

	got, err := store.GetCampaignBySlug(context.Background(), uid, c.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlug: %v", err)
	}
	if got.Name != "Summer Fair" {
		t.Errorf("name = %q, want unchanged %q (rolled back with the failed audit write)", got.Name, "Summer Fair")
	}
}

// TestArchiveCampaign_StampsArchivedMetadata asserts ArchiveCampaign adds
// "archived" to the audit entry's metadata (merged with whatever the caller
// already set) so the resulting campaign.updated row is self-describing —
// distinguishable from a plain rename — rather than passing the caller's
// entry through untouched.
func TestArchiveCampaign_StampsArchivedMetadata(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	entry := createEntry(uid, audit.ActionCampaignUpdated)
	entry.Metadata = map[string]any{"slug": c.Slug}
	if _, err := store.ArchiveCampaign(context.Background(), uid, c.Slug, true, auditor, entry); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}

	var metaRaw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, audit.ActionCampaignUpdated,
	).Scan(&metaRaw); err != nil {
		t.Fatalf("querying audit metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("unmarshalling audit metadata: %v", err)
	}
	if archived, ok := meta["archived"]; !ok || archived != true {
		t.Errorf("audit metadata[\"archived\"] = %v (present=%v), want true", archived, ok)
	}
	if meta["slug"] != c.Slug {
		t.Errorf("audit metadata[\"slug\"] = %v, want %q (caller-supplied metadata preserved)", meta["slug"], c.Slug)
	}
}

// TestUpdateCampaign_EmptyNameDoesNotNullColumn proves the fix for routing
// "name" through the generic nullable-string SET builder: a CampaignUpdate
// with Name pointing at "" must not attempt to write SQL NULL into the
// NOT NULL name column (which would surface as a raw constraint-violation
// error). The HTTP handler already rejects an empty name before it reaches
// the store, but the store itself must not corrupt data for any future
// caller that skips that guard.
func TestUpdateCampaign_EmptyNameDoesNotNullColumn(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	empty := ""
	updated, err := store.UpdateCampaign(context.Background(), uid, c.Slug, CampaignUpdate{Name: &empty}, auditor, createEntry(uid, audit.ActionCampaignUpdated))
	if err != nil {
		t.Fatalf("UpdateCampaign with empty name errored (should set literal \"\", not SQL NULL): %v", err)
	}
	if updated.Name != "" {
		t.Errorf("name = %q, want empty string", updated.Name)
	}
}

// TestSchema_InvertedWindowRejectedByCheckConstraint proves the
// starts_at <= ends_at invariant is enforced by the database itself (the
// migration 000010 CHECK constraint), not merely by the HTTP handler. It
// bypasses the store entirely — a direct raw INSERT — since neither
// CreateCampaign nor UpdateCampaign currently validates date ordering; the
// backstop this test checks for is the one write path that can never be
// skipped by a future store caller.
func TestSchema_InvertedWindowRejectedByCheckConstraint(t *testing.T) {
	pool := testPool(t)
	uid := seedUser(t, pool, "alice@example.com")

	starts := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	ends := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // before starts — inverted

	_, err := pool.Exec(context.Background(),
		`INSERT INTO campaigns (user_id, name, slug, starts_at, ends_at)
		 VALUES ($1, 'Inverted', 'inverted', $2, $3)`,
		uid, starts, ends,
	)
	if err == nil {
		t.Fatal("direct insert with ends_at before starts_at succeeded, want a check_violation")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgCheckViolation {
		t.Errorf("err = %v, want a check_violation (SQLSTATE %s)", err, pgCheckViolation)
	}
	if n := countCampaigns(t, pool); n != 0 {
		t.Errorf("campaigns after rejected insert = %d, want 0", n)
	}
}
