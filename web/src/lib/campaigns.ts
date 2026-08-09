// Pure, DOM-free data-shaping helpers for the campaigns list and detail views
// (#0103). Same discipline as utm.ts/charts.ts/linkDetail.ts — every function
// here is unit-testable without a DOM or Svelte; see campaigns.test.ts.

import type { Campaign, CampaignStats, CampaignWithCounts, Link, LinkBucket, UTMBucket } from './types';
import { shortUrl } from './links';
import { sortBuckets } from './linkDetail';
import { composeUtmUrl, emptyUtmParams, type UtmParams } from './utm';

// ── List filtering ──────────────────────────────────────────────────────────

/**
 * The campaigns list, filtered per the AC "archived campaigns are excluded
 * from the default list and reachable through an explicit filter": every
 * campaign when showArchived is true, otherwise only the non-archived ones.
 * Never mutates the input or reorders it — the server already returns
 * most-recently-created first (campaigns.Store.ListCampaignsForUser).
 */
export function visibleCampaigns(
  campaigns: CampaignWithCounts[],
  showArchived: boolean,
): CampaignWithCounts[] {
  return showArchived ? campaigns : campaigns.filter((c) => !c.archived);
}

// ── Date-only helpers (starts_at / ends_at / window_from / window_to) ──────
//
// starts_at, ends_at, and the stats window_from/window_to are DATE-ONLY
// values that happen to travel as full ISO-8601 instants (timestamptz
// columns on the wire; window_from/window_to are already "YYYY-MM-DD"). The
// API serializes timestamptz with the server's local UTC offset — e.g.
// "2026-06-30T17:00:00-07:00" for what the database actually holds as a
// UTC-midnight 2026-07-01 — so reading the date back via ANY local-time Date
// method (getFullYear/getMonth/getDate, toLocaleDateString without an
// explicit UTC timeZone, or slicing the raw ISO text) reads the PREVIOUS
// calendar day whenever that embedded offset is negative (#0103 bug: dates
// displayed one day early and walking backward on every Edit → Save round
// trip with no user edits, because the read side misread the day AND the
// write side wrote it back at UTC midnight — two different calendars
// talking past each other). Every helper below reads and writes exclusively
// via the UTC accessors, so the displayed/edited day is stable regardless of
// the browser's timezone.

/** Parses a "YYYY-MM-DD" date-only string as UTC midnight; null if malformed. */
function parseDateOnlyUTC(value: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return null;
  const ms = Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]));
  return Number.isNaN(ms) ? null : ms;
}

/**
 * Human date label for a date-only ISO value (starts_at/ends_at/window_from/
 * window_to), read in UTC — e.g. "Jun 1, 2026". Unlike linkDetail.ts's
 * formatDate (deliberately local-time — correct for a real instant like
 * created_at), this must NOT drift with the browser's timezone: the value
 * being formatted is a calendar date, not a moment in time. "" for
 * null/undefined/invalid input.
 */
export function formatDateOnly(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  });
}

/**
 * An <input type="date">'s value for a date-only ISO string — the UTC
 * calendar date, e.g. "2026-07-01". MUST read via getUTCFullYear/
 * getUTCMonth/getUTCDate, never iso.slice(0, 10): the API serializes
 * timestamptz with the server's local offset, so slicing the raw text reads
 * the PREVIOUS day whenever that offset is negative (see this section's
 * header comment). "" for null/undefined/invalid input.
 */
export function toDateInput(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const y = d.getUTCFullYear();
  const mo = String(d.getUTCMonth() + 1).padStart(2, '0');
  const day = String(d.getUTCDate()).padStart(2, '0');
  return `${y}-${mo}-${day}`;
}

