package handlers

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/campaigns"
	"github.com/brennanMKE/ShortLinks/internal/clicks"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// ── neutralizeFormulaField ───────────────────────────────────────────────

// TestNeutralizeFormulaField_PrefixesFormulaTriggerCharacters asserts every
// character the issue AC names by name ('=', '+', '-', '@') gets a leading
// single quote, so a spreadsheet renders the value as text instead of
// evaluating it. Mutation check performed by hand: deleting the function
// body's switch (returning s unchanged always) fails every case in this
// table except "plain text" and "empty string" — confirming the test
// actually pins the neutralization, not just the passthrough behavior.
func TestNeutralizeFormulaField_PrefixesFormulaTriggerCharacters(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"equals formula", "=SUM(A1:A9)", "'=SUM(A1:A9)"},
		{"plus formula", "+1+1", "'+1+1"},
		{"minus formula", "-1+1", "'-1+1"},
		{"at formula", "@SUM(1,1)", "'@SUM(1,1)"},
		{"plain text", "Storefront Window", "Storefront Window"},
		{"empty string", "", ""},
		{"leading space then equals is NOT prefixed", " =SUM(1)", " =SUM(1)"},
		{"non-ascii leading rune is not mistaken for a trigger byte", "café", "café"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := neutralizeFormulaField(tc.in)
			if got != tc.want {
				t.Errorf("neutralizeFormulaField(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ── buildCampaignExportRows ──────────────────────────────────────────────

func TestBuildCampaignExportRows_MatchesClicksAndComputesShare(t *testing.T) {
	created := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// Deliberately NOT in clicks-descending order. buildCampaignExportRows is
	// fed straight from ListLinksForCampaign, which returns created_at DESC,
	// id DESC — so in production linkRows routinely arrive out of clicks
	// order and the sort does real work on every request. A fixture that
	// happens to arrive pre-sorted cannot tell "sorted correctly" apart from
	// "not sorted at all": deleting the sort.SliceStable call outright used
	// to pass this entire package.
	linkRows := []links.Link{
		{Key: "ccc333", Title: "No clicks", DestinationURL: "https://c.example.com", CreatedAt: created},
		{Key: "aaa111", Title: "Flyer A", DestinationURL: "https://a.example.com", UTMSource: "print", CreatedAt: created},
		{Key: "bbb222", Title: "Flyer B", DestinationURL: "https://b.example.com", UTMSource: "email", CreatedAt: created},
	}
	byLink := []clicks.LinkBucket{
		{Key: "aaa111", Count: 30},
		{Key: "bbb222", Count: 10},
		// ccc333 has no entry — zero clicks in window.
	}

	rows := buildCampaignExportRows(linkRows, byLink)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}

	// Sorted by clicks_in_window desc. Because linkRows above arrive in a
	// different order (ccc333, aaa111, bbb222), these assertions pin that the
	// sort REORDERS — deleting it entirely fails here. No tie in this fixture
	// (30, 10, 0 are all distinct), so this asserts nothing about tiebreak
	// order; see TestBuildCampaignExportRows_TiesPreserveLinkRowsOrder.
	if rows[0].Key != "aaa111" || rows[0].ClicksInWindow != 30 || rows[0].SharePct != 75 {
		t.Errorf("rows[0] = %+v, want key=aaa111 clicks=30 share=75", rows[0])
	}
	if rows[1].Key != "bbb222" || rows[1].ClicksInWindow != 10 || rows[1].SharePct != 25 {
		t.Errorf("rows[1] = %+v, want key=bbb222 clicks=10 share=25", rows[1])
	}
	if rows[2].Key != "ccc333" || rows[2].ClicksInWindow != 0 || rows[2].SharePct != 0 {
		t.Errorf("rows[2] = %+v, want key=ccc333 clicks=0 share=0", rows[2])
	}

	// Shares reconcile to (approximately, allowing for rounding) 100 — same
	// AC as the on-screen table.
	var total int64
	for _, r := range rows {
		total += r.SharePct
	}
	if total != 100 {
		t.Errorf("sum of SharePct = %d, want 100", total)
	}
}

// TestBuildCampaignExportRows_NonExactShareIsRounded pins math.Round
// specifically (review finding B3). Every fixture above divides exactly
// (30/40->75, 10/40->25, 0/40->0), so math.Floor and math.Ceil each produce
// the IDENTICAL output to math.Round on all of them — swapping either in
// failed 0 tests before this one existed. A 2-vs-1 split does not divide
// exactly: 2/3*100 = 66.67 rounds to 67, 1/3*100 = 33.33 rounds to 33.
// math.Floor would silently ship 66/33 (summing to 99, not 100);
// math.Ceil would ship 67/34 (summing to 101). Verified live against the
// on-screen table for the same 2/1 split: the screen also shows 67%/33%.
func TestBuildCampaignExportRows_NonExactShareIsRounded(t *testing.T) {
	linkRows := []links.Link{
		{Key: "two", DestinationURL: "https://two.example.com"},
		{Key: "one", DestinationURL: "https://one.example.com"},
	}
	byLink := []clicks.LinkBucket{
		{Key: "two", Count: 2},
		{Key: "one", Count: 1},
	}
	rows := buildCampaignExportRows(linkRows, byLink)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].Key != "two" || rows[0].ClicksInWindow != 2 || rows[0].SharePct != 67 {
		t.Errorf("rows[0] = %+v, want key=two clicks=2 share=67", rows[0])
	}
	if rows[1].Key != "one" || rows[1].ClicksInWindow != 1 || rows[1].SharePct != 33 {
		t.Errorf("rows[1] = %+v, want key=one clicks=1 share=33", rows[1])
	}
}

// TestBuildCampaignExportRows_TiesPreserveLinkRowsOrder pins the sort's
// stability and tiebreak (review finding "also fix 2"). It pins ONLY those:
// its fixture is all-ties, so it cannot detect the sort being deleted
// outright — TestBuildCampaignExportRows_MatchesClicksAndComputesShare
// covers that, via a fixture that arrives out of clicks order. (An earlier
// version of this comment ran the two together and read as though this test
// closed both; it does not.) A key-ascending tiebreaker — an earlier version of
// buildCampaignExportRows used one — is deterministic but disagrees with
// the on-screen table: links.Store.ListLinksForCampaign (and therefore
// linkRows here, and detail.links on screen) arrives ordered
// `created_at DESC, id DESC`, and the table's own sortLinkRows
// (web/src/lib/campaigns.ts) is a STABLE sort on clicksInWindow alone, so a
// clicks tie keeps that creation order on screen. Live reproduction with a
// three-link, zero-click window: screen rendered expc/expb/expa (creation
// order); an export using a key-ascending tiebreak instead produced
// expa/expb/expc — identical numbers, different row order. This fixture
// mirrors that exact scenario.
func TestBuildCampaignExportRows_TiesPreserveLinkRowsOrder(t *testing.T) {
	linkRows := []links.Link{
		{Key: "expc", Title: "C", DestinationURL: "https://c.example.com"}, // newest (arrives first)
		{Key: "expb", Title: "B", DestinationURL: "https://b.example.com"},
		{Key: "expa", Title: "A", DestinationURL: "https://a.example.com"}, // oldest (arrives last)
	}
	rows := buildCampaignExportRows(linkRows, nil) // every row ties at 0 clicks
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	wantOrder := []string{"expc", "expb", "expa"}
	for i, want := range wantOrder {
		if rows[i].Key != want {
			t.Errorf("rows[%d].Key = %q, want %q (linkRows' own incoming order preserved on a clicks tie); full order = %v",
				i, rows[i].Key, want, []string{rows[0].Key, rows[1].Key, rows[2].Key})
			break
		}
	}
}

// TestBuildCampaignExportRows_ZeroTotalClicksYieldsZeroSharesNotDivideByZero
// covers the empty-window state (#0104 downstream constraint 1, addressed to
// this issue by name): every currently-assigned link still gets a row, with
// clicks_in_window=0 and share=0 rather than NaN/Inf/a panic.
func TestBuildCampaignExportRows_ZeroTotalClicksYieldsZeroSharesNotDivideByZero(t *testing.T) {
	linkRows := []links.Link{
		{Key: "z1", Title: "One", DestinationURL: "https://one.example.com"},
		{Key: "z2", Title: "Two", DestinationURL: "https://two.example.com"},
	}
	rows := buildCampaignExportRows(linkRows, nil)
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.ClicksInWindow != 0 || r.SharePct != 0 {
			t.Errorf("row %q = %+v, want clicks=0 share=0", r.Key, r)
		}
	}
}

