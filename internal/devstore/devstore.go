// Package devstore provides an in-memory storage backend for local development.
//
// It implements every store interface the HTTP handlers depend on so the app can
// boot and serve the SPA and API with no PostgreSQL running. The store is
// selected ONLY by an explicit STORAGE=json (see config.Config.DevMode) — never
// by an empty DATABASE_URL — so production can't accidentally engage it.
//
// Design choices:
//   - In-memory (not JSON files) for simplicity and reliability. The issue's
//     acceptance criteria allow either; in-memory removes file I/O complexity
//     and is the preferred approach documented in the implementer brief.
//   - A single mutex guards all mutable state so concurrent requests are safe.
//   - Seeded with a mock admin user and two sample links so the UI is not empty.
//   - Satisfies the Pinger interface with a no-op so /health returns 200.
//   - Auth/credential/passkey operations return stubs (dev auth bypass is #0058).
package devstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/reports"
)

var logger = slog.Default()

// seedAdminID is the fixed user id for the seeded admin account.
const seedAdminID int64 = 1

// Store is the in-memory dev store. It implements all handler-facing interfaces
// in a single struct to keep wiring simple — main.go constructs one *Store and
// passes it to every constructor that needs a store argument.
type Store struct {
	mu        sync.Mutex
	users     []auth.ManagedUser
	links     []links.Link
	rules     []filters.FilterRule
	campaigns []campaigns.Campaign
	audit     []audit.Record
	sessions  map[string]sessionEntry // token → session
	settings  []auth.Setting
	apiTokens []apiTokenEntry
	// credentials are the seeded dev passkeys (#0188).
	credentials []devCredential
	// nextLinkID is the auto-increment counter for link IDs.
	nextLinkID int64
	// nextRuleID is the auto-increment counter for filter rule IDs.
	nextRuleID int64
	// nextCampaignID is the auto-increment counter for campaign IDs.
	nextCampaignID int64
	// nextAuditID is the auto-increment counter for audit record IDs.
	nextAuditID int64
	// nextAPITokenID is the auto-increment counter for API token IDs.
	nextAPITokenID int64
}

type sessionEntry struct {
	userID    int64
	expiresAt time.Time
}

// New constructs a Store seeded with a mock admin user and two sample links.
// adminEmail is the ADMIN_EMAIL config value; a sensible default is used when empty.
func New(adminEmail string) *Store {
	if adminEmail == "" {
		adminEmail = "admin@localhost"
	}
	now := time.Now()

	s := &Store{
		sessions:       make(map[string]sessionEntry),
		nextLinkID:     3, // IDs 1 and 2 are used by seeded links
		nextRuleID:     1,
		nextCampaignID: 1,
		nextAuditID:    1,
	}

	// Seed the mock admin user (id=1).
	s.users = []auth.ManagedUser{
		{
			ID:        seedAdminID,
			Email:     adminEmail,
			IsAdmin:   true,
			Active:    true,
			CreatedAt: now,
		},
	}

	// Seed two sample links owned by the admin so the dashboard is not empty.
	s.links = []links.Link{
		{
			ID:             1,
			UserID:         seedAdminID,
			Key:            "wiki",
			DestinationURL: "https://www.wikipedia.org",
			Title:          "Wikipedia",
			Active:         true,
			DeniedReason:   0,
			CreatedAt:      now.Add(-48 * time.Hour),
		},
		{
			ID:             2,
			UserID:         seedAdminID,
			Key:            "gh",
			DestinationURL: "https://github.com",
			Title:          "GitHub",
			Active:         true,
			DeniedReason:   0,
			CreatedAt:      now.Add(-24 * time.Hour),
		},
	}

	s.credentials = seedCredentials(now)
	s.seedAuditLog(now)

	// Seed the registrations_enabled setting.
	s.settings = []auth.Setting{
		{Key: "registrations_enabled", Value: "true"},
		{Key: "home_redirect_url", Value: ""},
		{Key: "home_mode", Value: "default"},
		{Key: "home_title", Value: ""},
		{Key: "home_about", Value: ""},
	}

	logger.Info("devstore: seeded in-memory store", "admin_email", adminEmail, "links", len(s.links))
	return s
}

// seedAuditLog adds two more accounts and a spread of audit rows (only for
// entities that exist in the seed: users 1-3 and links 1-2) so the Admin
// audit log (filters, relative times, metadata) has something to show in dev
// mode, where the audit writer is otherwise nil.
func (s *Store) seedAuditLog(now time.Time) {
	s.users = append(s.users,
		auth.ManagedUser{ID: 2, Email: "sam@example.com", Active: true, CreatedAt: now.Add(-30 * 24 * time.Hour)},
		auth.ManagedUser{ID: 3, Email: "riley@example.com", Active: true, CreatedAt: now.Add(-9 * 24 * time.Hour)},
	)
	i64 := func(v int64) *int64 { return &v }
	str := func(v string) *string { return &v }
	meta := func(v string) json.RawMessage { return json.RawMessage(v) }
	rows := []struct {
		ago      time.Duration
		actor    *int64
		user     *int64
		action   string
		tType    *string
		tID      *int64
		metadata json.RawMessage
	}{
		{26 * 24 * time.Hour, i64(2), i64(2), audit.ActionAccountLogin, nil, nil, meta(`{"device":"MacBook"}`)},
		{20 * 24 * time.Hour, i64(1), i64(1), audit.ActionLinkCreated, str(audit.TargetLink), i64(1), meta(`{"key":"wiki","destination_url":"https://www.wikipedia.org"}`)},
		{6 * 24 * time.Hour, i64(3), i64(3), audit.ActionAccountRegistered, str(audit.TargetUser), i64(3), nil},
		{4 * 24 * time.Hour, i64(1), i64(1), audit.ActionLinkReactivated, str(audit.TargetLink), i64(2), meta(`{"key":"gh"}`)},
		{3 * 24 * time.Hour, i64(1), i64(1), audit.ActionSettingsUpdated, str(audit.TargetSettings), nil, meta(`{"key":"registrations_enabled","new_value":"true"}`)},
		{30 * time.Hour, i64(1), i64(1), audit.ActionLinkCreated, str(audit.TargetLink), i64(2), meta(`{"key":"gh","destination_url":"https://github.com","duplicate":false}`)},
		{5 * time.Hour, i64(1), i64(3), audit.ActionAccountDeactivated, str(audit.TargetUser), i64(3), meta(`{"reason":"spam","note":"bulk links"}`)},
		{3 * time.Hour, i64(2), i64(2), audit.ActionAccountLogout, nil, nil, nil},
		{2 * time.Hour, i64(1), i64(3), audit.ActionAccountReactivated, str(audit.TargetUser), i64(3), nil},
		{40 * time.Minute, i64(1), i64(1), audit.ActionAccountLogin, nil, nil, meta(`{"device":"iPhone"}`)},
		{5 * time.Minute, nil, nil, audit.ActionAccountRegistrationStarted, nil, nil, meta(`{"email":"new@example.com"}`)},
	}
	for _, r := range rows {
		s.audit = append(s.audit, audit.Record{
			ID: s.nextAuditID, ActorID: r.actor, UserID: r.user, Action: r.action,
			TargetType: r.tType, TargetID: r.tID, Metadata: r.metadata,
			CreatedAt: now.Add(-r.ago),
		})
		s.nextAuditID++
	}
}

// ── Pinger ─────────────────────────────────────────────────────────────────

// Ping satisfies handlers.Pinger; always returns nil so /health reports "ok".
func (s *Store) Ping(_ context.Context) error { return nil }

// ── middleware.SessionResolver ──────────────────────────────────────────────

// ResolveSession validates a dev session token. In dev mode sessions are seeded
// directly (see CreateDevSession); passkey login is not available until #0058.
func (s *Store) ResolveSession(ctx context.Context, token string, now time.Time) (auth.SessionUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.sessions[token]
	if !ok || !entry.expiresAt.After(now) {
		return auth.SessionUser{}, auth.ErrSessionInvalid
	}
	for _, u := range s.users {
		if u.ID == entry.userID {
			su := auth.SessionUser{ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin}
			// SET NULL parity: once the seeded passkey is removed, the
			// session is no longer tied to one.
			if u.ID == seedAdminID {
				for _, c := range s.credentials {
					if c.ID == seedCurrentCredentialID && c.userID == u.ID {
						cur := seedCurrentCredentialID
						su.CredentialID = &cur
					}
				}
			}
			return su, nil
		}
	}
	return auth.SessionUser{}, auth.ErrSessionInvalid
}