/**
 * Inverse of toDateInput: an <input type="date"> value ("YYYY-MM-DD") to a
 * UTC-midnight ISO instant to send back to the API. "" -> null (clears the
 * field, matching the patch-presence contract patchCampaignRequest expects —
 * see UpdateCampaignInput); a malformed value -> undefined (omit from the
 * request rather than send garbage). Round-trips with toDateInput:
 * toDateInput(toIsoDate(x)) === x for any valid "YYYY-MM-DD" x, in every
 * timezone — both ends work exclusively in UTC.
 */
export function toIsoDate(value: string): string | null | undefined {
  if (value === '') return null;
  const ms = parseDateOnlyUTC(value);
  return ms === null ? undefined : new Date(ms).toISOString();
}

// ── Date range / window labels ──────────────────────────────────────────────

/**
 * Human label for a campaign's starts_at/ends_at, e.g. "Jun 1, 2026 – Jun
 * 30, 2026", "Jun 1, 2026 – ongoing", or "No dates set" when neither is
 * present. Used by the list's "date range" column and the detail header.
 */
export function campaignDateRangeLabel(campaign: Pick<Campaign, 'starts_at' | 'ends_at'>): string {
  if (!campaign.starts_at && !campaign.ends_at) return 'No dates set';
  const start = campaign.starts_at ? formatDateOnly(campaign.starts_at) : 'Always';
  const end = campaign.ends_at ? formatDateOnly(campaign.ends_at) : 'ongoing';
  return `${start} – ${end}`;
}

/**
 * Label for the summary's WINDOWED numbers, distinguishing them from the
 * all-time link_count/total_clicks shown alongside (#0102 constraint 2 —
 * "four numbers, two time scales" must stay visibly distinct, never
 * reconciled). Built from CampaignStats.window_from/window_to — the window
 * the SERVER actually resolved and queried (clicks.campaignWindow), which
 * for a dated campaign already in flight is clamped at today and so is
 * shorter than the campaign's own starts_at/ends_at span. Deliberately NOT
 * re-derived from the campaign's nominal dates client-side — that would
 * drift from the clamp and silently overstate the window (#0103's "clicks
 * over five months" defect). Falls back to a generic label when stats are
 * absent (no provider wired, dev mode, or still loading).
 *
 * window_to is the EXCLUSIVE end of the half-open [window_from, window_to)
 * range (same convention charts.ts's fillDayGapsInWindow subtracts a day
 * for before walking calendar days). Rendering window_to VERBATIM as the
 * caption's end date disagreed with the chart axis by one day — e.g.
 * captioning "Aug 1 – Aug 2" over an axis whose only bucket is Aug 1, or
 * "Jul 1 – Aug 1" over an axis ending Jul 31 (#0104 review finding 3: "the
 * chart is right — the caption is wrong"). Subtracting a day here, the same
 * way the chart already does, is the fix.
 *
 * EMPTY WINDOW: when window_from === window_to the half-open range spans zero
 * days, and subtracting one puts the end BEFORE the start — rendering
 * "Aug 1, 2026 – Jul 31, 2026" (#0104 re-review finding 1). That state is
 * reachable: a campaign whose starts_at equals its ends_at is the natural way
 * to enter a one-day event, is permitted by the campaigns_check constraint
 * (ends_at >= starts_at), and the create form imposes no min/max. A backwards
 * range reads as a broken app rather than as "this window covers no days", so
 * say the latter explicitly.
 */
export function windowLabel(stats: Pick<CampaignStats, 'window_from' | 'window_to'> | null | undefined): string {
  if (!stats?.window_from || !stats?.window_to) return 'recent activity';
  const inclusiveEnd = subtractOneDayUTC(stats.window_to) ?? stats.window_to;
  if (inclusiveEnd < stats.window_from) {
    // Zero-day window. YYYY-MM-DD sorts lexicographically, so a plain string
    // comparison is a correct date comparison here.
    return `${formatDateOnly(stats.window_from)} (no days in window)`;
  }
  return `${formatDateOnly(stats.window_from)} – ${formatDateOnly(inclusiveEnd)}`;
}

