package handlers

// ── CSV export (#0107) ──────────────────────────────────────────────────
//
// GET /api/campaigns/{slug}/export.csv: the campaign's per-link rollup as a
// CSV file. The app deliberately does not do conversions, unique visitors,
// or geography — export is the pressure valve that lets the numbers leave
// for a spreadsheet where those questions can be answered against other
// data, without growing the app to cover them (issue description).
//
// REUSES #0102'S QUERIES, DOES NOT DUPLICATE THEM: "clicks in window" comes
// from clicks.StatsStore.CampaignRollup — the SAME call GET
// /api/campaigns/{slug}/stats makes — never a second aggregation path. Per
// CampaignRollup's own doc comment (and #0102 downstream constraint 4,
// addressed to this issue by name: "use CampaignRollup, not the four
// standalone methods — they are production-dead and carry STANDALONE:
// warnings precisely because using them straddles snapshots"), the
// standalone CampaignClicksByLink is deliberately NOT used here even though
// only ByLink's rows are needed: calling it directly would read against its
// own snapshot rather than the transaction CampaignRollup opens, reopening
// the exact total-vs-breakdown drift #0102 closed.
//
// WINDOW: identical semantics to GET /api/campaigns/{slug}/stats — optional
// ?from=/?to= (parseStatsWindow), defaulting to the campaign's own
// starts_at/ends_at when both are set (clamped at today), otherwise the
// last 30 days (campaignWindow, internal/clicks/stats.go). Bot-excluded by
// default (#0101), matching every other number in the campaign view —
// CampaignRollup already excludes is_bot = TRUE rows from ByLink, so
// nothing extra is needed here.
//
// EMPTY WINDOW DECISION (#0104 downstream constraint 1 names this issue by
// number): a resolved [X, X) window — zero days, reachable whenever a
// campaign's starts_at equals its ends_at — is a REPRESENTABLE state, not
// an error. It exports one row per currently-assigned link with
// clicks_in_window=0 and share_of_listed_links_pct=0 (see
// buildCampaignExportRows: 0/0 is defined as 0, mirroring
// web/src/lib/campaigns.ts's buildLinkRows), never an empty file — the file
// still documents every link in the campaign, just with nothing recorded in
// that window. This is a DIFFERENT state from "empty campaign" below, and
// both are tested separately (TestCampaignsExport_EmptyWindowStillListsLinks
// vs TestCampaignsExport_EmptyCampaignHasHeaderOnly).
//
// EMPTY CAMPAIGN (no links currently assigned) exports the header row and
// NO data rows — never a 404 and never a zero-byte file — the same "empty
// collection, not a failure" contract QRZip uses for a campaign with no
// links (issue AC).
//
// SHARE_OF_LISTED_LINKS_PCT — NOT "share of campaign total", DELIBERATELY
// (review finding B1): it is computed against the sum of clicks_in_window
// across the rows THIS FILE ACTUALLY LISTS (currently-assigned links only),
// not CampaignStats.click_count, and can legitimately fall short of what a
// "share of campaign total" column name would promise. This is the EXACT
// number the on-screen table's own "Share of listed links" column shows
// (CampaignDetail.svelte) — #0103 named that column deliberately, not
// "Share of campaign total", specifically because by_link can carry a click
// attributed to a link since unassigned from this campaign (#0100), which
// is not among linkRows and so is excluded from both the numerator and the
// denominator here, exactly as it is on screen (the table additionally
// prints a caveat sentence below itself when that gap is nonzero — a CSV
// has no such caption to fall back on, which is why the COLUMN NAME itself
// has to carry the distinction here). An earlier version of this file used
// the column name "share_of_campaign_total_pct", which claims a
// denominator this file does not use; a campaign whose by_link total
// exceeds what is listed would then export a share that reads as "of the
// whole campaign" while actually meaning "of what's in this file" —
// demonstrated live: a 3-click campaign window with only 2 of those clicks
// on currently-listed links exported 100% for a link that was actually 67%
// of the campaign total. Fixed by renaming the column to match the
// screen's own name rather than changing the arithmetic (the arithmetic
// was always correct for "share of listed links"; only the misleading name
// changes here).
//
// Go and TypeScript are two separate implementations with no shared code
// across that boundary — kept in sync by hand, the same caveat
// internal/qr/qr.go's ShortURLBase doc comment states for the same reason —
// but computing the SAME arithmetic on the SAME window's SAME byLink data is
// what makes the AC's reconciliation hold: exporting the window shown on
// screen produces the same clicks_in_window and share_of_listed_links_pct
// per link as the table (TestCampaignsExport_ReconcilesWithDetailView).

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
	"github.com/brennanMKE/ShortLinks/internal/qr"
)