// CreateDevSession inserts a session for the given userID and returns the token
// and its expiry. Used by the #0058 dev auth bypass to log in the mock admin
// without a WebAuthn ceremony.
func (s *Store) CreateDevSession(userID int64, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	tok, err := auth.NewSessionToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("devstore: generating session token: %w", err)
	}
	exp := time.Now().Add(ttl)
	s.mu.Lock()
	s.sessions[tok] = sessionEntry{userID: userID, expiresAt: exp}
	s.mu.Unlock()
	return tok, exp, nil
}

// DeleteSession removes a session token (logout). Returns the user id or 0 when
// not found (idempotent, mirrors auth.Store.DeleteSession).
func (s *Store) DeleteSession(_ context.Context, token string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[token]
	if !ok {
		return 0, nil
	}
	delete(s.sessions, token)
	return e.userID, nil
}

// DeleteSessionsForUser removes every session belonging to userID and returns
// how many were deleted ("sign out everywhere", #0094). Mirrors
// auth.Store.DeleteSessionsForUser; idempotent like DeleteSession.
func (s *Store) DeleteSessionsForUser(_ context.Context, userID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for tok, e := range s.sessions {
		if e.userID == userID {
			delete(s.sessions, tok)
			n++
		}
	}
	return n, nil
}

// ── settingStore (handlers.SettingsHandler) ─────────────────────────────────

// ListSettings returns all dev settings.
func (s *Store) ListSettings(_ context.Context) ([]auth.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.Setting, len(s.settings))
	copy(out, s.settings)
	return out, nil
}

// GetSetting reads one setting's value; ErrSettingNotFound when absent (#0125).
func (s *Store) GetSetting(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.settings {
		if st.Key == key {
			return st.Value, nil
		}
	}
	return "", auth.ErrSettingNotFound
}

// UpdateSetting updates an existing setting. Returns ErrSettingNotFound for an
// unknown key (no new keys are created via this path).
func (s *Store) UpdateSetting(_ context.Context, key, value string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, st := range s.settings {
		if st.Key == key {
			old := st.Value
			s.settings[i].Value = value
			s.settings[i].UpdatedAt = &now
			return old, nil
		}
	}
	return "", auth.ErrSettingNotFound
}

// ── credentialStore (handlers.CredentialsHandler) ──────────────────────────
// Dev mode has no WebAuthn ceremony, but it seeds two passkeys for the admin so
// the Account page has something to show: a device-bound one, which every dev
// session counts as having signed in with (see ResolveSession), and a synced one.

// seedCurrentCredentialID is the seeded passkey every dev session "used".
const seedCurrentCredentialID int64 = 1

func seedCredentials(now time.Time) []devCredential {
	used1 := now.Add(-2 * time.Hour)
	used2 := now.Add(-72 * time.Hour)
	return []devCredential{
		{userID: seedAdminID, ManagedCredential: auth.ManagedCredential{
			ID: seedCurrentCredentialID, DeviceName: "MacBook Touch ID",
			AAGUID: "00000000-0000-0000-0000-000000000000", SignCount: 14,
			BackupState: false, CreatedAt: now.AddDate(0, -8, 0), LastUsedAt: &used1,
		}},
		{userID: seedAdminID, ManagedCredential: auth.ManagedCredential{
			ID: 2, DeviceName: "iPhone",
			AAGUID: "fbfc3007-154e-4ecc-8c0b-6e020557d7bd", SignCount: 0,
			BackupState: true, CreatedAt: now.AddDate(0, -8, 0), LastUsedAt: &used2,
		}},
	}
}

type devCredential struct {
	userID int64
	auth.ManagedCredential
}

// ListCredentialsForUser returns the caller's seeded dev passkeys, newest first.
func (s *Store) ListCredentialsForUser(_ context.Context, userID int64) ([]auth.ManagedCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []auth.ManagedCredential{}
	for _, c := range s.credentials {
		if c.userID == userID {
			out = append(out, c.ManagedCredential)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// RenameCredential renames one of the caller's dev passkeys.
func (s *Store) RenameCredential(_ context.Context, userID, id int64, deviceName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.credentials {
		if s.credentials[i].ID == id && s.credentials[i].userID == userID {
			s.credentials[i].DeviceName = deviceName
			return nil
		}
	}
	return auth.ErrCredentialNotFound
}

// RevokeCredential removes one of the caller's dev passkeys, refusing the last.
func (s *Store) RevokeCredential(_ context.Context, userID, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, owned := -1, 0
	for i, c := range s.credentials {
		if c.userID != userID {
			continue
		}
		owned++
		if c.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return auth.ErrCredentialNotFound
	}
	if owned <= 1 {
		return auth.ErrLastCredential
	}
	s.credentials = append(s.credentials[:idx], s.credentials[idx+1:]...)
	return nil
}

// ── userStore (handlers.AdminUsersHandler) ──────────────────────────────────

// ListUsers returns all dev users.
func (s *Store) ListUsers(_ context.Context) ([]auth.ManagedUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.ManagedUser, len(s.users))
	copy(out, s.users)
	return out, nil
}

// GetUser returns the user detail for id.
func (s *Store) GetUser(_ context.Context, id int64) (auth.UserDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ID == id {
			var linkCount int64
			for _, l := range s.links {
				if l.UserID == id {
					linkCount++
				}
			}
			return auth.UserDetail{ManagedUser: u, LinkCount: linkCount, PasskeyCount: 0}, nil
		}
	}
	return auth.UserDetail{}, auth.ErrUserNotFound
}

// DeactivateUser sets active=false for the target user.
func (s *Store) DeactivateUser(_ context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.users {
		if u.ID == id {
			if u.IsAdmin {
				return auth.ManagedUser{}, auth.ErrUserIsAdmin
			}
			if !u.Active {
				return auth.ManagedUser{}, auth.ErrUserAlreadyInactive
			}
			s.users[i].Active = false
			return s.users[i], nil
		}
	}
	return auth.ManagedUser{}, auth.ErrUserNotFound
}

// ReactivateUser sets active=true for the target user.
func (s *Store) ReactivateUser(_ context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, u := range s.users {
		if u.ID == id {
			if u.Active {
				return auth.ManagedUser{}, auth.ErrUserAlreadyActive
			}
			s.users[i].Active = true
			return s.users[i], nil
		}
	}
	return auth.ManagedUser{}, auth.ErrUserNotFound
}

// ── auditReader (handlers.AdminAuditHandler) ────────────────────────────────

// ListAuditLog returns audit records newest-first, applying the same filter
// semantics as the Postgres reader (audit.Filter) and resolving emails from the
// in-memory users.
func (s *Store) ListAuditLog(_ context.Context, f audit.Filter, limit, offset int) ([]audit.Record, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	emailOf := func(id *int64) *string {
		if id == nil {
			return nil
		}
		for _, u := range s.users {
			if u.ID == *id {
				e := u.Email
				return &e
			}
		}
		return nil
	}
	same := func(p *string) bool { return p != nil && strings.EqualFold(*p, f.Email) }

	var filtered []audit.Record
	for i := len(s.audit) - 1; i >= 0; i-- {
		rec := s.audit[i]
		rec.ActorEmail, rec.UserEmail = emailOf(rec.ActorID), emailOf(rec.UserID)
		if f.UserID != nil && (rec.UserID == nil || *rec.UserID != *f.UserID) {
			continue
		}
		if f.Email != "" && !same(rec.ActorEmail) && !same(rec.UserEmail) {
			continue
		}
		if len(f.Actions) > 0 && !slices.Contains(f.Actions, rec.Action) {
			continue
		}
		if f.From != nil && rec.CreatedAt.Before(*f.From) {
			continue
		}
		if f.To != nil && !rec.CreatedAt.Before(*f.To) {
			continue
		}
		filtered = append(filtered, rec)
	}
	total := int64(len(filtered))
	if offset >= len(filtered) {
		return []audit.Record{}, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

// recordAudit appends an audit record (called by the audit.Logger shim below).
// The caller must NOT hold mu.
func (s *Store) recordAudit(e audit.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	tt := e.TargetType
	var ttPtr *string
	if tt != "" {
		ttPtr = &tt
	}
	var metaRaw json.RawMessage
	if e.Metadata != nil {
		b, err := json.Marshal(e.Metadata)
		if err == nil {
			metaRaw = b
		}
	}
	var ipPtr *string
	if e.IP != "" {
		ipPtr = &e.IP
	}
	s.audit = append(s.audit, audit.Record{
		ID:         s.nextAuditID,
		ActorID:    e.ActorID,
		UserID:     e.UserID,
		Action:     e.Action,
		TargetType: ttPtr,
		TargetID:   e.TargetID,
		Metadata:   metaRaw,
		IP:         ipPtr,
		CreatedAt:  now,
	})
	s.nextAuditID++
}

// ── filterRuleStore (handlers.URLFiltersHandler) ────────────────────────────

// List returns all filter rules.
func (s *Store) List(_ context.Context) ([]filters.FilterRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]filters.FilterRule, len(s.rules))
	copy(out, s.rules)
	return out, nil
}

// Create adds a filter rule.
func (s *Store) Create(_ context.Context, in filters.NewRule) (filters.FilterRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := filters.FilterRule{
		ID:          s.nextRuleID,
		Pattern:     in.Pattern,
		ReasonCode:  in.ReasonCode,
		Description: in.Description,
		Active:      true,
		CreatedBy:   &in.CreatedBy,
		CreatedAt:   time.Now(),
	}
	s.nextRuleID++
	s.rules = append(s.rules, r)
	return r, nil
}

// Get returns a filter rule by id.
func (s *Store) Get(_ context.Context, id int64) (filters.FilterRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.ID == id {
			return r, nil
		}
	}
	return filters.FilterRule{}, filters.ErrRuleNotFound
}

