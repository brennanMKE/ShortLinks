package links

import "testing"

func TestGenerateKeyN_LengthAndAlphabet(t *testing.T) {
	for _, n := range []int{MinKeyLength, 12, 21, MaxKeyLength} {
		seen := map[string]bool{}
		for i := 0; i < 200; i++ {
			key, err := GenerateKeyN(n)
			if err != nil {
				t.Fatalf("GenerateKeyN(%d): %v", n, err)
			}
			if len(key) != n || !isBase62(key) {
				t.Fatalf("GenerateKeyN(%d) = %q, want %d base-62 chars", n, key, n)
			}
			seen[key] = true
		}
		if len(seen) != 200 {
			t.Errorf("GenerateKeyN(%d) produced duplicates in 200 draws", n)
		}
	}
}

func TestGenerateKeyN_OutOfRange(t *testing.T) {
	for _, n := range []int{-1, 0, MaxKeyLength + 1} {
		if _, err := GenerateKeyN(n); err == nil {
			t.Errorf("GenerateKeyN(%d) succeeded, want error", n)
		}
	}
}

func TestValidKeyLength(t *testing.T) {
	for n, want := range map[int]bool{-1: false, 0: false, 5: false, 6: true, 12: true, 32: true, 33: false} {
		if got := ValidKeyLength(n); got != want {
			t.Errorf("ValidKeyLength(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestKeyGenerator_UsesLength(t *testing.T) {
	key, err := KeyGenerator(16)(func(string) (bool, error) { return false, nil })
	if err != nil || len(key) != 16 || !isBase62(key) {
		t.Fatalf("KeyGenerator(16) = %q, %v", key, err)
	}
}
