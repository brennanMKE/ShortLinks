package clicks

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL or skips, truncating the clicks/links/
// users tables before and after so each run starts and leaves clean.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}

	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})
	return pool
}

func truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`TRUNCATE clicks, links, campaigns, users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Repair a burst that was hard-killed. The concurrency tests disable
	// autovacuum on clicks for the duration of a write burst and RESET it in
	// their cleanup, but SIGKILL / a -timeout panic runs no deferred code, so
	// the table can be left carrying {autovacuum_enabled=false}. That is a
	// pg_dump-visible reloption — i.e. the #0110 drift — and it persists until
	// something clears it. Doing it here means ANY subsequent run of the
	// package repairs it, not only one that reaches the concurrency tests.
	if _, err := pool.Exec(ctx, `ALTER TABLE clicks RESET (autovacuum_enabled)`); err != nil {
		t.Fatalf("truncate: reset autovacuum reloption on clicks: %v", err)
	}
}

// seedUser inserts an active account and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, is_admin, active, created_at)
		 VALUES ($1, FALSE, TRUE, now()) RETURNING id`, email,
	).Scan(&id); err != nil {
		t.Fatalf("seed user %s: %v", email, err)
	}
	return id
}

// seedLink inserts an active, non-denied link and returns its id.
func seedLink(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at)
		 VALUES ($1, $2, $3, TRUE, 0, now()) RETURNING id`,
		userID, key, dest,
	).Scan(&id); err != nil {
		t.Fatalf("seed link %q: %v", key, err)
	}
	return id
}

// seedCampaign inserts a minimal campaign owned by userID and returns its id.
func seedCampaign(t *testing.T, pool *pgxpool.Pool, userID int64, name, slug string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO campaigns (user_id, name, slug, archived, created_at, updated_at)
		 VALUES ($1, $2, $3, FALSE, now(), now()) RETURNING id`,
		userID, name, slug,
	).Scan(&id); err != nil {
		t.Fatalf("seed campaign %q: %v", slug, err)
	}
	return id
}

// seededLinkUTM is the stored discrete UTM (#0099) input to seedLinkWithUTM.
// Empty fields store SQL NULL, mirroring links.Store's nullIfEmpty convention.
type seededLinkUTM struct {
	Source, Medium, Campaign, Term, Content string
}

// seedLinkWithUTM inserts an active, non-denied link carrying stored discrete
// UTM columns (#0099) and, when campaignID is non-nil, a campaign assignment
// (#0098/#0099). It backs the #0100 fallback/denormalization tests, which need
// a link with real stored values to fall back to and/or a real campaign to
// resolve.
func seedLinkWithUTM(t *testing.T, pool *pgxpool.Pool, userID int64, key, dest string, campaignID *int64, utm seededLinkUTM) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO links (user_id, key, destination_url, active, denied_reason, created_at,
		                     campaign_id, utm_source, utm_medium, utm_campaign, utm_term, utm_content)
		 VALUES ($1, $2, $3, TRUE, 0, now(), $4, $5, $6, $7, $8, $9)
		 RETURNING id`,
		userID, key, dest, campaignID,
		nullIfEmptyTest(utm.Source), nullIfEmptyTest(utm.Medium), nullIfEmptyTest(utm.Campaign),
		nullIfEmptyTest(utm.Term), nullIfEmptyTest(utm.Content),
	).Scan(&id); err != nil {
		t.Fatalf("seed link with UTM %q: %v", key, err)
	}
	return id
}

// nullIfEmptyTest mirrors links.Store's unexported nullIfEmpty for this test's
// own direct INSERTs (this package cannot import the unexported helper from
// internal/links).
func nullIfEmptyTest(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestRecorder_PersistsClick records one click and reads it back, asserting the
// link_id, metadata, and all five utm_* values round-trip exactly.
func TestRecorder_PersistsClick(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "alice@example.com")
	linkID := seedLink(t, pool, uid, "abc123", "https://example.com/page")

	when := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := Click{
		Key:         "abc123",
		ClickedAt:   when,
		IPAddress:   "203.0.113.7",
		UserAgent:   "test-agent",
		Referer:     "https://referrer.example",
		UTMSource:   "news",
		UTMMedium:   "email",
		UTMCampaign: "launch",
		UTMTerm:     "go",
		UTMContent:  "hero",
	}
	if err := rec.Record(context.Background(), c); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var (
		gotLinkID                                  int64
		gotIP, ua, ref, src, med, camp, term, cont string
		clickedAt                                  time.Time
	)
	if err := pool.QueryRow(context.Background(),
		`SELECT link_id, clicked_at, host(ip_address), user_agent, referer,
		        utm_source, utm_medium, utm_campaign, utm_term, utm_content
		   FROM clicks WHERE link_id = $1`, linkID,
	).Scan(&gotLinkID, &clickedAt, &gotIP, &ua, &ref, &src, &med, &camp, &term, &cont); err != nil {
		t.Fatalf("read back click: %v", err)
	}

	if gotLinkID != linkID {
		t.Errorf("link_id = %d, want %d", gotLinkID, linkID)
	}
	if !clickedAt.Equal(when) {
		t.Errorf("clicked_at = %v, want %v", clickedAt, when)
	}
	if gotIP != "203.0.113.7" {
		t.Errorf("ip_address = %q, want 203.0.113.7", gotIP)
	}
	if ua != "test-agent" || ref != "https://referrer.example" {
		t.Errorf("metadata mismatch: ua=%q ref=%q", ua, ref)
	}
	if src != "news" || med != "email" || camp != "launch" || term != "go" || cont != "hero" {
		t.Errorf("utm mismatch: src=%q med=%q camp=%q term=%q cont=%q", src, med, camp, term, cont)
	}
}

// TestRecorder_UnknownKeyInsertsNothing asserts that recording a click for a key
// that does not exist inserts zero rows (and does not error), so the redirect
// path's best-effort recording never fails on a bogus key.
func TestRecorder_UnknownKeyInsertsNothing(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	if err := rec.Record(context.Background(), Click{Key: "nope42"}); err != nil {
		t.Fatalf("Record unknown key: %v", err)
	}
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM clicks`).Scan(&n); err != nil {
		t.Fatalf("count clicks: %v", err)
	}
	if n != 0 {
		t.Errorf("clicks rows = %d, want 0 for unknown key", n)
	}
}

