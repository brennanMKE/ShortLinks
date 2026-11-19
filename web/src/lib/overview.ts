// Pure view-model logic for the Overview (#0162): change labels, campaign
// chips, and the Top content rows for each grouping. No DOM, no Svelte, so
// overview.test.ts covers it directly.

import type {
  BreakdownData,
  ContentGroup,
  OverviewCampaign,
  SourceCount,
  TopLink,
} from './reports';

// ── Change versus the previous period ────────────────────────────────────

export interface Change {
  kind: 'up' | 'down' | 'flat' | 'new' | 'none';
  /** Whole percent, absolute value. Only for up/down. */
  pct: number;
  /** Text for the tile or cell: "18%", "new", "0%" or "". */
  text: string;
  /** Arrow glyph, or "". */
  arrow: string;
  /** Hover text with the absolute prior number. */
  title: string;
}

const NONE: Change = { kind: 'none', pct: 0, text: '', arrow: '', title: '' };

/**
 * Compares a count with the previous period's. A previous period of zero
 * reads "new" (a percentage of zero is meaningless); a missing previous
 * period, or zero in both, has no change to show.
 */
export function changeOf(cur: number, prev: number | null | undefined): Change {
  if (prev === null || prev === undefined) return NONE;
  if (prev === 0) {
    return cur > 0 ? { kind: 'new', pct: 0, text: 'new', arrow: '', title: 'Previous period: 0' } : NONE;
  }
  const pct = Math.round((Math.abs(cur - prev) / prev) * 100);
  const title = `Previous period: ${prev.toLocaleString()}`;
  if (cur === prev || pct === 0) return { kind: 'flat', pct: 0, text: '0%', arrow: '', title };
  return cur > prev
    ? { kind: 'up', pct, text: `${pct}%`, arrow: '▲', title }
    : { kind: 'down', pct, text: `${pct}%`, arrow: '▼', title };
}

// ── Campaign chips ───────────────────────────────────────────────────────

export interface Chip {
  /** Campaign slug, or 'none' for clicks with no campaign. */
  value: string;
  label: string;
  clicks: number;
}

export interface ChipLayout {
  /** Shown in the row after "All". */
  visible: Chip[];
  /** Behind the More menu. */
  more: Chip[];
}

/** Campaign chips shown in the row after "All"; the rest go behind More. */
export const MAX_CHIPS = 5;

/**
 * Orders the report's campaigns for the chip row: campaigns by clicks, then
 * "No campaign" last. A selected campaign with no clicks in the window still
 * gets a chip so the focus does not silently vanish.
 */
export function campaignChips(campaigns: OverviewCampaign[], selected: string): Chip[] {
  const named = campaigns
    .filter((c) => c.slug !== null && c.clicks > 0)
    .map((c) => ({ value: c.slug as string, label: c.name, clicks: c.clicks }))
    .sort((a, b) => b.clicks - a.clicks);
  const none = campaigns.find((c) => c.slug === null && c.clicks > 0);
  const chips = [...named];
  if (none) chips.push({ value: 'none', label: 'No campaign', clicks: none.clicks });
  if (selected && !chips.some((c) => c.value === selected)) {
    chips.push({ value: selected, label: selected === 'none' ? 'No campaign' : selected, clicks: 0 });
  }
  return chips;
}

/**
 * Splits chips between the row and the More menu: "All" plus the top five
 * campaigns. A selected chip that would land in More takes the last
 * visible slot, so the active focus is always on screen.
 */
export function layoutChips(chips: Chip[], selected: string, max = MAX_CHIPS): ChipLayout {
  const room = Math.max(0, max);
  if (chips.length <= room) return { visible: chips, more: [] };
  let visible = chips.slice(0, room);
  let more = chips.slice(room);
  const sel = more.find((c) => c.value === selected);
  if (sel && room > 0) {
    const bumped = visible[room - 1];
    visible = [...visible.slice(0, room - 1), sel];
    more = [bumped, ...more.filter((c) => c !== sel)];
  }
  return { visible, more };
}

// ── Stacked source bar ───────────────────────────────────────────────────

export interface Segment {
  label: string;
  clicks: number;
  /** Share of the bar, 0-100. */
  pct: number;
  /** 1-based categorical slot (--series-N); 0 for the muted "other". */
  slot: number;
}

/** Sources shown by name before the rest fold into "other". */
export const SOURCE_SLOTS = 3;

/** "(none)" for the report's empty-value token. */
export function sourceLabel(value: string): string {
  return value === '~none' || value === '' ? '(none)' : value;
}

/** Colour slots (--series-1..6); sources beyond them share the muted "other" colour. */
export const COLOR_SLOTS = 6;

/** A source's stable colour slot (1-based), or 0 for the muted colour. */
export type SourceColors = Map<string, number>;

/**
 * One colour per source for the whole list: sources ranked by their total
 * clicks across every row (ties by name), the top six get --series-1..6. The
 * same source is therefore the same colour in every row and every grouping.
 */
