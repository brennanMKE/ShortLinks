package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/qr"
)

const (
	defaultIdleLimit      = 50
	defaultIdleMinAgeDays = 7
)

// IdleLink is one link with no counted clicks in the window.
type IdleLink struct {
	Key         string    `json:"key"`
	ShortURL    string    `json:"short_url"`
	Title       string    `json:"title"`
	Label       string    `json:"label"`
	Destination string    `json:"destination"`
	ContentKey  string    `json:"content_key"`
	Campaign    *string   `json:"campaign_slug"`
	UTMSource   string    `json:"utm_source"`
	UTMMedium   string    `json:"utm_medium"`
	Placement   string    `json:"placement"`
	CreatedAt   time.Time `json:"created_at"`
	AgeDays     int       `json:"age_days"`
	// LastClickedAt is the newest counted click ever, null when there is none.
	LastClickedAt *time.Time `json:"last_clicked_at"`
}

// IdleLinksData is the idle-links payload.
type IdleLinksData struct {
	MinAgeDays int        `json:"min_age_days"`
	TotalIdle  int        `json:"total_idle"`
	Links      []IdleLink `json:"links"`
}

var idleLinksReport = Report{
	Name: "idle-links",
	Description: "Active, unexpired links with no counted clicks in the window, oldest first, with when each was last clicked. " +
		"The campaign and UTM/placement filters narrow which links are considered by the link's own fields.",
	Params: []ParamSpec{
		{Name: "min_age_days", Type: "int", Default: "7", Description: "Only links created at least this many calendar days ago in tz (created on or before today minus this), so brand-new links are not flagged (0-3650)."},
	},
	DefaultLimit: defaultIdleLimit,
	Run:          runIdleLinks,
}

// linkFilterSQL builds a condition on the link's own campaign, UTM and
// placement fields from the shared filters (same OR-within, AND-across
// semantics and ~none handling as Predicate). Bots are not a link property
// and are ignored here. nextArg numbers the first placeholder.
func (p Params) linkFilterSQL(nextArg int) (string, []any) { return p.LinkFilterSQL(nextArg) }

// LinkFilterSQL is the exported form of linkFilterSQL for list queries outside
// this package (#0144). The condition references the links table as l and
// the owning user as $1.
func (p Params) LinkFilterSQL(nextArg int) (string, []any) {
	var conds []string
	var args []any
	if p.Campaign != "" {
		if p.Campaign == CampaignNone {
			conds = append(conds, "l.campaign_id IS NULL")
		} else {
			conds = append(conds, fmt.Sprintf("l.campaign_id IN (SELECT c.id FROM campaigns c WHERE c.user_id = $1 AND c.slug = $%d)", nextArg+len(args)))
			args = append(args, p.Campaign)
		}
	}
	for _, d := range Dimensions {
		vals := p.Filters[d]
		if len(vals) == 0 {
			continue
		}
		col := dimensionColumn[d]
		expr := "NULLIF(btrim(l." + strings.TrimPrefix(col.expr, "k.") + "), '')"
		if col.normalized {
			expr = "NULLIF(lower(btrim(" + col.expr + ")), '')"
		}
		var parts []string
		var plain []string
		for _, v := range vals {
			if v == NoneValue {
				parts = append(parts, expr+" IS NULL")
				continue
			}
			if col.normalized {
				v = strings.ToLower(strings.TrimSpace(v))
			}
			plain = append(plain, v)
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

func runIdleLinks(ctx context.Context, req *Request) (Output, error) {
	minAge, err := req.IntParam("min_age_days", defaultIdleMinAgeDays, 0, 3650)
	if err != nil {
		return Output{}, err
	}
	limit := req.Params.LimitOr(defaultIdleLimit)
	w := req.Windows.Current
	botCond := "AND k.is_bot = FALSE"
	if req.Params.IncludeBots {
		botCond = ""
	}
	filter, fargs := req.Params.linkFilterSQL(6)
	// A link is old enough when its creation date in tz is at least minAge
	// calendar days before today: created before the midnight that starts the
	// day after (today - minAge). Calendar arithmetic, so DST days are fine.
	nowLocal := req.Now.In(req.Params.TZ)
	cutoff := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day()-minAge+1, 0, 0, 0, 0, req.Params.TZ)
	args := append([]any{req.UserID, w.Start, w.End, cutoff, req.Now}, fargs...)
	q := `SELECT l.key, COALESCE(l.title, ''), l.destination_url, c.slug,
	        COALESCE(l.utm_source, ''), COALESCE(l.utm_medium, ''), COALESCE(l.placement, ''), l.created_at,
	        (SELECT max(k.clicked_at) FROM clicks k WHERE k.link_id = l.id ` + botCond + `),
	        count(*) OVER ()
	      FROM links l LEFT JOIN campaigns c ON c.id = l.campaign_id
	      WHERE l.user_id = $1 AND COALESCE(l.active, TRUE) AND l.denied_reason = 0
	        AND (l.expires_at IS NULL OR l.expires_at > $5)
	        AND l.created_at < $4
	        AND NOT EXISTS (SELECT 1 FROM clicks k WHERE k.link_id = l.id AND k.clicked_at >= $2 AND k.clicked_at < $3 ` + botCond + `)
	        AND (` + filter + `)
	      ORDER BY l.created_at, l.id LIMIT ` + fmt.Sprint(limit)
	rows, err := req.Q.Query(ctx, q, args...)
	if err != nil {
		return Output{}, fmt.Errorf("reports: idle links: %w", err)
	}
	defer rows.Close()
	data := IdleLinksData{MinAgeDays: minAge, Links: []IdleLink{}}
	for rows.Next() {
		var l IdleLink
		var total int
		if err := rows.Scan(&l.Key, &l.Title, &l.Destination, &l.Campaign, &l.UTMSource, &l.UTMMedium,
			&l.Placement, &l.CreatedAt, &l.LastClickedAt, &total); err != nil {
			return Output{}, fmt.Errorf("reports: idle links scan: %w", err)
		}
		data.TotalIdle = total
		l.ShortURL = qr.ShortURL(req.BaseURL, l.Key)
		l.ContentKey = ContentKey(l.Destination)
		_, l.Label = LinkLabel(l.Title, l.UTMSource)
		l.AgeDays = int(req.Now.Sub(l.CreatedAt).Hours() / 24)
		data.Links = append(data.Links, l)
	}
	if err := rows.Err(); err != nil {
		return Output{}, fmt.Errorf("reports: idle links: %w", err)
	}
	return Output{Highlights: idleHighlights(data, req), Data: data}, nil
}

func idleHighlights(d IdleLinksData, req *Request) []string {
	w := req.Windows.Current
	days := int64(w.Days())
	if d.TotalIdle == 0 {
		return []string{fmt.Sprintf("Every active link at least %d days old had a click in the last %d days.", d.MinAgeDays, days)}
	}
	n := int64(d.TotalIdle)
	out := []string{fmt.Sprintf("%d %s had no clicks in %d %s (%s to %s).", n, plural(n, "link has", "links have"), days, plural(days, "day", "days"), w.From, w.To)}
	o := d.Links[0]
	name := o.Title
	if name == "" {
		name = o.Key
	}
	s := fmt.Sprintf("Oldest: %q (/u/%s), created %d days ago", name, o.Key, o.AgeDays)
	if o.LastClickedAt == nil {
		s += " and never clicked."
	} else {
		s += ", last clicked " + o.LastClickedAt.In(req.Params.TZ).Format(dayLayout) + "."
	}
	return append(out, s)
}
