// Pure helpers for the link-detail view's link-performance report (#0169):
// the range control's hash state, the report request, siblings and referrer
// rows, and the previous-period change. No DOM or network; see linkReport.test.ts.

import type { DayBucket } from './types';

export type RangeKind = '7d' | '30d' | '90d' | 'custom';
export const RANGE_PRESETS: RangeKind[] = ['7d', '30d', '90d'];
export const DEFAULT_RANGE: RangeKind = '30d';

/** The range control's state, as held in the hash query (`?range=7d` or `?range=custom&from=…&to=…`). */
export interface RangeSel {
  kind: RangeKind;
  from: string;
  to: string;
}

const DAY_RE = /^\d{4}-\d{2}-\d{2}$/;

/** Reads the range from the hash query; anything invalid falls back to 30 days. */
export function parseRange(q: URLSearchParams): RangeSel {
  const r = q.get('range');
  if (r === '7d' || r === '90d' || r === '30d') return { kind: r, from: '', to: '' };
  if (r === 'custom') {
    const from = q.get('from') ?? '';
    const to = q.get('to') ?? '';
    if (DAY_RE.test(from) && DAY_RE.test(to) && from <= to) return { kind: 'custom', from, to };
  }
  return { kind: DEFAULT_RANGE, from: '', to: '' };
}

/** The hash query for a range; the default range is omitted so the URL stays clean. */
export function rangeQuery(sel: RangeSel): string {
  if (sel.kind === 'custom') return `range=custom&from=${sel.from}&to=${sel.to}`;
  if (sel.kind === DEFAULT_RANGE) return '';
  return `range=${sel.kind}`;
}

/** The query string sent to GET /api/reports/link-performance; `tz` is the chart time zone. */
export function reportQuery(key: string, sel: RangeSel, tz?: string): string {
  const p = new URLSearchParams({ key, compare: 'previous' });
  if (tz) p.set('tz', tz);
  if (sel.kind === 'custom') {
    p.set('from', sel.from);
    p.set('to', sel.to);
  } else {
    p.set('range', sel.kind);
  }
  return p.toString();
}

export interface DayCount {
  date: string;
  clicks: number;
}
export interface BreakdownRow {
  value: string;
  clicks: number;
  share: number;
  previous_clicks: number | null;
}
export interface Sibling {
  key: string;
  label: string;
  campaign_slug: string | null;
  active: boolean;
  clicks: number;
}
export interface LinkPerformance {
  report: string;
  window: { from: string; to: string; tz: string; partial_last_day: boolean };
  previous_window: { from: string; to: string } | null;
  data: {
    link: { key: string; label: string; content_key: string };
    totals: { clicks: number; previous_clicks: number | null; all_time: number; last_clicked_at: string | null };
    timeseries: DayCount[];
    previous_timeseries: DayCount[] | null;
    by_referer: BreakdownRow[];
    siblings: Sibling[];
    truncated?: boolean;
  };
}

/**
 * True only for a real link-performance body. The in-memory dev server has no
 * /api/reports route, so the request falls through to the SPA catch-all and
 * "succeeds" with the HTML shell as a string; treating that as a report threw
 * when the view read report.window (#0169 fallback).
 */
export function isLinkPerformance(x: unknown): x is LinkPerformance {
  if (typeof x !== 'object' || x === null) return false;
  const r = x as { window?: unknown; data?: unknown };
  const w = r.window as { from?: unknown; to?: unknown } | null | undefined;
  return (
    typeof w === 'object' && w !== null && typeof w.from === 'string' && typeof w.to === 'string' &&
    typeof r.data === 'object' && r.data !== null
  );
}

/** Report days are {date, clicks}; the chart takes {date, count}. */
export function toDayBuckets(days: DayCount[] | null | undefined): DayBucket[] {
  return (days ?? []).map((d) => ({ date: d.date, count: d.clicks }));
}

/** "▲ 22%" style change versus the previous period; null when there is nothing to compare. */
export interface Change {
  dir: 'up' | 'down' | 'flat';
  text: string;
  /** Screen-reader wording. */
  label: string;
}
export function percentChange(current: number, previous: number | null | undefined): Change | null {
  if (previous === null || previous === undefined) return null;
  if (previous === 0) {
    return current > 0 ? { dir: 'up', text: '▲ new', label: 'up from no clicks in the previous period' } : null;
  }
  const pct = Math.round(((current - previous) / previous) * 100);
  if (pct === 0) return { dir: 'flat', text: '0%', label: 'unchanged from the previous period' };
  const abs = Math.abs(pct).toLocaleString('en-US');
  return pct > 0
    ? { dir: 'up', text: `▲ ${abs}%`, label: `up ${abs}% from the previous period` }
    : { dir: 'down', text: `▼ ${abs}%`, label: `down ${abs}% from the previous period` };
}

export interface SiblingRow {
  key: string;
  label: string;
  campaign: string | null;
  clicks: number;
  current: boolean;
}
/** This link plus its siblings, most clicks first (ties by label), for the InlineBar list. */
export function siblingRows(
  self: { key: string; label: string; campaign_slug?: string | null; clicks: number },
  siblings: Sibling[] | null | undefined,
): SiblingRow[] {
  const rows: SiblingRow[] = [
    { key: self.key, label: self.label, campaign: self.campaign_slug ?? null, clicks: self.clicks, current: true },
    ...(siblings ?? []).map((s) => ({ key: s.key, label: s.label, campaign: s.campaign_slug, clicks: s.clicks, current: false })),
  ];
  return rows.sort((a, b) => b.clicks - a.clicks || a.label.localeCompare(b.label));
}

/** Referrer display name: the API's "(direct)" is direct traffic and app opens. */
export function referrerLabel(value: string): string {
  return value === '(direct)' || value === '' ? '(direct / app)' : value;
}

/** Strips the scheme for display; the href keeps it. */
export function stripScheme(url: string): string {
  return url.replace(/^https?:\/\//i, '');
}

/** Splits a scheme-less URL after each "/" so the view can offer a line break only there. */
export function urlSegments(display: string): string[] {
  return display.match(/[^/]+\/?|\//g) ?? [display];
}

/** Heading label for a range: "7 days" or "Jul 3 – Aug 1". */
export function rangeLabel(sel: RangeSel, win: { from: string; to: string } | null): string {
  if (sel.kind !== 'custom') return `${sel.kind.replace('d', '')} days`;
  const w = win ?? sel;
  return `${shortDay(w.from)} – ${shortDay(w.to)}`;
}
function shortDay(iso: string): string {
  const [y, m, d] = iso.split('-').map(Number);
  if (!y || !m || !d) return iso;
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' });
}
