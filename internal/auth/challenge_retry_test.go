package auth

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestSaveChallenge_RetryKeepsOnlyLatest (#0172): the verify step can run more
// than once for one token (the browser's create step failed and the user
// retried). Each Save must leave exactly one challenge so finish consumes the
// latest, not an arbitrary stale row.
func TestSaveChallenge_RetryKeepsOnlyLatest(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	now := time.Now()

	t.Run("registration", func(t *testing.T) {
		const token = "retry-reg-token"
		if _, err := store.CreatePendingRegistration(ctx, "retry-reg@example.com", token, now); err != nil {
			t.Fatal(err)
		}
		first, second := []byte("challenge-one-aaaaaaaaaaaaaaaaaaaa"), []byte("challenge-two-bbbbbbbbbbbbbbbbbbbb")
		if err := store.SaveRegistrationChallenge(ctx, first, token, []byte("h1"), now); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRegistrationChallenge(ctx, second, token, []byte("h2"), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if n := scanCount(t, pool, `SELECT COUNT(*) FROM webauthn_challenges WHERE pending_registration_token = $1`, token); n != 1 {
			t.Fatalf("challenge rows = %d, want 1", n)
		}
		got, handle, err := store.ConsumeRegistrationChallenge(ctx, pool, token, now.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, second) || !bytes.Equal(handle, []byte("h2")) {
			t.Errorf("consumed %q / %q, want the latest challenge and handle", got, handle)
		}
	})

	t.Run("recovery", func(t *testing.T) {
		const token = "retry-rec-token"
		insertUser(t, pool, "retry-rec@example.com", false)
		var userID int64
		if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, "retry-rec@example.com").Scan(&userID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateRecoveryToken(ctx, "retry-rec@example.com", token, now); err != nil {
			t.Fatal(err)
		}
		first, second := []byte("challenge-one-aaaaaaaaaaaaaaaaaaaa"), []byte("challenge-two-bbbbbbbbbbbbbbbbbbbb")
		if err := store.SaveRecoveryChallenge(ctx, first, userID, token, []byte("h1"), now); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveRecoveryChallenge(ctx, second, userID, token, []byte("h2"), now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if n := scanCount(t, pool, `SELECT COUNT(*) FROM webauthn_challenges WHERE pending_registration_token = $1`, token); n != 1 {
			t.Fatalf("challenge rows = %d, want 1", n)
		}
		got, _, handle, err := store.ConsumeRecoveryChallenge(ctx, pool, token, now.Add(2*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, second) || !bytes.Equal(handle, []byte("h2")) {
			t.Errorf("consumed %q / %q, want the latest challenge and handle", got, handle)
		}
	})
}

// TestSaveChallenge_ConcurrentVerifyLeavesOne: simultaneous verify calls for one
// token must not both insert (the row lock serializes them).
func TestSaveChallenge_ConcurrentVerifyLeavesOne(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()
	const token = "concurrent-reg-token"
	if _, err := store.CreatePendingRegistration(ctx, "concurrent@example.com", token, time.Now()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- store.SaveRegistrationChallenge(ctx, []byte(fmt.Sprintf("concurrent-challenge-%02d-xxxxxxxxxx", i)), token, []byte("h"), time.Now())
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := scanCount(t, pool, `SELECT COUNT(*) FROM webauthn_challenges WHERE pending_registration_token = $1`, token); n != 1 {
		t.Fatalf("challenge rows = %d, want 1", n)
	}
}