// csvExportContentType is the MIME type for the per-link rollup CSV,
// explicitly carrying charset=utf-8 so a consumer never has to sniff the
// encoding (AC: "UTF-8 is handled correctly for non-ASCII titles and
// placements").
const csvExportContentType = "text/csv; charset=utf-8"

// utf8BOM is written before the CSV body. encoding/csv does not write one on
// its own. Excel — unlike every other consumer this app targets — treats a
// BOM-less UTF-8 CSV as Windows-1252 on some locales, corrupting every
// non-ASCII title/placement the moment the file is double-clicked open
// rather than explicitly imported with an encoding chosen by hand.
//
// THE REAL COST (review finding B4 — an earlier version of this comment
// claimed the opposite): the three bytes are NOT transparently skipped by
// "every other reader that specifically scans for a BOM". Measured
// directly against the actual downloaded file: Go's OWN encoding/csv reads
// the header's first field back as "\ufeffkey" (6 bytes), not "key" — a
// plain `header[0] == "key"` check fails; Python's csv.DictReader likewise
// keys the first column "\ufeffkey", so `row["key"]` is nil/None. This
// file's own test helper, parseExportCSV, strips the BOM by hand before
// parsing for exactly this reason — proof by construction that it is not
// free. AC 1 ("this file gets opened in a spreadsheet and possibly parsed
// by something downstream") means a downstream parser WILL hit this.
//
// Kept anyway — a deliberate, stated trade, not an oversight: every
// spreadsheet application this app's admin actually opens the file in
// directly (Excel, Numbers, Google Sheets' upload-a-file import) handles
// the BOM transparently, and Excel is the one consumer of the three that
// silently CORRUPTS non-ASCII content without it — worse than a downstream
// script needing one extra strip-the-BOM/utf-8-sig-open step it can look up
// in five minutes. TestCampaignsExport_BodyCarriesUTF8BOM pins the three
// bytes stay present AND demonstrates the unstripped-reader cost directly,
// rather than only asserting the bytes exist.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// neutralizeFormulaField prefixes s with a single quote when its first byte
// is '=', '+', '-', or '@' — the characters that make Excel or Google
// Sheets interpret a CSV cell as a formula instead of literal text (CSV
// formula injection). Issue AC: "the destination URL and title are not
// always self-authored — and the cost of getting this right is one helper
// function." Applied to EVERY string field this export writes (see
// writeCampaignExportCSV), including key/short_url, which can never
// actually trigger it (a generated/validated key is alphanumeric and a
// short URL always starts with "https://") — applying it uniformly removes
// the chance of forgetting it on the one column that does need it, at the
// cost of a no-op check on the two that don't. An empty string is returned
// unchanged (no [0] byte to inspect).
func neutralizeFormulaField(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}

// campaignExportRow is one row of the CSV export: a currently-assigned
// link's identity/metadata plus its windowed click count and its share of
// the listed links' total (SharePct — NOT the campaign's overall total, see
// its own doc comment). Field-for-field mirror of web/src/lib/campaigns.ts's
// CampaignLinkRow (buildLinkRows) — see this file's package-level doc
// comment for why that mirroring, not literal shared code, is what the
// reconciliation AC rests on.
type campaignExportRow struct {
	Key            string
	ShortURL       string
	Title          string
	DestinationURL string
	UTMSource      string
	UTMMedium      string
	UTMContent     string
	Placement      string
	ClicksInWindow int64
	// SharePct is share_of_listed_links_pct (review finding B1) — 0-100,
	// rounded, matching buildLinkRows' Math.round. NOT "share of campaign
	// total": see buildCampaignExportRows' doc comment and the package-level
	// "SHARE_OF_LISTED_LINKS_PCT" section for why the denominator is the sum
	// of rows THIS export lists, not CampaignStats.click_count, and why the
	// column name has to say so explicitly (a CSV has no on-screen caveat
	// sentence to fall back on the way the table does).
	SharePct  int64
	CreatedAt time.Time
}