// Update applies a partial update to a filter rule.
func (s *Store) Update(_ context.Context, id int64, upd filters.RuleUpdate) (filters.FilterRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rules {
		if r.ID != id {
			continue
		}
		if upd.Pattern != nil {
			s.rules[i].Pattern = *upd.Pattern
		}
		if upd.ReasonCode != nil {
			s.rules[i].ReasonCode = *upd.ReasonCode
		}
		if upd.Description != nil {
			s.rules[i].Description = *upd.Description
		}
		if upd.Active != nil {
			s.rules[i].Active = *upd.Active
		}
		return s.rules[i], nil
	}
	return filters.FilterRule{}, filters.ErrRuleNotFound
}

// Delete removes a filter rule.
func (s *Store) Delete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rules {
		if r.ID == id {
			s.rules = append(s.rules[:i], s.rules[i+1:]...)
			return nil
		}
	}
	return filters.ErrRuleNotFound
}

// LoadActive returns active filter rules (as filters.Rule for the engine).
func (s *Store) LoadActive(_ context.Context) ([]filters.Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []filters.Rule
	for _, r := range s.rules {
		if r.Active {
			out = append(out, filters.Rule{
				ID:         r.ID,
				Pattern:    r.Pattern,
				ReasonCode: int(r.ReasonCode),
			})
		}
	}
	return out, nil
}

// ── campaignStore (handlers.CampaignsHandler) ───────────────────────────────
//
// Slug generation reuses the real campaigns.Slugify/GenerateUniqueSlug pure
// functions (no DB round trip needed for either in-memory or Postgres), so
// dev-mode slugs behave identically to production. auditor/entry are accepted
// to satisfy the interface but ignored, matching DeactivateUser/
// ReactivateUser above and the package doc's "pass nil auditor to all
// handlers in dev mode" note — main.go wires NewCampaignsHandler(ds, nil).

// copyTimePtr returns a fresh copy of t (nil in, nil out). campaigns.Campaign
// stores StartsAt/EndsAt as *time.Time; without this, storing a caller's
// pointer directly — or returning the store's own pointer directly — would
// alias mutable state across the store boundary. A caller mutating
// *returned.StartsAt would then silently corrupt what the store holds (or
// vice versa): confirmed by a probe that wrote 1999 through a returned
// pointer and observed a subsequent GetCampaignBySlug echo it back. #0102's
// window-clamping arithmetic is exactly the kind of code that could trigger
// this if it ever mutates through the pointer instead of rebinding it.
func copyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// cloneCampaign returns a copy of c whose StartsAt/EndsAt point at freshly
// allocated time.Time values, sharing no pointer with c. Called on every
// value that crosses the store boundary in either direction (an incoming
// NewCampaign/CampaignUpdate's pointers before storing them, and every
// campaigns.Campaign returned to a caller) so mutating a pointer on one side
// can never reach the other.
func cloneCampaign(c campaigns.Campaign) campaigns.Campaign {
	c.StartsAt = copyTimePtr(c.StartsAt)
	c.EndsAt = copyTimePtr(c.EndsAt)
	return c
}