// TestBuildCampaignExportRows_UnassignedLinkClicksExcludedFromDenominator
// mirrors web/src/lib/campaigns.ts's buildLinkRows doc comment: byLink can
// carry a click attributed to a link that has SINCE been unassigned from
// this campaign (#0100). That entry must not inflate the denominator (which
// would make every listed row's share fall short of 100%) or appear as its
// own row (it has no corresponding entry in linkRows, so it cannot).
func TestBuildCampaignExportRows_UnassignedLinkClicksExcludedFromDenominator(t *testing.T) {
	linkRows := []links.Link{
		{Key: "listed", Title: "Listed", DestinationURL: "https://listed.example.com"},
	}
	byLink := []clicks.LinkBucket{
		{Key: "listed", Count: 5},
		{Key: "since-unassigned", Count: 95}, // not in linkRows
	}
	rows := buildCampaignExportRows(linkRows, byLink)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1 (only currently-assigned links get a row)", len(rows))
	}
	if rows[0].ClicksInWindow != 5 {
		t.Errorf("ClicksInWindow = %d, want 5", rows[0].ClicksInWindow)
	}
	// Denominator is 5 (listed only), not 100 (listed + unassigned) — so the
	// one listed row's share is 100%, matching what the on-screen table
	// would show for the same data.
	if rows[0].SharePct != 100 {
		t.Errorf("SharePct = %d, want 100 (denominator excludes the unassigned link's clicks)", rows[0].SharePct)
	}
}

