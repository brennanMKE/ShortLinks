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
		`TRUNCATE links, campaigns, audit_log, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// seedLink inserts an active, non-denied link for userID, optionally
// assigned to a campaign (pass nil for none), and returns its id. Raw SQL
// (mirroring handlers/links_test.go's seedLink) since this package does not
// import internal/links.
func seedLink(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string, campaignID *int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, campaign_id)
		 VALUES ($1, $2, $3, TRUE, 0, now(), $4) RETURNING id`,
		userID, key, dest, campaignID,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

// linkCampaignID reads a link's current campaign_id (nil if NULL), for
// assertions that a mutation actually reached the links table.
func linkCampaignID(t *testing.T, pool *pgxpool.Pool, linkID int64) *int64 {
	t.Helper()
	var id *int64
	if err := pool.QueryRow(context.Background(),
		`SELECT campaign_id FROM links WHERE id = $1`, linkID,
	).Scan(&id); err != nil {
		t.Fatalf("reading link campaign_id: %v", err)
	}
	return id
}

// seedClick inserts a click row directly (raw SQL — this package does not
// import internal/clicks) carrying the given link_id/campaign_id/is_bot,
// backing the #0102 link_count/total_clicks aggregation tests below.
func seedClick(t *testing.T, pool *pgxpool.Pool, linkID int64, campaignID *int64, isBot bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO clicks (link_id, campaign_id, is_bot, clicked_at) VALUES ($1, $2, $3, now())`,
		linkID, campaignID, isBot,
	); err != nil {
		t.Fatalf("seed click: %v", err)
	}
}

// linkClickCount reads a single link's bot-excluded click count directly,
// using the SAME correlated-subquery spelling (`AND c.is_bot = FALSE`)
// links.Store.ListLinks/GetLink already use for their own click_count column
// (see internal/links/store.go) — the actual cross-check
// TestListCampaignsForUser_LinkCountAndTotalClicksAggregate's doc comment
// claims: campaigns.total_clicks must equal the SUM of every assigned
// link's own click_count, not merely an absolute number picked to match
// whatever the store happens to compute.
func linkClickCount(t *testing.T, pool *pgxpool.Pool, linkID int64) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM clicks c WHERE c.link_id = $1 AND c.is_bot = FALSE`, linkID,
	).Scan(&n); err != nil {
		t.Fatalf("reading link click count: %v", err)
	}
	return n
}