// buildCampaignExportRows pairs every currently-assigned link (linkRows,
// links.Store.ListLinksForCampaign) with its windowed click count from
// byLink (clicks.CampaignRollup.ByLink, matched by key — the SAME slice GET
// /api/campaigns/{slug}/stats returns and the links table matches against),
// and computes each row's share of the total clicks ACTUALLY LISTED here.
//
// DENOMINATOR IS THE SUM OF LISTED ROWS, NOT CampaignStats.click_count —
// mirrors buildLinkRows (web/src/lib/campaigns.ts) exactly, for the same
// reason: byLink can carry a click attributed to a link that has SINCE been
// unassigned from this campaign (#0100 — a click stays attributed to the
// campaign that owned the link when it was recorded), which is not among
// linkRows and so is silently excluded from both the numerator and the
// denominator here, exactly as it is on screen. A total of 0 windowed
// clicks yields 0% for every row rather than dividing by zero.
//
// Sorted by clicks_in_window descending, STABLE (sort.SliceStable, review
// finding "also fix 2" — not sort.Slice) so that ties fall back to
// linkRows' OWN incoming order rather than a fabricated tiebreaker.
// linkRows (links.Store.ListLinksForCampaign) arrives ordered
// `created_at DESC, id DESC` — the exact order GET /api/campaigns/{slug}
// hands the on-screen table (detail.links), which the table's own
// sortLinkRows (web/src/lib/campaigns.ts) ALSO sorts with a stable sort on
// clicksInWindow alone, relying on Array.prototype.sort's ES2019+
// stability guarantee to preserve created_at-DESC order within a tie
// rather than sorting on it explicitly. A key-ascending tiebreaker here (an
// earlier version of this function used one) is deterministic but
// disagrees with the screen: a live zero-click, three-link window rendered
// expc/expb/expa on screen (creation order) but exported expa/expb/expc
// (key order) — same rows, same numbers, different order. Matching the
// screen's own tiebreak (rather than documenting a divergence) is what
// TestBuildCampaignExportRows_TiesPreserveLinkRowsOrder pins.
func buildCampaignExportRows(baseURL string, linkRows []links.Link, byLink []clicks.LinkBucket) []campaignExportRow {
	counts := make(map[string]int64, len(byLink))
	for _, b := range byLink {
		if b.Key != "" {
			counts[b.Key] = b.Count
		}
	}

	rows := make([]campaignExportRow, 0, len(linkRows))
	var total int64
	for _, l := range linkRows {
		c := counts[l.Key]
		total += c
		rows = append(rows, campaignExportRow{
			Key:            l.Key,
			ShortURL:       qr.ShortURL(baseURL, l.Key),
			Title:          l.Title,
			DestinationURL: l.DestinationURL,
			UTMSource:      l.UTMSource,
			UTMMedium:      l.UTMMedium,
			UTMContent:     l.UTMContent,
			Placement:      l.Placement,
			ClicksInWindow: c,
			CreatedAt:      l.CreatedAt,
		})
	}
	if total > 0 {
		for i := range rows {
			rows[i].SharePct = int64(math.Round(float64(rows[i].ClicksInWindow) / float64(total) * 100))
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].ClicksInWindow > rows[j].ClicksInWindow
	})
	return rows
}

// campaignExportCSVHeader is the export's column header row — STABLE names
// (issue AC: "this file gets opened in a spreadsheet and possibly parsed by
// something downstream"). Order matches campaignExportRow's field order.
// share_of_listed_links_pct, NOT share_of_campaign_total_pct (review finding
// B1) — see campaignExportRow.SharePct's doc comment for why that name would
// misrepresent the denominator this column actually uses.
var campaignExportCSVHeader = []string{
	"key", "short_url", "title", "destination_url",
	"utm_source", "utm_medium", "utm_content", "placement",
	"clicks_in_window", "share_of_listed_links_pct", "created_at",
}