// TestBuildCampaignExportRows_EmptyCampaignReturnsNoRows covers the AC "an
// empty campaign exports headers and no rows" at the row-building layer —
// the handler-level equivalent (asserting the actual CSV body) lives in
// TestCampaignsExport_EmptyCampaignHasHeaderOnly.
func TestBuildCampaignExportRows_EmptyCampaignReturnsNoRows(t *testing.T) {
	rows := buildCampaignExportRows(nil, nil)
	if len(rows) != 0 {
		t.Errorf("len(rows) = %d, want 0", len(rows))
	}
}

// ── writeCampaignExportCSV ───────────────────────────────────────────────

// parseExportCSV is a small test helper: strips a leading UTF-8 BOM (if
// present — writeCampaignExportCSV's caller, Export, always adds one; some
// tests below write directly to a csv.Writer without it) and parses the
// remainder with encoding/csv, mirroring how a spreadsheet or downstream
// script would actually read the file.
func parseExportCSV(t *testing.T, body []byte) [][]string {
	t.Helper()
	body = bytes.TrimPrefix(body, utf8BOM)
	records, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll: %v", err)
	}
	return records
}

func TestWriteCampaignExportCSV_HeaderAndRowsRoundTrip(t *testing.T) {
	created := time.Date(2026, 7, 4, 9, 30, 0, 0, time.UTC)
	rows := []campaignExportRow{
		{
			Key: "abc123", ShortURL: "https://go.sstools.co/u/abc123",
			Title: "Summer, \"Big\" Sale", DestinationURL: "https://example.com/?a=1&b=2",
			UTMSource: "email", UTMMedium: "newsletter", UTMContent: "cta-button",
			Placement: "Main St, 5th Ave", ClicksInWindow: 42, SharePct: 84, CreatedAt: created,
		},
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, rows); err != nil {
		t.Fatalf("writeCampaignExportCSV: %v", err)
	}

	records := parseExportCSV(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2 (header + 1 row)", len(records))
	}

	// LITERAL expected header (review finding B2 — comparing against
	// campaignExportCSVHeader itself, the SAME variable the writer wrote
	// from, pins nothing: renaming that variable's own contents changes
	// both sides of the comparison together and the test stays green).
	// Renaming "share_of_campaign_total_pct" -> "share_pct" in
	// campaignExportCSVHeader failed 0 tests before this literal existed.
	wantHeader := []string{
		"key", "short_url", "title", "destination_url",
		"utm_source", "utm_medium", "utm_content", "placement",
		"clicks_in_window", "share_of_listed_links_pct", "created_at",
	}
	if got := records[0]; len(got) != len(wantHeader) {
		t.Fatalf("header = %v, want %v", got, wantHeader)
	} else {
		for i := range wantHeader {
			if got[i] != wantHeader[i] {
				t.Errorf("header[%d] = %q, want %q (full header %v)", i, got[i], wantHeader[i], got)
			}
		}
	}

	got := records[1]
	want := []string{
		"abc123", "https://go.sstools.co/u/abc123",
		`Summer, "Big" Sale`, "https://example.com/?a=1&b=2",
		"email", "newsletter", "cta-button", "Main St, 5th Ave",
		"42", "84", "2026-07-04T09:30:00Z",
	}
	if len(got) != len(want) {
		t.Fatalf("row has %d fields, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d (%s) = %q, want %q", i, campaignExportCSVHeader[i], got[i], want[i])
		}
	}
}

