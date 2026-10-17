package devstore

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// apiTokenEntry is the in-memory api_tokens row. Like the real store, only the
// digest of the secret is kept.
type apiTokenEntry struct {
	auth.APIToken
	userID int64
	hash   []byte
}

// CreateAPIToken mirrors auth.Store.CreateAPIToken in memory. The audit
// arguments are ignored (dev mode has no audit writer).
func (s *Store) CreateAPIToken(_ context.Context, in auth.NewAPIToken, _ *audit.Logger, _ audit.Entry) (auth.APIToken, string, error) {
	secret, err := auth.NewAPITokenSecret()
	if err != nil {
		return auth.APIToken{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.apiTokens {
		if t.userID == in.UserID && t.RevokedAt == nil && t.Name == in.Name {
			return auth.APIToken{}, "", auth.ErrAPITokenNameTaken
		}
	}
	s.nextAPITokenID++
	t := auth.APIToken{
		ID:        s.nextAPITokenID,
		Name:      in.Name,
		Prefix:    secret[:len(auth.APITokenPrefix)+8],
		ReadOnly:  in.ReadOnly,
		CreatedAt: time.Now(),
		ExpiresAt: in.ExpiresAt,
	}
	s.apiTokens = append(s.apiTokens, apiTokenEntry{APIToken: t, userID: in.UserID, hash: auth.HashAPIToken(secret)})
	return t, secret, nil
}

// ListAPITokensForUser mirrors auth.Store.ListAPITokensForUser (newest first).
func (s *Store) ListAPITokensForUser(_ context.Context, userID int64) ([]auth.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []auth.APIToken
	for i := len(s.apiTokens) - 1; i >= 0; i-- {
		if s.apiTokens[i].userID == userID {
			out = append(out, s.apiTokens[i].APIToken)
		}
	}
	return out, nil
}

// RevokeAPIToken mirrors auth.Store.RevokeAPIToken.
func (s *Store) RevokeAPIToken(_ context.Context, userID, id int64, now time.Time, _ *audit.Logger, _ audit.Entry) (auth.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.apiTokens {
		t := &s.apiTokens[i]
		if t.ID == id && t.userID == userID && t.RevokedAt == nil {
			at := now
			t.RevokedAt = &at
			return t.APIToken, nil
		}
	}
	return auth.APIToken{}, auth.ErrAPITokenNotFound
}

// ResolveAPIToken mirrors auth.Store.ResolveAPIToken: live token, active owner,
// never admin.
func (s *Store) ResolveAPIToken(_ context.Context, token, ip string, now time.Time) (auth.APITokenUser, error) {
	if !strings.HasPrefix(token, auth.APITokenPrefix) {
		return auth.APITokenUser{}, auth.ErrAPITokenInvalid
	}
	hash := auth.HashAPIToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.apiTokens {
		t := &s.apiTokens[i]
		if !bytes.Equal(t.hash, hash) || t.RevokedAt != nil || (t.ExpiresAt != nil && !t.ExpiresAt.After(now)) {
			continue
		}
		for _, u := range s.users {
			if u.ID == t.userID && u.Active {
				at := now
				t.LastUsedAt, t.LastUsedIP = &at, ip
				return auth.APITokenUser{
					SessionUser: auth.SessionUser{ID: u.ID, Email: u.Email},
					TokenID:     t.ID,
					TokenName:   t.Name,
					ReadOnly:    t.ReadOnly,
				}, nil
			}
		}
	}
	return auth.APITokenUser{}, auth.ErrAPITokenInvalid
}
