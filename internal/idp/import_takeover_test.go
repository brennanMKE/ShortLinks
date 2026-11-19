package idp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/db"
)

// importFixture gives a clean database and a config for tests that seed
// ShortLinks rows by hand (no ceremony needed to test account matching).
func importFixture(t *testing.T) (context.Context, *pgxpool.Pool, *config.Config) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	reset := func() {
		_, _ = pool.Exec(ctx, `DROP SCHEMA IF EXISTS portiko CASCADE`)
		_, _ = pool.Exec(ctx, `TRUNCATE webauthn_challenges, pending_registrations, sessions, passkey_credentials, audit_log, clicks, links, users RESTART IDENTITY CASCADE`)
	}
	reset()
	t.Cleanup(func() { reset(); pool.Close() })
	return ctx, pool, &config.Config{
		BaseURL: "http://localhost:8211", DatabaseURL: dsn, WebAuthnRPID: "localhost", WebAuthnRPOrigin: "http://localhost:8211",
		PortikoEnabled: true, PortikoClientSecret: "0123456789abcdef0123", PortikoCryptoKey: [32]byte{1, 2, 3},
	}
}

func seedUserWithPasskey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, email string, credID []byte) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (email) VALUES ($1) RETURNING id`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO passkey_credentials (user_id, credential_id, public_key, user_handle, backup_eligible, backup_state, created_at)
		VALUES ($1, $2, '\x01', $3, TRUE, FALSE, now())`, id, credID, credID); err != nil {
		t.Fatal(err)
	}
	return id
}

func portikoRows(t *testing.T, ctx context.Context, cfg *config.Config, q string) int {
	t.Helper()
	pp, err := pgxpool.New(ctx, mustDerive(t, cfg.DatabaseURL))
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	return count(t, ctx, pp, q)
}

func onlyFailure(t *testing.T, rep *ImportReport, email, want string) {
	t.Helper()
	if len(rep.Failed) != 1 || rep.Failed[0].Email != email || !strings.Contains(rep.Failed[0].Err.Error(), want) {
		t.Fatalf("failed = %+v, want one failure for %s containing %q", rep.Failed, email, want)
	}
}

// Two legacy rows whose emails differ only by case (users.email is
// case-sensitive in ShortLinks) normalize to one Portiko identifier. The second
// must not be attached to the first one's account.
func TestImportRefusesToMergeEmailsDifferingByCase(t *testing.T) {
	ctx, pool, cfg := importFixture(t)
	a := seedUserWithPasskey(t, ctx, pool, "Dup@Example.com", []byte{1, 1})
	b := seedUserWithPasskey(t, ctx, pool, "dup@example.com", []byte{2, 2})

	rep, err := Import(ctx, cfg, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	onlyFailure(t, rep, "dup@example.com", "already belongs to ShortLinks user")
	if rep.CredentialsImported != 1 || rep.UsersCreated != 1 {
		t.Errorf("report = %+v, want only the first user imported", rep)
	}
	var subjA, subjB *string
	_ = pool.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, a).Scan(&subjA)
	_ = pool.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, b).Scan(&subjB)
	if subjA == nil || subjB != nil {
		t.Errorf("subjects A=%v B=%v; want A set, B unset", subjA, subjB)
	}
	if n := portikoRows(t, ctx, cfg, `SELECT count(*) FROM passkey_credentials`); n != 1 {
		t.Errorf("B's passkey reached Portiko: %d credentials, want 1", n)
	}
	// And it keeps failing safely on re-run.
	rep, _ = Import(ctx, cfg, pool, false)
	onlyFailure(t, rep, "dup@example.com", "already belongs to ShortLinks user")
	if n := portikoRows(t, ctx, cfg, `SELECT count(*) FROM passkey_credentials`); n != 1 {
		t.Errorf("re-run merged B: %d credentials", n)
	}
}

// A is imported, A's address then changes in ShortLinks, and B registers with
// A's old address. B must not be merged into A's Portiko account.
func TestImportRefusesToMergeReusedEmail(t *testing.T) {
	ctx, pool, cfg := importFixture(t)
	a := seedUserWithPasskey(t, ctx, pool, "old@example.com", []byte{1, 1})
	if rep, err := Import(ctx, cfg, pool, false); err != nil || len(rep.Failed) != 0 || rep.UsersCreated != 1 {
		t.Fatalf("first import = %+v, %v", rep, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET email = 'new@example.com' WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	b := seedUserWithPasskey(t, ctx, pool, "old@example.com", []byte{2, 2})

	rep, err := Import(ctx, cfg, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	onlyFailure(t, rep, "old@example.com", "already belongs to ShortLinks user")
	var subjB *string
	_ = pool.QueryRow(ctx, `SELECT subject FROM users WHERE id = $1`, b).Scan(&subjB)
	if subjB != nil {
		t.Error("B was given A's subject")
	}
	if n := portikoRows(t, ctx, cfg, `SELECT count(*) FROM passkey_credentials`); n != 1 {
		t.Errorf("B's passkey reached A's account: %d credentials, want 1", n)
	}
	if len(rep.AdoptedByEmail) != 0 {
		t.Errorf("adopted by email = %v, want none", rep.AdoptedByEmail)
	}
}

// An unverified identifier is not a basis for adoption, and an adoption by
// verified email is reported so an operator can review it.
func TestImportAdoptionNeedsVerifiedEmailAndIsReported(t *testing.T) {
	ctx, pool, cfg := importFixture(t)
	c := seedUserWithPasskey(t, ctx, pool, "carol@example.com", []byte{3, 3})
	if rep, err := Import(ctx, cfg, pool, false); err != nil || len(rep.Failed) != 0 {
		t.Fatalf("first import = %+v, %v", rep, err)
	}
	// Lose the recorded subject, as after a crash between the two commits.
	if _, err := pool.Exec(ctx, `UPDATE users SET subject = NULL WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	rep, err := Import(ctx, cfg, pool, false)
	if err != nil || len(rep.Failed) != 0 || len(rep.AdoptedByEmail) != 1 || rep.AdoptedByEmail[0] != "carol@example.com" {
		t.Fatalf("recovery = %+v, %v", rep, err)
	}
	var out strings.Builder
	rep.Print(&out, false)
	if !strings.Contains(out.String(), "carol@example.com: matched an existing Portiko account by verified email") {
		t.Errorf("output does not list the adoption:\n%s", out.String())
	}

	// Same again with the identifier unverified: no adoption, and the import
	// fails (the address is taken) rather than reusing the account.
	pp, err := pgxpool.New(ctx, mustDerive(t, cfg.DatabaseURL))
	if err != nil {
		t.Fatal(err)
	}
	defer pp.Close()
	if _, err := pp.Exec(ctx, `UPDATE identifiers SET verified_at = NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET subject = NULL WHERE id = $1`, c); err != nil {
		t.Fatal(err)
	}
	rep, err = Import(ctx, cfg, pool, false)
	if err != nil || len(rep.Failed) != 1 || len(rep.AdoptedByEmail) != 0 {
		t.Fatalf("unverified = %+v, %v; want a failure and no adoption", rep, err)
	}
}
