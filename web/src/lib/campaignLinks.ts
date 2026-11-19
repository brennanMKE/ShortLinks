// Data shaping for the campaign Links table (#0157): content grouping from the
// Go `top-content` report (#0140), channel/meta lines, share, and the CSV of a
// selection. Pure functions, no DOM.

import type { CampaignLinkRow } from './campaigns';
import type { CampaignStats } from './types';
import { shortUrl } from './links';

// ── Report shapes (internal/reports/top_content.go) ───────────────────────

export interface TopContentLink {
  key: string;
  label: string;
  utm_source: string;
  utm_medium: string;
  campaign_slug: string | null;
  clicks: number;
}

export interface TopContentGroup {
  content_key: string;
  title: string;
  path: string;
  clicks: number;
  links: TopContentLink[];
}

export interface TopContentData {
  groups: TopContentGroup[];
  total_groups: number;
  truncated?: boolean;
}

/** The report window for a campaign's resolved stats window. */
export interface ReportWindow {
  from: string;
  /** Inclusive last day (the stats window is half-open). */
  to: string;
}

function subtractDay(value: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!m) return null;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]) - 86_400_000);
  return d.toISOString().slice(0, 10);
}

/**
 * The inclusive from/to the reports need to cover the same days the campaign's
 * stats window ([from, to), UTC) covers. null for a missing or empty window.
 */
export function reportWindow(
  stats: Pick<CampaignStats, 'window_from' | 'window_to'> | null | undefined,
): ReportWindow | null {
  if (!stats?.window_from || !stats?.window_to) return null;
  const to = subtractDay(stats.window_to);
  if (!to || to < stats.window_from) return null;
  return { from: stats.window_from, to };
}

// ── Groups ────────────────────────────────────────────────────────────────

export interface ContentRow extends CampaignLinkRow {
  /** Channel suffix cut from the title by the Go grouping ("Mastodon guide post 84"). */
  label: string;
}

export interface ViewGroup {
  id: string;
  title: string;
  path: string;
  links: ContentRow[];
  clicks: number;
  /** 0-100, share of all clicks in the table. */
  share: number;
}

/** Destination path ("/guides/x"), "" when unparseable. */
export function destinationPath(url: string): string {
  try {
    const p = new URL(url).pathname.replace(/\/+$/, '');
    return p === '' ? '/' : p;
  } catch {
    return '';
  }
}

export function shareOf(clicks: number, total: number): number {
  return total > 0 ? Math.round((clicks / total) * 100) : 0;
}

/**
 * Groups the campaign's links by the report's content groups. Links the report
 * did not return (report missing a link, or a stale response) become their own
 * single-link group, so every row is always shown.
 */
export function buildViewGroups(
  rows: CampaignLinkRow[],
  groups: TopContentGroup[] | null | undefined,
): ViewGroup[] {
  const total = rows.reduce((s, r) => s + r.clicksInWindow, 0);
  const byKey = new Map(rows.map((r) => [r.key, r]));
  const placed = new Set<string>();
  const out: ViewGroup[] = [];

  for (const g of groups ?? []) {
    const links: ContentRow[] = [];
    for (const gl of g.links) {
      const row = byKey.get(gl.key);
      if (!row || placed.has(gl.key)) continue;
      placed.add(gl.key);
      links.push({ ...row, label: gl.label });
    }
    if (links.length === 0) continue;
    links.sort((a, b) => b.clicksInWindow - a.clicksInWindow || a.key.localeCompare(b.key));
    const clicks = links.reduce((s, l) => s + l.clicksInWindow, 0);
    out.push({
      id: g.content_key,
      title: g.title || links[0].title || links[0].key,
      path: g.path || destinationPath(links[0].destination_url),
      links,
      clicks,
      share: shareOf(clicks, total),
    });
  }
  for (const r of rows) {
    if (placed.has(r.key)) continue;
    out.push({
      id: `link:${r.key}`,
      title: r.title || r.key,
      path: destinationPath(r.destination_url),
      links: [{ ...r, label: '' }],
      clicks: r.clicksInWindow,
      share: shareOf(r.clicksInWindow, total),
    });
  }
  return out;
}

export type SortKey = 'clicks' | 'link';

export function sortViewGroups(groups: ViewGroup[], key: SortKey, dir: 'asc' | 'desc'): ViewGroup[] {
  const sign = dir === 'asc' ? 1 : -1;
  return [...groups].sort((a, b) => {
    const d =
      key === 'clicks'
        ? a.clicks - b.clicks
        : a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' });
    return sign * d || a.title.localeCompare(b.title);
  });
}

