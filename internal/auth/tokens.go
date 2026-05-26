package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// registrationTokenLen is the number of random bytes behind a pending
// registration / magic-link token before URL-safe base64 encoding.
const registrationTokenLen = 32

// sessionTokenLen is the number of random bytes behind a session token, per the
// PRD's "random 32-byte token" requirement.
const sessionTokenLen = 32

// randomBytes returns n cryptographically random bytes.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return b, nil
}

// randomURLToken returns a URL-safe, unpadded base64 token over n random bytes.
// Used for both the magic-link registration token and the session token, so
// neither needs escaping in a URL or a Set-Cookie header.
func randomURLToken(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
