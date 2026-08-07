package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/audit"
)

// newLoginServiceAudited is like newLoginServiceWithMailer but also wires a
// real audit.Logger over the same test pool, for the #0094 tests that assert
// the session.revoked_all row.
func newLoginServiceAudited(t *testing.T, pool *pgxpool.Pool, mailer Mailer) *LoginService {
	t.Helper()
	svc := newLoginServiceWithMailer(t, pool, mailer)
	svc.auditor = audit.New(pool)
	return svc
}

// seedExtraSession inserts an additional live sessions row for userID with a
// distinct token, so a test can prove a bulk revoke removes MORE than one
// session ("N sessions across devices").
func seedExtraSession(t *testing.T, pool *pgxpool.Pool, userID int64, token string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`INSERT INTO sessions (user_id, token, created_at, expires_at, last_seen_at)
		 VALUES ($1, $2, now(), now() + interval '30 days', now())`,
		userID, token,
	); err != nil {
		t.Fatalf("seed extra session: %v", err)
	}
}

// userAndCredentialCounts returns the row counts for a single user's users and
// passkey_credentials rows, so a test can assert neither is touched by
// LogoutAll.
func userAndCredentialCounts(t *testing.T, pool *pgxpool.Pool, userID int64) (users, creds int) {
	t.Helper()
	users = scanCount(t, pool, `SELECT COUNT(*) FROM users WHERE id = $1`, userID)
	creds = scanCount(t, pool, `SELECT COUNT(*) FROM passkey_credentials WHERE user_id = $1`, userID)
	return users, creds
}

// TestDeleteSessionsForUser_RemovesAllAndIsIdempotent is the store-level proof
// for #0094: a user with several live sessions ends with zero after one call,
// and a second call is a clean no-op returning 0 (no error).
func TestDeleteSessionsForUser_RemovesAllAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	insertUser(t, pool, "bulk@example.com", false)
	var userID int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, "bulk@example.com").Scan(&userID); err != nil {
		t.Fatalf("read seeded user id: %v", err)
	}
	seedExtraSession(t, pool, userID, "tok-a")
	seedExtraSession(t, pool, userID, "tok-b")
	seedExtraSession(t, pool, userID, "tok-c")
	if got := countSessionsForUser(t, pool, userID); got != 3 {
		t.Fatalf("precondition: sessions = %d, want 3", got)
	}

	store := NewStore(pool)
	n, err := store.DeleteSessionsForUser(context.Background(), pool, userID)
	if err != nil {
		t.Fatalf("DeleteSessionsForUser: %v", err)
	}
	if n != 3 {
		t.Errorf("revoked count = %d, want 3", n)
	}
	if got := countSessionsForUser(t, pool, userID); got != 0 {
		t.Errorf("sessions after delete = %d, want 0", got)
	}

	// Second call: clean no-op, zero rows, no error.
	n2, err := store.DeleteSessionsForUser(context.Background(), pool, userID)
	if err != nil {
		t.Fatalf("DeleteSessionsForUser (second call): %v", err)
	}
	if n2 != 0 {
		t.Errorf("second-call revoked count = %d, want 0", n2)
	}
}

// TestLogoutAll_RevokesAllSessionsUsersAndCredentialsUntouched is the primary
// end-to-end proof for #0094: a registered+logged-in account with multiple
// live sessions ends with zero sessions after LogoutAll, its users and
// passkey_credentials rows are untouched, and a normal passkey login succeeds
// immediately afterward (proving the account is still fully enrolled).
func TestLogoutAll_RevokesAllSessionsUsersAndCredentialsUntouched(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "everywhere@example.com")
	// Registration itself creates one session; log in again for a second, so
	// there are multiple "sessions across devices" to revoke.
	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if got := countSessionsForUser(t, pool, acct.user.ID); got < 2 {
		t.Fatalf("precondition: sessions = %d, want >= 2", got)
	}
	wantUsers, wantCreds := userAndCredentialCounts(t, pool, acct.user.ID)

	revoked, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "everywhere@example.com", "")
	if err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	if revoked < 2 {
		t.Errorf("revoked count = %d, want >= 2", revoked)
	}
	if got := countSessionsForUser(t, pool, acct.user.ID); got != 0 {
		t.Errorf("sessions after LogoutAll = %d, want 0", got)
	}

	// A second call is a clean no-op.
	revoked2, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "everywhere@example.com", "")
	if err != nil {
		t.Errorf("second LogoutAll returned error: %v", err)
	}
	if revoked2 != 0 {
		t.Errorf("second-call revoked count = %d, want 0", revoked2)
	}

	// users and passkey_credentials rows are untouched.
	gotUsers, gotCreds := userAndCredentialCounts(t, pool, acct.user.ID)
	if gotUsers != wantUsers {
		t.Errorf("users rows = %d, want %d (untouched)", gotUsers, wantUsers)
	}
	if gotCreds != wantCreds {
		t.Errorf("passkey_credentials rows = %d, want %d (untouched)", gotCreds, wantCreds)
	}

	// A normal passkey login must still succeed with the SAME (existing)
	// credential, proving the account remains fully enrolled.
	if _, err := driveLogin(t, loginSvc, acct, ""); err != nil {
		t.Fatalf("driveLogin after LogoutAll: %v", err)
	}
}

