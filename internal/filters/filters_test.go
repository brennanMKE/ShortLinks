package filters

import (
	"testing"
)

// TestReasonLabel verifies the code→label mapping matches the PRD table and that
// an unknown code falls back to the "Other" label.
func TestReasonLabel(t *testing.T) {
	cases := []struct {
		code int
		want string
	}{
		{ReasonNone, "Not denied"},
		{ReasonMalware, "Malware or ransomware"},
		{ReasonPhishing, "Phishing"},
		{ReasonSpam, "Spam"},
		{ReasonAdultContent, "Adult content"},
		{ReasonPolicyViolation, "Policy violation"},
		{ReasonOther, "Other"},
		{99, "Other"}, // unknown code → Other fallback
	}
	for _, c := range cases {
		if got := ReasonLabel(c.code); got != c.want {
			t.Errorf("ReasonLabel(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}

// TestReasonCodeConstants pins the numeric values so they never drift from the
// PRD's "Denial Reason Codes" table (and from links.denied_reason consumers).
func TestReasonCodeConstants(t *testing.T) {
	pins := map[int]int{
		ReasonNone:            0,
		ReasonMalware:         1,
		ReasonPhishing:        2,
		ReasonSpam:            3,
		ReasonAdultContent:    4,
		ReasonPolicyViolation: 5,
		ReasonOther:           6,
	}
	for got, want := range pins {
		if got != want {
			t.Errorf("reason constant = %d, want %d", got, want)
		}
	}
}

// TestValidReasonCode rejects 0 (a rule always denies) and out-of-range codes.
func TestValidReasonCode(t *testing.T) {
	for code := -1; code <= 8; code++ {
		want := code >= 1 && code <= 6
		if got := ValidReasonCode(code); got != want {
			t.Errorf("ValidReasonCode(%d) = %v, want %v", code, got, want)
		}
	}
}

// TestEvaluate_FirstMatchWins asserts evaluation returns the FIRST matching
// rule's reason code and id, even when a later rule would also match.
func TestEvaluate_FirstMatchWins(t *testing.T) {
	rules := CompileRules([]Rule{
		{ID: 1, Pattern: `evil\.com`, ReasonCode: ReasonMalware},
		{ID: 2, Pattern: `\.com`, ReasonCode: ReasonSpam}, // would also match
	}, nil)

	code, ruleID, matched := Evaluate(rules, "http://evil.com/path")
	if !matched {
		t.Fatal("expected a match")
	}
	if code != ReasonMalware {
		t.Errorf("code = %d, want %d (malware, first rule)", code, ReasonMalware)
	}
	if ruleID != 1 {
		t.Errorf("ruleID = %d, want 1 (first rule)", ruleID)
	}
}

// TestEvaluate_NoMatch asserts a URL matching no rule yields (ReasonNone, 0,
// false).
func TestEvaluate_NoMatch(t *testing.T) {
	rules := CompileRules([]Rule{
		{ID: 1, Pattern: `evil\.com`, ReasonCode: ReasonMalware},
		{ID: 2, Pattern: `phish\.net`, ReasonCode: ReasonPhishing},
	}, nil)

	code, ruleID, matched := Evaluate(rules, "https://www.wikipedia.org")
	if matched {
		t.Errorf("expected no match, got matched=true code=%d rule=%d", code, ruleID)
	}
	if code != ReasonNone {
		t.Errorf("code = %d, want %d (none)", code, ReasonNone)
	}
	if ruleID != 0 {
		t.Errorf("ruleID = %d, want 0", ruleID)
	}
}

// TestCompileRules_SkipsMalformed asserts an uncompilable pattern is dropped
// from the compiled set without breaking evaluation of the remaining rules — a
// single bad rule must never block the engine.
func TestCompileRules_SkipsMalformed(t *testing.T) {
	in := []Rule{
		{ID: 1, Pattern: `(unclosed`, ReasonCode: ReasonMalware}, // malformed
		{ID: 2, Pattern: `phish\.net`, ReasonCode: ReasonPhishing},
	}
	compiled := CompileRules(in, nil)
	if len(compiled) != 1 {
		t.Fatalf("compiled len = %d, want 1 (malformed rule skipped)", len(compiled))
	}
	if compiled[0].ID != 2 {
		t.Errorf("surviving rule id = %d, want 2", compiled[0].ID)
	}

	// The good rule still matches; the bad rule's absence does not break the scan.
	code, ruleID, matched := Evaluate(compiled, "http://phish.net/login")
	if !matched || code != ReasonPhishing || ruleID != 2 {
		t.Errorf("Evaluate = (%d,%d,%v), want (phishing,2,true)", code, ruleID, matched)
	}
}

// TestEvaluate_SkipsMalformedInline asserts that even when Evaluate is handed
// raw (uncompiled) rules including a malformed one, the bad rule is skipped on
// the fly rather than aborting the scan.
func TestEvaluate_SkipsMalformedInline(t *testing.T) {
	raw := []Rule{
		{ID: 1, Pattern: `(unclosed`, ReasonCode: ReasonMalware},
		{ID: 2, Pattern: `spam\.io`, ReasonCode: ReasonSpam},
	}
	code, ruleID, matched := Evaluate(raw, "http://spam.io/x")
	if !matched || code != ReasonSpam || ruleID != 2 {
		t.Errorf("Evaluate = (%d,%d,%v), want (spam,2,true)", code, ruleID, matched)
	}
}