// CreateCampaign inserts a new campaign owned by in.UserID.
func (s *Store) CreateCampaign(_ context.Context, in campaigns.NewCampaign, _ *audit.Logger, _ audit.Entry) (campaigns.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	base := campaigns.Slugify(in.Name)
	slug, err := campaigns.GenerateUniqueSlug(base, func(candidate string) (bool, error) {
		for _, c := range s.campaigns {
			if c.UserID == in.UserID && c.Slug == candidate {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return campaigns.Campaign{}, err
	}

	defaultUTMCampaign := in.DefaultUTMCampaign
	if defaultUTMCampaign == "" {
		defaultUTMCampaign = slug
	}

	now := time.Now()
	stored := campaigns.Campaign{
		ID:                 s.nextCampaignID,
		UserID:             in.UserID,
		Name:               in.Name,
		Slug:               slug,
		Description:        in.Description,
		StartsAt:           copyTimePtr(in.StartsAt), // never alias the caller's pointer
		EndsAt:             copyTimePtr(in.EndsAt),
		Archived:           false,
		DefaultUTMSource:   in.DefaultUTMSource,
		DefaultUTMMedium:   in.DefaultUTMMedium,
		DefaultUTMCampaign: defaultUTMCampaign,
		DefaultUTMTerm:     in.DefaultUTMTerm,
		DefaultUTMContent:  in.DefaultUTMContent,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	s.nextCampaignID++
	s.campaigns = append(s.campaigns, stored)
	return cloneCampaign(stored), nil // a second, independent copy for the caller
}

// ListCampaignsForUser returns userID's campaigns, most recently created
// first, matching campaigns.Store.ListCampaignsForUser's ordering, each
// paired with its link_count/total_clicks (#0102).
func (s *Store) ListCampaignsForUser(_ context.Context, userID int64) ([]campaigns.CampaignWithCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]campaigns.CampaignWithCounts, 0)
	for i := len(s.campaigns) - 1; i >= 0; i-- {
		if c := s.campaigns[i]; c.UserID == userID {
			out = append(out, s.campaignWithCountsLocked(c))
		}
	}
	return out, nil
}

// CampaignDailyClicks is the dev-mode twin of campaigns.Store.CampaignDailyClicks
// (#0146). Dev mode never persists a click, so there is nothing to report.
func (s *Store) CampaignDailyClicks(_ context.Context, _ int64, _, _ time.Time, _ string) (map[int64]map[string]int64, error) {
	return map[int64]map[string]int64{}, nil
}

// campaignWithCountsLocked pairs a cloned copy of c with its link_count/
// total_clicks (#0102). Callers must already hold s.mu. LinkCount is real
// (counted from the in-memory links this campaign's own AssignLinkToCampaign/
// UnassignLinkFromCampaign track); TotalClicks is always 0, matching
// UTMStatsForLink/ClicksOverTime/RecordClick's existing dev-mode
// convention — dev mode never persists a click, so there is no click data to
// aggregate (see RecordClick's doc comment).
func (s *Store) campaignWithCountsLocked(c campaigns.Campaign) campaigns.CampaignWithCounts {
	var linkCount int64
	for _, l := range s.links {
		if l.CampaignID != nil && *l.CampaignID == c.ID {
			linkCount++
		}
	}
	return campaigns.CampaignWithCounts{
		Campaign:    cloneCampaign(c),
		LinkCount:   linkCount,
		TotalClicks: 0,
	}
}

// GetCampaignBySlug returns a campaign by slug, scoped to userID.
func (s *Store) GetCampaignBySlug(_ context.Context, userID int64, slug string) (campaigns.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.campaigns {
		if c.UserID == userID && c.Slug == slug {
			return cloneCampaign(c), nil
		}
	}
	return campaigns.Campaign{}, campaigns.ErrCampaignNotFound
}

// GetCampaignBySlugWithCounts returns a campaign by slug, scoped to userID,
// paired with its link_count/total_clicks (#0102) — the dev-mode twin of
// campaigns.Store.GetCampaignBySlugWithCounts, required so GET
// /api/campaigns/{slug} does not regress to the SPA-catch-all failure in dev
// mode (#0098's downstream constraint 2).
func (s *Store) GetCampaignBySlugWithCounts(_ context.Context, userID int64, slug string) (campaigns.CampaignWithCounts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.campaigns {
		if c.UserID == userID && c.Slug == slug {
			return s.campaignWithCountsLocked(c), nil
		}
	}
	return campaigns.CampaignWithCounts{}, campaigns.ErrCampaignNotFound
}

// GetCampaignByID returns a campaign by id, scoped to userID — mirrors
// campaigns.Store.GetCampaignByID. Backs POST /api/links' optional
// campaign_id field (#0099) in dev mode the same way GetCampaignBySlug backs
// campaign_slug.
func (s *Store) GetCampaignByID(_ context.Context, userID, id int64) (campaigns.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.campaigns {
		if c.UserID == userID && c.ID == id {
			return cloneCampaign(c), nil
		}
	}
	return campaigns.Campaign{}, campaigns.ErrCampaignNotFound
}

// UpdateCampaign applies a partial update to userID's own campaign (by slug).
// Matches campaigns.Store.UpdateCampaign's no-op behavior: an update with no
// fields set does NOT touch UpdatedAt (see the len(setClauses)==0 early
// return in store.go) — a dev-built UI keying off updated_at must see the
// same behavior it would get from Postgres.
func (s *Store) UpdateCampaign(_ context.Context, userID int64, slug string, upd campaigns.CampaignUpdate, _ *audit.Logger, _ audit.Entry) (campaigns.Campaign, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.campaigns {
		if c.UserID != userID || c.Slug != slug {
			continue
		}
		changed := false
		if upd.Name != nil {
			s.campaigns[i].Name = *upd.Name
			changed = true
		}
		if upd.Description != nil {
			s.campaigns[i].Description = *upd.Description
			changed = true
		}
		if upd.StartsAt != nil {
			s.campaigns[i].StartsAt = copyTimePtr(*upd.StartsAt) // never alias the caller's pointer
			changed = true
		}
		if upd.EndsAt != nil {
			s.campaigns[i].EndsAt = copyTimePtr(*upd.EndsAt)
			changed = true
		}
		if upd.Archived != nil {
			s.campaigns[i].Archived = *upd.Archived
			changed = true
		}
		if upd.DefaultUTMSource != nil {
			s.campaigns[i].DefaultUTMSource = *upd.DefaultUTMSource
			changed = true
		}
		if upd.DefaultUTMMedium != nil {
			s.campaigns[i].DefaultUTMMedium = *upd.DefaultUTMMedium
			changed = true
		}
		if upd.DefaultUTMCampaign != nil {
			s.campaigns[i].DefaultUTMCampaign = *upd.DefaultUTMCampaign
			changed = true
		}
		if upd.DefaultUTMTerm != nil {
			s.campaigns[i].DefaultUTMTerm = *upd.DefaultUTMTerm
			changed = true
		}
		if upd.DefaultUTMContent != nil {
			s.campaigns[i].DefaultUTMContent = *upd.DefaultUTMContent
			changed = true
		}
		if changed {
			s.campaigns[i].UpdatedAt = time.Now()
		}
		return cloneCampaign(s.campaigns[i]), nil
	}
	return campaigns.Campaign{}, campaigns.ErrCampaignNotFound
}

// DeleteCampaign permanently removes userID's own campaign (by slug).
func (s *Store) DeleteCampaign(_ context.Context, userID int64, slug string, _ *audit.Logger, _ audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.campaigns {
		if c.UserID == userID && c.Slug == slug {
			// Mirror migration 000011's ON DELETE SET NULL: every link
			// currently assigned to this campaign is unassigned, not left
			// pointing at a now-deleted id. Without this, dev mode drifts
			// from Postgres (a link would keep a dead campaign_id, and
			// GetLink's campaign lookup would silently show no name/slug for
			// an id ListLinksForCampaign still matches on) — exactly the
			// kind of gap #0098's downstream constraint 2 exists to prevent,
			// since #0103 builds against ./scripts/dev.sh.
			for j := range s.links {
				if s.links[j].CampaignID != nil && *s.links[j].CampaignID == c.ID {
					s.links[j].CampaignID = nil
				}
			}
			s.campaigns = append(s.campaigns[:i], s.campaigns[i+1:]...)
			return nil
		}
	}
	return campaigns.ErrCampaignNotFound
}

// ── linkStore (handlers.LinksHandler) ───────────────────────────────────────

// KeyExists reports whether any link already uses the given key.
func (s *Store) KeyExists(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.Key == key {
			return true, nil
		}
	}
	return false, nil
}

// copyInt64Ptr returns a fresh copy of p (nil in, nil out). Mirrors
// copyTimePtr's rationale above (campaigns.Campaign's StartsAt/EndsAt),
// applied here to links.Link.CampaignID (#0099): without this, storing a
// caller's pointer directly — or returning the store's own pointer directly
// — would alias mutable state across the store boundary. Applied on every
// write into s.links AND every read out of it, per the #0098 gotcha ("do not
// reintroduce the pattern for any new pointer fields").
func copyInt64Ptr(p *int64) *int64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneLink returns a copy of l whose CampaignID points at a freshly
// allocated int64, sharing no pointer with l, and with CampaignName/
// CampaignSlug populated from the matching campaign row (mirroring
// links.Store.GetLink's LEFT JOIN) when it has one. Called on every
// links.Link that crosses the store boundary in either direction so mutating
// a pointer on one side can never reach the other. Caller must hold s.mu.
func (s *Store) cloneLink(l links.Link) links.Link {
	l.CampaignID = copyInt64Ptr(l.CampaignID)
	l.CampaignName = ""
	l.CampaignSlug = ""
	if l.CampaignID != nil {
		for _, c := range s.campaigns {
			if c.ID == *l.CampaignID {
				l.CampaignName = c.Name
				l.CampaignSlug = c.Slug
				break
			}
		}
	}
	return l
}

// newLinkFromInput mirrors links.Store's helper of the same name: builds the
// in-memory Link from a NewLink's fields (everything except the
// store-generated id/created_at), including the #0099 campaign/UTM/placement
// columns. CampaignID is deep-copied so the stored link never shares a
// pointer with the caller's NewLink.
func newLinkFromInput(in links.NewLink) links.Link {
	return links.Link{
		UserID:         in.UserID,
		Key:            in.Key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         true,
		DeniedReason:   0,
		ExpiresAt:      in.ExpiresAt,
		CampaignID:     copyInt64Ptr(in.CampaignID),
		UTMSource:      in.UTMSource,
		UTMMedium:      in.UTMMedium,
		UTMCampaign:    in.UTMCampaign,
		UTMTerm:        in.UTMTerm,
		UTMContent:     in.UTMContent,
		Placement:      in.Placement,
	}
}

// applyRequestedMetadata mirrors links.Store's applyRequestedMetadataTx
// (#0099 review item 4): on a CreateOrReactivateLink duplicate/reactivate
// match, forward-merges campaign_id/utm_*/placement from a NewLink onto the
// matched *links.Link in place — a field the request actually supplied
// (non-nil CampaignID, non-blank UTM/placement) is written; a blank field is
// left untouched, never cleared. Caller must hold s.mu.
func applyRequestedMetadata(l *links.Link, in links.NewLink) {
	if in.CampaignID != nil {
		l.CampaignID = copyInt64Ptr(in.CampaignID)
	}
	if in.UTMSource != "" {
		l.UTMSource = in.UTMSource
	}
	if in.UTMMedium != "" {
		l.UTMMedium = in.UTMMedium
	}
	if in.UTMCampaign != "" {
		l.UTMCampaign = in.UTMCampaign
	}
	if in.UTMTerm != "" {
		l.UTMTerm = in.UTMTerm
	}
	if in.UTMContent != "" {
		l.UTMContent = in.UTMContent
	}
	if in.Placement != "" {
		l.Placement = in.Placement
	}
}

// CreateLink inserts a new active link.
func (s *Store) CreateLink(_ context.Context, in links.NewLink) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.Key == in.Key {
			return links.Link{}, links.ErrKeyTaken
		}
	}
	l := newLinkFromInput(in)
	l.ID = s.nextLinkID
	l.CreatedAt = time.Now()
	s.nextLinkID++
	s.links = append(s.links, l)
	return s.cloneLink(l), nil
}