export function sourceColors(items: SourceCount[]): SourceColors {
  const totals = new Map<string, number>();
  for (const it of items) totals.set(it.value, (totals.get(it.value) ?? 0) + it.clicks);
  const order = [...totals.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1));
  const map: SourceColors = new Map();
  order.slice(0, COLOR_SLOTS).forEach(([v], i) => map.set(v, i + 1));
  return map;
}

/**
 * Top sources by clicks as bar segments; the rest fold into "other". With
 * `colors` each segment takes its source's global slot; without, per-row rank.
 */
export function sourceSegments(by: SourceCount[], colors?: SourceColors, slots = SOURCE_SLOTS): Segment[] {
  const sorted = [...by].filter((s) => s.clicks > 0).sort((a, b) => b.clicks - a.clicks);
  const total = sorted.reduce((n, s) => n + s.clicks, 0);
  if (total === 0) return [];
  const head = sorted.slice(0, slots);
  const rest = sorted.slice(slots);
  const segs: Segment[] = head.map((s, i) => ({
    label: sourceLabel(s.value),
    clicks: s.clicks,
    pct: (s.clicks / total) * 100,
    slot: colors ? (colors.get(s.value) ?? 0) : i + 1,
  }));
  const restClicks = rest.reduce((n, s) => n + s.clicks, 0);
  if (restClicks > 0) {
    segs.push({ label: `other (${rest.length})`, clicks: restClicks, pct: (restClicks / total) * 100, slot: 0 });
  }
  return segs;
}

// ── Top content rows ─────────────────────────────────────────────────────

export interface ChildRow {
  key: string;
  label: string;
  meta: string;
  clicks: number;
}

export interface TopRow {
  id: string;
  title: string;
  /** Muted second line: path and link count, or key and source. */
  meta: string;
  segments: Segment[];
  clicks: number;
  change: Change;
  children: ChildRow[];
  /** The /u/ key for rows that are a single link (group by Link). */
  linkKey?: string;
}

export function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}

function groupChange(g: ContentGroup): Change {
  if (g.previous_clicks === null || g.previous_clicks === undefined) return NONE;
  return changeOf(g.clicks, g.previous_clicks);
}

/** Rows for "Group by Content": one per content group, links as children. */
export function contentRows(groups: ContentGroup[], colors?: SourceColors): TopRow[] {
  return groups.map((g) => ({
    id: g.content_key,
    title: g.title || g.path || g.content_key,
    meta: `${g.path} · ${g.links.length} ${plural(g.links.length, 'link', 'links')}`,
    segments: sourceSegments(g.by_source, colors),
    clicks: g.clicks,
    change: groupChange(g),
    children: g.links.map((l) => ({
      key: l.key,
      label: l.label || l.key,
      meta: [`/u/${l.key}`, l.utm_medium, l.utm_source].filter(Boolean).join(' · '),
      clicks: l.clicks,
    })),
  }));
}

/** Rows for "Group by Link": the content title, then `label · /u/key · source`. */
export function linkRows(links: TopLink[], colors?: SourceColors): TopRow[] {
  return links.map((l) => ({
    id: l.key,
    title: l.content_title || l.title || l.key,
    meta: [l.label, '/u/' + l.key, l.utm_source].filter(Boolean).join(' · '),
    segments: l.utm_source
      ? [{ label: l.utm_source, clicks: l.clicks, pct: 100, slot: colors ? (colors.get(l.utm_source) ?? 0) : 1 }]
      : [],
    clicks: l.clicks,
    change: l.previous_clicks === null ? NONE : changeOf(l.clicks, l.previous_clicks),
    children: [],
    linkKey: l.key,
  }));
}

/** Rows for "Group by Source": one per utm_source value; the bar is its share. */
export function sourceRows(b: BreakdownData, colors?: SourceColors): TopRow[] {
  const rows: TopRow[] = b.rows.map((r, i) => ({
    id: r.value,
    title: sourceLabel(r.value),
    meta: `${Math.round(r.share * 100)}% of clicks`,
    segments: [{ label: sourceLabel(r.value), clicks: r.clicks, pct: 100, slot: colors ? (colors.get(r.value) ?? 0) : (i % 6) + 1 }],
    clicks: r.clicks,
    change: r.previous_clicks === null ? NONE : changeOf(r.clicks, r.previous_clicks),
    children: [],
  }));
  if (b.other && b.other.clicks > 0) {
    rows.push({
      id: '~other',
      title: `${b.other.values} other ${plural(b.other.values, 'source', 'sources')}`,
      meta: b.total > 0 ? `${Math.round((b.other.clicks / b.total) * 100)}% of clicks` : '',
      segments: [{ label: 'other', clicks: b.other.clicks, pct: 100, slot: 0 }],
      clicks: b.other.clicks,
      change: NONE,
      children: [],
    });
  }
  return rows;
}

/** Bar width of a row relative to the largest row, 0-100. */
export function barWidth(clicks: number, max: number): number {
  return max > 0 ? Math.max(2, Math.round((clicks / max) * 100)) : 0;
}

// ── Labels ───────────────────────────────────────────────────────────────

/** "Tue, Nov 10" from a YYYY-MM-DD, in UTC so the day never shifts. */
export function dayLabel(iso: string): string {
  const d = new Date(iso + 'T00:00:00Z');
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC' });
}
