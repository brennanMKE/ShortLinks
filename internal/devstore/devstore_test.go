package devstore_test

import (
	"context"
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
