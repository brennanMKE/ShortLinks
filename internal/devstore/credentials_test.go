package devstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// The dev store seeds two passkeys (#0188): the session's own device-bound one
// and a synced one, so the Account page has both states to show.
func TestDevCredentials_CurrentAndSynced(t *testing.T) {
	s := New("")
	ctx := context.Background()

	creds, err := s.ListCredentialsForUser(ctx, seedAdminID)
	if err != nil || len(creds) != 2 {
		t.Fatalf("list = %v, %v; want 2 credentials", creds, err)
	}
	synced := map[int64]bool{}
	for _, c := range creds {
		synced[c.ID] = c.BackupState
	}
	if synced[seedCurrentCredentialID] || !synced[2] {
		t.Errorf("backup states = %v, want seeded current device-bound and the other synced", synced)
	}

	tok, _, _ := s.CreateDevSession(seedAdminID, time.Hour)
	u, err := s.ResolveSession(ctx, tok, time.Now())
	if err != nil || u.CredentialID == nil || *u.CredentialID != seedCurrentCredentialID {
		t.Errorf("dev session credential = %v, %v; want %d", u.CredentialID, err, seedCurrentCredentialID)
	}
}

func TestDevCredentials_RenameAndRevoke(t *testing.T) {
	s := New("")
	ctx := context.Background()

	if err := s.RenameCredential(ctx, seedAdminID, 2, "Phone"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := s.RenameCredential(ctx, 99, 2, "x"); !errors.Is(err, auth.ErrCredentialNotFound) {
		t.Errorf("rename other user's = %v, want not found", err)
	}
	if err := s.RevokeCredential(ctx, seedAdminID, 2); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := s.RevokeCredential(ctx, seedAdminID, seedCurrentCredentialID); !errors.Is(err, auth.ErrLastCredential) {
		t.Errorf("revoke last = %v, want ErrLastCredential", err)
	}
}

// Removing the session's passkey unlinks the session (SET NULL parity).
func TestDevCredentials_RevokedCurrentUnlinksSession(t *testing.T) {
	s := New("")
	ctx := context.Background()
	tok, _, _ := s.CreateDevSession(seedAdminID, time.Hour)
	if err := s.RevokeCredential(ctx, seedAdminID, seedCurrentCredentialID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	u, err := s.ResolveSession(ctx, tok, time.Now())
	if err != nil || u.CredentialID != nil {
		t.Errorf("session credential after revoke = %v, %v; want nil", u.CredentialID, err)
	}
}
