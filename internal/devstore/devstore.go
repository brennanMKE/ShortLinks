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
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
)

var logger = slog.Default()

// seedAdminID is the fixed user id for the seeded admin account.
const seedAdminID int64 = 1

// Store is the in-memory dev store. It implements all handler-facing interfaces
// in a single struct to keep wiring simple — main.go constructs one *Store and
// passes it to every constructor that needs a store argument.
type Store struct {
	mu      sync.Mutex
	users   []auth.ManagedUser
	links   []links.Link
	rules   []filters.FilterRule
	audit   []audit.Record
	sessions map[string]sessionEntry // token → session
	settings []auth.Setting
	// nextLinkID is the auto-increment counter for link IDs.
	nextLinkID int64
	// nextRuleID is the auto-increment counter for filter rule IDs.
	nextRuleID int64
	// nextAuditID is the auto-increment counter for audit record IDs.
	nextAuditID int64
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
		sessions:    make(map[string]sessionEntry),
		nextLinkID:  3, // IDs 1 and 2 are used by seeded links
		nextRuleID:  1,
		nextAuditID: 1,
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

	// Seed the registrations_enabled setting.
	s.settings = []auth.Setting{
		{Key: "registrations_enabled", Value: "true"},
	}

	logger.Info("devstore: seeded in-memory store", "admin_email", adminEmail, "links", len(s.links))
	return s
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
			return auth.SessionUser{ID: u.ID, Email: u.Email, IsAdmin: u.IsAdmin}, nil
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

// ── settingStore (handlers.SettingsHandler) ─────────────────────────────────

// ListSettings returns all dev settings.
func (s *Store) ListSettings(_ context.Context) ([]auth.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]auth.Setting, len(s.settings))
	copy(out, s.settings)
	return out, nil
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
// Passkey management is not available in dev; stub returns empty/not-found.

// ListCredentialsForUser returns an empty slice — no passkeys in dev mode.
func (s *Store) ListCredentialsForUser(_ context.Context, _ int64) ([]auth.ManagedCredential, error) {
	return []auth.ManagedCredential{}, nil
}

// RenameCredential always returns ErrCredentialNotFound in dev mode.
func (s *Store) RenameCredential(_ context.Context, _, _ int64, _ string) error {
	return auth.ErrCredentialNotFound
}

// RevokeCredential always returns ErrCredentialNotFound in dev mode.
func (s *Store) RevokeCredential(_ context.Context, _, _ int64) error {
	return auth.ErrCredentialNotFound
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

// ListAuditLog returns audit records newest-first with optional user filter.
func (s *Store) ListAuditLog(_ context.Context, userID *int64, limit, offset int) ([]audit.Record, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var filtered []audit.Record
	for i := len(s.audit) - 1; i >= 0; i-- {
		rec := s.audit[i]
		if userID != nil && (rec.UserID == nil || *rec.UserID != *userID) {
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

// CreateLink inserts a new active link.
func (s *Store) CreateLink(_ context.Context, in links.NewLink) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.Key == in.Key {
			return links.Link{}, links.ErrKeyTaken
		}
	}
	now := time.Now()
	l := links.Link{
		ID:             s.nextLinkID,
		UserID:         in.UserID,
		Key:            in.Key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         true,
		DeniedReason:   0,
		CreatedAt:      now,
		ExpiresAt:      in.ExpiresAt,
	}
	s.nextLinkID++
	s.links = append(s.links, l)
	return l, nil
}

// CreateOrReactivateLink implements per-user URL deduplication.
func (s *Store) CreateOrReactivateLink(_ context.Context, in links.NewLink, genKey func(exists func(key string) (bool, error)) (string, error)) (links.Link, links.CreateOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup lookup.
	for i, l := range s.links {
		if l.UserID == in.UserID && l.DestinationURL == in.DestinationURL && l.DeniedReason == 0 {
			if l.Active {
				return l, links.OutcomeActiveDuplicate, nil
			}
			s.links[i].Active = true
			return s.links[i], links.OutcomeReactivated, nil
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

	now := time.Now()
	l := links.Link{
		ID:             s.nextLinkID,
		UserID:         in.UserID,
		Key:            key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         true,
		DeniedReason:   0,
		CreatedAt:      now,
		ExpiresAt:      in.ExpiresAt,
	}
	s.nextLinkID++
	s.links = append(s.links, l)
	return l, links.OutcomeInserted, nil
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

	now := time.Now()
	l := links.Link{
		ID:             s.nextLinkID,
		UserID:         in.UserID,
		Key:            key,
		DestinationURL: in.DestinationURL,
		Title:          in.Title,
		Active:         false,
		DeniedReason:   reasonCode,
		CreatedAt:      now,
		ExpiresAt:      in.ExpiresAt,
	}
	s.nextLinkID++
	s.links = append(s.links, l)
	return l, nil
}

// ListLinks returns the user's links, most recent first, paginated.
func (s *Store) ListLinks(_ context.Context, userID int64, limit, offset int) ([]links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var owned []links.Link
	for i := len(s.links) - 1; i >= 0; i-- {
		if s.links[i].UserID == userID {
			owned = append(owned, s.links[i])
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

// GetLink returns a single link by key scoped to userID.
func (s *Store) GetLink(_ context.Context, userID int64, key string) (links.Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.links {
		if l.UserID == userID && l.Key == key {
			return l, nil
		}
	}
	return links.Link{}, links.ErrLinkNotFound
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
		return s.links[i], nil
	}
	return links.Link{}, links.ErrLinkNotFound
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
	ListLinks(ctx context.Context, userID int64, limit, offset int) ([]links.Link, error)
	CountLinks(ctx context.Context, userID int64) (int64, error)
	GetLink(ctx context.Context, userID int64, key string) (links.Link, error)
	UpdateLink(ctx context.Context, userID int64, key string, upd links.LinkUpdate) (links.Link, error)
	DeactivateLink(ctx context.Context, userID int64, key string) error
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
	ListAuditLog(ctx context.Context, userID *int64, limit, offset int) ([]audit.Record, int64, error)
} = (*Store)(nil)

var _ interface {
	List(ctx context.Context) ([]filters.FilterRule, error)
	Create(ctx context.Context, in filters.NewRule) (filters.FilterRule, error)
	Get(ctx context.Context, id int64) (filters.FilterRule, error)
	Update(ctx context.Context, id int64, upd filters.RuleUpdate) (filters.FilterRule, error)
	Delete(ctx context.Context, id int64) error
	LoadActive(ctx context.Context) ([]filters.Rule, error)
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