// findCampaignWithCounts returns the entry for slug, or nil if absent.
func findCampaignWithCounts(rows []CampaignWithCounts, slug string) *CampaignWithCounts {
	for i := range rows {
		if rows[i].Slug == slug {
			return &rows[i]
		}
	}
	return nil
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

// TestListCampaignsForUser_LinkCountAndTotalClicksAggregate is the #0102
// acceptance criterion at the store layer: link_count counts the links
// currently assigned to the campaign, and total_clicks sums their bot-
// excluded clicks — cross-checked against links.Store's own click_count
// convention (`AND is_bot = FALSE`, the same spelling reused here) rather
// than asserting only an absolute number, per #0101's downstream constraint
// 3.
func TestListCampaignsForUser_LinkCountAndTotalClicksAggregate(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link1 := seedLink(t, pool, alice, "cnt0001", "https://example.com/1", &c.ID)
	link2 := seedLink(t, pool, alice, "cnt0002", "https://example.com/2", &c.ID)
	// A link belonging to alice but NOT assigned to this campaign must not
	// contribute to either count.
	_ = seedLink(t, pool, alice, "cnt0003", "https://example.com/3", nil)

	seedClick(t, pool, link1, &c.ID, false)
	seedClick(t, pool, link1, &c.ID, false)
	seedClick(t, pool, link2, &c.ID, false)
	seedClick(t, pool, link2, &c.ID, true) // bot click, excluded

	got, err := store.ListCampaignsForUser(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	row := findCampaignWithCounts(got, c.Slug)
	if row == nil {
		t.Fatalf("campaign %q missing from list: %+v", c.Slug, got)
	}
	if row.LinkCount != 2 {
		t.Errorf("link_count = %d, want 2 (the unassigned link must not count)", row.LinkCount)
	}
	if row.TotalClicks != 3 {
		t.Errorf("total_clicks = %d, want 3 (bot click excluded)", row.TotalClicks)
	}

	// THE CROSS-CHECK the doc comment above promises: total_clicks must equal
	// the sum of each assigned link's own (independently-queried) click
	// count, using links.Store's own bot-exclusion spelling. This is what
	// actually proves campaignCountColumns' subquery and links.Store's
	// click_count subquery agree — asserting only the absolute number "3"
	// does not, since a coincidental off-by-nothing bug in BOTH places could
	// still satisfy that assertion.
	wantTotal := linkClickCount(t, pool, link1) + linkClickCount(t, pool, link2)
	if row.TotalClicks != wantTotal {
		t.Errorf("total_clicks = %d, want %d (sum of each assigned link's own click_count)", row.TotalClicks, wantTotal)
	}
}

// TestListCampaignsForUser_EmptyCampaignReturnsZeroCounts asserts a campaign
// with no links and no clicks appears with 0/0, not omitted or errored.
func TestListCampaignsForUser_EmptyCampaignReturnsZeroCounts(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Empty"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	got, err := store.ListCampaignsForUser(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	row := findCampaignWithCounts(got, c.Slug)
	if row == nil {
		t.Fatalf("campaign %q missing from list: %+v", c.Slug, got)
	}
	if row.LinkCount != 0 || row.TotalClicks != 0 {
		t.Errorf("empty campaign counts = link_count=%d total_clicks=%d, want 0/0", row.LinkCount, row.TotalClicks)
	}
}

// TestListCampaignsForUser_BotOnlyCampaignStillAppearsWithZeroTotalClicks is
// the #0101/#0102 LEFT JOIN...ON-vs-WHERE trap, generalized to the campaign
// list: a campaign whose every click is bot-flagged must still appear (with
// total_clicks = 0), not vanish because a filtering join collapsed its
// GROUP BY. campaignCountColumns uses correlated subqueries specifically to
// avoid that trap; this test would fail against a naive
// "JOIN clicks ... WHERE is_bot = FALSE" implementation the same way
// #0101's own link-list test caught the equivalent bug for links.
func TestListCampaignsForUser_BotOnlyCampaignStillAppearsWithZeroTotalClicks(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Bot Only"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link := seedLink(t, pool, alice, "botonly1", "https://example.com", &c.ID)
	seedClick(t, pool, link, &c.ID, true)
	seedClick(t, pool, link, &c.ID, true)

	got, err := store.ListCampaignsForUser(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	row := findCampaignWithCounts(got, c.Slug)
	if row == nil {
		t.Fatalf("bot-only campaign %q vanished from the list: %+v", c.Slug, got)
	}
	if row.LinkCount != 1 {
		t.Errorf("link_count = %d, want 1", row.LinkCount)
	}
	if row.TotalClicks != 0 {
		t.Errorf("total_clicks = %d, want 0 (all clicks are bot-flagged)", row.TotalClicks)
	}
}

// TestListCampaignsForUser_SinceUnassignedLinkClicksStillCountTowardTotalClicks
// asserts total_clicks is driven by clicks.campaign_id (#0100's
// denormalization), not links.campaign_id: a click recorded while its link
// belonged to the campaign keeps counting toward the campaign's
// total_clicks even after the link is later reassigned/unassigned.
func TestListCampaignsForUser_SinceUnassignedLinkClicksStillCountTowardTotalClicks(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Reassign"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link := seedLink(t, pool, alice, "reassign1", "https://example.com", &c.ID)
	seedClick(t, pool, link, &c.ID, false)
	seedClick(t, pool, link, &c.ID, false)

	// Unassign the link from the campaign directly (mirrors what
	// UnassignLinkFromCampaign does to links.campaign_id).
	if _, err := pool.Exec(context.Background(), `UPDATE links SET campaign_id = NULL WHERE id = $1`, link); err != nil {
		t.Fatalf("unassign link: %v", err)
	}

	got, err := store.ListCampaignsForUser(context.Background(), alice)
	if err != nil {
		t.Fatalf("ListCampaignsForUser: %v", err)
	}
	row := findCampaignWithCounts(got, c.Slug)
	if row == nil {
		t.Fatalf("campaign %q missing from list: %+v", c.Slug, got)
	}
	if row.LinkCount != 0 {
		t.Errorf("link_count = %d, want 0 (the link is no longer assigned)", row.LinkCount)
	}
	if row.TotalClicks != 2 {
		t.Errorf("total_clicks = %d, want 2 (historical clicks stay attributed via clicks.campaign_id)", row.TotalClicks)
	}
}

// TestGetCampaignBySlugWithCounts_ReturnsCounts asserts the detail-page
// lookup returns the same aggregate values ListCampaignsForUser computes.
func TestGetCampaignBySlugWithCounts_ReturnsCounts(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Detail"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link := seedLink(t, pool, alice, "detail01", "https://example.com", &c.ID)
	seedClick(t, pool, link, &c.ID, false)

	got, err := store.GetCampaignBySlugWithCounts(context.Background(), alice, c.Slug)
	if err != nil {
		t.Fatalf("GetCampaignBySlugWithCounts: %v", err)
	}
	if got.LinkCount != 1 || got.TotalClicks != 1 {
		t.Errorf("counts = link_count=%d total_clicks=%d, want 1/1", got.LinkCount, got.TotalClicks)
	}
}

// TestGetCampaignBySlugWithCounts_OwnershipEnforced asserts user B cannot
// read user A's campaign counts by slug: ErrCampaignNotFound,
// indistinguishable from a nonexistent slug.
func TestGetCampaignBySlugWithCounts_OwnershipEnforced(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Alice Only"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	_, err = store.GetCampaignBySlugWithCounts(context.Background(), bob, c.Slug)
	if !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("bob GetCampaignBySlugWithCounts(alice's slug) err = %v, want ErrCampaignNotFound", err)
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

// ── #0099: link membership ──────────────────────────────────────────────────

// TestGetCampaignByID_OwnershipScoped asserts GetCampaignByID mirrors
// GetCampaignBySlug's ownership contract: another user's lookup by id returns
// ErrCampaignNotFound, indistinguishable from a nonexistent id.
func TestGetCampaignByID_OwnershipScoped(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Summer Fair"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if _, err := store.GetCampaignByID(context.Background(), bob, c.ID); !errors.Is(err, ErrCampaignNotFound) {
		t.Errorf("bob GetCampaignByID(%d) err = %v, want ErrCampaignNotFound", c.ID, err)
	}
	got, err := store.GetCampaignByID(context.Background(), alice, c.ID)
	if err != nil {
		t.Fatalf("alice GetCampaignByID: %v", err)
	}
	if got.Slug != c.Slug {
		t.Errorf("got slug %q, want %q", got.Slug, c.Slug)
	}
}

// TestAssignLinkToCampaign_SetsColumnAndAudits proves AssignLinkToCampaign
// actually writes links.campaign_id (queried directly, not just via the
// return value) and writes a campaign.link_assigned audit row inside the
// same transaction.
func TestAssignLinkToCampaign_SetsColumnAndAudits(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com", nil)

	entry := createEntry(uid, audit.ActionCampaignLinkAssigned)
	entry.TargetType = audit.TargetLink
	if err := store.AssignLinkToCampaign(context.Background(), uid, c.ID, linkID, auditor, entry); err != nil {
		t.Fatalf("AssignLinkToCampaign: %v", err)
	}

	got := linkCampaignID(t, pool, linkID)
	if got == nil || *got != c.ID {
		t.Errorf("links.campaign_id = %v, want %d", got, c.ID)
	}
	if n := countAuditRows(t, pool, audit.ActionCampaignLinkAssigned); n != 1 {
		t.Errorf("campaign.link_assigned audit rows = %d, want 1", n)
	}
}

// TestAssignLinkToCampaign_MovesAlreadyAssignedLink proves the DECIDED
// behavior for the acceptance criterion "assigning an already-assigned link
// moves it (or is rejected)": assigning a link that already belongs to
// campaign A into campaign B succeeds and leaves it in B, not A. A rejecting
// implementation would return an error here instead of nil.
func TestAssignLinkToCampaign_MovesAlreadyAssignedLink(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	a, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Campaign A"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign A: %v", err)
	}
	b, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Campaign B"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com", &a.ID)

	entry := createEntry(uid, audit.ActionCampaignLinkAssigned)
	entry.TargetType = audit.TargetLink
	if err := store.AssignLinkToCampaign(context.Background(), uid, b.ID, linkID, auditor, entry); err != nil {
		t.Fatalf("AssignLinkToCampaign (move to B) returned an error, want success (moves, does not reject): %v", err)
	}

	got := linkCampaignID(t, pool, linkID)
	if got == nil || *got != b.ID {
		t.Errorf("links.campaign_id = %v, want %d (moved to B, not left in A)", got, b.ID)
	}
}

