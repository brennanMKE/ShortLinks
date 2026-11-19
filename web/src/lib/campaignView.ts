// Hash-query state and data shaping for the tabbed campaign detail (#0164):
// tab, date range, grouping and the matrix's column dimension, plus the pure
// helpers the Content tab, tiles and range control read. No DOM, no Svelte, so
// campaignView.test.ts covers it directly. The shared keys (range, from, to,
// group, tab) are read and written by lib/viewQuery.ts with this page's own
// defaults (CAMPAIGN_BASE); `by` is local to this page.

import * as vq from './viewQuery';
import type { CampaignComparisonRow, MatrixCell, MatrixRow } from './reports';

export type Tab = 'content' | 'links' | 'channels' | 'placements' | 'settings';
/** 'campaign' = the campaign's own dates. */
export type RangeKey = 'campaign' | '7d' | '30d' | '90d' | 'custom';
export type MatrixBy = 'source' | 'medium' | 'placement';
/** The Links table's grouping: by content (default) or a flat list. */
export type LinkGroup = 'content' | 'link';

export const TABS: { key: Tab; label: string }[] = [
  { key: 'content', label: 'Content' },
  { key: 'links', label: 'Links' },
  { key: 'channels', label: 'Channels' },
  { key: 'placements', label: 'Placements' },
  { key: 'settings', label: 'Settings' },
];

export const MATRIX_BYS: { key: MatrixBy; label: string }[] = [
  { key: 'source', label: 'Source' },
  { key: 'medium', label: 'Medium' },
  { key: 'placement', label: 'Placement' },
];

export interface CampaignView {
  tab: Tab;
  range: RangeKey;
  /** YYYY-MM-DD; only meaningful when range is 'custom'. */
  from: string;
  to: string;
  by: MatrixBy;
  group: LinkGroup;
}

export const DEFAULT_VIEW: CampaignView = { tab: 'content', range: 'campaign', from: '', to: '', by: 'source', group: 'content' };

/** This page's viewQuery defaults: the range is the campaign's own dates. */
export const CAMPAIGN_BASE: vq.ViewState = { ...vq.DEFAULT_STATE, range: 'campaign', tab: '' };

/**
 * Reads the view from a raw hash query. Unknown or malformed values fall back
 * to defaults. `?group=0` (#0157) is read as a flat list, same as group=link.
 */
export function parseCampaignView(rawQuery: string): CampaignView {
  const s = vq.parse(rawQuery, CAMPAIGN_BASE);
  const by = new URLSearchParams(rawQuery).get('by');
  return {
    tab: TABS.some((t) => t.key === s.tab) ? (s.tab as Tab) : 'content',
    range: s.range,
    from: s.from,
    to: s.to,
    by: by === 'medium' || by === 'placement' ? by : 'source',
    group: s.group === 'link' ? 'link' : 'content',
  };
}

/** The query string after applying `patch` to `rawQuery`, defaults omitted. */
export function patchCampaignQuery(rawQuery: string, patch: Partial<CampaignView>): string {
  const base = vq.parse(rawQuery, CAMPAIGN_BASE);
  const v = { ...parseCampaignView(rawQuery), ...patch };
  const qs = vq.serialize(
    { ...base, range: v.range, from: v.from, to: v.to, group: v.group, tab: v.tab === 'content' ? '' : v.tab },
    CAMPAIGN_BASE,
  );
  const parts = qs ? [qs] : [];
  if (v.by !== DEFAULT_VIEW.by) parts.push('by=' + v.by);
  return parts.join('&');
}

/** The hash path of a campaign with `patch` applied to the current query. */
export function campaignPath(slug: string, rawQuery: string, patch: Partial<CampaignView> = {}): string {
  const qs = patchCampaignQuery(rawQuery, patch);
  return `/campaigns/${encodeURIComponent(slug)}${qs ? '?' + qs : ''}`;
}

// ── Window ──────────────────────────────────────────────────────────────

export function addDays(iso: string, n: number): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso);
  if (!m) return iso;
  return new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]) + n * 86_400_000).toISOString().slice(0, 10);
}

