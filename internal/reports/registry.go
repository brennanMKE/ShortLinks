package reports

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrReportNotFound is returned by Service.Run for an unregistered report name.
var ErrReportNotFound = errors.New("reports: report not found")

// ParamSpec describes one query parameter, for the catalogue.
type ParamSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Default     string   `json:"default,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Values      []string `json:"values,omitempty"`
}

// CommonParams documents the parameters ParseParams accepts; every report
// takes all of them.
var CommonParams = []ParamSpec{
	{Name: "range", Type: "enum", Description: "Window ending today (today is a partial day).", Default: DefaultRange, Values: []string{"7d", "30d", "90d", "365d", "all"}},
	{Name: "from", Type: "date", Description: "First day of a custom window, YYYY-MM-DD in tz, inclusive. Overrides range."},
	{Name: "to", Type: "date", Description: "Last day of a custom window, YYYY-MM-DD in tz, inclusive. Defaults to today; requires from."},
	{Name: "tz", Type: "string", Description: "IANA time zone used to bucket days, e.g. America/Los_Angeles.", Default: "DEFAULT_TIMEZONE setting, else UTC"},
	{Name: "compare", Type: "enum", Description: "previous adds the same-length window immediately before; ignored for range=all.", Default: ComparePrevious, Values: []string{"previous", "none"}},
	{Name: "campaign", Type: "string", Description: "Campaign slug, or none for clicks with no campaign. Default all."},
	{Name: "source", Type: "list", Description: "utm_source values, comma-separated; ~none matches empty."},
	{Name: "medium", Type: "list", Description: "utm_medium values, comma-separated; ~none matches empty."},
	{Name: "utm_campaign", Type: "list", Description: "utm_campaign values, comma-separated; ~none matches empty."},
	{Name: "content", Type: "list", Description: "utm_content values, comma-separated; ~none matches empty."},
	{Name: "term", Type: "list", Description: "utm_term values, comma-separated; ~none matches empty."},
	{Name: "placement", Type: "list", Description: "Link placement values, comma-separated (case-insensitive); ~none matches empty."},
	{Name: "include_bots", Type: "bool", Description: "Count clicks classified as bots.", Default: "false"},
	{Name: "limit", Type: "int", Description: "Maximum rows for ranked lists, 1-500. Default is report-specific (default_limit in the catalogue)."},
}

// Querier is the slice of pgx.Tx a report needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Request is everything a report's Run function receives. All queries must go
// through Q so they share one REPEATABLE READ snapshot.
type Request struct {
	UserID  int64
	Params  Params
	Raw     url.Values // full query, for the report's own extra parameters
	Windows ResolvedWindows
	Now     time.Time
	Q       Querier
	// BaseURL is the deployment's public base URL, for building short URLs.
	BaseURL string
}

// IntParam reads a report-specific integer parameter in [min, max].
func (r *Request) IntParam(name string, def, min, max int) (int, error) {
	s := strings.TrimSpace(r.Raw.Get(name))
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, paramErr(name, "invalid_"+name, fmt.Sprintf("%s must be an integer from %d to %d", name, min, max))
	}
	return n, nil
}

// RequiredString reads a report-specific parameter that must be present and
// non-empty; otherwise it returns a *ParamError coded missing_<name>.
func (r *Request) RequiredString(name string) (string, error) {
	s := strings.TrimSpace(r.Raw.Get(name))
	if s == "" {
		return "", paramErr(name, "missing_"+name, name+" is required")
	}
	return s, nil
}

// EnumParam reads a report-specific parameter that must be one of allowed,
// returning def when absent. Otherwise it returns a *ParamError coded
// invalid_<name>.
func (r *Request) EnumParam(name, def string, allowed ...string) (string, error) {
	s := strings.TrimSpace(r.Raw.Get(name))
	if s == "" {
		return def, nil
	}
	for _, a := range allowed {
		if s == a {
			return s, nil
		}
	}
	return "", paramErr(name, "invalid_"+name, name+" must be one of "+strings.Join(allowed, ", "))
}

// Output is what a report's Run returns: the highlights and the data payload
// the Service wraps in the Envelope.
type Output struct {
	Highlights []string
	Data       any
}

// RunFunc runs one report.
type RunFunc func(ctx context.Context, req *Request) (Output, error)

// Report is one registered report.
type Report struct {
	Name        string
	Description string
	// Params lists the report's extra parameters (beyond CommonParams).
	Params []ParamSpec
	// DefaultLimit is the value Params.LimitOr should be given for this
	// report; 0 means the report has no limit. It is shown in the catalogue.
	DefaultLimit int
	// Windows, when set, overrides the windows the Service resolved from the
	// parameters. It receives a Request whose Windows hold the standard
	// resolution (and whose Params.RangeExplicit says whether the caller chose
	// a range) and returns the windows the report runs with and the envelope
	// reports. Use it for report-specific defaults, e.g. a campaign's dates.
	Windows func(ctx context.Context, req *Request) (ResolvedWindows, error)
	Run     RunFunc
}

// ReportInfo is a report's catalogue entry.
type ReportInfo struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Path        string      `json:"path"`
	Params      []ParamSpec `json:"params"`
	// DefaultLimit is the report's default for the limit parameter (omitted
	// when the report has none).
	DefaultLimit int `json:"default_limit,omitempty"`
}

// Catalogue is the GET /api/reports body.
type Catalogue struct {
	Reports      []ReportInfo `json:"reports"`
	CommonParams []ParamSpec  `json:"common_params"`
}

// Registry holds reports in registration order.
type Registry struct {
	order []string
	by    map[string]Report
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{by: map[string]Report{}} }

// Register adds a report. It panics on an empty or duplicate name or a nil
// Run, which are programming errors caught at startup.
func (r *Registry) Register(rep Report) {
	if rep.Name == "" || rep.Run == nil {
		panic("reports: report needs a name and a Run function")
	}
	if _, dup := r.by[rep.Name]; dup {
		panic("reports: duplicate report " + rep.Name)
	}
	if rep.Params == nil {
		rep.Params = []ParamSpec{}
	}
	r.by[rep.Name] = rep
	r.order = append(r.order, rep.Name)
}

// Get returns the named report.
func (r *Registry) Get(name string) (Report, bool) {
	rep, ok := r.by[name]
	return rep, ok
}

// Catalogue lists every registered report.
func (r *Registry) Catalogue() Catalogue {
	c := Catalogue{Reports: make([]ReportInfo, 0, len(r.order)), CommonParams: CommonParams}
	for _, n := range r.order {
		rep := r.by[n]
		c.Reports = append(c.Reports, ReportInfo{Name: rep.Name, Description: rep.Description, Path: "/api/reports/" + rep.Name, Params: rep.Params, DefaultLimit: rep.DefaultLimit})
	}
	return c
}

// NewDefaultRegistry returns a registry holding every built-in report.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(overviewReport)
	r.Register(topContentReport)
	r.Register(topLinksReport)
	r.Register(breakdownReport)
	r.Register(timeseriesReport)
	r.Register(moversReport)
	r.Register(idleLinksReport)
	r.Register(campaignComparisonReport)
	return r
}

// WindowJSON is the envelope's window.
type WindowJSON struct {
	From           string `json:"from"`
	To             string `json:"to"`
	TZ             string `json:"tz"`
	PartialLastDay bool   `json:"partial_last_day"`
}

// PrevWindowJSON is the envelope's previous_window.
type PrevWindowJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// FiltersJSON echoes the filters that were applied.
type FiltersJSON struct {
	Campaign    string   `json:"campaign,omitempty"`
	Source      []string `json:"source,omitempty"`
	Medium      []string `json:"medium,omitempty"`
	UTMCampaign []string `json:"utm_campaign,omitempty"`
	Content     []string `json:"content,omitempty"`
	Term        []string `json:"term,omitempty"`
	Placement   []string `json:"placement,omitempty"`
	IncludeBots bool     `json:"include_bots,omitempty"`
}

// Envelope is the response wrapper every report shares.
type Envelope struct {
	Report         string          `json:"report"`
	Window         WindowJSON      `json:"window"`
	PreviousWindow *PrevWindowJSON `json:"previous_window"`
	Filters        FiltersJSON     `json:"filters"`
	GeneratedAt    time.Time       `json:"generated_at"`
	Highlights     []string        `json:"highlights"`
	Data           any             `json:"data"`
}

func filtersJSON(p Params) FiltersJSON {
	return FiltersJSON{
		Campaign: p.Campaign, Source: p.Filters["source"], Medium: p.Filters["medium"],
		UTMCampaign: p.Filters["utm_campaign"], Content: p.Filters["content"],
		Term: p.Filters["term"], Placement: p.Filters["placement"], IncludeBots: p.IncludeBots,
	}
}

// Service runs reports for a user.
type Service struct {
	Pool      *pgxpool.Pool
	Registry  *Registry
	DefaultTZ string
	BaseURL   string           // BASE_URL; used for short_url fields
	Now       func() time.Time // nil means time.Now
}

// NewService builds a Service over the built-in registry.
func NewService(pool *pgxpool.Pool, defaultTZ string) *Service {
	return &Service{Pool: pool, Registry: NewDefaultRegistry(), DefaultTZ: defaultTZ}
}

// Catalogue lists the registered reports (GET /api/reports).
func (s *Service) Catalogue() Catalogue { return s.Registry.Catalogue() }

// Run executes the named report for userID. It returns ErrReportNotFound for an
// unknown name and a *ParamError for bad parameters. Everything the report
// reads, including the range=all lookup, happens in one REPEATABLE READ
// read-only transaction.
func (s *Service) Run(ctx context.Context, userID int64, name string, raw url.Values) (*Envelope, error) {
	rep, ok := s.Registry.Get(name)
	if !ok {
		return nil, ErrReportNotFound
	}
	p, err := ParseParams(raw, s.DefaultTZ)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	if err := p.Validate(now); err != nil {
		return nil, err
	}

	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("reports: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var earliest *time.Time
	if p.From == "" && p.Range == RangeAll {
		var t *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT min(k.clicked_at) FROM clicks k JOIN links l ON l.id = k.link_id WHERE l.user_id = $1`, userID).Scan(&t); err != nil {
			return nil, fmt.Errorf("reports: earliest click: %w", err)
		}
		earliest = t
	}
	wins := ResolveWindows(p, now, earliest)

	req := &Request{UserID: userID, Params: p, Raw: raw, Windows: wins, Now: now, Q: tx, BaseURL: s.BaseURL}
	if rep.Windows != nil {
		if wins, err = rep.Windows(ctx, req); err != nil {
			return nil, err
		}
		req.Windows = wins
	}
	out, err := rep.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	env := &Envelope{
		Report:  rep.Name,
		Window:  WindowJSON{From: wins.Current.From, To: wins.Current.To, TZ: p.TZName, PartialLastDay: wins.Current.PartialLastDay},
		Filters: filtersJSON(p), GeneratedAt: now.UTC().Truncate(time.Second),
		Highlights: out.Highlights, Data: out.Data,
	}
	if env.Highlights == nil {
		env.Highlights = []string{}
	}
	if wins.Previous != nil {
		env.PreviousWindow = &PrevWindowJSON{From: wins.Previous.From, To: wins.Previous.To}
	}
	return env, nil
}
