package links

import (
	"errors"
	"strings"
	"testing"
)

// isBase62 reports whether every rune in s is part of the base-62 alphabet.
func isBase62(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune(keyAlphabet, r) {
			return false
		}
	}
	return true
}

func TestGenerateKey_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 1000; i++ {
		key, err := GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey returned error: %v", err)
		}
		if len(key) != KeyLength {
			t.Fatalf("GenerateKey length = %d, want %d (key=%q)", len(key), KeyLength, key)
		}
		if !isBase62(key) {
			t.Fatalf("GenerateKey produced non-base-62 key: %q", key)
		}
	}
}

func TestGenerateKey_Distinct(t *testing.T) {
	const n = 5000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		key, err := GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey returned error: %v", err)
		}
		seen[key] = struct{}{}
	}
	// With a 62^6 (~56.8 billion) key space, 5000 draws should essentially never
	// collide. Allow a tiny margin to avoid flakiness from astronomically rare luck.
	if len(seen) < n-2 {
		t.Fatalf("expected ~%d distinct keys, got %d", n, len(seen))
	}
}

func TestGenerateUniqueKey_SucceedsWhenNeverTaken(t *testing.T) {
	calls := 0
	exists := func(string) (bool, error) {
		calls++
		return false, nil
	}
	key, err := GenerateUniqueKey(exists)
	if err != nil {
		t.Fatalf("GenerateUniqueKey returned error: %v", err)
	}
	if len(key) != KeyLength || !isBase62(key) {
		t.Fatalf("GenerateUniqueKey returned invalid key: %q", key)
	}
	if calls != 1 {
		t.Fatalf("expected exists to be called once, got %d", calls)
	}
}

func TestGenerateUniqueKey_RetriesThenSucceeds(t *testing.T) {
	// Report a collision for the first 3 attempts, then succeed on the 4th.
	collisions := 3
	calls := 0
	exists := func(string) (bool, error) {
		calls++
		if collisions > 0 {
			collisions--
			return true, nil
		}
		return false, nil
	}
	key, err := GenerateUniqueKey(exists)
	if err != nil {
		t.Fatalf("GenerateUniqueKey returned error: %v", err)
	}
	if len(key) != KeyLength || !isBase62(key) {
		t.Fatalf("GenerateUniqueKey returned invalid key: %q", key)
	}
	if calls != 4 {
		t.Fatalf("expected 4 attempts (3 collisions + 1 success), got %d", calls)
	}
}

func TestGenerateUniqueKey_ErrorAfterMaxCollisions(t *testing.T) {
	calls := 0
	exists := func(string) (bool, error) {
		calls++
		return true, nil // always taken
	}
	key, err := GenerateUniqueKey(exists)
	if !errors.Is(err, ErrKeyCollision) {
		t.Fatalf("expected ErrKeyCollision, got err=%v key=%q", err, key)
	}
	if key != "" {
		t.Fatalf("expected empty key on collision failure, got %q", key)
	}
	if calls != maxKeyAttempts {
		t.Fatalf("expected exactly %d attempts, got %d", maxKeyAttempts, calls)
	}
}

func TestGenerateUniqueKey_PropagatesExistsError(t *testing.T) {
	sentinel := errors.New("database unavailable")
	exists := func(string) (bool, error) {
		return false, sentinel
	}
	key, err := GenerateUniqueKey(exists)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel error, got err=%v key=%q", err, key)
	}
	if key != "" {
		t.Fatalf("expected empty key when exists errors, got %q", key)
	}
}

func TestGenerateUniqueKey_NilCallback(t *testing.T) {
	if _, err := GenerateUniqueKey(nil); err == nil {
		t.Fatal("expected error for nil exists callback, got nil")
	}
}
