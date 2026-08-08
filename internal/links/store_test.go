package links

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// batchTestPool/seedBatchUser reuse resolver_test.go's DB skeleton
// (resolverTestPool/seedUserL) directly — same package, same TEST_DATABASE_URL
// gate, same truncate. No need for a second copy.

// keyExistsInDB is a small helper that queries the pool directly (NOT the
// store, so it does not depend on the behavior under test) to confirm
// whether a key made it into the table.
func keyExistsInDB(t *testing.T, pool *pgxpool.Pool, key string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM links WHERE key = $1)`, key,
	).Scan(&exists); err != nil {
		t.Fatalf("checking key exists: %v", err)
	}
	return exists
}

// TestCreateLinksBatch_InsertsAllRowsAtomically asserts N rows submitted in
// one call produce N distinct links, all committed, each carrying its own
// campaign_id/UTM/placement.
func TestCreateLinksBatch_InsertsAllRowsAtomically(t *testing.T) {
	pool := resolverTestPool(t)
	userID := seedUserL(t, pool)
	store := NewStore(pool)
	ctx := context.Background()

	rows := []NewLink{
		{UserID: userID, DestinationURL: "https://example.com/promo?utm_source=a", UTMSource: "a", UTMMedium: "email", Placement: "row-1"},
		{UserID: userID, DestinationURL: "https://example.com/promo?utm_source=b", UTMSource: "b", UTMMedium: "social", Placement: "row-2"},
		{UserID: userID, DestinationURL: "https://example.com/promo?utm_source=c", UTMSource: "c", UTMMedium: "print", Placement: "row-3"},
	}

	created, err := store.CreateLinksBatch(ctx, rows, GenerateUniqueKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("created = %d links, want 3", len(created))
	}

	keys := make(map[string]bool, 3)
	for i, l := range created {
		if l.ID == 0 {
			t.Errorf("row %d: ID = 0, want a real id", i)
		}
		if keys[l.Key] {
			t.Errorf("row %d: key %q reused within the batch", i, l.Key)
		}
		keys[l.Key] = true
		if l.Placement != rows[i].Placement {
			t.Errorf("row %d: Placement = %q, want %q", i, l.Placement, rows[i].Placement)
		}
		if !keyExistsInDB(t, pool, l.Key) {
			t.Errorf("row %d: key %q not found in DB after commit", i, l.Key)
		}
	}
}

// TestCreateLinksBatch_TwoRowsDifferingOnlyInPlacementCreateTwoDistinctLinks
// is the central test for #0105: two rows sharing a BYTE-IDENTICAL
// destination_url (placement is never baked into the URL — #0099's data
// model) must still produce two separate links, because CreateLinksBatch
// bypasses CreateOrReactivateLink's dedup entirely rather than extending it.
//
// MUTATION CHECK (performed manually, not committed): swapping the insert
// loop below to call CreateOrReactivateLink per row instead of doing a bare
// INSERT — i.e. reintroducing the dedup lookup this method exists to skip —
// makes this test fail: it returns exactly ONE link (OutcomeInserted then
// OutcomeActiveDuplicate on the second row), so `len(created) != 2` fires
// immediately. Restoring the real bypass-dedup implementation makes it pass
// again. This confirms the test actually exercises the bypass rather than
// merely restating it.
func TestCreateLinksBatch_TwoRowsDifferingOnlyInPlacementCreateTwoDistinctLinks(t *testing.T) {
	pool := resolverTestPool(t)
	userID := seedUserL(t, pool)
	store := NewStore(pool)
	ctx := context.Background()

	const sharedDest = "https://example.com/summer-sale?utm_source=flyer&utm_medium=print"
	rows := []NewLink{
		{UserID: userID, DestinationURL: sharedDest, UTMSource: "flyer", UTMMedium: "print", Placement: "18th & Texas board"},
		{UserID: userID, DestinationURL: sharedDest, UTMSource: "flyer", UTMMedium: "print", Placement: "Congress & 6th board"},
	}
	if rows[0].DestinationURL != rows[1].DestinationURL {
		t.Fatal("test setup bug: the two rows must share a byte-identical destination_url")
	}

	created, err := store.CreateLinksBatch(ctx, rows, GenerateUniqueKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d links, want 2 (dedup must be bypassed for batch rows sharing a destination_url)", len(created))
	}
	if created[0].ID == created[1].ID {
		t.Fatal("both rows resolved to the SAME link id — dedup collapsed them onto one row")
	}
	if created[0].Key == created[1].Key {
		t.Fatal("both rows got the SAME key")
	}
	if created[0].DestinationURL != created[1].DestinationURL {
		t.Errorf("destination_url differs between rows (%q vs %q); the trap requires them to be IDENTICAL",
			created[0].DestinationURL, created[1].DestinationURL)
	}
	if created[0].Placement == created[1].Placement {
		t.Fatal("both rows have the SAME placement — test setup bug")
	}
	if created[0].Placement != "18th & Texas board" || created[1].Placement != "Congress & 6th board" {
		t.Errorf("placements = %q, %q; want the two distinct input placements preserved per row",
			created[0].Placement, created[1].Placement)
	}

	// Confirm directly against the DB (not just the returned in-memory
	// values) that there really are two committed rows.
	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM links WHERE user_id = $1 AND destination_url = $2`, userID, sharedDest,
	).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("DB row count for the shared destination_url = %d, want 2", count)
	}
}