/**
 * Subtracts one calendar day from a "YYYY-MM-DD" date-only string, in UTC.
 * Returns null for malformed input. The half-open-window → inclusive-end
 * conversion windowLabel needs (see its doc comment); mirrors the
 * subtraction charts.ts's fillDayGapsInWindow already does for the chart
 * axis, so caption and axis read the same last day.
 */
function subtractOneDayUTC(value: string): string | null {
  const ms = parseDateOnlyUTC(value);
  if (ms === null) return null;
  const d = new Date(ms - 86_400_000);
  const y = d.getUTCFullYear();
  const mo = String(d.getUTCMonth() + 1).padStart(2, '0');
  const da = String(d.getUTCDate()).padStart(2, '0');
  return `${y}-${mo}-${da}`;
}

/**
 * Number of calendar days the stats window actually spans, from
 * CampaignStats.window_from/window_to — the SAME authoritative, already-
 * clamped window windowLabel labels (see its doc comment for why this is
 * not re-derived from the campaign's own starts_at/ends_at). Always at
 * least 1, so a same-day (or missing/malformed) window never divides by
 * zero; falls back to 30 (the server's own default-window span) when stats
 * are absent.
 */
export function windowDayCount(stats: Pick<CampaignStats, 'window_from' | 'window_to'> | null | undefined): number {
  if (stats?.window_from && stats?.window_to) {
    const from = parseDateOnlyUTC(stats.window_from);
    const to = parseDateOnlyUTC(stats.window_to);
    if (from !== null && to !== null && to >= from) {
      return Math.max(1, Math.round((to - from) / 86_400_000));
    }
  }
  return 30;
}

/**
 * "Clicks per day" average for the summary, rounded to one decimal place (so
 * a low-volume campaign reads as "0.3/day" rather than collapsing to 0) and
 * safe against a zero or negative day count.
 */
export function clicksPerDayAverage(clickCount: number, days: number): number {
  if (days <= 0) return 0;
  return Math.round((clickCount / days) * 10) / 10;
}

// ── Channel breakdown (#0104) ───────────────────────────────────────────────

/**
 * One channel-breakdown dimension prepared for display, mirroring
 * linkDetail.ts's `UTMDimension` shape/discipline but for the three
 * dimensions the campaign detail view shows (#0104's scope: source, medium,
 * content — NOT referer, which CampaignStats also carries but this issue
 * does not surface).
 */
export interface CampaignUTMDimension {
  dimension: 'source' | 'medium' | 'content';
  label: string;
  buckets: UTMBucket[];
}

/**
 * The three channel-breakdown dimensions for the campaign detail view's
 * chart slot, each labeled and sorted by count descending (reusing
 * linkDetail.ts's `sortBuckets` rather than a second copy of that ordering).
 * Dimensions with no rows are still returned (empty `buckets`); use
 * `isEmptyCampaignChannelStats` to gate the whole section the way
 * LinkDetail's `isEmptyStats` does for the per-link UTM grid — the two views
 * should feel like the same product, not a second charting idiom.
 */
export function campaignUtmDimensions(stats: CampaignStats | undefined | null): CampaignUTMDimension[] {
  return [
    { dimension: 'source', label: 'Source', buckets: sortBuckets(stats?.by_source) },
    { dimension: 'medium', label: 'Medium', buckets: sortBuckets(stats?.by_medium) },
    { dimension: 'content', label: 'Content', buckets: sortBuckets(stats?.by_content) },
  ];
}

/**
 * Whether a campaign's windowed stats carry no channel data worth charting:
 * absent, zero windowed clicks, or every one of the three dimensions above
 * is empty. Mirrors linkDetail.ts's `isEmptyStats` (same three-part check,
 * against CampaignStats's fields instead of ClickStats's).
 */
export function isEmptyCampaignChannelStats(stats: CampaignStats | undefined | null): boolean {
  if (!stats) return true;
  if (stats.click_count > 0) return false;
  const dims = [stats.by_source, stats.by_medium, stats.by_content];
  return dims.every((d) => !d || d.length === 0);
}

