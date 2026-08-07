package campaigns

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// slugNonAlnum matches any run of characters that are not lowercase ASCII
// letters or digits, so Slugify can collapse them to a single hyphen.
var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a deterministic, URL-safe base slug from a campaign name:
// lowercased, runs of non-alphanumeric characters collapsed to a single
// hyphen, and leading/trailing hyphens trimmed. The same name always produces
// the same slug. A name that slugifies to nothing (empty, or entirely
// non-alphanumeric) falls back to "campaign" so a slug is never empty.
func Slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugNonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "campaign"
	}
	return s
}

// maxSlugAttempts bounds how many suffixed candidates GenerateUniqueSlug will
// try (base, base-2, base-3, ...) before giving up. Practically unreachable —
// it exists only to bound the loop.
const maxSlugAttempts = 1000

// ErrSlugCollision is returned by GenerateUniqueSlug when even the widest
// suffix range collides.
var ErrSlugCollision = errors.New("campaigns: failed to generate a unique slug")

// GenerateUniqueSlug returns base if it is free (per exists), or base
// suffixed with "-2", "-3", ... until exists reports false. exists is scoped
// to whatever uniqueness domain the caller cares about — the real caller
// scopes it to (user_id, slug), matching the per-user UNIQUE constraint, so a
// collision within one user's campaigns gets a suffix rather than an error,
// while two different users can hold the same base slug unsuffixed.
func GenerateUniqueSlug(base string, exists func(candidate string) (bool, error)) (string, error) {
	if exists == nil {
		return "", errors.New("campaigns: exists callback must not be nil")
	}

	taken, err := exists(base)
	if err != nil {
		return "", fmt.Errorf("campaigns: checking slug uniqueness: %w", err)
	}
	if !taken {
		return base, nil
	}

	for n := 2; n <= maxSlugAttempts; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		taken, err := exists(candidate)
		if err != nil {
			return "", fmt.Errorf("campaigns: checking slug uniqueness: %w", err)
		}
		if !taken {
			return candidate, nil
		}
	}

	return "", ErrSlugCollision
}
