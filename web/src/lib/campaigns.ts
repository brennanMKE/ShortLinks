// Pure, DOM-free data-shaping helpers for the campaigns list and detail views
// (#0103). Same discipline as utm.ts/charts.ts/linkDetail.ts — every function
// here is unit-testable without a DOM or Svelte; see campaigns.test.ts.

import type { Campaign, CampaignStats, CampaignWithCounts, Link, LinkBucket } from './types';
import { shortUrl } from './links';

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
 */
export function windowLabel(stats: Pick<CampaignStats, 'window_from' | 'window_to'> | null | undefined): string {
  if (!stats?.window_from || !stats?.window_to) return 'recent activity';
  return `${formatDateOnly(stats.window_from)} – ${formatDateOnly(stats.window_to)}`;
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