// TestLogoutAll_OtherUsersSessionsUntouched seeds a second account, logs both
// in, and asserts LogoutAll on the first leaves the second account's sessions
// alone.
func TestLogoutAll_OtherUsersSessionsUntouched(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)

	alice := registerWithAuthenticator(t, regSvc, pool, "alice-everywhere@example.com")
	bob := registerWithAuthenticator(t, regSvc, pool, "bob-everywhere@example.com")
	bobSessionsBefore := countSessionsForUser(t, pool, bob.user.ID)
	if bobSessionsBefore == 0 {
		t.Fatal("precondition: bob has no sessions to protect")
	}

	if _, err := loginSvc.LogoutAll(context.Background(), alice.user.ID, "alice-everywhere@example.com", ""); err != nil {
		t.Fatalf("LogoutAll(alice): %v", err)
	}

	if got := countSessionsForUser(t, pool, alice.user.ID); got != 0 {
		t.Errorf("alice sessions = %d, want 0", got)
	}
	if got := countSessionsForUser(t, pool, bob.user.ID); got != bobSessionsBefore {
		t.Errorf("bob sessions = %d, want %d (untouched)", got, bobSessionsBefore)
	}
}

// TestLogoutAll_OwnSessionRevokedOldTokenRejected asserts the caller's own
// session token is one of the rows deleted, so the very next attempt to
// resolve it (as RequireSession would on the next request) is rejected.
func TestLogoutAll_OwnSessionRevokedOldTokenRejected(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginService(t, pool)
	store := NewStore(pool)

	acct := registerWithAuthenticator(t, regSvc, pool, "own-session@example.com")
	result, err := driveLogin(t, loginSvc, acct, "")
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if _, err := store.ResolveSession(context.Background(), result.SessionToken, time.Now()); err != nil {
		t.Fatalf("precondition: ResolveSession(own token) = %v, want nil", err)
	}

	if _, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "own-session@example.com", ""); err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}

	if _, err := store.ResolveSession(context.Background(), result.SessionToken, time.Now()); !errors.Is(err, ErrSessionInvalid) {
		t.Errorf("ResolveSession(old token) after LogoutAll = %v, want ErrSessionInvalid", err)
	}
}

// TestLogoutAll_WritesAuditRowWithRevokedCount proves the session.revoked_all
// audit entry is written inside the same transaction as the session delete,
// attributed to the account, and carries the revoked count in its metadata.
func TestLogoutAll_WritesAuditRowWithRevokedCount(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	loginSvc := newLoginServiceAudited(t, pool, &recordingMailer{})

	acct := registerWithAuthenticator(t, regSvc, pool, "audited-everywhere@example.com")
	seedExtraSession(t, pool, acct.user.ID, "extra-audit-tok")
	sessionsBefore := countSessionsForUser(t, pool, acct.user.ID)

	revoked, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "audited-everywhere@example.com", "203.0.113.9")
	if err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	if revoked != int64(sessionsBefore) {
		t.Fatalf("revoked = %d, want %d", revoked, sessionsBefore)
	}

	if n := auditCount(t, pool, audit.ActionSessionsRevokedAll); n != 1 {
		t.Fatalf("session.revoked_all rows = %d, want 1", n)
	}
	actor, meta := lastAuditMeta(t, pool, audit.ActionSessionsRevokedAll)
	if actor == nil || *actor != acct.user.ID {
		t.Errorf("session.revoked_all actor_id = %v, want %d", actor, acct.user.ID)
	}
	gotCount, ok := meta["revoked_count"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("metadata missing numeric revoked_count: %v", meta)
	}
	if int64(gotCount) != revoked {
		t.Errorf("metadata revoked_count = %v, want %d", gotCount, revoked)
	}
}