/**
 * The from/to for GET /api/campaigns/{slug}/stats. Both are civil days in the
 * chart time zone (the same `tz` the request sends) and `to` is EXCLUSIVE
 * ([from, to), parseStatsWindow), so "through day D" is to = D + 1.
 *
 * - Campaign dates: nothing. The server's default is the campaign's own
 *   dates, last day and today included (#0190, #0119), or the last 30 days
 *   when it has fewer than both dates.
 * - 7d / 30d / 90d are that many days ending today, today included.
 * - Custom: `to` is an inclusive day in the control; no end means through today.
 */
export function statsRange(v: CampaignView, today: string): { from?: string; to?: string } {
  if (v.range === 'campaign') return {};
  if (v.range === 'custom') return { from: v.from, to: addDays(v.to || today, 1) };
  const days = v.range === '7d' ? 7 : v.range === '90d' ? 90 : 30;
  return { from: addDays(today, -(days - 1)), to: addDays(today, 1) };
}

// ── Matrix ──────────────────────────────────────────────────────────────

/** Column heading for a matrix value. */
export function columnLabel(col: string): string {
  if (col === 'other') return 'Other';
  if (col === '' || col === '~none') return '(none)';
  return col;
}

/** The link a cell opens: its only link, or the one with the most clicks. */
export function cellKey(cell: MatrixCell | undefined): string {
  if (!cell) return '';
  return cell.key ?? cell.keys?.[0] ?? '';
}

/** Every link key with clicks in a row, in no particular order. */
export function rowKeys(row: MatrixRow): string[] {
  const out = new Set<string>();
  for (const c of Object.values(row.cells)) {
    if (c.key) out.add(c.key);
    for (const k of c.keys ?? []) out.add(k);
  }
  return [...out];
}

/** Rows whose title contains `q` (case-insensitive); all rows for a blank query. */
export function filterRows(rows: MatrixRow[], q: string): MatrixRow[] {
  const needle = q.trim().toLowerCase();
  return needle ? rows.filter((r) => r.title.toLowerCase().includes(needle)) : rows;
}

/** The largest cell, for scaling the shade. */
export function matrixMax(rows: MatrixRow[]): number {
  let max = 0;
  for (const r of rows) for (const c of Object.values(r.cells)) if (c.clicks > max) max = c.clicks;
  return max;
}

/** Shade strength, 0 for no clicks, otherwise 8-45 (percent of the accent) scaled to the largest cell; the cap keeps the number readable on top. */
export function shadePct(clicks: number, max: number): number {
  if (clicks <= 0 || max <= 0) return 0;
  return Math.round(8 + (clicks / max) * 37);
}

/**
 * The status tag. GET /api/campaigns/{slug} carries no `status` (only the list
 * does), so it is derived from the dates the way the list's is: archived,
 * scheduled (starts later), ended (ended earlier) or live.
 */
export function campaignStatusOf(
  c: { archived: boolean; starts_at: string | null; ends_at: string | null; status?: string },
  today: string,
): 'live' | 'scheduled' | 'ended' | 'archived' {
  if (c.archived) return 'archived';
  const start = c.starts_at?.slice(0, 10);
  const end = c.ends_at?.slice(0, 10);
  if (start && start > today) return 'scheduled';
  if (end && end < today) return 'ended';
  return 'live';
}

// ── Tiles ───────────────────────────────────────────────────────────────

/** Links in the campaign that were not clicked in the window. */
export function idleLinks(links: number, linksClicked: number): number {
  return Math.max(0, links - linksClicked);
}

export interface TopContentTile {
  title: string;
  clicks: number;
  /** Whole percent of the campaign's clicks. */
  sharePct: number;
}

export function topContentTile(rows: MatrixRow[], totalClicks: number): TopContentTile | null {
  const top = rows[0];
  if (!top || top.total <= 0) return null;
  return { title: top.title, clicks: top.total, sharePct: totalClicks > 0 ? Math.round((top.total / totalClicks) * 100) : 0 };
}

/** The campaign's row of a campaign-comparison response, or null. */
export function comparisonRow(rows: CampaignComparisonRow[] | undefined, slug: string): CampaignComparisonRow | null {
  return rows?.find((r) => r.slug === slug) ?? null;
}