// CreateOrReactivateLink implements per-user URL deduplication.
func (s *Store) CreateOrReactivateLink(_ context.Context, in links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, links.CreateOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup lookup.
	for i, l := range s.links {
		if l.UserID == in.UserID && l.DestinationURL == in.DestinationURL && l.DeniedReason == 0 {
			if l.Active {
				applyRequestedMetadata(&s.links[i], in)
				return s.cloneLink(s.links[i]), links.OutcomeActiveDuplicate, nil
			}
			s.links[i].Active = true
			applyRequestedMetadata(&s.links[i], in)
			return s.cloneLink(s.links[i]), links.OutcomeReactivated, nil
		}
	}

	// No existing link — mint a unique key.
	key, err := genKey(func(candidate string) (bool, error) {
		for _, l := range s.links {
			if l.Key == candidate {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return links.Link{}, 0, err
	}

	l := newLinkFromInput(in)
	l.ID = s.nextLinkID
	l.Key = key
	l.CreatedAt = time.Now()
	s.nextLinkID++
	s.links = append(s.links, l)
	return s.cloneLink(l), links.OutcomeInserted, nil
}

// CreateDeniedLink inserts a denied link row.
func (s *Store) CreateDeniedLink(_ context.Context, in links.NewLink, reasonCode int16, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := genKey(func(candidate string) (bool, error) {
		for _, l := range s.links {
			if l.Key == candidate {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return links.Link{}, err
	}

	l := newLinkFromInput(in)
	l.ID = s.nextLinkID
	l.Key = key
	l.Active = false
	l.DeniedReason = reasonCode
	l.CreatedAt = time.Now()
	s.nextLinkID++
	s.links = append(s.links, l)
	return s.cloneLink(l), nil
}

// CreateLinksBatch inserts N new links under one lock acquisition, mirroring
// links.Store.CreateLinksBatch (#0105): dedup is bypassed ENTIRELY (no
// destination_url lookup at all, unlike CreateOrReactivateLink above), so two
// rows sharing a destination_url — e.g. two batch rows differing only in
// `placement`, which is never baked into the URL — both insert as distinct
// links rather than collapsing onto one. There is no transaction concept in
// this in-memory store; holding s.mu for the whole loop is the dev-mode
// equivalent of the real store's single transaction.
//
// ATOMIC ON A genKey FAILURE, mirroring the real store's transaction
// rollback: startLen/startNextID snapshot s.links' length and s.nextLinkID
// before the loop starts, and a genKey failure on ANY row restores both —
// truncating s.links back to startLen and resetting s.nextLinkID to
// startNextID — before returning the error. An earlier version of this
// method restored NEITHER: it appended each row to s.links (and advanced
// s.nextLinkID) as soon as genKey succeeded FOR THAT ROW, so a failure on
// row 3 left rows 1 and 2 permanently in s.links even though its own doc
// comment claimed "none of the caller-visible state changes" — true only for
// the failing row itself, not the rows before it (review finding 3). This
// store is dev-only and the failure path is near-unreachable in practice
// (genKey only fails on key-space exhaustion), but ./scripts/dev.sh runs
// against this exact implementation — #0106's UI work runs on it directly —
// and #0098's Gotchas record two issues' worth of history where a devstore
// divergence from the real store surfaced only much later. Holding s.mu for
// the whole call (already true before this fix) means no concurrent read can
// observe the rolled-back rows in between; the snapshot/restore is what
// makes the ROLLBACK ITSELF correct, not just invisible to other goroutines.
func (s *Store) CreateLinksBatch(_ context.Context, rows []links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) ([]links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	startLen := len(s.links)
	startNextID := s.nextLinkID

	out := make([]links.Link, 0, len(rows))
	for _, in := range rows {
		key, err := genKey(func(candidate string) (bool, error) {
			for _, l := range s.links {
				if l.Key == candidate {
					return true, nil
				}
			}
			return false, nil
		})
		if err != nil {
			// Roll back every row appended earlier IN THIS CALL — restoring
			// the snapshot taken above is what actually makes this atomic;
			// without it, a failure on row 3 would leave rows 1 and 2
			// permanently in s.links, contradicting both this comment and
			// the real store's (Postgres) transactional guarantee that
			// BatchCreateLinks documents.
			s.links = s.links[:startLen]
			s.nextLinkID = startNextID
			return nil, err
		}

		l := newLinkFromInput(in)
		l.ID = s.nextLinkID
		l.Key = key
		l.CreatedAt = time.Now()
		s.nextLinkID++
		s.links = append(s.links, l)
		out = append(out, s.cloneLink(l))
	}
	return out, nil
}

// ListLinks returns the user's links, most recent first, paginated.
func (s *Store) ListLinks(_ context.Context, userID int64, limit, offset int) ([]links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var owned []links.Link
	for i := len(s.links) - 1; i >= 0; i-- {
		if s.links[i].UserID == userID {
			owned = append(owned, s.cloneLink(s.links[i]))
		}
	}
	if offset >= len(owned) {
		return []links.Link{}, nil
	}
	end := offset + limit
	if end > len(owned) {
		end = len(owned)
	}
	return owned[offset:end], nil
}

// ListLinksFiltered is the dev-mode twin of links.Store.ListLinksFiltered
// (#0144). It applies q, campaign, source, medium, status and content_key and
// the created/-created/title sorts. The dev store records no clicks, so every
// click count is 0: sort=clicks and sort=clicks_window fall back to newest
// first, and clicks_window is always 0.
func (s *Store) ListLinksFiltered(_ context.Context, userID int64, opts links.ListOptions) ([]links.Link, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	q := strings.ToLower(opts.Query)
	var matched []links.Link
	for i := len(s.links) - 1; i >= 0; i-- { // newest first
		l := s.cloneLink(s.links[i])
		if l.UserID != userID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Title), q) && !strings.Contains(strings.ToLower(l.Key), q) && !strings.Contains(strings.ToLower(l.DestinationURL), q) {
			continue
		}
		expired := l.ExpiresAt != nil && !l.ExpiresAt.After(now)
		switch opts.Status {
		case links.StatusActive:
			if !l.Active || l.DeniedReason != 0 || expired {
				continue
			}
		case links.StatusInactive:
			if (l.Active && l.DeniedReason == 0) || expired {
				continue
			}
		case links.StatusExpired:
			if !expired {
				continue
			}
		}
		if c := opts.Filter.Campaign; c != "" {
			if c == reports.CampaignNone && l.CampaignSlug != "" || c != reports.CampaignNone && l.CampaignSlug != c {
				continue
			}
		}
		if !devValueIn(opts.Filter.Filters["source"], l.UTMSource) || !devValueIn(opts.Filter.Filters["medium"], l.UTMMedium) {
			continue
		}
		if opts.ContentKey != "" && reports.ContentKey(l.DestinationURL) != opts.ContentKey {
			continue
		}
		matched = append(matched, l)
	}
	if opts.Sort == links.SortCreatedAsc {
		for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
			matched[i], matched[j] = matched[j], matched[i]
		}
	}
	if opts.Sort == links.SortTitle {
		sort.SliceStable(matched, func(a, b int) bool {
			ta, tb := strings.TrimSpace(matched[a].Title), strings.TrimSpace(matched[b].Title)
			if (ta == "") != (tb == "") {
				return tb == ""
			}
			return strings.ToLower(ta) < strings.ToLower(tb)
		})
	}
	return links.Page(matched, opts.Limit, opts.Offset), int64(len(matched)), nil
}

// devValueIn reports whether v satisfies an OR-list of wanted values (empty
// list = no filter; reports.NoneValue matches an empty v).
func devValueIn(want []string, v string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if w == reports.NoneValue && strings.TrimSpace(v) == "" || w == v {
			return true
		}
	}
	return false
}

// CountLinks returns the total number of links owned by userID.
func (s *Store) CountLinks(_ context.Context, userID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, l := range s.links {
		if l.UserID == userID {
			n++
		}
	}
	return n, nil
}

// GetLink returns a single link by key scoped to userID, with its campaign
// name/slug populated (via cloneLink) when it is assigned to one.
func (s *Store) GetLink(_ context.Context, userID int64, key string) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.UserID == userID && l.Key == key {
			return s.cloneLink(l), nil
		}
	}
	return links.Link{}, links.ErrLinkNotFound
}