// TestLogoutAll_SendsNotificationEmail asserts a notification is sent to the
// account's address via the injected mailer, carrying the timestamp.
func TestLogoutAll_SendsNotificationEmail(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	mailer := &recordingMailer{}
	loginSvc := newLoginServiceWithMailer(t, pool, mailer)

	acct := registerWithAuthenticator(t, regSvc, pool, "notify-everywhere@example.com")

	before := time.Now()
	if _, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "notify-everywhere@example.com", ""); err != nil {
		t.Fatalf("LogoutAll: %v", err)
	}
	after := time.Now()

	calls, to, at := mailer.sessionsRevokedRecorded()
	if calls != 1 {
		t.Fatalf("SendSessionsRevoked calls = %d, want 1", calls)
	}
	if to != "notify-everywhere@example.com" {
		t.Errorf("notified address = %q, want notify-everywhere@example.com", to)
	}
	if at.Before(before) || at.After(after) {
		t.Errorf("notification timestamp %v not within [%v, %v]", at, before, after)
	}
}

// TestLogoutAll_MailerErrorDoesNotFailOperation asserts a mailer stubbed to
// return an error does not fail LogoutAll: sessions are still revoked, no
// error is returned, and the failure is logged.
func TestLogoutAll_MailerErrorDoesNotFailOperation(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	mailer := &recordingMailer{sessionsRevokedErr: errors.New("smtp down")}
	loginSvc := newLoginServiceWithMailer(t, pool, mailer)
	var logBuf bytes.Buffer
	loginSvc.log = slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	acct := registerWithAuthenticator(t, regSvc, pool, "mailer-fails@example.com")

	revoked, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "mailer-fails@example.com", "")
	if err != nil {
		t.Fatalf("LogoutAll returned error despite mailer failure: %v", err)
	}
	if revoked == 0 {
		t.Error("revoked count = 0, want at least the registration session")
	}
	if got := countSessionsForUser(t, pool, acct.user.ID); got != 0 {
		t.Errorf("sessions after LogoutAll = %d, want 0 (still revoked despite mailer error)", got)
	}
	if calls, _, _ := mailer.sessionsRevokedRecorded(); calls != 1 {
		t.Errorf("SendSessionsRevoked calls = %d, want 1 (attempted)", calls)
	}
	if logged := logBuf.String(); !strings.Contains(logged, "sessions-revoked notification failed") {
		t.Errorf("mailer failure was not logged: %s", logged)
	}
}

// TestLogoutAll_NoMailWhenCommitFails proves the ordering invariant: the
// notification is sent only after the transaction commits. The test injects a
// commit function that always fails (the `commit` seam exists solely for this
// test — see the field's doc comment in login.go) and asserts LogoutAll
// returns an error, no mail was sent, and — because the whole transaction
// rolled back — the session the account had before the call is still there.
func TestLogoutAll_NoMailWhenCommitFails(t *testing.T) {
	pool := testPool(t)
	setRegistrationsEnabled(t, pool, true)
	regSvc := newService(t, pool, &recordingMailer{}, "")
	mailer := &recordingMailer{}
	loginSvc := newLoginServiceWithMailer(t, pool, mailer)
	loginSvc.commit = func(_ context.Context, _ pgx.Tx) error {
		return errors.New("simulated commit failure")
	}

	acct := registerWithAuthenticator(t, regSvc, pool, "commit-fails@example.com")
	sessionsBefore := countSessionsForUser(t, pool, acct.user.ID)
	if sessionsBefore == 0 {
		t.Fatal("precondition: account has no session to protect")
	}

	_, err := loginSvc.LogoutAll(context.Background(), acct.user.ID, "commit-fails@example.com", "")
	if err == nil {
		t.Fatal("LogoutAll returned nil error despite a failed commit")
	}

	if calls, _, _ := mailer.sessionsRevokedRecorded(); calls != 0 {
		t.Errorf("SendSessionsRevoked calls = %d, want 0 (commit never succeeded)", calls)
	}
	// The delete was rolled back along with everything else in the tx.
	if got := countSessionsForUser(t, pool, acct.user.ID); got != sessionsBefore {
		t.Errorf("sessions after failed commit = %d, want %d (rolled back)", got, sessionsBefore)
	}
}