// TestWriteCampaignExportCSV_FormulaTriggerFieldsAreNeutralized asserts the
// CSV body ACTUALLY WRITTEN carries the neutralized (quote-prefixed) value
// — not just that the helper function returns the right string in isolation
// (TestNeutralizeFormulaField... above). This is the mutation-sensitive
// version: removing the neutralizeFormulaField calls from
// writeCampaignExportCSV (leaving the helper itself untouched and its own
// unit test still green) fails THIS test.
//
// ALL 8 text columns, not just title/placement (review finding "also fix
// 1"): an earlier version of this test only set/checked Title and
// Placement, so dropping the neutralizeFormulaField call on any OTHER
// column — destination_url and utm_source explicitly, since the issue AC
// names "the destination URL and title" as the columns that are not always
// self-authored — failed 0 tests. key/short_url are included too even
// though a real generated key/URL can never trigger this in practice (see
// neutralizeFormulaField's doc comment) — the point is pinning that
// writeCampaignExportCSV calls the helper on every string column
// UNCONDITIONALLY, not that every column is independently at risk.
func TestWriteCampaignExportCSV_FormulaTriggerFieldsAreNeutralized(t *testing.T) {
	rows := []campaignExportRow{
		{
			Key:            "=key",
			ShortURL:       "+shorturl",
			Title:          "=cmd|'/c calc'!A1",
			DestinationURL: "-evil.example.com",
			UTMSource:      "@source",
			UTMMedium:      "=medium",
			UTMContent:     "+content",
			Placement:      "@import evil",
			ClicksInWindow: 1, SharePct: 100,
		},
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, rows); err != nil {
		t.Fatalf("writeCampaignExportCSV: %v", err)
	}
	records := parseExportCSV(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	row := records[1]
	wantPrefixes := []struct {
		col    int
		name   string
		prefix string
	}{
		{0, "key", "'="},
		{1, "short_url", "'+"},
		{2, "title", "'="},
		{3, "destination_url", "'-"},
		{4, "utm_source", "'@"},
		{5, "utm_medium", "'="},
		{6, "utm_content", "'+"},
		{7, "placement", "'@"},
	}
	for _, wp := range wantPrefixes {
		if !strings.HasPrefix(row[wp.col], wp.prefix) {
			t.Errorf("%s = %q, want a leading single-quote before its trigger character (prefix %q)", wp.name, row[wp.col], wp.prefix)
		}
	}
}

// TestWriteCampaignExportCSV_CreatedAtNormalizedToUTC pins the .UTC() call
// on CreatedAt before formatting (review finding "also fix 3"). Every
// fixture elsewhere in this file already constructs CreatedAt in UTC, so
// dropping .UTC() failed 0 tests before this one existed — it works live
// only because the database happens to hand back UTC-friendly values in
// practice, and #0103 already lost a full review round to exactly this
// class of "works by accident, breaks under a non-UTC Location" date bug.
// A time constructed in a FIXED non-UTC zone must still format with a "Z"
// UTC suffix, not its own "-08:00" offset.
func TestWriteCampaignExportCSV_CreatedAtNormalizedToUTC(t *testing.T) {
	pst := time.FixedZone("PST", -8*60*60)
	// 2026-07-04T01:30:00-08:00 is the same instant as 2026-07-04T09:30:00Z.
	created := time.Date(2026, 7, 4, 1, 30, 0, 0, pst)
	rows := []campaignExportRow{
		{Key: "k1", ShortURL: "https://go.sstools.co/u/k1", ClicksInWindow: 1, SharePct: 100, CreatedAt: created},
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, rows); err != nil {
		t.Fatalf("writeCampaignExportCSV: %v", err)
	}
	records := parseExportCSV(t, buf.Bytes())
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2", len(records))
	}
	got := records[1][10]
	want := "2026-07-04T09:30:00Z"
	if got != want {
		t.Errorf("created_at = %q, want %q (normalized to UTC, not the input's -08:00 offset)", got, want)
	}
}

