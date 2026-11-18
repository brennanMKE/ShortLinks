package reports

import (
	"fmt"
	"strings"
)

// Table aliases every report query must use so the predicate below resolves:
//
//	FROM clicks k JOIN links l ON l.id = k.link_id WHERE l.user_id = $1 ...
//
// Parameter $1 is always the caller's user id (UserArg); the predicate's own
// arguments are numbered from the nextArg the caller passes.
const UserArg = 1

// Skip tokens for Predicate, in addition to the Dimensions names.
const (
	SkipCampaign = "campaign"
	SkipBots     = "bots"
)

// dimensionColumn maps a dimension to the SQL expression it filters on and
// whether the comparison is case/space-normalized. placement lives on links
// (clicks do not store it) and is normalized the way the campaign placement
// rollup groups it (lower(btrim(...))).
var dimensionColumn = map[string]struct {
	expr       string
	normalized bool
}{
	"source":       {"k.utm_source", false},
	"medium":       {"k.utm_medium", false},
	"utm_campaign": {"k.utm_campaign", false},
	"content":      {"k.utm_content", false},
	"term":         {"k.utm_term", false},
	"placement":    {"l.placement", true},
}

// DimensionExpr returns the SQL expression that yields dimension d's value for
// grouping, folding NULL, empty and whitespace-only into NULL (so a facet can
// report them as NoneValue). Placement is lower-cased and trimmed.
func DimensionExpr(d string) (string, bool) {
	c, ok := dimensionColumn[d]
	if !ok {
		return "", false
	}
	if c.normalized {
		return "NULLIF(lower(btrim(" + c.expr + ")), '')", true
	}
	return "NULLIF(btrim(" + c.expr + "), '')", true
}

// Predicate builds the boolean SQL condition for p's campaign, dimension and
// bot filters, to be AND-ed after the caller's user scope and time window:
//
//	WHERE l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3 AND (<sql>)
//
// nextArg is the number of the first placeholder the predicate may use.
// skip lists parts to leave out (SkipCampaign, SkipBots, or a dimension name),
// which is how a facet ignores its own dimension's filter and how the campaign
// breakdown ignores the campaign filter. The result is never empty ("TRUE"
// when nothing applies), values are always bound as arguments, and OR applies
// within a dimension while AND applies across them.
func (p Params) Predicate(nextArg int, skip ...string) (string, []any) {
	skipped := map[string]bool{}
	for _, s := range skip {
		skipped[s] = true
	}
	var conds []string
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}

	if !p.IncludeBots && !skipped[SkipBots] {
		conds = append(conds, "k.is_bot = FALSE")
	}

	if p.Campaign != "" && !skipped[SkipCampaign] {
		if p.Campaign == CampaignNone {
			conds = append(conds, "k.campaign_id IS NULL")
		} else {
			add(fmt.Sprintf("k.campaign_id IN (SELECT c.id FROM campaigns c WHERE c.user_id = $%d AND c.slug = $%d)",
				UserArg, nextArg+len(args)), p.Campaign)
		}
	}

	for _, d := range Dimensions {
		vals := p.Filters[d]
		if len(vals) == 0 || skipped[d] {
			continue
		}
		col := dimensionColumn[d]
		expr, _ := DimensionExpr(d)
		var wantNone bool
		var plain []string
		for _, v := range vals {
			if v == NoneValue {
				wantNone = true
				continue
			}
			if col.normalized {
				v = strings.ToLower(strings.TrimSpace(v))
			}
			plain = append(plain, v)
		}
		var parts []string
		if wantNone {
			parts = append(parts, expr+" IS NULL")
		}
		if len(plain) > 0 {
			parts = append(parts, fmt.Sprintf("%s = ANY($%d::text[])", expr, nextArg+len(args)))
			args = append(args, plain)
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}

	if len(conds) == 0 {
		return "TRUE", nil
	}
	return strings.Join(conds, " AND "), args
}

// TZArg is the placeholder number the scoped query reserves for the time zone
// name; use DayExpr to bucket by day.
const TZArg = 4

// DayExpr buckets a click into its civil day in the request's time zone.
const DayExpr = `date_trunc('day', k.clicked_at AT TIME ZONE $4)`

// ScopeOpts tunes ScopedQuery.
type ScopeOpts struct {
	// Joins is extra JOIN text placed before WHERE, e.g.
	// "LEFT JOIN campaigns c ON c.id = k.campaign_id".
	Joins string
	// Skip lists Predicate skip tokens (SkipCampaign, SkipBots, a dimension).
	Skip []string
	// Where is an extra constant condition AND-ed on, e.g. "k.is_bot = TRUE".
	// It must not contain placeholders.
	Where string
}

// Scope is a scoped, filtered FROM/WHERE ready to append to a SELECT list.
type Scope struct {
	// SQL starts with " FROM" and ends with the WHERE condition, so a caller
	// can append " GROUP BY ..." directly.
	SQL string
	// Args are the placeholders $1..$(Next-1): user id, window start, window
	// end, time zone name, then the predicate's arguments.
	Args []any
	// Next is the first free placeholder number for the caller's own args.
	Next int
}

// ScopedQuery builds the query base every report shares:
//
//	FROM clicks k JOIN links l ON l.id = k.link_id [joins]
//	WHERE l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3 AND (<predicate>)
//
// scoped to userID and the half-open window w. The time zone is always bound
// as $4 (TZArg) and referenced, so DayExpr works and Postgres never sees an
// unreferenced parameter. Append caller args after Args and number them from
// Next.
func (p Params) ScopedQuery(userID int64, w Window, opts ScopeOpts) Scope {
	pred, predArgs := p.Predicate(TZArg+1, opts.Skip...)
	sql := " FROM clicks k JOIN links l ON l.id = k.link_id"
	if opts.Joins != "" {
		sql += " " + opts.Joins
	}
	sql += " WHERE l.user_id = $1 AND k.clicked_at >= $2 AND k.clicked_at < $3 AND $4::text IS NOT NULL AND (" + pred + ")"
	if opts.Where != "" {
		sql += " AND (" + opts.Where + ")"
	}
	args := append([]any{userID, w.Start, w.End, p.TZName}, predArgs...)
	return Scope{SQL: sql, Args: args, Next: len(args) + 1}
}