// ListLinksForCampaign returns every link assigned to campaignID, scoped to
// userID, most-recently-created first — mirrors
// links.Store.ListLinksForCampaign.
func (s *Store) ListLinksForCampaign(_ context.Context, userID, campaignID int64) ([]links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]links.Link, 0)
	for i := len(s.links) - 1; i >= 0; i-- {
		l := s.links[i]
		if l.UserID == userID && l.CampaignID != nil && *l.CampaignID == campaignID {
			out = append(out, s.cloneLink(l))
		}
	}
	return out, nil
}

// UpdateLink applies a partial update to the user's own link.
func (s *Store) UpdateLink(_ context.Context, userID int64, key string, upd links.LinkUpdate) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.UserID != userID || l.Key != key {
			continue
		}
		if upd.Title != nil {
			s.links[i].Title = *upd.Title
		}
		if upd.DestinationURL != nil {
			s.links[i].DestinationURL = *upd.DestinationURL
		}
		if upd.ExpiresAt != nil {
			s.links[i].ExpiresAt = *upd.ExpiresAt
		}
		if upd.UTMSource != nil {
			s.links[i].UTMSource = *upd.UTMSource
		}
		if upd.UTMMedium != nil {
			s.links[i].UTMMedium = *upd.UTMMedium
		}
		if upd.UTMCampaign != nil {
			s.links[i].UTMCampaign = *upd.UTMCampaign
		}
		if upd.UTMTerm != nil {
			s.links[i].UTMTerm = *upd.UTMTerm
		}
		if upd.UTMContent != nil {
			s.links[i].UTMContent = *upd.UTMContent
		}
		if upd.Placement != nil {
			s.links[i].Placement = *upd.Placement
		}
		return s.cloneLink(s.links[i]), nil
	}
	return links.Link{}, links.ErrLinkNotFound
}

// AssignLinkToCampaign moves userID's own link (by id) into userID's own
// campaign (by id), overwriting any prior assignment — mirrors
// campaigns.Store.AssignLinkToCampaign's "moves it" decision. Returns
// campaigns.ErrLinkNotFound when no matching, owned link exists.
func (s *Store) AssignLinkToCampaign(_ context.Context, userID, campaignID, linkID int64, _ *audit.Logger, _ audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.ID == linkID && l.UserID == userID {
			id := campaignID
			s.links[i].CampaignID = &id
			return nil
		}
	}
	return campaigns.ErrLinkNotFound
}

// UnassignLinkFromCampaign clears userID's own link's campaign_id, but only
// if it currently points at campaignID — mirrors
// campaigns.Store.UnassignLinkFromCampaign.
func (s *Store) UnassignLinkFromCampaign(_ context.Context, userID, campaignID, linkID int64, _ *audit.Logger, _ audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.ID == linkID && l.UserID == userID && l.CampaignID != nil && *l.CampaignID == campaignID {
			s.links[i].CampaignID = nil
			return nil
		}
	}
	return campaigns.ErrLinkNotFound
}

// DeactivateLink soft-deletes the user's own link.
func (s *Store) DeactivateLink(_ context.Context, userID int64, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.UserID == userID && l.Key == key {
			s.links[i].Active = false
			return nil
		}
	}
	return links.ErrLinkNotFound
}

// ReactivateLink sets active=true on the user's own link, refusing a
// filter-denied one (links.ErrLinkDenied). expires_at is untouched.
func (s *Store) ReactivateLink(_ context.Context, userID int64, key, expectedDest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.links {
		if l.UserID == userID && l.Key == key {
			if l.DeniedReason != 0 {
				return links.ErrLinkDenied
			}
			if l.DestinationURL != expectedDest {
				return links.ErrLinkChanged
			}
			s.links[i].Active = true
			return nil
		}
	}
	return links.ErrLinkNotFound
}

// ── LinkResolver (handlers.RedirectHandler) ──────────────────────────────────

// ResolveByKey looks up a link by key for the redirect handler. Not user-scoped.
// Returns ErrLinkNotFound when absent.
func (s *Store) ResolveByKey(_ context.Context, key string) (links.Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.Key == key {
			return links.Resolution{
				DestinationURL: l.DestinationURL,
				Active:         l.Active,
				ExpiresAt:      l.ExpiresAt,
				DeniedReason:   l.DeniedReason,
			}, nil
		}
	}
	return links.Resolution{}, links.ErrLinkNotFound
}

// Resolve implements handlers.LinkResolver directly so the redirect handler
// can be wired without going through links.NewResolver (which requires a
// concrete *links.Store). In dev mode there is no Ristretto cache needed for
// correctness, but a simple map lookup suffices.
func (s *Store) Resolve(ctx context.Context, key string) (cache.CachedLink, bool, error) {
	res, err := s.ResolveByKey(ctx, key)
	switch {
	case errors.Is(err, links.ErrLinkNotFound):
		return cache.CachedLink{}, false, nil
	case err != nil:
		return cache.CachedLink{}, false, err
	}
	return cache.CachedLink{
		DestinationURL: res.DestinationURL,
		Active:         res.Active,
		ExpiresAt:      res.ExpiresAt,
		DeniedReason:   res.DeniedReason,
	}, true, nil
}

// ── statsProvider (handlers.LinksHandler) ───────────────────────────────────
// Returns empty/zero stats — click analytics are not available in dev mode.

// UTMStatsForLink returns empty UTM stats.
func (s *Store) UTMStatsForLink(_ context.Context, _ int64) (clicks.UTMStats, error) {
	return clicks.UTMStats{
		ClickCount: 0,
		BySource:   []clicks.Bucket{},
		ByMedium:   []clicks.Bucket{},
		ByCampaign: []clicks.Bucket{},
	}, nil
}

// ClicksOverTime returns an empty timeseries.
func (s *Store) ClicksOverTime(_ context.Context, _ int64, _, _ time.Time) (clicks.TimeseriesResult, error) {
	return clicks.TimeseriesResult{Days: []clicks.DayBucket{}}, nil
}

// ── campaignStatsProvider (handlers.CampaignsHandler) ───────────────────────
// Returns empty/zero campaign stats (#0102) for the same reason
// UTMStatsForLink/ClicksOverTime do above: dev mode never persists a click
// (RecordClick is a no-op), so there is no click data to aggregate. Real
// wiring uses *clicks.StatsStore; these twins exist only so GET
// /api/campaigns/{slug}/stats and the stats/timeseries fields on GET
// /api/campaigns/{slug} return valid (empty) JSON in dev mode rather than
// panicking on a nil stats provider or falling through to the SPA
// catch-all — the same #0098 lesson (#0098's downstream constraint 2) that
// motivated every other devstore twin in this file.

// CampaignStats returns zero totals and empty (non-nil) breakdowns.
func (s *Store) CampaignStats(_ context.Context, _ int64, _, _ time.Time, _ *time.Location) (clicks.CampaignStats, error) {
	return clicks.CampaignStats{
		BySource:  []clicks.Bucket{},
		ByMedium:  []clicks.Bucket{},
		ByContent: []clicks.Bucket{},
		ByReferer: []clicks.Bucket{},
	}, nil
}

// CampaignClicksOverTime returns an empty timeseries.
func (s *Store) CampaignClicksOverTime(_ context.Context, _ int64, _, _ time.Time, _ *time.Location) (clicks.TimeseriesResult, error) {
	return clicks.TimeseriesResult{Days: []clicks.DayBucket{}}, nil
}

