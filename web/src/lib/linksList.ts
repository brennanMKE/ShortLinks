// Pure helpers behind the Links list (#0167): the hash state -> GET /api/links
// query, the sort menu, grouping a page by content key, the bulk-action
// messages and "View all links" from the Overview. No DOM, no Svelte.

import type { LinkListQuery } from './api';
import type { Link } from './types';
import { DEFAULT_STATE, emptyFilters, viewPath, type ViewState } from './viewQuery';

export const PER_PAGE = 20;

export interface SortOption {
  /** Value in the hash `sort=` parameter; '' is the default. */
  key: string;
  label: string;
  /** The #0144 `sort` parameter. */
  api: string;
}

export const SORTS: SortOption[] = [
  { key: '', label: 'Clicks, 30 days', api: 'clicks_window' },
  { key: 'clicks', label: 'Clicks, all time', api: 'clicks' },
  { key: 'newest', label: 'Newest', api: 'created' },
  { key: 'title', label: 'Title', api: 'title' },
];

export const STATUSES: { key: string; label: string }[] = [
  { key: '', label: 'Active' },
  { key: 'inactive', label: 'Inactive' },
  { key: 'expired', label: 'Expired' },
  { key: 'all', label: 'All' },
];

export function sortOption(key: string): SortOption {
  return SORTS.find((s) => s.key === key) ?? SORTS[0];
}

export function statusLabel(key: string): string {
  return (STATUSES.find((s) => s.key === key) ?? STATUSES[0]).label;
}

/** The #0144 request for a view. The 30-day column is always requested. */
export function toListQuery(view: ViewState, tz: string, perPage = PER_PAGE): LinkListQuery {
  return {
    page: view.page,
    perPage,
    q: view.q.trim(),
    campaign: view.campaign,
    source: view.filters.source,
    medium: view.filters.medium,
    status: view.status === '' ? 'active' : view.status,
    sort: sortOption(view.sort).api,
    window: '30d',
    tz,
  };
}

/** Any narrowing at all (search, campaign, UTM filter or a non-default status)? */
export function isNarrowed(view: ViewState): boolean {
  return (
    view.q.trim() !== '' ||
    view.campaign !== '' ||
    view.filters.source.length > 0 ||
    view.filters.medium.length > 0 ||
    view.status !== ''
  );
}

/** Whether a link that was just created would appear in this view (SSE prepend). */
export function acceptsNewLink(view: ViewState): boolean {
  return view.page === 1 && view.sort === 'newest' && !isNarrowed(view);
}

/**
 * "View all links" from the Overview: carries the campaign focus and the
 * Source and Medium filters, the ones the links list can apply. (The other UTM
 * filters have no equivalent in GET /api/links.)
 */
export function linksHrefFromOverview(view: ViewState): string {
  const s: ViewState = {
    ...DEFAULT_STATE,
    campaign: view.campaign,
    filters: { ...emptyFilters(), source: view.filters.source, medium: view.filters.medium },
  };
  return viewPath(s, '/links');
}

// ── Grouping by content ─────────────────────────────────────────────────────

export type ListRow =
  | { kind: 'link'; id: string; link: Link; child: boolean }
  | { kind: 'group'; id: string; contentKey: string; label: string; links: Link[]; open: boolean };

/**
 * Collapses links that share a content key into one header row (expandable to
 * its links). A content with a single link on the page stays a plain row.
 * Order follows each content's first link, so the server's sort is kept.
 * Only the links on the current page are grouped, since the API pages links.
 */
export function groupRows(links: Link[], grouped: boolean, expanded: ReadonlySet<string>): ListRow[] {
  if (!grouped) return links.map((l) => ({ kind: 'link', id: l.key, link: l, child: false }));
  const order: string[] = [];
  const byKey = new Map<string, Link[]>();
  for (const l of links) {
    const ck = l.content_key || 'link:' + l.key;
    if (!byKey.has(ck)) {
      byKey.set(ck, []);
      order.push(ck);
    }
    byKey.get(ck)!.push(l);
  }
  const rows: ListRow[] = [];
  for (const ck of order) {
    const ls = byKey.get(ck)!;
    if (ls.length === 1) {
      rows.push({ kind: 'link', id: ls[0].key, link: ls[0], child: false });
      continue;
    }
    const open = expanded.has(ck);
    rows.push({ kind: 'group', id: 'group:' + ck, contentKey: ck, label: ls[0].label || ls[0].title || ls[0].key, links: ls, open });
    if (open) for (const l of ls) rows.push({ kind: 'link', id: l.key, link: l, child: true });
  }
  return rows;
}

export function sumClicks(links: Link[], get: (l: Link) => number): number {
  return links.reduce((n, l) => n + (get(l) || 0), 0);
}

// ── Bulk actions ────────────────────────────────────────────────────────────

export function plural(n: number, one: string, many: string): string {
  return n === 1 ? one : many;
}

export function copiedMessage(n: number): string {
  return `Copied ${n} short ${plural(n, 'URL', 'URLs')}`;
}

export function bulkDeactivatedMessage(n: number): string {
  return `${n} ${plural(n, 'link', 'links')} deactivated`;
}

/** `campaign` is the campaign's name, or '' for "removed from their campaign". */
export function bulkAssignedMessage(n: number, campaign: string): string {
  const links = `${n} ${plural(n, 'link', 'links')}`;
  return campaign ? `${links} added to ${campaign}` : `${links} removed from ${n === 1 ? 'its' : 'their'} campaign`;
}

export function bulkFailureMessage(done: number, failed: number, verb: string): string {
  if (done === 0) return `Could not ${verb} ${failed === 1 ? 'that link' : 'those links'}. Please try again.`;
  return `${done} done, ${failed} could not be changed. Please try again.`;
}

/** Keys grouped by the campaign slug each link had ('' = none), so an undo restores each. */
export function previousCampaigns(links: Link[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const l of links) {
    const slug = l.campaign_slug ?? '';
    if (!out.has(slug)) out.set(slug, []);
    out.get(slug)!.push(l.key);
  }
  return out;
}