// TestRecorder_EmptyUTMStoredAsNull asserts empty-string metadata folds to SQL
// NULL so the analytics "(none)" bucket is driven by genuine absence.
func TestRecorder_EmptyUTMStoredAsNull(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "bob@example.com")
	seedLink(t, pool, uid, "bare01", "https://example.com")

	if err := rec.Record(context.Background(), Click{Key: "bare01"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var srcNull, ipNull bool
	if err := pool.QueryRow(context.Background(),
		`SELECT utm_source IS NULL, ip_address IS NULL FROM clicks WHERE link_id IN
		   (SELECT id FROM links WHERE key = 'bare01')`,
	).Scan(&srcNull, &ipNull); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !srcNull {
		t.Error("utm_source should be NULL when empty")
	}
	if !ipNull {
		t.Error("ip_address should be NULL when empty")
	}
}

// clickRow is the subset of a clicks row the #0100 fallback/campaign tests
// below read back. Empty string fields mean the underlying column was NULL.
type clickRow struct {
	Source, Medium, Campaign, Term, Content string
	CampaignID                              *int64
	IsBot                                   bool
}

// readClick reads back the single clicks row recorded for the link identified
// by key. Fails the test outright if there is not exactly one such row, so a
// bug that inserts zero or duplicate rows fails loudly here rather than being
// silently masked by Scan reading an arbitrary row.
func readClick(t *testing.T, pool *pgxpool.Pool, key string) clickRow {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM clicks c JOIN links l ON l.id = c.link_id WHERE l.key = $1`, key,
	).Scan(&n); err != nil {
		t.Fatalf("count clicks for key %q: %v", key, err)
	}
	if n != 1 {
		t.Fatalf("clicks rows for key %q = %d, want exactly 1", key, n)
	}

	var src, med, camp, term, cont *string
	var campID *int64
	var isBot bool
	if err := pool.QueryRow(context.Background(),
		`SELECT c.utm_source, c.utm_medium, c.utm_campaign, c.utm_term, c.utm_content, c.campaign_id, c.is_bot
		   FROM clicks c JOIN links l ON l.id = c.link_id
		  WHERE l.key = $1`, key,
	).Scan(&src, &med, &camp, &term, &cont, &campID, &isBot); err != nil {
		t.Fatalf("read back click for key %q: %v", key, err)
	}
	row := clickRow{CampaignID: campID, IsBot: isBot}
	if src != nil {
		row.Source = *src
	}
	if med != nil {
		row.Medium = *med
	}
	if camp != nil {
		row.Campaign = *camp
	}
	if term != nil {
		row.Term = *term
	}
	if cont != nil {
		row.Content = *cont
	}
	return row
}

// TestRecorder_FallsBackToLinkStoredUTMWhenAllInboundOmitted asserts the core
// #0100 fallback: a click on a bare short URL (no inbound utm_* at all)
// records the link's own stored discrete UTM values (#0099), not NULL. Every
// one of the five keys falls back here, mutation-checked by the mixed-case
// test below which requires per-key (not all-or-nothing) resolution.
func TestRecorder_FallsBackToLinkStoredUTMWhenAllInboundOmitted(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "fallback@example.com")
	seedLinkWithUTM(t, pool, uid, "fb0001", "https://example.com", nil, seededLinkUTM{
		Source: "link-src", Medium: "link-med", Campaign: "link-camp", Term: "link-term", Content: "link-content",
	})

	if err := rec.Record(context.Background(), Click{Key: "fb0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "fb0001")
	want := clickRow{Source: "link-src", Medium: "link-med", Campaign: "link-camp", Term: "link-term", Content: "link-content"}
	if got.Source != want.Source || got.Medium != want.Medium || got.Campaign != want.Campaign ||
		got.Term != want.Term || got.Content != want.Content {
		t.Errorf("fallback UTM = %+v, want %+v", got, want)
	}
	// This link was seeded with campaignID=nil (no campaign membership at
	// all), distinct from TestRecorder_DeletingCampaignNullsClickCampaignIDWithoutDeletingRow's
	// "had a campaign, then it was deleted" NULL — this asserts the plain
	// campaign-less case records NULL too, not some other sentinel.
	if got.CampaignID != nil {
		t.Errorf("campaign_id = %v, want nil for a link with no campaign", *got.CampaignID)
	}
}

// TestRecorder_InboundUTMOverridesLinkStoredUTM asserts the documented
// override behavior is preserved, not silently inverted by the #0100
// fallback: an inbound utm_source WINS over the link's own stored value.
func TestRecorder_InboundUTMOverridesLinkStoredUTM(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "override@example.com")
	seedLinkWithUTM(t, pool, uid, "ov0001", "https://example.com", nil, seededLinkUTM{
		Source: "link-src",
	})

	if err := rec.Record(context.Background(), Click{Key: "ov0001", UTMSource: "inbound-src"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "ov0001")
	if got.Source != "inbound-src" {
		t.Errorf("utm_source = %q, want inbound value %q (inbound must win over the link's stored %q)",
			got.Source, "inbound-src", "link-src")
	}
}

// TestRecorder_MixedInboundAndStoredUTMResolveIndependently is the mixed-case
// acceptance criterion: inbound supplies SOME keys, the link supplies the
// rest, and each key must resolve independently. This is exactly what a naive
// all-or-nothing fallback (e.g. "use inbound if ANY utm_* key is present,
// else use the link entirely") gets wrong — such a bug would either leak the
// link's medium/campaign/content as NULL (because some inbound value was
// present) or clobber the genuinely-supplied inbound source/term with the
// link's stored values (because not every key was supplied).
func TestRecorder_MixedInboundAndStoredUTMResolveIndependently(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "mixed@example.com")
	seedLinkWithUTM(t, pool, uid, "mix0001", "https://example.com", nil, seededLinkUTM{
		Source: "link-src", Medium: "link-med", Campaign: "link-camp", Term: "link-term", Content: "link-content",
	})

	// Inbound supplies only source and term; medium/campaign/content are
	// left blank (absent from the short URL's query string).
	c := Click{
		Key:       "mix0001",
		UTMSource: "inbound-src",
		UTMTerm:   "inbound-term",
	}
	if err := rec.Record(context.Background(), c); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "mix0001")
	want := clickRow{
		Source:   "inbound-src",  // inbound wins
		Medium:   "link-med",     // falls back
		Campaign: "link-camp",    // falls back
		Term:     "inbound-term", // inbound wins
		Content:  "link-content", // falls back
	}
	if got != (clickRow{Source: want.Source, Medium: want.Medium, Campaign: want.Campaign, Term: want.Term, Content: want.Content}) {
		t.Errorf("mixed fallback = %+v, want %+v", got, want)
	}
}

// TestRecorder_EmptyStringInboundTreatedAsAbsentNotLiteralOverride asserts
// that an inbound utm_source of "" (as opposed to the key being genuinely
// supplied with a non-empty value) is treated as ABSENT and falls back to the
// link's stored value — never written as a literal empty-string override.
// Unlike TestRecorder_EmptyUTMStoredAsNull (which uses a link with NO stored
// UTM, so an all-NULL outcome is ambiguous between "correctly fell back to
// nothing" and "incorrectly stored the empty string as NULL"), this link DOES
// have a stored value, so a bug that skips the fallback and stores the
// literal "" would surface as a mismatch here instead of passing vacuously.
func TestRecorder_EmptyStringInboundTreatedAsAbsentNotLiteralOverride(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "empty-absent@example.com")
	seedLinkWithUTM(t, pool, uid, "ea0001", "https://example.com", nil, seededLinkUTM{
		Source: "stored-value",
	})

	if err := rec.Record(context.Background(), Click{Key: "ea0001", UTMSource: ""}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "ea0001")
	if got.Source != "stored-value" {
		t.Errorf("utm_source = %q, want fallback to link's stored %q (empty inbound must not override)",
			got.Source, "stored-value")
	}
}

// TestRecorder_StoresLinkCampaignIDAtRecordTime asserts a click on a link
// assigned to a campaign records that campaign's id.
func TestRecorder_StoresLinkCampaignIDAtRecordTime(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "camp-record@example.com")
	campID := seedCampaign(t, pool, uid, "Launch", "launch")
	seedLinkWithUTM(t, pool, uid, "cr0001", "https://example.com", &campID, seededLinkUTM{})

	if err := rec.Record(context.Background(), Click{Key: "cr0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "cr0001")
	if got.CampaignID == nil || *got.CampaignID != campID {
		t.Errorf("campaign_id = %v, want %d", got.CampaignID, campID)
	}
}

// TestRecorder_ClickCampaignIDSurvivesLaterLinkReassignment is the direct
// assertion of #0100's whole reason for denormalizing campaign_id onto
// clicks: a click recorded while a link belonged to Campaign A must keep
// reporting Campaign A even after the link is later reassigned to Campaign B.
// A join-at-query-time implementation (clicks -> links -> campaigns) would
// silently rewrite this click to Campaign B; this test would catch that.
func TestRecorder_ClickCampaignIDSurvivesLaterLinkReassignment(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "reassign@example.com")
	campA := seedCampaign(t, pool, uid, "Campaign A", "campaign-a")
	campB := seedCampaign(t, pool, uid, "Campaign B", "campaign-b")
	linkID := seedLinkWithUTM(t, pool, uid, "ra0001", "https://example.com", &campA, seededLinkUTM{})

	if err := rec.Record(context.Background(), Click{Key: "ra0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	before := readClick(t, pool, "ra0001")
	if before.CampaignID == nil || *before.CampaignID != campA {
		t.Fatalf("precondition failed: campaign_id = %v, want %d (campaign A)", before.CampaignID, campA)
	}

	// Reassign the link to Campaign B, exactly as
	// campaigns.Store.AssignLinkToCampaign would.
	if _, err := pool.Exec(context.Background(),
		`UPDATE links SET campaign_id = $1 WHERE id = $2`, campB, linkID,
	); err != nil {
		t.Fatalf("reassign link: %v", err)
	}

	after := readClick(t, pool, "ra0001")
	if after.CampaignID == nil || *after.CampaignID != campA {
		t.Errorf("campaign_id after reassignment = %v, want unchanged %d (campaign A) — "+
			"the historical click must not silently follow the link to campaign B", after.CampaignID, campA)
	}
}

// TestRecorder_DeletingCampaignNullsClickCampaignIDWithoutDeletingRow asserts
// the migration's ON DELETE SET NULL: deleting a campaign clears campaign_id
// on its historical clicks but deletes no click row (mirroring links'
// existing ON DELETE SET NULL from migration 000011, and clicks.link_id's
// non-cascading FK from migration 000003 — click history outlives the
// entities it references).
func TestRecorder_DeletingCampaignNullsClickCampaignIDWithoutDeletingRow(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "camp-delete@example.com")
	campID := seedCampaign(t, pool, uid, "Doomed", "doomed")
	seedLinkWithUTM(t, pool, uid, "cd0001", "https://example.com", &campID, seededLinkUTM{})

	if err := rec.Record(context.Background(), Click{Key: "cd0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if _, err := pool.Exec(context.Background(), `DELETE FROM campaigns WHERE id = $1`, campID); err != nil {
		t.Fatalf("delete campaign: %v", err)
	}

	got := readClick(t, pool, "cd0001") // readClick itself asserts exactly one row still exists
	if got.CampaignID != nil {
		t.Errorf("campaign_id after campaign deletion = %v, want nil (SET NULL)", *got.CampaignID)
	}
}

// TestRecorder_ClassifiesBotUserAgentAndStillRecordsRow is the #0101
// end-to-end assertion at the recorder layer: a click whose User-Agent
// matches BotUserAgentSubstrings is written with is_bot = TRUE, and — the
// issue's explicit requirement — the row is still written at all. A bug that
// dropped bot clicks instead of flagging them would pass a naive "is_bot is
// true" check vacuously (no row to read), so this also asserts exactly one
// row exists via readClick's built-in count check.
func TestRecorder_ClassifiesBotUserAgentAndStillRecordsRow(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "bot-record@example.com")
	seedLink(t, pool, uid, "bot0001", "https://example.com")

	c := Click{Key: "bot0001", UserAgent: "Twitterbot/1.0"}
	if err := rec.Record(context.Background(), c); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "bot0001") // asserts exactly one row exists
	if !got.IsBot {
		t.Error("is_bot = false, want true for a Twitterbot User-Agent")
	}
}

// TestRecorder_RealBrowserUserAgentNotFlagged is the mirror of the bot test
// above: a normal browser click must record is_bot = FALSE, so classification
// isn't a bug that flags everything (which would make the bot test above
// pass vacuously alongside a broken, always-true classifier).
func TestRecorder_RealBrowserUserAgentNotFlagged(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "human-record@example.com")
	seedLink(t, pool, uid, "hum0001", "https://example.com")

	c := Click{
		Key:       "hum0001",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36",
	}
	if err := rec.Record(context.Background(), c); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "hum0001")
	if got.IsBot {
		t.Error("is_bot = true, want false for a real Chrome-on-macOS User-Agent")
	}
}

// TestRecorder_EmptyUserAgentNotFlaggedAsBot asserts the record path applies
// the same deliberate empty-UA policy as IsBot itself: a click with no
// User-Agent at all is recorded as is_bot = FALSE, not TRUE.
func TestRecorder_EmptyUserAgentNotFlaggedAsBot(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "empty-ua-record@example.com")
	seedLink(t, pool, uid, "eua0001", "https://example.com")

	if err := rec.Record(context.Background(), Click{Key: "eua0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got := readClick(t, pool, "eua0001")
	if got.IsBot {
		t.Error("is_bot = true, want false for an absent User-Agent")
	}
}

// TestExplain_IdxClicksCampaignTimeUsed verifies (rather than assumes, per the
// issue's explicit instruction) that idx_clicks_campaign_time is actually
// usable by a campaign-and-window query — the composite index #0102's
// campaign timeseries query depends on.
//
// This asserts two separate things, deliberately not conflated into one
// check:
//   - the EXPLAIN plan uses the index BY NAME (proves the query shape can
//     reach it at all — catches the index being dropped, or built on the
//     wrong column(s) entirely, e.g. (is_bot) instead of (campaign_id,
//     clicked_at));
//   - pg_indexes.indexdef records the columns in (campaign_id, clicked_at)
//     ORDER — proves the actual design decision the issue asked to verify,
//     not just "an index named idx_clicks_campaign_time exists". Reversed
//     order, (clicked_at, campaign_id), is same-name and Postgres still
//     happily uses it for this query (both quals land in Index Cond either
//     way) — so the EXPLAIN check alone passes on a reversed index. Reversed
//     order is also the version that would degrade in production: the
//     leading column becomes the range predicate, so a campaign-scoped scan
//     can no longer seek directly to that campaign's rows.
func TestExplain_IdxClicksCampaignTimeUsed(t *testing.T) {
	pool := testPool(t)
	rec := NewRecorder(pool, nil)

	uid := seedUser(t, pool, "explain@example.com")
	campID := seedCampaign(t, pool, uid, "Explain", "explain")
	seedLinkWithUTM(t, pool, uid, "ex0001", "https://example.com", &campID, seededLinkUTM{})

	if err := rec.Record(context.Background(), Click{Key: "ex0001"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Column-order check: read the index's actual definition back from the
	// catalog rather than trusting the migration file matches what got
	// applied.
	var indexdef string
	if err := pool.QueryRow(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'idx_clicks_campaign_time'`,
	).Scan(&indexdef); err != nil {
		t.Fatalf("read pg_indexes definition: %v", err)
	}
	if !strings.Contains(indexdef, "(campaign_id, clicked_at)") {
		t.Errorf("idx_clicks_campaign_time column order = %q, want columns in (campaign_id, clicked_at) order "+
			"(campaign_id must lead so a campaign-scoped scan can seek directly to it)", indexdef)
	}

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A handful of test rows costs less than a seq scan by Postgres's own
	// estimate regardless of which index exists, which would make this test
	// pass even if idx_clicks_campaign_time were dropped entirely. Forcing
	// enable_seqscan off for this transaction isolates the real question —
	// is this query SHAPE compatible with the index at all — from "is the
	// table big enough today for the planner to prefer it unprompted".
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	rows, err := tx.Query(ctx,
		`EXPLAIN SELECT COUNT(*) FROM clicks
		  WHERE campaign_id = $1
		    AND clicked_at >= $2
		    AND clicked_at < $3`,
		campID, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour),
	)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate explain rows: %v", err)
	}

	got := plan.String()
	if !strings.Contains(got, "idx_clicks_campaign_time") {
		t.Errorf("EXPLAIN plan does not use idx_clicks_campaign_time:\n%s", got)
	}
}