// CampaignClicksByLink returns an empty (non-nil) breakdown.
func (s *Store) CampaignClicksByLink(_ context.Context, _ int64, _, _ time.Time, _ *time.Location) ([]clicks.LinkBucket, error) {
	return []clicks.LinkBucket{}, nil
}

// CampaignSeriesByLink returns an empty (non-nil) series list — no "Other"
// row either, since there is nothing to fold.
func (s *Store) CampaignSeriesByLink(_ context.Context, _ int64, _, _ time.Time, _ *time.Location) ([]clicks.LinkSeries, error) {
	return []clicks.LinkSeries{}, nil
}

// CampaignSummary returns zero/empty CampaignStats paired with an empty
// timeseries — the dev-mode twin of clicks.StatsStore.CampaignSummary, the
// method handlers.CampaignsHandler.Get actually calls (#0102 review: the
// handler no longer calls the four individual methods above directly, so
// this and CampaignRollup are the ones that must exist for dev mode to
// implement handlers.campaignStatsProvider). The four individual methods
// above are kept as direct-call twins of clicks.StatsStore's own public
// surface, even though nothing in this package's interface assertions
// requires them anymore.
func (s *Store) CampaignSummary(ctx context.Context, campaignID int64, from, to time.Time, loc *time.Location) (clicks.CampaignSummary, error) {
	stats, err := s.CampaignStats(ctx, campaignID, from, to, loc)
	if err != nil {
		return clicks.CampaignSummary{}, err
	}
	ts, err := s.CampaignClicksOverTime(ctx, campaignID, from, to, loc)
	if err != nil {
		return clicks.CampaignSummary{}, err
	}
	return clicks.CampaignSummary{Stats: stats, Timeseries: ts}, nil
}

// CampaignRollup returns zero/empty values for every quarter of the payload
// — the dev-mode twin of clicks.StatsStore.CampaignRollup, the method
// handlers.CampaignsHandler.Stats calls.
func (s *Store) CampaignRollup(ctx context.Context, campaignID int64, from, to time.Time, loc *time.Location) (clicks.CampaignRollup, error) {
	summary, err := s.CampaignSummary(ctx, campaignID, from, to, loc)
	if err != nil {
		return clicks.CampaignRollup{}, err
	}
	byLink, err := s.CampaignClicksByLink(ctx, campaignID, from, to, loc)
	if err != nil {
		return clicks.CampaignRollup{}, err
	}
	seriesByLink, err := s.CampaignSeriesByLink(ctx, campaignID, from, to, loc)
	if err != nil {
		return clicks.CampaignRollup{}, err
	}
	return clicks.CampaignRollup{
		Stats:        summary.Stats,
		Timeseries:   summary.Timeseries,
		ByLink:       byLink,
		SeriesByLink: seriesByLink,
	}, nil
}

// ── ClickSink (handlers.NewClickRecorder adapter) ───────────────────────────

// RecordClick is a no-op in dev mode (clicks are not persisted).
func (s *Store) RecordClick(_ clicks.Click) {}

// ── cache.Delete shim (cacheEvictor) ────────────────────────────────────────

// CacheDelete satisfies the cacheEvictor interface for the LinksHandler.
// In dev mode there is no Ristretto cache, so this is a no-op.
func (s *Store) CacheDelete(_ string) {}

// ── RuleCache shims ──────────────────────────────────────────────────────────

// Rules returns the currently active compiled filter rules.
// The dev store holds FilterRules; we compile them here on each call (there
// are typically zero rules in dev so this is cheap).
func (s *Store) Rules(ctx context.Context) ([]filters.Rule, error) {
	active, err := s.LoadActive(ctx)
	if err != nil {
		return nil, err
	}
	return filters.CompileRules(active, logger), nil
}

// Invalidate is a no-op — there is no TTL cache in dev.
func (s *Store) Invalidate() {}

// ── audit.Logger shim ───────────────────────────────────────────────────────

// DevAuditLogger wraps *Store and satisfies the *audit.Logger drop-in by
// recording entries in memory rather than Postgres. Handlers receive *audit.Logger
// (a concrete type), so we provide a wrapper that satisfies the calls the
// handlers actually make on it via a thin shim.
//
// Because audit.Logger is a concrete struct (not an interface) we cannot
// implement it directly. Instead, we expose a NewDevAuditLogger constructor
// that returns a *DevAuditLogger that provides Record and Write with compatible
// signatures, and the main.go wiring passes nil for the *audit.Logger while
// using the devstore's own record path for audit.
//
// NOTE: In practice the handlers check `if h.auditor != nil` before calling
// Record/WriteTx. So for #0057 we pass nil auditor to all handlers — audit
// entries are silently skipped in dev mode. The dev store still implements the
// auditReader interface so GET /admin/audit works (returns empty log).

// RegistrationsEnabled returns the value of the registrations_enabled setting.
// Used by the registration service — not available in dev (passkey auth is
// stubbed), but included for completeness.
func (s *Store) RegistrationsEnabled(_ context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.settings {
		if st.Key == "registrations_enabled" {
			return st.Value == "true", nil
		}
	}
	return false, nil
}

// ── Auth service stubs ───────────────────────────────────────────────────────
// The full WebAuthn ceremony is not available in dev mode. The auth handler is
// still mounted (so the routes exist) but its services are backed by stubs that
// return errors that the handler already handles gracefully.

// DevRegistrar is a stub registrar that refuses all registration attempts in dev mode.
// It satisfies the handlers.registrar interface.
type DevRegistrar struct{}

func (DevRegistrar) StartRegistration(_ context.Context, _, _ string) error {
	return errors.New("devstore: registration not available in dev mode")
}
func (DevRegistrar) VerifyRegistration(_ context.Context, _ string) (*protocol.CredentialCreation, error) {
	return nil, auth.ErrTokenInvalid
}
func (DevRegistrar) FinishRegistration(_ context.Context, _, _, _ string, _ *http.Request) (auth.FinishResult, error) {
	return auth.FinishResult{}, auth.ErrTokenInvalid
}

// DevRecoverer is a stub recoverer for dev mode.
// It satisfies the handlers.recoverer interface.
type DevRecoverer struct{}

func (DevRecoverer) StartRecovery(_ context.Context, _, _ string) error { return nil }
func (DevRecoverer) VerifyRecovery(_ context.Context, _ string) (*protocol.CredentialCreation, error) {
	return nil, auth.ErrTokenInvalid
}
func (DevRecoverer) FinishRecovery(_ context.Context, _, _, _ string, _ *http.Request) (auth.RecoveryResult, error) {
	return auth.RecoveryResult{}, auth.ErrTokenInvalid
}

// ── seedAdminEmail helpers ───────────────────────────────────────────────────

// AdminEmail returns the email of the seeded admin user.
func (s *Store) AdminEmail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.ID == seedAdminID {
			return u.Email
		}
	}
	return ""
}

// AdminID returns the id of the seeded admin user.
func (s *Store) AdminID() int64 { return seedAdminID }

// ── noCacheEvictor ───────────────────────────────────────────────────────────

// NoCacheEvictor is a no-op cacheEvictor that satisfies the handler's optional
// dependency so we don't need a real Ristretto cache in dev mode.
type NoCacheEvictor struct{}

func (NoCacheEvictor) Delete(_ string) {}

// ── noRuleCache ──────────────────────────────────────────────────────────────

// noOpRuleCache is an alias so the Store can also satisfy the ruleProvider
// and ruleCacheInvalidator handler interfaces directly.

// ── Logout shim ─────────────────────────────────────────────────────────────

// devLoginService is a thin wrapper that satisfies the authenticator interface
// expected by handlers.NewAuthHandler, delegating Logout to the store's session
// deletion so the logout route still works in dev mode.
type devLoginService struct {
	store *Store
}

func (d *devLoginService) StartLogin(_ context.Context, _ string) (*protocol.CredentialAssertion, error) {
	return nil, fmt.Errorf("devstore: passkey login not available (use dev auth bypass)")
}

func (d *devLoginService) FinishLogin(_ context.Context, _ string, _ *http.Request) (auth.LoginResult, error) {
	return auth.LoginResult{}, fmt.Errorf("devstore: passkey login not available (use dev auth bypass)")
}