// TestCreateLinksBatch_KeyCollisionRetriesWithinSameTransaction forces two
// rows to collide on the same first-choice key and asserts the second row's
// exists-check sees the FIRST row's already-inserted (uncommitted) key and
// retries — i.e. the collision path holds when N keys are minted inside ONE
// transaction, not just one at a time (the issue's explicit acceptance
// criterion). scripted genKey below deterministically reproduces the
// collision instead of relying on crypto/rand luck.
//
// MUTATION CHECK (performed manually, not committed): changing the exists
// closure inside CreateLinksBatch from `tx.QueryRow` to `s.pool.QueryRow`
// makes this test fail — the pool connection does not see row 1's
// uncommitted insert, so row 2's exists-check for "AAAAAA" reports false,
// row 2 also tries to insert key "AAAAAA", and the second INSERT fails its
// UNIQUE(key) constraint, so CreateLinksBatch returns ErrKeyTaken instead of
// two links. Restoring `tx.QueryRow` makes it pass again.
func TestCreateLinksBatch_KeyCollisionRetriesWithinSameTransaction(t *testing.T) {
	pool := resolverTestPool(t)
	userID := seedUserL(t, pool)
	store := NewStore(pool)
	ctx := context.Background()

	rows := []NewLink{
		{UserID: userID, DestinationURL: "https://example.com/a"},
		{UserID: userID, DestinationURL: "https://example.com/b"},
	}

	// Scripted key sequence: row 1 is offered "AAAAAA" and takes it (not yet
	// taken). Row 2 is ALSO first offered "AAAAAA" — colliding with row 1,
	// which by then has already been inserted (uncommitted) in the same
	// transaction — and must retry to "BBBBBB".
	script := []string{"AAAAAA", "AAAAAA", "BBBBBB"}
	idx := 0
	scriptedGenKey := func(exists func(string) (bool, error)) (string, error) {
		for {
			if idx >= len(script) {
				return "", errors.New("key script exhausted")
			}
			candidate := script[idx]
			idx++
			taken, err := exists(candidate)
			if err != nil {
				return "", err
			}
			if !taken {
				return candidate, nil
			}
		}
	}

	created, err := store.CreateLinksBatch(ctx, rows, scriptedGenKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %d links, want 2", len(created))
	}
	if created[0].Key != "AAAAAA" {
		t.Errorf("row 0 key = %q, want %q", created[0].Key, "AAAAAA")
	}
	if created[1].Key != "BBBBBB" {
		t.Errorf("row 1 key = %q, want %q (must retry past the AAAAAA collision with row 0, seen WITHIN the same transaction)",
			created[1].Key, "BBBBBB")
	}
}

// TestCreateLinksBatch_FailureRollsBackEarlierRows asserts the whole call is
// atomic: when a later row fails (here, a user_id that violates the
// links.user_id foreign key — the row 2 user does not exist), nothing from
// row 1, inserted earlier IN THE SAME CALL, survives.
func TestCreateLinksBatch_FailureRollsBackEarlierRows(t *testing.T) {
	pool := resolverTestPool(t)
	userID := seedUserL(t, pool)
	store := NewStore(pool)
	ctx := context.Background()

	const noSuchUserID = int64(9_999_999)
	rows := []NewLink{
		{UserID: userID, DestinationURL: "https://example.com/first-row"},
		{UserID: noSuchUserID, DestinationURL: "https://example.com/second-row"}, // FK violation
	}

	_, err := store.CreateLinksBatch(ctx, rows, GenerateUniqueKey)
	if err == nil {
		t.Fatal("CreateLinksBatch: expected an error from the invalid second row, got nil")
	}

	var count int64
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM links WHERE destination_url IN ($1, $2)`,
		"https://example.com/first-row", "https://example.com/second-row",
	).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("row count after a failed batch = %d, want 0 (row 1 must roll back with row 2, not survive alone)", count)
	}
}

// TestCreateLinksBatch_EmptyRowsIsANoOp asserts calling with zero rows
// returns an empty, non-nil slice and touches no transaction/DB state.
func TestCreateLinksBatch_EmptyRowsIsANoOp(t *testing.T) {
	pool := resolverTestPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	created, err := store.CreateLinksBatch(ctx, nil, GenerateUniqueKey)
	if err != nil {
		t.Fatalf("CreateLinksBatch: %v", err)
	}
	if created == nil {
		t.Error("created = nil, want a non-nil empty slice")
	}
	if len(created) != 0 {
		t.Errorf("created = %d links, want 0", len(created))
	}
}