// queryCounter is a minimal pgx.QueryTracer that counts how many
// Query/QueryRow/Exec CALLS are issued while it is attached — this is what
// backs TestRecorder_RecordIssuesExactlyOneStatement's "no additional query
// on the redirect path" assertion. TraceQueryStart fires once per
// Query/QueryRow/Exec call per the pgx v5 tracer contract, so this is a
// direct call count, not an inference from timing.
//
// This is deliberately NOT a network-round-trip count: the pool's default
// QueryExecMode ("cache statement") also fires TracePrepareStart/PrepareEnd
// — an extra Parse/Describe round trip — the first time a given SQL string
// runs on a given connection, then reuses the prepared statement (zero extra
// round trips) on every subsequent call with that SQL on that connection.
// TraceQueryStart itself still fires exactly once per call either way, so
// the "exactly 1" assertion below is correct for what #0100 requires (one
// INSERT statement, not a separate link lookup beside it) — it just isn't
// proof of "exactly one TCP round trip" on a connection's first use.
type queryCounter struct {
	mu    sync.Mutex
	count int
	sqls  []string
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.count++
	c.sqls = append(c.sqls, data.SQL)
	c.mu.Unlock()
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *queryCounter) reset() {
	c.mu.Lock()
	c.count = 0
	c.sqls = nil
	c.mu.Unlock()
}