// ── Links table rows ─────────────────────────────────────────────────────

/** One row of the campaign detail's links table: a Link plus its windowed clicks and share of the campaign total (#0103). */
export interface CampaignLinkRow extends Link {
  clicksInWindow: number;
  shareOfTotal: number; // 0–100, rounded, safe against divide-by-zero
}

/**
 * Builds the links-table rows: pairs each currently-assigned link with its
 * windowed click count from `byLink` (GET .../stats's by_link, matched by
 * key) and computes each row's share of the total clicks ACTUALLY SHOWN in
 * the table.
 *
 * Deliberately denominated against the sum of the matched rows, not against
 * CampaignStats.click_count or total_clicks: by_link can carry entries for
 * links no longer assigned to the campaign (#0100 — a click stays
 * attributed to the campaign that owned the link when it was recorded, even
 * after the link is later reassigned or unassigned), which would otherwise
 * make the visible shares fall short of 100%. Denominating against the
 * displayed rows keeps the AC ("share of campaign total sums to 100%,
 * allowing for rounding") true for exactly what is rendered.
 *
 * byLink defaults to [] when absent (no stats provider wired, dev mode, or
 * still loading — #0102 constraint 5), in which case every row is 0/0%
 * rather than throwing or dividing by zero.
 */
export function buildLinkRows(links: Link[], byLink: LinkBucket[] | undefined | null): CampaignLinkRow[] {
  const counts = new Map<string, number>();
  for (const b of byLink ?? []) {
    if (b.key) counts.set(b.key, b.count);
  }
  const withCounts = links.map((l) => ({ ...l, clicksInWindow: counts.get(l.key) ?? 0 }));
  const total = withCounts.reduce((sum, r) => sum + r.clicksInWindow, 0);
  return withCounts.map((r) => ({
    ...r,
    shareOfTotal: total === 0 ? 0 : Math.round((r.clicksInWindow / total) * 100),
  }));
}

/**
 * Clicks in `byLink` attributed to a key NOT among `links` — i.e. clicks
 * from a link that has since been unassigned or reassigned away from this
 * campaign (#0100: a click stays attributed to the campaign that owned the
 * link when it happened), which the links table's "Share of listed links"
 * column therefore excludes from both its numerator and denominator (see
 * buildLinkRows's doc comment). Used to render the note under the table
 * explaining why by_link's total can exceed what the table displays, so
 * "Share of listed links" summing to 100% doesn't get misread as "100% of
 * this campaign's clicks" (#0103). 0 when every by_link entry matches a
 * currently-assigned link, or byLink is empty/absent.
 */
export function unlistedClickCount(
  links: Pick<Link, 'key'>[],
  byLink: LinkBucket[] | undefined | null,
): number {
  const keys = new Set(links.map((l) => l.key));
  let total = 0;
  for (const b of byLink ?? []) {
    if (b.key && !keys.has(b.key)) total += b.count;
  }
  return total;
}

/**
 * Sorts rows by windowed clicks. `Array.prototype.sort` is stable (ES2019+),
 * so rows with EQUAL counts keep their relative order from `rows` — this
 * function exists to make that guarantee explicit and testable (AC:
 * "sorting by clicks is stable and does not reorder rows with equal counts
 * arbitrarily between renders"). Never mutates the input.
 */
export function sortLinkRows(
  rows: CampaignLinkRow[],
  direction: 'desc' | 'asc' = 'desc',
): CampaignLinkRow[] {
  const sign = direction === 'desc' ? -1 : 1;
  return [...rows].sort((a, b) => sign * (a.clicksInWindow - b.clicksInWindow));
}

// ── Assign / unassign ───────────────────────────────────────────────────

/** POST /api/campaigns/{slug}/links's server-enforced per-request cap (maxAssignLinksKeys, #0099). */
export const MAX_ASSIGN_KEYS_PER_REQUEST = 50;

