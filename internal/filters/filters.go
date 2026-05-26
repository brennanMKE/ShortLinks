package filters

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// Denial reason codes stored in links.denied_reason and referenced by
// url_filter_rules.reason_code. Zero means the link is not denied; any non-zero
// value means it was blocked. These match the PRD's "Denial Reason Codes" table
// exactly.
const (
	// ReasonNone (0) means the URL matched no rule and is permitted.
	ReasonNone int = iota
	// ReasonMalware (1): malware or ransomware.
	ReasonMalware
	// ReasonPhishing (2): phishing.
	ReasonPhishing
	// ReasonSpam (3): spam.
	ReasonSpam
	// ReasonAdultContent (4): adult content.
	ReasonAdultContent
	// ReasonPolicyViolation (5): policy violation.
	ReasonPolicyViolation
	// ReasonOther (6): other.
	ReasonOther
)

// reasonLabels maps each denial reason code to its human-readable label,
// matching the PRD's "Denial Reason Codes" table.
var reasonLabels = map[int]string{
	ReasonNone:            "Not denied",
	ReasonMalware:         "Malware or ransomware",
	ReasonPhishing:        "Phishing",
	ReasonSpam:            "Spam",
	ReasonAdultContent:    "Adult content",
	ReasonPolicyViolation: "Policy violation",
	ReasonOther:           "Other",
}

// ReasonLabel returns the human-readable label for a denial reason code. An
// unknown code falls back to the "Other" label so the API always emits a
// non-empty, meaningful string.
func ReasonLabel(code int) string {
	if label, ok := reasonLabels[code]; ok {
		return label
	}
	return reasonLabels[ReasonOther]
}

// ValidReasonCode reports whether code is one of the defined denial reason
// codes (1..6). Zero ("none") is not a valid code to assign to a rule — a rule
// always denies — so it is rejected here, used when validating a new rule.
func ValidReasonCode(code int) bool {
	return code >= ReasonMalware && code <= ReasonOther
}

// Rule is a single URL filter rule loaded from url_filter_rules. Only the fields
// the engine needs to evaluate a URL are carried here; the admin CRUD layer adds
// the full row shape in its own view type.
type Rule struct {
	// ID is the url_filter_rules.id, surfaced as matched_rule_id on a denial.
	ID int64
	// Pattern is the Go-compatible regular expression tested against the
	// destination URL.
	Pattern string
	// ReasonCode is the denial reason applied when Pattern matches.
	ReasonCode int

	// compiled is the pre-compiled form of Pattern, populated lazily by
	// CompileRules so Evaluate does not recompile on every link creation. A nil
	// compiled means the pattern was not (yet) compiled or failed to compile.
	compiled *regexp.Regexp
}

// LoadActiveRules reads every active rule from url_filter_rules in id order
// (deterministic "first match wins" ordering) and returns them. It is the
// DB-backed source of truth behind the 60-second rule cache; callers normally go
// through the cache rather than hitting this on every link creation. The rules
// are returned uncompiled — CompileRules (or NewEngine) compiles them.
func LoadActiveRules(ctx context.Context, q Querier) ([]Rule, error) {
	rows, err := q.Query(ctx,
		`SELECT id, pattern, reason_code
		   FROM url_filter_rules
		  WHERE active = TRUE
		  ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("filters: loading active rules: %w", err)
	}
	defer rows.Close()

	out := make([]Rule, 0)
	for rows.Next() {
		var r Rule
		var reason int16
		if err := rows.Scan(&r.ID, &r.Pattern, &reason); err != nil {
			return nil, fmt.Errorf("filters: scanning rule: %w", err)
		}
		r.ReasonCode = int(reason)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filters: iterating rules: %w", err)
	}
	return out, nil
}

// Querier is the minimal pgx surface LoadActiveRules needs. *pgxpool.Pool and
// pgx.Tx both satisfy it, mirroring the links/auth stores.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// CompileRules compiles each rule's pattern once and returns a new slice with
// the compiled forms attached. A rule whose pattern fails to compile is SKIPPED
// (excluded from the result) and logged — one bad rule must never break
// evaluation of the rest. The input slice is not mutated.
func CompileRules(rules []Rule, logger *slog.Logger) []Rule {
	if logger == nil {
		logger = slog.Default()
	}
	compiled := make([]Rule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			logger.Warn("filters: skipping uncompilable filter rule",
				"rule_id", r.ID, "pattern", r.Pattern, "error", err)
			continue
		}
		r.compiled = re
		compiled = append(compiled, r)
	}
	return compiled
}

// Evaluate tests url against rules in order and returns the FIRST matching
// rule's reason code, its id, and matched=true. When no rule matches it returns
// (ReasonNone, 0, false).
//
// Rules carrying a pre-compiled regex (from CompileRules) are matched against
// the compiled form; any rule without a compiled form is compiled on the fly,
// and a pattern that fails to compile is skipped rather than aborting the scan,
// so a single bad rule never blocks evaluation. The standard path supplies
// pre-compiled rules from the engine/cache, so the on-the-fly fallback is only
// hit by direct callers that pass raw rules.
func Evaluate(rules []Rule, url string) (reasonCode int, ruleID int64, matched bool) {
	for _, r := range rules {
		re := r.compiled
		if re == nil {
			var err error
			re, err = regexp.Compile(r.Pattern)
			if err != nil {
				// Skip an uncompilable rule rather than abort the scan.
				continue
			}
		}
		if re.MatchString(url) {
			return r.ReasonCode, r.ID, true
		}
	}
	return ReasonNone, 0, false
}
