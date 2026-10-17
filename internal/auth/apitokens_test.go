package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// Live-DB tests for the API-token store (skipped without TEST_DATABASE_URL).

func userIDByEmail(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email = $1`, email).Scan(&id); err != nil {
		t.Fatalf("lookup user %s: %v", email, err)
	}
	return id
}

func mintToken(t *testing.T, s *Store, userID int64, name string, readOnly bool, exp *time.Time) (APIToken, string) {
	t.Helper()
	tok, secret, err := s.CreateAPIToken(context.Background(),
		NewAPIToken{UserID: userID, Name: name, ReadOnly: readOnly, ExpiresAt: exp}, nil, audit.Entry{})
	if err != nil {
		t.Fatalf("CreateAPIToken(%q): %v", name, err)
	}
	return tok, secret
}

// The plaintext is never stored: only its SHA-256 digest, plus a short prefix.
func TestCreateAPIToken_StoresOnlyDigest(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "tok@example.com", false)
	uid := userIDByEmail(t, pool, "tok@example.com")
	s := NewStore(pool)

	tok, secret := mintToken(t, s, uid, "agent", false, nil)
	if !strings.HasPrefix(secret, APITokenPrefix) || len(secret) < 40 {
		t.Fatalf("secret %q: want slk_ prefix and ≥40 chars", secret)
	}
	if !strings.HasPrefix(secret, tok.Prefix) || len(tok.Prefix) != apiTokenDisplayLen {
		t.Errorf("prefix %q is not a %d-char head of the secret", tok.Prefix, apiTokenDisplayLen)
	}

	var hash []byte
	var prefix string
	if err := pool.QueryRow(context.Background(),
		`SELECT token_hash, token_prefix FROM api_tokens WHERE id = $1`, tok.ID).Scan(&hash, &prefix); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, HashAPIToken(secret)) {
		t.Error("stored hash is not sha256(secret)")
	}
	if n := scanCount(t, pool,
		`SELECT COUNT(*) FROM api_tokens t WHERE t::text LIKE '%' || $1 || '%'`, secret[len(APITokenPrefix):]); n != 0 {
		t.Error("plaintext secret appears in the api_tokens row")
	}
}

func TestResolveAPIToken(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "admin-owner@example.com", true)
	uid := userIDByEmail(t, pool, "admin-owner@example.com")
	s := NewStore(pool)
	ctx := context.Background()
	now := time.Now()

	tok, secret := mintToken(t, s, uid, "agent", true, nil)
	u, err := s.ResolveAPIToken(ctx, secret, "203.0.113.9", now)
	if err != nil {
		t.Fatalf("resolve valid token: %v", err)
	}
	if u.ID != uid || u.TokenID != tok.ID || u.TokenName != "agent" || !u.ReadOnly {
		t.Errorf("resolved %+v", u)
	}
	if u.IsAdmin {
		t.Error("a token minted by an admin must not resolve as admin")
	}

	// last_used is recorded, then throttled.
	list, _ := s.ListAPITokensForUser(ctx, uid)
	if list[0].LastUsedAt == nil || list[0].LastUsedIP != "203.0.113.9" {
		t.Fatalf("last used not recorded: %+v", list[0])
	}
	first := *list[0].LastUsedAt
	if _, err := s.ResolveAPIToken(ctx, secret, "198.51.100.1", now.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListAPITokensForUser(ctx, uid)
	if !list[0].LastUsedAt.Equal(first) || list[0].LastUsedIP != "203.0.113.9" {
		t.Error("last_used was rewritten inside the throttle window")
	}

	for _, bad := range []string{"", "slk_", "slk_wrong", secret + "x", strings.TrimPrefix(secret, APITokenPrefix)} {
		if _, err := s.ResolveAPIToken(ctx, bad, "", now); !errors.Is(err, ErrAPITokenInvalid) {
			t.Errorf("resolve %q: err = %v, want ErrAPITokenInvalid", bad, err)
		}
	}
}

func TestResolveAPIToken_ExpiryRevocationDeactivation(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "u@example.com", false)
	uid := userIDByEmail(t, pool, "u@example.com")
	s := NewStore(pool)
	ctx := context.Background()
	now := time.Now()

	exp := now.Add(time.Hour)
	_, expiring := mintToken(t, s, uid, "expiring", false, &exp)
	if _, err := s.ResolveAPIToken(ctx, expiring, "", now); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	if _, err := s.ResolveAPIToken(ctx, expiring, "", exp.Add(time.Second)); !errors.Is(err, ErrAPITokenInvalid) {
		t.Errorf("after expiry: err = %v, want invalid", err)
	}

	forever, foreverSecret := mintToken(t, s, uid, "forever", false, nil)
	if _, err := s.ResolveAPIToken(ctx, foreverSecret, "", now.AddDate(50, 0, 0)); err != nil {
		t.Errorf("never-expiring token rejected 50y out: %v", err)
	}

	if _, err := s.RevokeAPIToken(ctx, uid, forever.ID, now, nil, audit.Entry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIToken(ctx, foreverSecret, "", now); !errors.Is(err, ErrAPITokenInvalid) {
		t.Errorf("after revoke: err = %v, want invalid", err)
	}
	if _, err := s.RevokeAPIToken(ctx, uid, forever.ID, now, nil, audit.Entry{}); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("double revoke: err = %v, want not found", err)
	}

	// Deactivating the owner revokes their tokens, and reactivating does not
	// bring them back.
	_, live := mintToken(t, s, uid, "live", false, nil)
	if _, err := s.DeactivateUser(ctx, uid, nil, audit.Entry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIToken(ctx, live, "", now); !errors.Is(err, ErrAPITokenInvalid) {
		t.Errorf("deactivated owner: err = %v, want invalid", err)
	}
	if _, err := s.ReactivateUser(ctx, uid, nil, audit.Entry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveAPIToken(ctx, live, "", now); !errors.Is(err, ErrAPITokenInvalid) {
		t.Errorf("after reactivation: err = %v, want still revoked", err)
	}
}

func TestAPIToken_OwnershipAndNames(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "a@example.com", false)
	insertUser(t, pool, "b@example.com", false)
	a, b := userIDByEmail(t, pool, "a@example.com"), userIDByEmail(t, pool, "b@example.com")
	s := NewStore(pool)
	ctx := context.Background()

	tok, _ := mintToken(t, s, a, "agent", false, nil)
	if _, _, err := s.CreateAPIToken(ctx, NewAPIToken{UserID: a, Name: "agent"}, nil, audit.Entry{}); !errors.Is(err, ErrAPITokenNameTaken) {
		t.Errorf("duplicate live name: err = %v, want ErrAPITokenNameTaken", err)
	}
	mintToken(t, s, b, "agent", false, nil) // another user may reuse the name

	if _, err := s.RevokeAPIToken(ctx, b, tok.ID, time.Now(), nil, audit.Entry{}); !errors.Is(err, ErrAPITokenNotFound) {
		t.Errorf("revoking another user's token: err = %v, want not found", err)
	}
	if list, _ := s.ListAPITokensForUser(ctx, b); len(list) != 1 || list[0].ID == tok.ID {
		t.Errorf("user b sees %+v", list)
	}

	if _, err := s.RevokeAPIToken(ctx, a, tok.ID, time.Now(), nil, audit.Entry{}); err != nil {
		t.Fatal(err)
	}
	mintToken(t, s, a, "agent", false, nil) // name is free again once revoked
}

// Create/revoke write their audit rows, and any row written under a context
// marked by audit.WithAPIToken is attributed to that token.
func TestAPIToken_Audit(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "aud@example.com", false)
	uid := userIDByEmail(t, pool, "aud@example.com")
	s := NewStore(pool)
	al := audit.New(pool)
	ctx := context.Background()

	tok, _, err := s.CreateAPIToken(ctx, NewAPIToken{UserID: uid, Name: "agent"}, al,
		audit.Entry{ActorID: &uid, UserID: &uid, Action: audit.ActionAPITokenCreated, TargetType: audit.TargetAPIToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RevokeAPIToken(ctx, uid, tok.ID, time.Now(), al,
		audit.Entry{ActorID: &uid, UserID: &uid, Action: audit.ActionAPITokenRevoked, TargetType: audit.TargetAPIToken}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{audit.ActionAPITokenCreated, audit.ActionAPITokenRevoked} {
		if n := scanCount(t, pool, `SELECT COUNT(*) FROM audit_log WHERE action = $1 AND target_id = $2`, action, tok.ID); n != 1 {
			t.Errorf("%s rows = %d, want 1", action, n)
		}
	}

	tokCtx := audit.WithAPIToken(ctx, audit.APITokenRef{ID: tok.ID, Name: "agent"})
	if err := al.Write(tokCtx, audit.Entry{ActorID: &uid, Action: audit.ActionLinkCreated, Metadata: map[string]any{"key": "abc"}}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT metadata FROM audit_log WHERE action = $1`, audit.ActionLinkCreated).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	_ = json.Unmarshal(raw, &meta)
	if meta["key"] != "abc" || meta["via"] != "api_token" || meta["api_token_name"] != "agent" || meta["api_token_id"] != float64(tok.ID) {
		t.Errorf("metadata = %s", raw)
	}
}
