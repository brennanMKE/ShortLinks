package devstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/devstore"
	"github.com/brennanMKE/ShortLinks/internal/links"
)

func TestNew_SeedsAdminAndLinks(t *testing.T) {
	s := devstore.New("admin@test.local")

	if s.AdminEmail() != "admin@test.local" {
		t.Errorf("AdminEmail = %q, want %q", s.AdminEmail(), "admin@test.local")
	}
	if s.AdminID() != 1 {
		t.Errorf("AdminID = %d, want 1", s.AdminID())
	}

	ctx := context.Background()

	// Two sample links seeded.
	n, err := s.CountLinks(ctx, 1)
	if err != nil {
		t.Fatalf("CountLinks: %v", err)
	}
	if n != 2 {
		t.Errorf("CountLinks = %d, want 2", n)
	}
}

func TestPing(t *testing.T) {
	s := devstore.New("")
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestResolve_SeededLinks(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()

	entry, found, err := s.Resolve(ctx, "wiki")
	if err != nil {
		t.Fatalf("Resolve wiki: %v", err)
	}
	if !found {
		t.Fatal("Resolve wiki: not found")
	}
	if entry.DestinationURL != "https://www.wikipedia.org" {
		t.Errorf("DestinationURL = %q, want https://www.wikipedia.org", entry.DestinationURL)
	}
	if !entry.Active {
		t.Error("wiki link should be active")
	}

	// Absent key.
	_, found, err = s.Resolve(ctx, "doesnotexist")
	if err != nil {
		t.Fatalf("Resolve missing: %v", err)
	}
	if found {
		t.Error("Resolve missing key: expected not found")
	}
}

func TestCreateLink_RoundTrip(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()
	const userID = int64(1)

	in := links.NewLink{
		UserID:         userID,
		Key:            "test",
		DestinationURL: "https://example.com",
		Title:          "Test Link",
	}
	created, err := s.CreateLink(ctx, in)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if created.Key != "test" {
		t.Errorf("Key = %q, want %q", created.Key, "test")
	}

	fetched, err := s.GetLink(ctx, userID, "test")
	if err != nil {
		t.Fatalf("GetLink: %v", err)
	}
	if fetched.DestinationURL != "https://example.com" {
		t.Errorf("DestinationURL = %q, want https://example.com", fetched.DestinationURL)
	}

	// Duplicate key rejected.
	_, err = s.CreateLink(ctx, in)
	if err == nil {
		t.Error("expected ErrKeyTaken for duplicate key, got nil")
	}
}

func TestDeactivateLink(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	if err := s.DeactivateLink(ctx, 1, "wiki"); err != nil {
		t.Fatalf("DeactivateLink: %v", err)
	}
	link, err := s.GetLink(ctx, 1, "wiki")
	if err != nil {
		t.Fatalf("GetLink after deactivate: %v", err)
	}
	if link.Active {
		t.Error("link should be inactive after DeactivateLink")
	}
}

func TestSettings(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	settings, err := s.ListSettings(ctx)
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	if len(settings) == 0 {
		t.Fatal("expected at least one setting")
	}

	old, err := s.UpdateSetting(ctx, "registrations_enabled", "false", time.Now())
	if err != nil {
		t.Fatalf("UpdateSetting: %v", err)
	}
	if old != "true" {
		t.Errorf("old value = %q, want %q", old, "true")
	}

	// Unknown key.
	_, err = s.UpdateSetting(ctx, "no_such_key", "val", time.Now())
	if err == nil {
		t.Error("expected ErrSettingNotFound for unknown key")
	}
}

func TestSession(t *testing.T) {
	s := devstore.New("admin@test.local")
	ctx := context.Background()

	tok, exp, err := s.CreateDevSession(1, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateDevSession: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty session token")
	}

	u, err := s.ResolveSession(ctx, tok, time.Now())
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if u.ID != 1 {
		t.Errorf("session user ID = %d, want 1", u.ID)
	}
	if !u.IsAdmin {
		t.Error("seeded admin should have is_admin=true")
	}

	// Expired session.
	_, err = s.ResolveSession(ctx, tok, exp.Add(time.Hour))
	if err == nil {
		t.Error("expected error for expired session")
	}

	// Unknown token.
	_, err = s.ResolveSession(ctx, "bogus-token", time.Now())
	if err == nil {
		t.Error("expected error for unknown token")
	}
	if err != auth.ErrSessionInvalid {
		t.Errorf("err = %v, want ErrSessionInvalid", err)
	}
}

func TestCreateOrReactivateLink(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	in := links.NewLink{
		UserID:         1,
		DestinationURL: "https://newsite.example.com",
		Title:          "New",
	}

	l1, outcome1, err := s.CreateOrReactivateLink(ctx, in, links.GenerateUniqueKey)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if outcome1 != links.OutcomeInserted {
		t.Errorf("outcome = %v, want OutcomeInserted", outcome1)
	}

	// Same URL again → active duplicate.
	_, outcome2, err := s.CreateOrReactivateLink(ctx, in, links.GenerateUniqueKey)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if outcome2 != links.OutcomeActiveDuplicate {
		t.Errorf("outcome = %v, want OutcomeActiveDuplicate", outcome2)
	}

	// Deactivate then re-create → reactivation.
	if err := s.DeactivateLink(ctx, 1, l1.Key); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	_, outcome3, err := s.CreateOrReactivateLink(ctx, in, links.GenerateUniqueKey)
	if err != nil {
		t.Fatalf("third create: %v", err)
	}
	if outcome3 != links.OutcomeReactivated {
		t.Errorf("outcome = %v, want OutcomeReactivated", outcome3)
	}
}

// TestCreateLinksBatch_TwoRowsDifferingOnlyInPlacementCreateTwoDistinctLinks
// is the dev-mode (in-memory) twin of the same-named test in
// internal/links/store_test.go: two rows sharing a byte-identical
// destination_url (placement is never baked into the URL) must still
// produce two links from the devstore twin, not one — CreateLinksBatch
// bypasses CreateOrReactivateLink's dedup lookup entirely (#0105), so
// ./scripts/dev.sh (STORAGE=json) exhibits the SAME fix real Postgres does,
// rather than silently regressing to the trap in dev mode only.
func TestCreateLinksBatch_TwoRowsDifferingOnlyInPlacementCreateTwoDistinctLinks(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	const sharedDest = "https://example.com/summer-sale?utm_source=flyer&utm_medium=print"
	rows := []links.NewLink{
		{UserID: 1, DestinationURL: sharedDest, UTMSource: "flyer", UTMMedium: "print", Placement: "18th & Texas board"},
		{UserID: 1, DestinationURL: sharedDest, UTMSource: "flyer", UTMMedium: "print", Placement: "Congress & 6th board"},
	}

	created, err := s.CreateLinksBatch(ctx, rows, links.GenerateUniqueKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d links, want 2 (dedup must be bypassed for batch rows sharing a destination_url)", len(created))
	}
	if created[0].ID == created[1].ID {
		t.Fatal("both rows resolved to the SAME link id — dedup collapsed them onto one row")
	}
	if created[0].Key == created[1].Key {
		t.Fatal("both rows got the SAME key")
	}
	if created[0].Placement == created[1].Placement {
		t.Fatal("both rows report the SAME placement — test setup or clone bug")
	}
}

// TestCreateLinksBatch_KeyCollisionRetriesAcrossRows mirrors the same-named
// Postgres-backed test: two rows scripted to collide on their first-choice
// key must retry — proving the in-memory exists closure sees row 1's
// already-appended key when checking row 2's candidate, since devstore has
// no transaction boundary and the whole loop runs under one lock instead.
func TestCreateLinksBatch_KeyCollisionRetriesAcrossRows(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	rows := []links.NewLink{
		{UserID: 1, DestinationURL: "https://example.com/a"},
		{UserID: 1, DestinationURL: "https://example.com/b"},
	}

	script := []string{"AAAAAA", "AAAAAA", "BBBBBB"}
	idx := 0
	scriptedGenKey := func(exists func(string) (bool, error)) (string, error) {
		for {
			if idx >= len(script) {
				t.Fatal("key script exhausted")
			}
			candidate := script[idx]
			idx++
			taken, err := exists(candidate)
			if err != nil {
				return "", err
			}
			if !taken {
				return candidate, nil
			}
		}
	}

	created, err := s.CreateLinksBatch(ctx, rows, scriptedGenKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d links, want 2", len(created))
	}
	if created[0].Key != "AAAAAA" {
		t.Errorf("row 0 key = %q, want %q", created[0].Key, "AAAAAA")
	}
	if created[1].Key != "BBBBBB" {
		t.Errorf("row 1 key = %q, want %q", created[1].Key, "BBBBBB")
	}
}

// TestCreateLinksBatch_GenKeyFailureRollsBackEarlierRows is review finding
// 3's regression test: when a LATER row's genKey call fails, rows appended
// EARLIER in the same call must not survive in the in-memory store, mirroring
// the real (Postgres) store's transactional all-or-nothing guarantee. Before
// this fix, only s.nextLinkID's restoration was ever attempted (and even
// that wasn't — see below); s.links itself kept whatever had already been
// appended, so a 3-row batch that failed on row 3 left rows 1 and 2
// permanently in the store despite CreateLinksBatch returning an error.
//
// Asserts two independent things a broken rollback could get wrong:
//  1. CountLinks for the batch's user is unchanged after the failed call —
//     rows 1 and 2 did not survive.
//  2. s.nextLinkID was also restored — a link created AFTER the failed batch
//     gets the SAME id a rolled-back row would have used, not one advanced
//     past it. devstore.New seeds exactly two links with ids 1 and 2, so
//     nextLinkID starts at 3; a correct rollback means the next successful
//     create after the failure still gets id 3.
func TestCreateLinksBatch_GenKeyFailureRollsBackEarlierRows(t *testing.T) {
	s := devstore.New("")
	ctx := context.Background()

	before, err := s.CountLinks(ctx, 1)
	if err != nil {
		t.Fatalf("CountLinks (before): %v", err)
	}

	rows := []links.NewLink{
		{UserID: 1, DestinationURL: "https://example.com/first-row"},
		{UserID: 1, DestinationURL: "https://example.com/second-row"},
		{UserID: 1, DestinationURL: "https://example.com/third-row"}, // genKey fails on this row
	}

	calls := 0
	failOnThirdRow := func(exists func(string) (bool, error)) (string, error) {
		calls++
		if calls == 3 {
			return "", errors.New("simulated key-space exhaustion on row 3")
		}
		if _, err := exists(fmt.Sprintf("KEY%d", calls)); err != nil {
			return "", err
		}
		return fmt.Sprintf("KEY%d", calls), nil
	}

	_, err = s.CreateLinksBatch(ctx, rows, failOnThirdRow)
	if err == nil {
		t.Fatal("CreateLinksBatch: expected an error from row 3's genKey failure, got nil")
	}

	after, err := s.CountLinks(ctx, 1)
	if err != nil {
		t.Fatalf("CountLinks (after): %v", err)
	}
	if after != before {
		t.Fatalf("CountLinks after a failed batch = %d, want %d (rows 1 and 2, appended earlier in the SAME call, must roll back with row 3)", after, before)
	}

	// nextLinkID must also be restored: the next link created after the
	// failed batch must reuse the id the rolled-back rows would have taken,
	// not skip past it.
	created, err := s.CreateLinksBatch(ctx, []links.NewLink{{UserID: 1, DestinationURL: "https://example.com/after-the-failure"}}, links.GenerateUniqueKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch (post-failure): %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d links, want 1", len(created))
	}
	const wantID = int64(3) // devstore.New seeds ids 1 and 2; nextLinkID starts at 3.
	if created[0].ID != wantID {
		t.Errorf("post-failure link id = %d, want %d (nextLinkID was not restored after the earlier failure)", created[0].ID, wantID)
	}
}
