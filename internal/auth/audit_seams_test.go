package auth

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/config"
)

// newAuditedServices builds a registration + login service over the test pool,
// both wired to a real audit.Logger, so the #0025 auth-ceremony seams write rows
// against the live DB.
func newAuditedServices(t *testing.T, pool *pgxpool.Pool) (*RegistrationService, *LoginService) {
	t.Helper()
	cfg := &config.Config{WebAuthnRPID: testRPID, WebAuthnRPOrigin: testRPOrigin}
	wa, err := NewWebAuthn(cfg)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	logger := audit.New(pool)
	store := NewStore(pool)
	reg := NewRegistrationService(store, wa, &recordingMailer{}, logger, cfg)
	login := NewLoginService(store, wa, logger, nil)
	return reg, login
}

// auditCount returns how many audit_log rows exist for an action.
func auditCount(t *testing.T, pool *pgxpool.Pool, action string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatalf("count audit rows for %q: %v", action, err)
	}
	return n
}

// lastAuditMeta returns the most recent audit_log row's metadata (decoded) and
// actor_id for an action, failing if none exists.
func lastAuditMeta(t *testing.T, pool *pgxpool.Pool, action string) (actor *int64, meta map[string]any) {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT actor_id, metadata FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`,
		action).Scan(&actor, &raw); err != nil {
		t.Fatalf("no audit row for %q: %v", action, err)
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &meta); err != nil {
			t.Fatalf("decode metadata for %q: %v", action, err)
		}
	}
	return actor, meta
}

// TestAudit_RegistrationAndLoginSeams drives a full registration ceremony then a
// real login against a virtual authenticator and proves the #0025 auth seams
// fired: account.registered + credential.added (in the registration tx) and
// account.login (in the login tx).
func TestAudit_RegistrationAndLoginSeams(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc, loginSvc := newAuditedServices(t, pool)

	const email = "audited@example.com"
	acct := registerWithAuthenticator(t, regSvc, pool, email)

	// Registration ceremony seams.
	if n := auditCount(t, pool, audit.ActionAccountRegistered); n != 1 {
		t.Errorf("account.registered rows = %d, want 1", n)
	}
	addedActor, addedMeta := lastAuditMeta(t, pool, audit.ActionCredentialAdded)
	if addedActor == nil || *addedActor != acct.user.ID {
		t.Errorf("credential.added actor_id = %v, want %d", addedActor, acct.user.ID)
	}
	if addedMeta["device_name"] != "Synced Passkey" {
		t.Errorf("credential.added metadata.device_name = %v, want %q", addedMeta["device_name"], "Synced Passkey")
	}
	if _, ok := addedMeta["aaguid"]; !ok {
		t.Errorf("credential.added metadata missing aaguid: %v", addedMeta)
	}

	// Login ceremony seam.
	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("driveLogin: %v", err)
	}
	loginActor, _ := lastAuditMeta(t, pool, audit.ActionAccountLogin)
	if loginActor == nil || *loginActor != acct.user.ID {
		t.Errorf("account.login actor_id = %v, want %d", loginActor, acct.user.ID)
	}

	// Logout seam (fire-and-forget through the pool).
	result, err := driveLogin(t, loginSvc, acct, "")
	if err != nil {
		t.Fatalf("driveLogin (for logout): %v", err)
	}
	if err := loginSvc.Logout(context.Background(), result.SessionToken, "203.0.113.5"); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	logoutActor, _ := lastAuditMeta(t, pool, audit.ActionAccountLogout)
	if logoutActor == nil || *logoutActor != acct.user.ID {
		t.Errorf("account.logout actor_id = %v, want %d", logoutActor, acct.user.ID)
	}
}