/**
 * Splits a list of keys into chunks no larger than
 * MAX_ASSIGN_KEYS_PER_REQUEST, so the view can drive several sequential
 * POST /api/campaigns/{slug}/links calls instead of one oversized request
 * that 400s. Empty input yields [].
 */
export function chunkKeys(keys: string[], size = MAX_ASSIGN_KEYS_PER_REQUEST): string[][] {
  if (keys.length === 0) return [];
  const out: string[][] = [];
  for (let i = 0; i < keys.length; i += size) {
    out.push(keys.slice(i, i + size));
  }
  return out;
}

/**
 * Parses the assign form's free-text input into a de-duplicated, ordered
 * list of link keys. Accepts bare keys, full short URLs
 * (https://go.sstools.co/u/{key}), or a mix, separated by commas,
 * whitespace, or newlines — so a user can paste several copied short URLs
 * at once. Blank entries are dropped.
 */
export function parseKeysInput(text: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\s,]+/)) {
    const token = raw.trim();
    if (token === '') continue;
    const key = extractKey(token);
    if (key === '' || seen.has(key)) continue;
    seen.add(key);
    out.push(key);
  }
  return out;
}

/**
 * Extracts the key from a bare key or a `.../u/{key}` short URL. No `$`
 * anchor: a query string or fragment pasted along with the short URL (e.g.
 * copying a link that already carries `?utm_source=...`) must not defeat
 * the match — `[^/?#]+` already stops at the next `/`, `?`, or `#`, so the
 * key is found regardless of what (if anything) follows it.
 */