// TestAssignLinkToCampaign_LinkOwnershipEnforced asserts assigning a link
// that belongs to a DIFFERENT user fails (ErrLinkNotFound) and leaves the
// link's campaign_id untouched — the WHERE user_id = $3 defense-in-depth
// backstop on the UPDATE itself, independent of whatever ownership check the
// handler already performed.
func TestAssignLinkToCampaign_LinkOwnershipEnforced(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: alice, Name: "Alice Campaign"}, auditor, createEntry(alice, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	bobLinkID := seedLink(t, pool, bob, "bobs-link", "https://example.com", nil)

	entry := createEntry(alice, audit.ActionCampaignLinkAssigned)
	entry.TargetType = audit.TargetLink
	err = store.AssignLinkToCampaign(context.Background(), alice, c.ID, bobLinkID, auditor, entry)
	if !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("alice AssignLinkToCampaign(bob's link) err = %v, want ErrLinkNotFound", err)
	}
	if got := linkCampaignID(t, pool, bobLinkID); got != nil {
		t.Errorf("bob's link campaign_id = %v, want still NULL (unaffected by alice's failed assign)", *got)
	}
}

// TestAssignLinkToCampaign_CampaignOwnershipEnforced is the symmetric
// counterpart to TestAssignLinkToCampaign_LinkOwnershipEnforced (#0099
// review item 7): AssignLinkToCampaign's UPDATE re-verifies that campaignID
// itself belongs to userID, not just that linkID does. Calls the store
// directly with alice's OWN link but BOB's campaign id — a combination the
// handler can never actually produce (it resolves campaignID via the
// already-scoped GetCampaignBySlug/GetCampaignByID first), but this proves
// the store's own defense-in-depth does not have an asymmetric gap between
// its two foreign ids.
func TestAssignLinkToCampaign_CampaignOwnershipEnforced(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")

	bobCampaign, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: bob, Name: "Bob Campaign"}, auditor, createEntry(bob, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	aliceLinkID := seedLink(t, pool, alice, "alices-link", "https://example.com", nil)

	entry := createEntry(alice, audit.ActionCampaignLinkAssigned)
	entry.TargetType = audit.TargetLink
	err = store.AssignLinkToCampaign(context.Background(), alice, bobCampaign.ID, aliceLinkID, auditor, entry)
	if !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("alice AssignLinkToCampaign(her own link, bob's campaign) err = %v, want ErrLinkNotFound", err)
	}
	if got := linkCampaignID(t, pool, aliceLinkID); got != nil {
		t.Errorf("alice's link campaign_id = %v, want still NULL (never assigned into bob's campaign)", *got)
	}
}