func (c *queryCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// TestRecorder_RecordIssuesExactlyOneStatement asserts the issue's explicit
// constraint that the redirect path must not gain a query: campaign_id
// resolution and the per-key UTM fallback both had to fit inside the existing
// single INSERT...SELECT, not add a lookup beside it. A pgx.QueryTracer
// attached to a dedicated pool counts every Query/QueryRow/Exec round trip;
// after seeding (which is reset out of the count), a single rec.Record call
// must produce exactly one traced statement.
func TestRecorder_RecordIssuesExactlyOneStatement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping live DB integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	counter := &queryCounter{}
	cfg.ConnConfig.Tracer = counter

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping test db: %v", err)
	}
	truncate(t, pool)
	t.Cleanup(func() {
		truncate(t, pool)
		pool.Close()
	})

	uid := seedUser(t, pool, "statement-count@example.com")
	campID := seedCampaign(t, pool, uid, "Count", "count")
	seedLinkWithUTM(t, pool, uid, "sc0001", "https://example.com", &campID, seededLinkUTM{Source: "link-src"})

	rec := NewRecorder(pool, nil)
	counter.reset() // exclude the seeding queries above; only rec.Record itself is under test

	if err := rec.Record(context.Background(), Click{Key: "sc0001", UTMTerm: "inbound-term"}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if got := counter.Count(); got != 1 {
		t.Errorf("Record issued %d statement(s) (%v), want exactly 1 — "+
			"campaign_id and the UTM fallback must resolve inside the single INSERT, not a separate lookup", got, counter.sqls)
	}
}