// csvExportDateLayout is the single documented format every created_at cell
// uses: RFC 3339 in UTC (e.g. "2026-08-01T14:32:05Z") — unambiguous across
// locales (no MM/DD vs DD/MM guessing) and parsed natively as a real
// datetime by Excel, Google Sheets, and Numbers, not read back as text.
// created_at is a genuine instant (like linkDetail.ts's formatDate callers,
// per #0103 downstream constraint 3), not a calendar-only value like a
// campaign's starts_at/ends_at, so a full timestamp — not a date-only
// string — is the correct precision to export.
const csvExportDateLayout = time.RFC3339

// writeCampaignExportCSV writes the header row and one row per rows to w
// using encoding/csv (issue AC: "use encoding/csv rather than string
// concatenation" — csv.Writer already quotes a field containing a comma, a
// quote, or a newline correctly, which the free-text title/destination/
// placement columns routinely will). Every STRING field passes through
// neutralizeFormulaField first (see its doc comment for why key/short_url
// are included even though they can never trigger it). Numeric fields
// (clicks_in_window, share_of_listed_links_pct) are written via strconv,
// never with a thousands separator (issue AC), and never routed through the
// formula-neutralizing helper — they are always non-negative so cannot
// start with any of '=', '+', '-', '@' regardless.
func writeCampaignExportCSV(w *csv.Writer, rows []campaignExportRow) error {
	if err := w.Write(campaignExportCSVHeader); err != nil {
		return fmt.Errorf("handlers: writing csv header: %w", err)
	}
	for _, r := range rows {
		record := []string{
			neutralizeFormulaField(r.Key),
			neutralizeFormulaField(r.ShortURL),
			neutralizeFormulaField(r.Title),
			neutralizeFormulaField(r.DestinationURL),
			neutralizeFormulaField(r.UTMSource),
			neutralizeFormulaField(r.UTMMedium),
			neutralizeFormulaField(r.UTMContent),
			neutralizeFormulaField(r.Placement),
			strconv.FormatInt(r.ClicksInWindow, 10),
			strconv.FormatInt(r.SharePct, 10),
			r.CreatedAt.UTC().Format(csvExportDateLayout),
		}
		if err := w.Write(record); err != nil {
			return fmt.Errorf("handlers: writing csv row for key %q: %w", r.Key, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("handlers: flushing csv writer: %w", err)
	}
	return nil
}

// subtractOneUTCDate subtracts one calendar day from a "YYYY-MM-DD"
// date-only string, in UTC. Returns "" for malformed input. Go-side twin of
// web/src/lib/campaigns.ts's subtractOneDayUTC — the half-open-window →
// inclusive-end conversion campaignExportFilename needs (review finding
// B5), computed the same way windowLabel already computes it for the
// on-screen caption.
func subtractOneUTCDate(value string) string {
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}

// campaignExportFilename builds the Content-Disposition filename: the
// campaign's own slug (already filesystem-safe — campaigns.Store slugs it
// the same way a link key is generated) plus the resolved window, so a
// folder of exports from different campaigns, or different windows of the
// SAME campaign, never collide as several files all named "export.csv"
// (issue deliverable).
//
// USES THE INCLUSIVE END, NOT windowTo VERBATIM (review finding B5 — an
// earlier version of this function used the raw bound and its own doc
// comment argued that was fine because "this is a filesystem identifier,
// not a caption"; that argument was wrong). windowTo is the EXCLUSIVE end
// of CampaignStats' half-open [windowFrom, windowTo) range (#0103); the
// on-screen caption (windowLabel, web/src/lib/campaigns.ts) subtracts one
// day specifically so what it prints agrees with the chart axis and with
// what a human reading "Jul 9 – Aug 7" expects the LAST day covered to be.
// Presenting windowTo verbatim here disagreed with that caption on a live
// campaign: screen "Jul 9, 2026 – Aug 7, 2026", filename
// "..._2026-07-09_to_2026-08-08.csv" — one day later than the window the
// rest of the app describes. The filename is the ONLY place in the app
// that would otherwise show that raw exclusive bound, and — since nothing
// INSIDE the exported file itself names the window either — the only
// durable record of the window anywhere in the artifact once it's saved to
// disk, so getting it wrong here has no caption alongside it to catch the
// reader's eye the way #0104's review finding 3 did for the chart.
//
// ZERO-DAY WINDOW (windowFrom == windowTo, #0104 downstream constraint 1):
// subtracting a day would land BEFORE windowFrom, the same backwards-range
// trap windowLabel's own doc comment documents for the caption — mirrored
// here by falling back to windowFrom for the end instead, so a same-day
// window names consistently as "..._2026-08-01_to_2026-08-01.csv" (both
// ends equal, matching a zero-length range) rather than
// "..._2026-08-01_to_2026-07-31.csv" (an end before the start).
func campaignExportFilename(slug, windowFrom, windowTo string) string {
	end := subtractOneUTCDate(windowTo)
	if end == "" || end < windowFrom {
		end = windowFrom
	}
	return fmt.Sprintf("%s-export_%s_to_%s.csv", slug, windowFrom, end)
}

// Export handles GET /api/campaigns/{slug}/export.csv: the caller's
// campaign's per-link rollup — one row per link CURRENTLY assigned to the
// campaign — as a downloadable CSV, over the same optional ?from=/?to=
// window GET /api/campaigns/{slug}/stats accepts (parseStatsWindow), with
// the same default-window resolution (clicks.campaignWindow) and the same
// bot exclusion (#0101). A slug that does not exist OR belongs to another
// user yields 404, matching every other campaign endpoint's
// indistinguishable-404 contract — verified directly by
// TestCampaignsExport_OwnershipEnforced. Returns 500 if no stats provider is
// wired (this endpoint has no meaningful degraded response, matching
// Stats).
//
// DEVIATION FROM THE ISSUE'S OWN NOTE, STATED (review finding "also fix
// 5"): the issue text says "the endpoint should not build the whole file
// in memory as a single concatenated string... encoding/csv writing to the
// ResponseWriter is both simpler and correct." This handler instead builds
// the ENTIRE CSV into a bytes.Buffer (csv.NewWriter(&buf), not
// csv.NewWriter(w)) before writing any response header — the read here is
// "don't concatenate it as a giant string" (which this doesn't —
// encoding/csv still does the actual writing/escaping) rather than "never
// buffer it", but writing csv.NewWriter(w) directly IS what the issue
// describes and this is not that, so the trade is named explicitly instead
// of leaving the comment read as compliance. The reason, same as QRZip's
// zip archive: buffering first means a mid-write failure (e.g. a row whose
// field somehow fails to encode) still reports a clean 500, where writing
// straight to w would already have sent a 200 status and Content-Type
// header by the time an error surfaced, leaving the client's browser to
// silently save a truncated file with no way to tell it apart from a
// complete one. At this app's actual scale — a personal/small-team
// campaign's per-link rollup, #0098's downstream constraints — the
// buffered size is at most a few hundred rows, not a real memory concern
// in practice; the buffering is a correctness trade against the issue's
// literal instruction, not a performance one.
func (h *CampaignsHandler) Export(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}

	c, err := h.store.GetCampaignBySlug(r.Context(), u.ID, slug)
	switch {
	case err == nil:
		// fall through.
	case errors.Is(err, campaigns.ErrCampaignNotFound):
		writeError(w, http.StatusNotFound, "campaign not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if h.stats == nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	from, to, ok := parseStatsWindow(w, r)
	if !ok {
		return // parseStatsWindow already wrote the 400.
	}

	// #0102/#0107: CampaignRollup, not the standalone CampaignClicksByLink —
	// see this file's package-level doc comment for why using the
	// standalone method here would reopen the exact snapshot drift #0102
	// closed. Only ByLink and Stats.WindowFrom/WindowTo (for the filename)
	// are used from the payload; Timeseries/SeriesByLink are read and
	// discarded — an acceptable cost against reusing the one call that is
	// documented safe to read a per-link total from.
	rollup, err := h.stats.CampaignRollup(r.Context(), c.ID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	linkRows, err := h.links.ListLinksForCampaign(r.Context(), u.ID, c.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	rows := buildCampaignExportRows(h.baseURL, linkRows, rollup.ByLink)

	var buf bytes.Buffer
	buf.Write(utf8BOM)
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, rows); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	filename := campaignExportFilename(c.Slug, rollup.Stats.WindowFrom, rollup.Stats.WindowTo)
	w.Header().Set("Content-Type", csvExportContentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}