// TestWriteCampaignExportCSV_EmptyRowsWritesHeaderOnly is the CSV-writer
// layer's half of "an empty campaign exports headers and no rows" (AC).
func TestWriteCampaignExportCSV_EmptyRowsWritesHeaderOnly(t *testing.T) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, nil); err != nil {
		t.Fatalf("writeCampaignExportCSV: %v", err)
	}
	records := parseExportCSV(t, buf.Bytes())
	if len(records) != 1 {
		t.Fatalf("len(records) = %d, want 1 (header only)", len(records))
	}
}

// TestWriteCampaignExportCSV_UTF8FieldsRoundTrip covers the AC "UTF-8 is
// handled correctly for non-ASCII titles and placements" at the writer
// layer directly (bypassing HTTP) — the handler-level equivalent lives in
// TestCampaignsExport_NonASCIIFieldsRoundTripAsUTF8.
func TestWriteCampaignExportCSV_UTF8FieldsRoundTrip(t *testing.T) {
	rows := []campaignExportRow{
		{
			Key: "u1", ShortURL: "https://go.sstools.co/u/u1",
			Title: "日本語のタイトル", DestinationURL: "https://example.com",
			Placement: "café — 5ème étage", ClicksInWindow: 3, SharePct: 100,
		},
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := writeCampaignExportCSV(cw, rows); err != nil {
		t.Fatalf("writeCampaignExportCSV: %v", err)
	}
	records := parseExportCSV(t, buf.Bytes())
	if records[1][2] != "日本語のタイトル" {
		t.Errorf("title = %q, want 日本語のタイトル", records[1][2])
	}
	if records[1][7] != "café — 5ème étage" {
		t.Errorf("placement = %q, want café — 5ème étage", records[1][7])
	}
}

// ── campaignExportFilename ───────────────────────────────────────────────

// TestCampaignExportFilename_IdentifiesCampaignAndWindow asserts the
// INCLUSIVE end (review finding B5): windowTo is the raw EXCLUSIVE bound
// (CampaignStats.WindowTo, half-open [from, to)), so the filename's end
// date is one day BEFORE it, matching windowLabel's own caption conversion
// (web/src/lib/campaigns.ts). An earlier version of this function used
// windowTo verbatim, so this test would have wanted
// "..._to_2026-08-01.csv" (the exclusive bound) instead of
// "..._to_2026-07-31.csv" (the inclusive end) — that earlier behavior
// disagreed with the on-screen caption for the identical window.
func TestCampaignExportFilename_IdentifiesCampaignAndWindow(t *testing.T) {
	got := campaignExportFilename("summer-promo", "2026-07-01", "2026-08-01")
	want := "summer-promo-export_2026-07-01_to_2026-07-31.csv"
	if got != want {
		t.Errorf("campaignExportFilename = %q, want %q", got, want)
	}
}

// TestCampaignExportFilename_MatchesScreenCaptionInclusiveEnd reproduces
// the exact live example from review finding B5: a campaign window whose
// raw bounds are window_from=2026-07-09, window_to=2026-08-08 (exclusive)
// rendered on screen as the caption "Jul 9, 2026 – Aug 7, 2026"
// (windowLabel). The filename must name the SAME last day, Aug 7, not
// Aug 8.
func TestCampaignExportFilename_MatchesScreenCaptionInclusiveEnd(t *testing.T) {
	got := campaignExportFilename("promo", "2026-07-09", "2026-08-08")
	want := "promo-export_2026-07-09_to_2026-08-07.csv"
	if got != want {
		t.Errorf("campaignExportFilename = %q, want %q (Aug 7, matching the screen caption's inclusive end, not Aug 8)", got, want)
	}
}

// TestCampaignExportFilename_ZeroDayWindowKeepsSameDateBothEnds covers the
// zero-day mirror case B5 calls out: window_from == window_to (a same-day
// campaign, #0104 downstream constraint 1). Naively subtracting a day would
// land BEFORE window_from — the exact backwards-range trap windowLabel's
// own doc comment documents for the caption ("Aug 1 (no days in window)",
// never "Aug 1 – Jul 31"). The filename falls back to window_from for the
// end instead, so both ends read the same date.
func TestCampaignExportFilename_ZeroDayWindowKeepsSameDateBothEnds(t *testing.T) {
	got := campaignExportFilename("event", "2026-08-01", "2026-08-01")
	want := "event-export_2026-08-01_to_2026-08-01.csv"
	if got != want {
		t.Errorf("campaignExportFilename = %q, want %q (same date both ends, not a backwards range)", got, want)
	}
}

func TestCampaignExportFilename_DiffersAcrossWindows(t *testing.T) {
	a := campaignExportFilename("slug", "2026-07-01", "2026-08-01")
	b := campaignExportFilename("slug", "2026-08-01", "2026-09-01")
	if a == b {
		t.Errorf("filenames for two different windows of the same campaign must differ, both = %q", a)
	}
}

// TestSubtractOneUTCDate pins subtractOneUTCDate directly: the Go-side
// twin of web/src/lib/campaigns.ts's subtractOneDayUTC that
// campaignExportFilename relies on for its inclusive-end conversion.
func TestSubtractOneUTCDate(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ordinary date", "2026-08-08", "2026-08-07"},
		{"crosses a month boundary", "2026-08-01", "2026-07-31"},
		{"crosses a year boundary", "2026-01-01", "2025-12-31"},
		{"malformed input returns empty", "not-a-date", ""},
		{"empty input returns empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := subtractOneUTCDate(tc.in); got != tc.want {
				t.Errorf("subtractOneUTCDate(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ── Export handler (DB-backed, mirrors qr_test.go's QRZip test shape) ────

// campaignsMuxNoStats is campaignsMux with a nil stats provider, for
// TestCampaignsExport_NoStatsProviderReturns500 — the same nil-degradation
// contract Stats/Get document (campaignStatsProvider's doc comment) but
// exercised directly for Export, which has no meaningful degraded response.
func campaignsMuxNoStats(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	h := NewCampaignsHandler(campaigns.NewStore(pool), links.NewStore(pool), nil, nil, nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/campaigns", requireSession(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/campaigns/{slug}/export.csv", requireSession(http.HandlerFunc(h.Export)))
	return mux
}

// readAll reads and returns resp's full body, closing nothing (callers
// already defer resp.Body.Close()). itoa (int64 → string) is already
// defined package-wide in url_filters_test.go and reused as-is below.
func readAll(resp *http.Response) ([]byte, error) {
	return io.ReadAll(resp.Body)
}

func TestCampaignsExport_OwnershipEnforced(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	bob := seedUser(t, pool, "bob@example.com")
	seedSession(t, pool, alice, "alice-token")
	seedSession(t, pool, bob, "bob-token")
	bobCampaign := createCampaign(t, srv, "bob-token", `{"name":"Bob Campaign"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+bobCampaign.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (alice must not export bob's campaign)", resp.StatusCode)
	}
}

func TestCampaignsExport_Unauthenticated401(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/campaigns/anything/export.csv")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestCampaignsExport_EmptyCampaignHasHeaderOnly(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Empty Export"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty campaign is not a 404)", resp.StatusCode)
	}
	body, err := readAll(resp)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("body is empty, want at least a header row")
	}
	records := parseExportCSV(t, body)
	if len(records) != 1 {
		t.Fatalf("records = %v, want exactly 1 (header only, no data rows)", records)
	}
}

func TestCampaignsExport_ContentTypeAndDisposition(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Headers Test"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != csvExportContentType {
		t.Errorf("Content-Type = %q, want %q", ct, csvExportContentType)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, c.Slug) || !strings.HasSuffix(strings.TrimSuffix(cd, `"`), ".csv") {
		t.Errorf("Content-Disposition = %q, want attachment with the slug and a .csv filename", cd)
	}
}

// TestCampaignsExport_BodyCarriesUTF8BOM pins the three-byte UTF-8 BOM
// (utf8BOM) at the front of the raw response body, AND demonstrates the
// real cost documented on utf8BOM (review finding B4) rather than only
// asserting the bytes are present: reading the body with encoding/csv
// WITHOUT stripping the BOM first (unlike parseExportCSV, which strips it
// by hand) yields a header field of "\ufeffkey", not "key" — the exact
// failure a naive downstream parser would hit. Deleting
// `buf.Write(utf8BOM)` from Export failed 0 other tests in this file
// before this one existed.
func TestCampaignsExport_BodyCarriesUTF8BOM(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"BOM Test"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) < 3 || !bytes.Equal(body[:3], utf8BOM) {
		got := body
		if len(got) > 3 {
			got = got[:3]
		}
		t.Fatalf("body does not start with the UTF-8 BOM: first bytes %v, want %v", got, utf8BOM)
	}

	// The cost: parsing WITHOUT stripping the BOM first (parseExportCSV
	// strips it; this deliberately does not) reads the header's first
	// field with the BOM still attached.
	rawRecords, err := csv.NewReader(bytes.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll (BOM not stripped): %v", err)
	}
	if len(rawRecords) == 0 || len(rawRecords[0]) == 0 {
		t.Fatalf("no header record parsed")
	}
	if rawRecords[0][0] == "key" {
		t.Errorf("unstripped header[0] unexpectedly equals bare \"key\" — the BOM cost claim on utf8BOM's doc comment is stale, update it")
	}
	if want := "\ufeffkey"; rawRecords[0][0] != want {
		t.Errorf("unstripped header[0] = %q, want %q (the BOM-prefixed field a naive parser actually sees)", rawRecords[0][0], want)
	}
}

// TestCampaignsExport_ReconcilesWithDetailView is the AC's central claim,
// verified directly: for the SAME window, GET .../stats's by_link (what the
// on-screen table's "clicks in window"/"Share of listed links" columns are
// built from — web/src/lib/campaigns.ts's buildLinkRows) must equal the
// CSV's clicks_in_window/share_of_listed_links_pct, row for row.
func TestCampaignsExport_ReconcilesWithDetailView(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Reconcile"}`)
	campID := campaignRowID(t, pool, c.Slug)

	linkA := seedLinkWithCampaign(t, pool, alice, "reca0001", "https://a.example.com", &campID)
	linkB := seedLinkWithCampaign(t, pool, alice, "recb0002", "https://b.example.com", &campID)
	for i := 0; i < 3; i++ {
		seedClickForHandler(t, pool, linkA, campID, false)
	}
	seedClickForHandler(t, pool, linkB, campID, false)
	seedClickForHandler(t, pool, linkA, campID, true) // bot, excluded from both sides

	// GET .../stats — the same source the on-screen links table reads.
	statsReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/stats", nil)
	statsResp, err := srv.Client().Do(withCookie(statsReq, "alice-token"))
	if err != nil {
		t.Fatalf("stats request: %v", err)
	}
	defer statsResp.Body.Close()
	var statsBody struct {
		ByLink []struct {
			Key   string `json:"key"`
			Count int64  `json:"count"`
		} `json:"by_link"`
	}
	if err := json.NewDecoder(statsResp.Body).Decode(&statsBody); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	screenCounts := map[string]int64{}
	var screenTotal int64
	for _, b := range statsBody.ByLink {
		screenCounts[b.Key] = b.Count
		screenTotal += b.Count
	}

	// GET .../export.csv — must reconcile with the above.
	expReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	expResp, err := srv.Client().Do(withCookie(expReq, "alice-token"))
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer expResp.Body.Close()
	body, err := readAll(expResp)
	if err != nil {
		t.Fatalf("read export body: %v", err)
	}
	records := parseExportCSV(t, body)
	if len(records) != 3 { // header + 2 links
		t.Fatalf("records = %v, want header + 2 rows", records)
	}
	for _, row := range records[1:] {
		key := row[0]
		gotClicks := row[8]
		gotShare := row[9]
		wantClicks := screenCounts[key]
		wantShare := int64(0)
		if screenTotal > 0 {
			wantShare = (wantClicks*100 + screenTotal/2) / screenTotal // matches Math.round for these non-negative values
		}
		if gotClicks != itoa(wantClicks) {
			t.Errorf("row %s clicks_in_window = %s, want %d (matching by_link)", key, gotClicks, wantClicks)
		}
		if gotShare != itoa(wantShare) {
			t.Errorf("row %s share = %s, want %d", key, gotShare, wantShare)
		}
	}
}

// TestCampaignsExport_EmptyWindowStillListsLinks covers the empty-window
// decision (#0104 downstream constraint 1): a campaign whose starts_at
// equals its ends_at resolves to a zero-day [X, X) window. The export must
// still list every currently-assigned link, each with 0 clicks/0% share —
// not an empty file.
func TestCampaignsExport_EmptyWindowStillListsLinks(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"Zero Day"}`)
	campID := campaignRowID(t, pool, c.Slug)
	seedLinkWithCampaign(t, pool, alice, "zeroday1", "https://example.com", &campID)

	sameDay := time.Now().UTC().Format("2006-01-02")
	url := srv.URL + "/api/campaigns/" + c.Slug + "/export.csv?from=" + sameDay + "&to=" + sameDay
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := readAll(resp)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	records := parseExportCSV(t, body)
	if len(records) != 2 { // header + the one link, even though the window is empty
		t.Fatalf("records = %v, want header + 1 row (link still listed, zero clicks)", records)
	}
	if records[1][0] != "zeroday1" {
		t.Errorf("row key = %q, want zeroday1", records[1][0])
	}
	if records[1][8] != "0" || records[1][9] != "0" {
		t.Errorf("clicks/share = %s/%s, want 0/0 for a zero-day window", records[1][8], records[1][9])
	}
}

// TestCampaignsExport_NonASCIIFieldsRoundTripAsUTF8 seeds a title and
// placement carrying non-ASCII characters directly, end to end through the
// HTTP response, not just through writeCampaignExportCSV in isolation.
func TestCampaignsExport_NonASCIIFieldsRoundTripAsUTF8(t *testing.T) {
	pool := credsTestPool(t)
	srv := httptest.NewServer(campaignsMux(t, pool))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"UTF8"}`)
	campID := campaignRowID(t, pool, c.Slug)

	if _, err := pool.Exec(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at, campaign_id, title, placement)
		 VALUES ($1, 'utf8key1', 'https://example.com', TRUE, 0, now(), $2, $3, $4)`,
		alice, campID, "日本語のタイトル", "café — 5ème étage",
	); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, err := readAll(resp)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	records := parseExportCSV(t, body)
	if len(records) != 2 {
		t.Fatalf("records = %v, want header + 1 row", records)
	}
	if records[1][2] != "日本語のタイトル" {
		t.Errorf("title = %q, want 日本語のタイトル", records[1][2])
	}
	if records[1][7] != "café — 5ème étage" {
		t.Errorf("placement = %q, want café — 5ème étage", records[1][7])
	}
}

// TestCampaignsExport_NoStatsProviderReturns500 mirrors
// TestCampaignsStats_* coverage of the same nil-provider degradation
// (campaignStatsProvider's doc comment): Export has no meaningful degraded
// response, so it 500s rather than silently omitting the per-link numbers.
func TestCampaignsExport_NoStatsProviderReturns500(t *testing.T) {
	pool := credsTestPool(t)
	authStoreMux := campaignsMuxNoStats(t, pool)
	srv := httptest.NewServer(authStoreMux)
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")
	c := createCampaign(t, srv, "alice-token", `{"name":"No Stats"}`)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/campaigns/"+c.Slug+"/export.csv", nil)
	resp, err := srv.Client().Do(withCookie(req, "alice-token"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}
