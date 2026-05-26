package audit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// auditTestPool connects to TEST_DATABASE_URL or skips. It truncates audit_log
// (and the users it references) before and after the test so each run starts
// clean and leaves the DB clean.
func auditTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncate := func() {
		if _, err := pool.Exec(context.Background(),
			`TRUNCATE audit_log, users RESTART IDENTITY CASCADE`); err != nil {
			t.Fatalf("truncate audit_log: %v", err)
		}
	}
	truncate()
	t.Cleanup(func() {
		truncate()
		pool.Close()
	})
	return pool
}

// seedUser inserts an active account and returns its id, so an entry's
// actor_id/user_id can satisfy the FK constraint.
func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

func ptr(v int64) *int64 { return &v }

// TestWrite_FullEntryPersists writes an entry with every column populated and a
// JSONB metadata object, then reads the row back and asserts each column,
// including the round-tripped metadata JSON.
func TestWrite_FullEntryPersists(t *testing.T) {
	pool := auditTestPool(t)
	actor := seedUser(t, pool, "actor@example.com")
	logger := New(pool)

	target := int64(42)
	meta := map[string]any{
		"key":             "abc123",
		"destination_url": "https://example.com",
		"title":           "Example",
		"duplicate":       false,
	}
	if err := logger.Write(context.Background(), Entry{
		ActorID:    &actor,
		UserID:     &actor,
		Action:     ActionLinkCreated,
		TargetType: TargetLink,
		TargetID:   &target,
		Metadata:   meta,
		IP:         "203.0.113.7",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		gotActor  *int64
		gotUser   *int64
		gotAction string
		gotType   *string
		gotTarget *int64
		gotMeta   []byte
		gotIP     *string
	)
	err := pool.QueryRow(context.Background(),
		`SELECT actor_id, user_id, action, target_type, target_id, metadata, host(ip_address)
		   FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&gotActor, &gotUser, &gotAction, &gotType, &gotTarget, &gotMeta, &gotIP)
	if err != nil {
		t.Fatalf("read back row: %v", err)
	}

	if gotActor == nil || *gotActor != actor {
		t.Errorf("actor_id = %v, want %d", gotActor, actor)
	}
	if gotUser == nil || *gotUser != actor {
		t.Errorf("user_id = %v, want %d", gotUser, actor)
	}
	if gotAction != ActionLinkCreated {
		t.Errorf("action = %q, want %q", gotAction, ActionLinkCreated)
	}
	if gotType == nil || *gotType != TargetLink {
		t.Errorf("target_type = %v, want %q", gotType, TargetLink)
	}
	if gotTarget == nil || *gotTarget != target {
		t.Errorf("target_id = %v, want %d", gotTarget, target)
	}
	if gotIP == nil || *gotIP != "203.0.113.7" {
		t.Errorf("ip_address = %v, want 203.0.113.7", gotIP)
	}

	var roundTripped map[string]any
	if err := json.Unmarshal(gotMeta, &roundTripped); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if roundTripped["key"] != "abc123" || roundTripped["destination_url"] != "https://example.com" {
		t.Errorf("metadata = %v, want key/destination_url preserved", roundTripped)
	}
	if roundTripped["duplicate"] != false {
		t.Errorf("metadata.duplicate = %v, want false", roundTripped["duplicate"])
	}
}

// TestWrite_NilActorAndTargetStoreNull confirms nil pointer fields and nil
// metadata are stored as SQL NULL (e.g. a pre-auth registration_started event).
func TestWrite_NilActorAndTargetStoreNull(t *testing.T) {
	pool := auditTestPool(t)
	logger := New(pool)

	if err := logger.Write(context.Background(), Entry{
		Action: ActionAccountRegistrationStarted,
		// ActorID, UserID, TargetID, Metadata, IP all zero/nil → NULL.
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		actorNull  bool
		userNull   bool
		targetNull bool
		metaNull   bool
		ipNull     bool
		gotAction  string
	)
	err := pool.QueryRow(context.Background(),
		`SELECT actor_id IS NULL, user_id IS NULL, target_id IS NULL,
		        metadata IS NULL, ip_address IS NULL, action
		   FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&actorNull, &userNull, &targetNull, &metaNull, &ipNull, &gotAction)
	if err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if !actorNull || !userNull || !targetNull || !metaNull || !ipNull {
		t.Errorf("nullable columns: actor=%v user=%v target=%v meta=%v ip=%v, want all NULL",
			!actorNull, !userNull, !targetNull, !metaNull, !ipNull)
	}
	if gotAction != ActionAccountRegistrationStarted {
		t.Errorf("action = %q, want %q", gotAction, ActionAccountRegistrationStarted)
	}
}

// TestWrite_PartialActorWithUser confirms the admin-on-other-user shape (actor
// set, user different) the #0028 user-management seams will use: both are
// persisted independently.
func TestWrite_PartialActorWithUser(t *testing.T) {
	pool := auditTestPool(t)
	admin := seedUser(t, pool, "admin@example.com")
	victim := seedUser(t, pool, "victim@example.com")
	logger := New(pool)

	if err := logger.Write(context.Background(), Entry{
		ActorID:    &admin,
		UserID:     &victim,
		Action:     ActionAccountDeactivated,
		TargetType: TargetUser,
		TargetID:   ptr(victim),
		Metadata:   map[string]any{"reason": "spam", "note": ""},
		IP:         "198.51.100.9",
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var gotActor, gotUser int64
	if err := pool.QueryRow(context.Background(),
		`SELECT actor_id, user_id FROM audit_log ORDER BY id DESC LIMIT 1`,
	).Scan(&gotActor, &gotUser); err != nil {
		t.Fatalf("read back row: %v", err)
	}
	if gotActor != admin || gotUser != victim {
		t.Errorf("actor=%d user=%d, want actor=%d user=%d", gotActor, gotUser, admin, victim)
	}
}