// TestUnassignLinkFromCampaign_ClearsColumn proves UnassignLinkFromCampaign
// writes campaign_id = NULL (queried directly) and audits
// campaign.link_unassigned.
func TestUnassignLinkFromCampaign_ClearsColumn(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com", &c.ID)

	entry := createEntry(uid, audit.ActionCampaignLinkUnassigned)
	entry.TargetType = audit.TargetLink
	if err := store.UnassignLinkFromCampaign(context.Background(), uid, c.ID, linkID, auditor, entry); err != nil {
		t.Fatalf("UnassignLinkFromCampaign: %v", err)
	}

	if got := linkCampaignID(t, pool, linkID); got != nil {
		t.Errorf("links.campaign_id = %v, want NULL", *got)
	}
	if n := countAuditRows(t, pool, audit.ActionCampaignLinkUnassigned); n != 1 {
		t.Errorf("campaign.link_unassigned audit rows = %d, want 1", n)
	}
}

// TestUnassignLinkFromCampaign_WrongCampaignReturnsNotFound asserts
// unassigning a link that is currently assigned to a DIFFERENT campaign (not
// campaignID) reports ErrLinkNotFound and leaves the link's actual
// assignment untouched, rather than silently clearing it.
func TestUnassignLinkFromCampaign_WrongCampaignReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	a, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Campaign A"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign A: %v", err)
	}
	b, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Campaign B"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com", &a.ID)

	entry := createEntry(uid, audit.ActionCampaignLinkUnassigned)
	entry.TargetType = audit.TargetLink
	err = store.UnassignLinkFromCampaign(context.Background(), uid, b.ID, linkID, auditor, entry)
	if !errors.Is(err, ErrLinkNotFound) {
		t.Errorf("UnassignLinkFromCampaign(wrong campaign B) err = %v, want ErrLinkNotFound", err)
	}
	got := linkCampaignID(t, pool, linkID)
	if got == nil || *got != a.ID {
		t.Errorf("links.campaign_id = %v, want still %d (unaffected by the failed unassign)", got, a.ID)
	}
}

