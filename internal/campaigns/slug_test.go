package campaigns

import (
	"errors"
	"testing"
)

// TestSlugify_Deterministic asserts the same name always produces the same
// slug (called twice, compared).
func TestSlugify_Deterministic(t *testing.T) {
	for _, name := range []string{"Summer Fair 2026", "  École  d'été  ", "already-a-slug"} {
		a := Slugify(name)
		b := Slugify(name)
		if a != b {
			t.Errorf("Slugify(%q) not deterministic: %q vs %q", name, a, b)
		}
	}
}

// TestSlugify_Cases asserts specific inputs map to the expected lowercase,
// hyphenated slug.
func TestSlugify_Cases(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"Summer Fair", "summer-fair"},
		{"Summer   Fair   2026!", "summer-fair-2026"},
		{"  leading and trailing  ", "leading-and-trailing"},
		{"ALLCAPS", "allcaps"},
		{"already-a-slug", "already-a-slug"},
		{"a_b_c", "a-b-c"},
		{"---", "campaign"},
		{"", "campaign"},
		{"   ", "campaign"},
	}
	for _, c := range cases {
		if got := Slugify(c.name); got != c.want {
			t.Errorf("Slugify(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestGenerateUniqueSlug_BaseFree asserts a free base slug is returned
// unsuffixed.
func TestGenerateUniqueSlug_BaseFree(t *testing.T) {
	got, err := GenerateUniqueSlug("summer-fair", func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("GenerateUniqueSlug: %v", err)
	}
	if got != "summer-fair" {
		t.Errorf("slug = %q, want %q", got, "summer-fair")
	}
}

// TestGenerateUniqueSlug_CollisionGetsSuffix asserts a taken base slug is
// suffixed with -2, not rejected with an error.
func TestGenerateUniqueSlug_CollisionGetsSuffix(t *testing.T) {
	taken := map[string]bool{"summer-fair": true}
	got, err := GenerateUniqueSlug("summer-fair", func(candidate string) (bool, error) {
		return taken[candidate], nil
	})
	if err != nil {
		t.Fatalf("GenerateUniqueSlug: %v", err)
	}
	if got != "summer-fair-2" {
		t.Errorf("slug = %q, want %q", got, "summer-fair-2")
	}
}

// TestGenerateUniqueSlug_MultipleCollisionsIncrementSuffix asserts the
// suffix keeps incrementing past multiple taken candidates.
func TestGenerateUniqueSlug_MultipleCollisionsIncrementSuffix(t *testing.T) {
	taken := map[string]bool{
		"summer-fair":   true,
		"summer-fair-2": true,
		"summer-fair-3": true,
	}
	got, err := GenerateUniqueSlug("summer-fair", func(candidate string) (bool, error) {
		return taken[candidate], nil
	})
	if err != nil {
		t.Fatalf("GenerateUniqueSlug: %v", err)
	}
	if got != "summer-fair-4" {
		t.Errorf("slug = %q, want %q", got, "summer-fair-4")
	}
}

// TestGenerateUniqueSlug_ExistsErrorPropagates asserts an error from exists
// is returned rather than swallowed.
func TestGenerateUniqueSlug_ExistsErrorPropagates(t *testing.T) {
	wantErr := errors.New("db down")
	_, err := GenerateUniqueSlug("summer-fair", func(string) (bool, error) { return false, wantErr })
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapping %v", err, wantErr)
	}
}

// TestGenerateUniqueSlug_ExhaustedAttemptsCollides asserts ErrSlugCollision
// when every candidate up to maxSlugAttempts is taken.
func TestGenerateUniqueSlug_ExhaustedAttemptsCollides(t *testing.T) {
	_, err := GenerateUniqueSlug("summer-fair", func(string) (bool, error) { return true, nil })
	if !errors.Is(err, ErrSlugCollision) {
		t.Errorf("err = %v, want ErrSlugCollision", err)
	}
}