// ── Table rows ────────────────────────────────────────────────────────────

export type TableRow =
  | { kind: 'group'; id: string; group: ViewGroup; expanded: boolean }
  | { kind: 'link'; id: string; link: ContentRow; share: number; child: boolean; title: string; path: string };

/**
 * Flattens groups into table rows. A one-link group is a plain link row; a
 * multi-link group is a group row followed, when expanded, by its links.
 */
export function flattenGroups(groups: ViewGroup[], expanded: ReadonlySet<string>, total: number): TableRow[] {
  const out: TableRow[] = [];
  for (const g of groups) {
    if (g.links.length === 1) {
      const l = g.links[0];
      out.push({ kind: 'link', id: l.key, link: l, share: shareOf(l.clicksInWindow, total), child: false, title: g.title, path: g.path });
      continue;
    }
    const open = expanded.has(g.id);
    out.push({ kind: 'group', id: `group:${g.id}`, group: g, expanded: open });
    if (open) {
      for (const l of g.links) {
        out.push({
          kind: 'link',
          id: l.key,
          link: l,
          share: shareOf(l.clicksInWindow, total),
          child: true,
          title: l.label || l.title || l.key,
          path: g.path,
        });
      }
    }
  }
  return out;
}

/** Flat (ungrouped) rows: the clean content title leads, the channel suffix moves to line 2. */
export function flatRows(groups: ViewGroup[], key: SortKey, dir: 'asc' | 'desc', total: number): TableRow[] {
  const links: Extract<TableRow, { kind: 'link' }>[] = [];
  for (const g of groups) {
    for (const l of g.links) {
      links.push({ kind: 'link', id: l.key, link: l, share: shareOf(l.clicksInWindow, total), child: false, title: g.title, path: g.path });
    }
  }
  const sign = dir === 'asc' ? 1 : -1;
  return links.sort((a, b) => {
    const d =
      key === 'clicks'
        ? a.link.clicksInWindow - b.link.clicksInWindow
        : a.title.localeCompare(b.title, undefined, { numeric: true, sensitivity: 'base' });
    return sign * d || a.link.key.localeCompare(b.link.key);
  });
}

// ── Cell text ─────────────────────────────────────────────────────────────

/** "mastodon · social"; just the one that is set or when they match ("email"); "" when neither is. */
export function channelText(l: Pick<CampaignLinkRow, 'utm_source' | 'utm_medium'>): string {
  if (l.utm_source && l.utm_source === l.utm_medium) return l.utm_source;
  return [l.utm_source, l.utm_medium].filter(Boolean).join(' · ');
}

/** Line 2 of a link cell: `/u/key · label · /path` (empty parts dropped). */
export function linkMeta(key: string, label: string, path: string): string {
  return [`/u/${key}`, label, path].filter(Boolean).join(' · ');
}

/** Distinct sources of a group's links, e.g. "mastodon · bluesky · email". */
export function groupChannels(links: Pick<CampaignLinkRow, 'utm_source'>[]): string {
  const seen: string[] = [];
  for (const l of links) if (l.utm_source && !seen.includes(l.utm_source)) seen.push(l.utm_source);
  return seen.join(' · ');
}

/** Short form for a narrow column: the names when two or fewer, else "3 channels". */
export function groupChannelsShort(links: Pick<CampaignLinkRow, 'utm_source'>[]): string {
  const names = groupChannels(links);
  const n = names === '' ? 0 : names.split(' · ').length;
  return n > 2 ? `${n} channels` : names;
}

export function hasAnyPlacement(rows: Pick<CampaignLinkRow, 'placement'>[]): boolean {
  return rows.some((r) => (r.placement ?? '').trim() !== '');
}

// ── CSV of the selection ──────────────────────────────────────────────────

function csvCell(v: string | number): string {
  const s = String(v);
  return /[",\n\r]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

export function selectedLinksCsv(rows: CampaignLinkRow[]): string {
  const header = ['key', 'short_url', 'title', 'destination_url', 'utm_source', 'utm_medium', 'placement', 'clicks', 'share_percent'];
  const lines = [header.join(',')];
  for (const r of rows) {
    lines.push(
      [r.key, shortUrl(r.key), r.title, r.destination_url, r.utm_source, r.utm_medium, r.placement, r.clicksInWindow, r.shareOfTotal]
        .map(csvCell)
        .join(','),
    );
  }
  return lines.join('\r\n') + '\r\n';
}
