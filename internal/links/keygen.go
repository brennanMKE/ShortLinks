package links

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// keyAlphabet is the base-62 alphabet used for generated short-link keys.
// The order is irrelevant to uniqueness; all 62 characters are equally likely.
const keyAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// KeyLength is the number of characters in a generated short-link key.
const KeyLength = 6

// MinKeyLength and MaxKeyLength bound the key_length a client may request for a
// generated key (#0179). MaxKeyLength is also the longest key links.key holds
// (VARCHAR(32)) and the longest custom key the API accepts.
const (
	MinKeyLength = KeyLength
	MaxKeyLength = 32
)

// maxKeyAttempts is the number of times GenerateUniqueKey will try to find a
// non-colliding key before giving up.
const maxKeyAttempts = 5

// ErrKeyCollision is returned by GenerateUniqueKey when it fails to produce a
// unique key within maxKeyAttempts tries.
var ErrKeyCollision = errors.New("links: failed to generate a unique key after maximum attempts")

// GenerateKey returns a cryptographically random KeyLength-character base-62
// string drawn from [a-z0-9A-Z]. It uses crypto/rand so keys are unpredictable.
//
// To avoid modulo bias, each character is selected by rejection sampling: random
// bytes are read and any byte that falls outside the largest multiple of 62 that
// fits in a byte (i.e. >= 248) is discarded and re-drawn.
func GenerateKey() (string, error) { return GenerateKeyN(KeyLength) }

// GenerateKeyN is GenerateKey for an explicit length. length must be in
// [1, MaxKeyLength]; callers validate the client-facing 6..32 range with
// ValidKeyLength.
func GenerateKeyN(length int) (string, error) {
	if length < 1 || length > MaxKeyLength {
		return "", fmt.Errorf("links: key length %d out of range", length)
	}
	const n = len(keyAlphabet) // 62
	// Largest multiple of n that fits in a byte; bytes >= limit are rejected to
	// keep the distribution uniform across the alphabet.
	limit := byte(256 / n * n) // 256/62*62 = 248

	out := make([]byte, length)
	// Read in a reasonably sized buffer to amortize syscalls; refill as needed.
	buf := make([]byte, length*2)
	bufPos := len(buf)

	for i := 0; i < length; {
		if bufPos >= len(buf) {
			if _, err := rand.Read(buf); err != nil {
				return "", fmt.Errorf("links: reading random bytes: %w", err)
			}
			bufPos = 0
		}
		b := buf[bufPos]
		bufPos++
		if b >= limit {
			continue // reject to avoid modulo bias
		}
		out[i] = keyAlphabet[b%byte(n)]
		i++
	}

	return string(out), nil
}

// GenerateUniqueKey generates a random key and confirms it is unique via the
// supplied exists callback, retrying up to maxKeyAttempts (5) times on
// collision. The exists callback reports whether a key is already taken (the
// real caller wires it to a database lookup, which keeps this logic testable
// without a database).
//
// It returns ErrKeyCollision if every attempt collides, and propagates any
// error returned by exists or by key generation.
func GenerateUniqueKey(exists func(key string) (bool, error)) (string, error) {
	return GenerateUniqueKeyN(KeyLength, exists)
}

// ValidKeyLength reports whether n is an acceptable client-requested
// key_length (MinKeyLength..MaxKeyLength).
func ValidKeyLength(n int) bool { return n >= MinKeyLength && n <= MaxKeyLength }

// KeyGenerator returns a GenerateUniqueKey-shaped function that mints keys of
// the given length, for the store methods that take a genKey callback.
func KeyGenerator(length int) func(exists func(key string) (bool, error)) (string, error) {
	return func(exists func(key string) (bool, error)) (string, error) {
		return GenerateUniqueKeyN(length, exists)
	}
}

// GenerateUniqueKeyN is GenerateUniqueKey for an explicit key length.
func GenerateUniqueKeyN(length int, exists func(key string) (bool, error)) (string, error) {
	if exists == nil {
		return "", errors.New("links: exists callback must not be nil")
	}

	for attempt := 0; attempt < maxKeyAttempts; attempt++ {
		key, err := GenerateKeyN(length)
		if err != nil {
			return "", err
		}
		taken, err := exists(key)
		if err != nil {
			return "", fmt.Errorf("links: checking key uniqueness: %w", err)
		}
		if !taken {
			return key, nil
		}
	}

	return "", ErrKeyCollision
}