function extractKey(token: string): string {
  const match = /\/u\/([^/?#]+)\/?/.exec(token);
  if (match) {
    try {
      return decodeURIComponent(match[1]);
    } catch {
      return match[1];
    }
  }
  return token;
}

// ── Copy-all ─────────────────────────────────────────────────────────────

/**
 * Builds the newline-separated text for "copy all short URLs" — every given
 * link's short URL, in the order provided.
 */
export function copyAllShortUrlsText(links: Pick<Link, 'key'>[]): string {
  return links.map((l) => shortUrl(l.key)).join('\n');
}

// ── QR codes (#0106) ─────────────────────────────────────────────────────

/**
 * URL for the bulk "every link's QR code" zip download for a campaign (
 * `GET /api/campaigns/{slug}/qr.zip` — internal/handlers/campaigns.go's
 * QRZip). Same-origin, cookie-authenticated like every other API route (see
 * links.ts's qrSvgUrl/qrPngUrl doc comment) — a plain `<a href download>`
 * is sufficient, no fetch/blob plumbing needed.
 */
export function campaignQrZipUrl(slug: string): string {
  return `/api/campaigns/${encodeURIComponent(slug)}/qr.zip`;
}

// ── CSV export (#0107) ───────────────────────────────────────────────────

/**
 * URL for the per-link CSV export download (`GET
 * /api/campaigns/{slug}/export.csv` — internal/handlers/campaigns.go's
 * Export). Same-origin, cookie-authenticated, no query params — deliberately
 * omitting `from`/`to` so the download reuses the SAME default window
 * getCampaignStats(slug) (no explicit window either, see CampaignDetail's
 * script) already resolved for the on-screen table, which is exactly what
 * makes the exported numbers reconcile with what's on screen: both requests
 * hit the server's identical default-window resolution
 * (clicks.campaignWindow) with nothing for the two calls to disagree about.
 * A plain `<a href download>` is sufficient here for the same reason
 * campaignQrZipUrl's doc comment gives — no fetch/blob plumbing needed.
 */
export function campaignExportCsvUrl(slug: string): string {
  return `/api/campaigns/${encodeURIComponent(slug)}/export.csv`;
}

// ── Message formatting ──────────────────────────────────────────────────

/**
 * Joins two sentence fragments with ". " (or just " " when `first` already
 * ends in terminal punctuation), so concatenating a server-authored message
 * with a client-authored follow-up never runs the two together with no
 * separator at all. `first` is typically an ApiError's message forwarded
 * verbatim from the server, which is NOT guaranteed to end in punctuation —
 * e.g. "link not found: nosuchkey" — so the previous bare
 * `${reason} ${clientSentence}` template literal read as one run-on
 * sentence ("...nosuchkey Some links...", #0103 fix 4). `first` is trimmed
 * before the punctuation check so trailing whitespace can't defeat it; an
 * empty (or whitespace-only) `first` yields `second` alone, with no leading
 * separator.
 */
export function joinSentences(first: string, second: string): string {
  const trimmed = first.trim();
  if (trimmed === '') return second;
  const needsPeriod = !/[.!?]$/.test(trimmed);
  return `${trimmed}${needsPeriod ? '.' : ''} ${second}`;
}

// ── Batch create (#0105) ────────────────────────────────────────────────
//
// One destination URL, then a row per channel. The issue's deliverable list
// is explicit about which fields vary PER ROW: source, medium, content,
// placement, and an optional title. utm_campaign and utm_term are NOT part
// of a row — they are shared across the whole batch (usually the campaign's
// own default_utm_campaign/default_utm_term, editable once for the batch),
// since repeating a field that is almost always identical across every
// channel in one promotion eight times would be exactly the fragmentation
// docs/utm.md's "Canonical casing" section warns about, just self-inflicted
// instead of caused by different authors. See BatchCreateLinks.svelte for
// the form that edits these.
//
// THE DEDUP TRAP (see links.Store.CreateLinksBatch's Go-side doc comment for
// the full history): `placement` is never baked into destination_url, so two
// rows differing only in placement compose to a byte-identical
// destination_url. The backend's fix is to bypass CreateOrReactivateLink's
// dedup entirely for the batch endpoint — nothing on THIS side needs to work
// around it — but the duplicateBatchRowIndices check below is a SEPARATE,
// deliberate concern: catching a genuine accidental double-entry (the same
// channel typed twice, placement included) before it reaches the server.
//
// PREFILLING default_utm_source/_medium/_content — ROW 1 ONLY, deliberately
// (review finding 2). The issue's bolded deliverable ("prefill each row from
// the campaign's default_utm_* values", #0098) was implemented for
// utm_campaign/utm_term (the shared pair above) but the other three were
// never read at all: a campaign with default_utm_source="flyer" produced
// entirely blank rows, while #0099's single-create form DOES prefill all
// five via fillBlankUtmParams — the same campaign prefilled "flyer" in one
// form and nothing in the other. initialBatchRows below fixes that, but only
// for the FIRST row, not every row: prefilling identical source/medium/
// content into every row would make rows 2..N byte-identical to row 1 under
// duplicateBatchRowIndices (an immediate, spurious "duplicate row" error
// before the user has typed anything), AND would make every untouched row
// non-blank, defeating isBlankBatchRow's skip-on-blank behavior — a form
// opened and closed without editing anything would submit N identical links
// instead of zero. Row 1 gets the campaign's starting point (still fully
// editable, per #0098/#0099's "starting point, never a lock" rule); rows
// 2..N stay genuinely blank and stay skippable, exactly like "add row"
// already produces.

/**
 * POST /api/campaigns/{slug}/links/batch's server-enforced per-request cap
 * (maxBatchCreateRows, #0105). Client twin of MAX_ASSIGN_KEYS_PER_REQUEST
 * above — review finding 4: without this, "+ Add row" was unbounded and a
 * user who filled in 51 rows got back a raw server 400 instead of the
 * button simply disabling at the limit.
 */
export const MAX_BATCH_CREATE_ROWS_PER_REQUEST = 50;

/**
 * One row of the batch-create form: the five fields that vary row to row.
 * utm_campaign/utm_term are handled separately (see the module doc comment
 * above) — they are not part of a row's own state.
 */
export interface BatchChannelRow {
  utm_source: string;
  utm_medium: string;
  utm_content: string;
  placement: string;
  title: string;
}

/** A fresh, entirely blank row — what "add row" appends. */
export function emptyBatchChannelRow(): BatchChannelRow {
  return { utm_source: '', utm_medium: '', utm_content: '', placement: '', title: '' };
}

/**
 * Builds the batch form's initial row list when a campaign detail view
 * first opens the batch-create panel: exactly ONE row, prefilled from the
 * campaign's default_utm_source/_medium/_content (review finding 2 — see
 * the module doc comment's "PREFILLING" section above for why row 1 only,
 * not every row). placement and title are never prefilled — the issue's
 * deliverable list does not name them as campaign defaults, and #0098's
 * campaigns table has no default_placement column to prefill from.
 *
 * A campaign whose three defaults are all empty (never set, or explicitly
 * cleared via PATCH) produces a row indistinguishable from
 * emptyBatchChannelRow() — isBlankBatchRow(rows[0]) is true, exactly the
 * pre-#0105-fix behavior, so this is a strict improvement with no new edge
 * case to guard.
 */
export function initialBatchRows(
  campaign: Pick<Campaign, 'default_utm_source' | 'default_utm_medium' | 'default_utm_content'>,
): BatchChannelRow[] {
  return [
    {
      ...emptyBatchChannelRow(),
      utm_source: campaign.default_utm_source,
      utm_medium: campaign.default_utm_medium,
      utm_content: campaign.default_utm_content,
    },
  ];
}

/**
 * Whether every one of a row's own five fields is blank/whitespace-only —
 * i.e. the row was added but never touched. Used to skip such rows rather
 * than composing a link with empty UTM values (#0105 AC). Deliberately does
 * NOT look at the batch's shared utm_campaign/utm_term (see the module doc
 * comment): those are usually non-empty campaign defaults applied to every
 * row, so their presence must never by itself make an otherwise-untouched
 * row count as "filled in".
 */
export function isBlankBatchRow(row: BatchChannelRow): boolean {
  return (
    row.utm_source.trim() === '' &&
    row.utm_medium.trim() === '' &&
    row.utm_content.trim() === '' &&
    row.placement.trim() === '' &&
    row.title.trim() === ''
  );
}

/** Only the non-blank rows, in their original order — what actually gets submitted. */
export function nonBlankBatchRows(rows: BatchChannelRow[]): BatchChannelRow[] {
  return rows.filter((r) => !isBlankBatchRow(r));
}

/**
 * Indices (into `rows`) of rows that duplicate an EARLIER row in the same
 * list, keyed on (utm_source, utm_medium, utm_content, placement) after
 * trimming.
 *
 * DELIBERATELY INCLUDES placement, unlike the issue's acceptance-criterion
 * shorthand ("same source+medium+content"): applied literally, that
 * definition would flag the exact "two rows differing only in placement"
 * case this issue exists to make work as a duplicate and block it — the
 * opposite of the fix. Two rows that differ ONLY in placement (two poster
 * locations for the same channel) are legitimate and must NOT appear here;
 * two rows identical even in placement (the same channel typed twice) are a
 * genuine accidental double-entry and DO appear here. Mirrors the backend's
 * duplicateKey exactly (internal/handlers/campaigns.go) so a row flagged
 * here would also be rejected server-side, and vice versa — which is exactly
 * why BLANK rows are skipped entirely below: the backend's duplicate check
 * (internal/handlers/campaigns.go's BatchCreateLinks) runs AFTER blank rows
 * are already dropped, so it never sees them and never flags one blank row
 * against another. A first version of this function scanned the full `rows`
 * array unconditionally, which meant adding a THIRD blank row (e.g. "eight
 * rows, fill in a few now, come back to the rest later" — exactly the
 * workflow the issue's own "eight rows at 375px" verification state
 * exercises) flagged it as a duplicate of the second blank row and disabled
 * the whole submit button, even though every blank row was always going to
 * be silently skipped and never reached the server at all. Caught rendering
 * this in a real browser, not by a unit test — see campaigns.test.ts's
 * "does not flag a third blank row" case, added after.
 *
 * Comparison is exact and case-sensitive — this module does not normalize
 * casing (see docs/utm.md's "Canonical casing" section); silently folding
 * case here would let two rows collide even though composeUtmUrl bakes them
 * as genuinely different values.
 */
export function duplicateBatchRowIndices(rows: BatchChannelRow[]): Set<number> {
  const seen = new Set<string>();
  const dupes = new Set<number>();
  rows.forEach((r, i) => {
    if (isBlankBatchRow(r)) return;
    const key = JSON.stringify([r.utm_source.trim(), r.utm_medium.trim(), r.utm_content.trim(), r.placement.trim()]);
    if (seen.has(key)) {
      dupes.add(i);
    } else {
      seen.add(key);
    }
  });
  return dupes;
}

/**
 * Composes one batch row's destination_url from the shared base URL, the
 * row's own source/medium/content, and the batch-level (shared)
 * utm_campaign/utm_term — reusing composeUtmUrl (#0048), the SAME pure
 * helper single-create's builder uses, rather than a second composition path
 * (issue note: "Batch composition is N calls to the same pure helper —
 * resist writing a second composition path").
 *
 * `previous` is ALWAYS emptyUtmParams(): a freshly-typed batch row's builder
 * never "owned" anything already present in the base URL — unlike an EDIT
 * form's builder, which owns exactly what utmParamsFromLink seeded it with
 * (see composeUtmUrl's own doc comment for why `previous` must not be
 * conflated with `params`, and #0105's issue notes for this exact
 * prescription: "For batch rows, previous should be emptyUtmParams() — a new
 * row's builder never owned anything").
 */
export function composeBatchRowDestinationUrl(
  baseUrl: string,
  row: BatchChannelRow,
  sharedUtmCampaign: string,
  sharedUtmTerm: string,
): string {
  const params: UtmParams = {
    utm_source: row.utm_source,
    utm_medium: row.utm_medium,
    utm_campaign: sharedUtmCampaign,
    utm_term: sharedUtmTerm,
    utm_content: row.utm_content,
  };
  return composeUtmUrl(baseUrl, params, emptyUtmParams());
}

/** One row of POST /api/campaigns/{slug}/links/batch's request body (api.ts's BatchCreateLinkRowInput mirrors this shape). */
export interface BatchCreateLinkRowPayload {
  destination_url: string;
  title?: string;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_term?: string;
  utm_content?: string;
  placement?: string;
}

/**
 * Builds the batch-create request payload: filters out blank rows, composes
 * each remaining row's destination_url (via composeBatchRowDestinationUrl),
 * and folds in the shared utm_campaign/utm_term. Pure — no fetch, no
 * validation of duplicates (call duplicateBatchRowIndices separately before
 * this so the caller can show a blocking error instead of silently
 * submitting past it).
 */
export function buildBatchCreateRows(
  baseUrl: string,
  rows: BatchChannelRow[],
  sharedUtmCampaign: string,
  sharedUtmTerm: string,
): BatchCreateLinkRowPayload[] {
  return nonBlankBatchRows(rows).map((r) => ({
    destination_url: composeBatchRowDestinationUrl(baseUrl, r, sharedUtmCampaign, sharedUtmTerm),
    title: r.title.trim() || undefined,
    utm_source: r.utm_source.trim() || undefined,
    utm_medium: r.utm_medium.trim() || undefined,
    utm_campaign: sharedUtmCampaign.trim() || undefined,
    utm_term: sharedUtmTerm.trim() || undefined,
    utm_content: r.utm_content.trim() || undefined,
    placement: r.placement.trim() || undefined,
  }));
}