// TestDeleteCampaign_SetsLinksCampaignIDNullAndKeepsLinks is the mutation-
// critical proof that migration 000011's ON DELETE SET NULL actually does
// what the issue requires: deleting a campaign with links succeeds, nulls
// their campaign_id (queried directly), and deletes NO link row. Without the
// FK's ON DELETE SET NULL, this DELETE would instead fail with a foreign-key
// violation surfaced as a 500 — this test would then fail at the
// `store.DeleteCampaign` call itself.
func TestDeleteCampaign_SetsLinksCampaignIDNullAndKeepsLinks(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	link1 := seedLink(t, pool, uid, "link1", "https://example.com/1", &c.ID)
	link2 := seedLink(t, pool, uid, "link2", "https://example.com/2", &c.ID)

	if err := store.DeleteCampaign(context.Background(), uid, c.Slug, auditor, createEntry(uid, audit.ActionCampaignDeleted)); err != nil {
		t.Fatalf("DeleteCampaign with links assigned: %v", err)
	}

	var linkCount int64
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM links WHERE id IN ($1, $2)`, link1, link2).Scan(&linkCount); err != nil {
		t.Fatalf("counting links: %v", err)
	}
	if linkCount != 2 {
		t.Errorf("links remaining = %d, want 2 (delete must not cascade to links)", linkCount)
	}
	if got := linkCampaignID(t, pool, link1); got != nil {
		t.Errorf("link1 campaign_id = %v, want NULL", *got)
	}
	if got := linkCampaignID(t, pool, link2); got != nil {
		t.Errorf("link2 campaign_id = %v, want NULL", *got)
	}
}

// TestDeleteCampaign_AuditRecordsUnassignedLinksCount asserts the DECIDED
// behavior for #0098's downstream constraint 3: rather than accept that the
// campaign.deleted audit row cannot describe how many links were unassigned
// (since the FK does the unassignment, not application code), DeleteCampaign
// counts them beforehand and stamps the count into the audit metadata.
func TestDeleteCampaign_AuditRecordsUnassignedLinksCount(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	auditor := audit.New(pool)
	uid := seedUser(t, pool, "alice@example.com")

	c, err := store.CreateCampaign(context.Background(), NewCampaign{UserID: uid, Name: "Summer Fair"}, auditor, createEntry(uid, audit.ActionCampaignCreated))
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	seedLink(t, pool, uid, "link1", "https://example.com/1", &c.ID)
	seedLink(t, pool, uid, "link2", "https://example.com/2", &c.ID)
	seedLink(t, pool, uid, "link3", "https://example.com/3", &c.ID)

	if err := store.DeleteCampaign(context.Background(), uid, c.Slug, auditor, createEntry(uid, audit.ActionCampaignDeleted)); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}

	var metaRaw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, audit.ActionCampaignDeleted,
	).Scan(&metaRaw); err != nil {
		t.Fatalf("querying audit metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("unmarshalling audit metadata: %v", err)
	}
	count, ok := meta["unassigned_links_count"]
	if !ok {
		t.Fatal("audit metadata[\"unassigned_links_count\"] is missing")
	}
	if count != float64(3) {
		t.Errorf("unassigned_links_count = %v, want 3", count)
	}
}

// TestDeleteCampaign_AuditRecordsZeroWhenNoLinks asserts the count is 0 (not
// absent) for a campaign with no links, so the metadata key is always
// present and reliably parseable.
func TestDeleteCampaign_AuditRecordsZeroWhenNoLinks(t *testing.T) {
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

	var metaRaw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT metadata FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, audit.ActionCampaignDeleted,
	).Scan(&metaRaw); err != nil {
		t.Fatalf("querying audit metadata: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("unmarshalling audit metadata: %v", err)
	}
	if count, ok := meta["unassigned_links_count"]; !ok || count != float64(0) {
		t.Errorf("unassigned_links_count = %v (present=%v), want 0", count, ok)
	}
}