func (d *devLoginService) Logout(ctx context.Context, token, _ string) error {
	_, err := d.store.DeleteSession(ctx, token)
	return err
}

// LogoutAll deletes every dev session for userID ("sign out everywhere",
// #0094). Dev mode has no mailer/audit wiring, so this is just the bulk
// session delete.
func (d *devLoginService) LogoutAll(ctx context.Context, userID int64, _, _ string) (int64, error) {
	return d.store.DeleteSessionsForUser(ctx, userID)
}

// NewDevLoginService returns an authenticator-compatible service that handles
// logout (session deletion) but stubs out passkey start/finish.
func NewDevLoginService(s *Store) *devLoginService { return &devLoginService{store: s} }

// ── filterRuleStore.LoadActive for the ruleCache loader ─────────────────────

// compile-time interface checks — the compiler will report any missing methods.
var _ interface {
	KeyExists(ctx context.Context, key string) (bool, error)
	CreateLink(ctx context.Context, in links.NewLink) (links.Link, error)
	CreateOrReactivateLink(ctx context.Context, in links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, links.CreateOutcome, error)
	CreateDeniedLink(ctx context.Context, in links.NewLink, reasonCode int16, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, error)
	CreateLinksBatch(ctx context.Context, rows []links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) ([]links.Link, error)
	ListLinks(ctx context.Context, userID int64, limit, offset int) ([]links.Link, error)
	ListLinksFiltered(ctx context.Context, userID int64, opts links.ListOptions) ([]links.Link, int64, error)
	CountLinks(ctx context.Context, userID int64) (int64, error)
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	UpdateLink(ctx context.Context, userID int64, key string, upd links.LinkUpdate) (links.Link, error)
	DeactivateLink(ctx context.Context, userID int64, key string) error
} = (*Store)(nil)

// linksHandler's campaignLookup interface (#0099): resolving an optional
// POST /api/links campaign_id/campaign_slug in dev mode.
var _ interface {
	GetCampaignByID(ctx context.Context, userID, id int64) (campaigns.Campaign, error)
	GetCampaignBySlug(ctx context.Context, userID int64, slug string) (campaigns.Campaign, error)
} = (*Store)(nil)

// campaignLinksProvider (handlers.CampaignsHandler's link-membership +
// batch-create endpoints, #0099/#0105): GetLink and CreateLinksBatch are
// already pinned above by other assertions; ListLinksForCampaign is unique to
// this interface.
var _ interface {
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	ListLinksForCampaign(ctx context.Context, userID, campaignID int64) ([]links.Link, error)
	CreateLinksBatch(ctx context.Context, rows []links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) ([]links.Link, error)
} = (*Store)(nil)

var _ interface {
	ListSettings(ctx context.Context) ([]auth.Setting, error)
	UpdateSetting(ctx context.Context, key, value string, now time.Time) (string, error)
} = (*Store)(nil)

var _ interface {
	ListCredentialsForUser(ctx context.Context, userID int64) ([]auth.ManagedCredential, error)
	RenameCredential(ctx context.Context, userID, id int64, deviceName string) error
	RevokeCredential(ctx context.Context, userID, id int64) error
} = (*Store)(nil)

var _ interface {
	ListUsers(ctx context.Context) ([]auth.ManagedUser, error)
	GetUser(ctx context.Context, id int64) (auth.UserDetail, error)
	DeactivateUser(ctx context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error)
	ReactivateUser(ctx context.Context, id int64, auditor *audit.Logger, entry audit.Entry) (auth.ManagedUser, error)
} = (*Store)(nil)

var _ interface {
	ListAuditLog(ctx context.Context, f audit.Filter, limit, offset int) ([]audit.Record, int64, error)
} = (*Store)(nil)

var _ interface {
	List(ctx context.Context) ([]filters.FilterRule, error)
	Create(ctx context.Context, in filters.NewRule) (filters.FilterRule, error)
	Get(ctx context.Context, id int64) (filters.FilterRule, error)
	Update(ctx context.Context, id int64, upd filters.RuleUpdate) (filters.FilterRule, error)
	Delete(ctx context.Context, id int64) error
	LoadActive(ctx context.Context) ([]filters.Rule, error)
} = (*Store)(nil)

// campaignStore (handlers.CampaignsHandler). Method names are prefixed with
// "Campaign" specifically so this interface and the filterRuleStore one above
// (bare Create/Update/Delete/Get/List) can both be satisfied by the same
// *Store — see the "Every method name is prefixed" note on
// campaigns.Store for why bare names would make that structurally
// impossible.
var _ interface {
	CreateCampaign(ctx context.Context, in campaigns.NewCampaign, auditor *audit.Logger, entry audit.Entry) (campaigns.Campaign, error)
	UpdateCampaign(ctx context.Context, userID int64, slug string, upd campaigns.CampaignUpdate, auditor *audit.Logger, entry audit.Entry) (campaigns.Campaign, error)
	DeleteCampaign(ctx context.Context, userID int64, slug string, auditor *audit.Logger, entry audit.Entry) error
	ListCampaignsForUser(ctx context.Context, userID int64) ([]campaigns.CampaignWithCounts, error)
	CampaignDailyClicks(ctx context.Context, userID int64, start, end time.Time, tz string) (map[int64]map[string]int64, error)
	GetCampaignBySlug(ctx context.Context, userID int64, slug string) (campaigns.Campaign, error)
	GetCampaignBySlugWithCounts(ctx context.Context, userID int64, slug string) (campaigns.CampaignWithCounts, error)
	AssignLinkToCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
	UnassignLinkFromCampaign(ctx context.Context, userID, campaignID, linkID int64, auditor *audit.Logger, entry audit.Entry) error
} = (*Store)(nil)

// campaignStatsProvider (handlers.CampaignsHandler, #0102): the two
// snapshot-consistent rollup methods the handler actually calls
// (CampaignSummary for Get, CampaignRollup for Stats) — mirroring the
// statsProvider assertion below for links. The four individual
// clicks.StatsStore-mirroring methods (CampaignStats/CampaignClicksOverTime/
// CampaignClicksByLink/CampaignSeriesByLink) still exist on *Store above but
// are no longer part of this interface, since the handler stopped calling
// them directly once combining their results one-at-a-time was found to let
// each quarter of a response read a different snapshot.
var _ interface {
	CampaignSummary(ctx context.Context, campaignID int64, from, to time.Time, loc *time.Location) (clicks.CampaignSummary, error)
	CampaignRollup(ctx context.Context, campaignID int64, from, to time.Time, loc *time.Location) (clicks.CampaignRollup, error)
} = (*Store)(nil)

var _ interface {
	UTMStatsForLink(ctx context.Context, linkID int64) (clicks.UTMStats, error)
	ClicksOverTime(ctx context.Context, linkID int64, from, to time.Time) (clicks.TimeseriesResult, error)
} = (*Store)(nil)

var _ interface {
	ResolveSession(ctx context.Context, token string, now time.Time) (auth.SessionUser, error)
} = (*Store)(nil)

var _ interface {
	Ping(ctx context.Context) error
} = (*Store)(nil)

// ── additional helpers ───────────────────────────────────────────────────────

// linksByKey returns a map of all links by key for the redirect path.
// Used by the Resolver's keyLookup interface.
func (s *Store) linksByKey() map[string]links.Link {
	m := make(map[string]links.Link, len(s.links))
	for _, l := range s.links {
		m[l.Key] = l
	}
	return m
}

// devRuleCache wraps the store to satisfy the handler's ruleProvider and
// ruleCacheInvalidator interfaces (both use the Store directly but need to be
// passed as these distinct interface types).

// Rules and Invalidate are already on *Store, so *Store satisfies those
// interfaces. The compiler checks above confirm it.

// Ensure *Store also satisfies the ruleProvider and ruleCacheInvalidator
// interfaces the handlers depend on.
var _ interface {
	Rules(ctx context.Context) ([]filters.Rule, error)
} = (*Store)(nil)

var _ interface {
	Invalidate()
} = (*Store)(nil)

// ── string helpers ───────────────────────────────────────────────────────────

// lowerEmail lowercases and trims an email string.
func lowerEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ensure lowerEmail is used to avoid the compiler removing it.
var _ = lowerEmail
