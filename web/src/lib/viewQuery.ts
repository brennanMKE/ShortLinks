// Hash-query state shared by the Overview, Links and Campaign detail (#0162,
// #0163): range, campaign focus, the UTM filters, grouping, sort and tab.
// Pure (no DOM, no Svelte) so viewQuery.test.ts covers it directly. See
// docs/ui-redesign.md section 3.2 (URL state).
//
//   #/?range=30d&campaign=guides-2026-11&source=mastodon,bluesky&medium=social&group=content
//   #/?from=2026-10-01&to=2026-10-31&placement=18th+%26+Texas+board
//
// UTM values are comma-joined with each value encodeURIComponent-ed, so a
// comma or ampersand inside a value survives; `~none` is the empty bucket.
// Because a decoded query cannot tell a separator comma from a value comma,
// parse() reads the RAW query (RouteState.rawQuery), not URLSearchParams.

/** 'campaign' (the campaign's own dates) is only a valid range for a base whose range is 'campaign' (#0164). */
export type RangeKey = '7d' | '30d' | '90d' | 'custom' | 'campaign';
export type GroupBy = 'content' | 'link' | 'source';

/** Filterable UTM-style dimensions, in display order. Names are the query keys and the reports' parameter names. */
export const UTM_KEYS = ['source', 'medium', 'utm_campaign', 'content', 'placement', 'term'] as const;
export type UtmKey = (typeof UTM_KEYS)[number];
export type UtmFilters = Record<UtmKey, string[]>;

/** The filter token for "no value". Mirrors internal/reports NoneValue. */
export const NONE = '~none';

export interface ViewState {
  range: RangeKey;
  /** YYYY-MM-DD; only meaningful when range is 'custom'. */
  from: string;
  to: string;
  /** Campaign slug, 'none' for clicks with no campaign, '' for All. */
  campaign: string;
  /** OR within a key, AND across keys. */
  filters: UtmFilters;
  group: GroupBy;
  /** A view-specific sort key; '' is the view's default. */
  sort: string;
  /** A view-specific tab key; '' is the view's default. */
  tab: string;
  /** Links list search text (#0167); '' is no search. */
  q: string;
  /** Links list status (#0167): '' is Active; 'inactive', 'expired' or 'all'. */
  status: string;
  /** Links list page, 1-based (#0167). */
  page: number;
}

export function emptyFilters(): UtmFilters {
  return { source: [], medium: [], utm_campaign: [], content: [], placement: [], term: [] };
}

export const DEFAULT_STATE: ViewState = {
  range: '30d',
  from: '',
  to: '',
  campaign: '',
  filters: emptyFilters(),
  group: 'content',
  sort: '',
  tab: '',
  q: '',
  status: '',
  page: 1,
};

const DAY = /^\d{4}-\d{2}-\d{2}$/;

function decode(s: string): string {
  try {
    return decodeURIComponent(s.replace(/\+/g, ' '));
  } catch {
    return s; // a malformed escape is kept literally rather than throwing
  }
}

/** The first occurrence of each key, values still encoded. */
function rawPairs(query: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const part of query.replace(/^\?/, '').split('&')) {
    if (part === '') continue;
    const eq = part.indexOf('=');
    const key = decode(eq === -1 ? part : part.slice(0, eq));
    if (!out.has(key)) out.set(key, eq === -1 ? '' : part.slice(eq + 1));
  }
  return out;
}

/** Splits a raw comma-joined value list into decoded, de-duplicated, non-empty values. */
function parseList(raw: string): string[] {
  const out: string[] = [];
  for (const piece of raw.split(',')) {
    const v = decode(piece).trim();
    if (v !== '' && !out.includes(v)) out.push(v);
  }
  return out;
}

/**
 * Reads the view state from a raw hash query. Unknown or malformed values fall
 * back to `base`, the view's defaults (DEFAULT_STATE unless a view has its
 * own, e.g. the campaign page's range of 'campaign').
 */
export function parse(query: string, base: ViewState = DEFAULT_STATE): ViewState {
  const q = rawPairs(query);
  const s: ViewState = { ...base, filters: emptyFilters() };
  const val = (k: string) => decode(q.get(k) ?? '');

  const from = val('from');
  const to = val('to');
  if (DAY.test(from)) {
    s.range = 'custom';
    s.from = from;
    s.to = DAY.test(to) && to >= from ? to : '';
  } else {
    const r = val('range');
    if (r === '7d' || r === '30d' || r === '90d') s.range = r;
    else if (r === 'campaign' && base.range === 'campaign') s.range = r;
  }
  s.campaign = val('campaign');
  for (const k of UTM_KEYS) s.filters[k] = parseList(q.get(k) ?? '');
  const g = val('group');
  // '0' is #0157's older "grouping off" for the campaign Links table.
  if (g === 'link' || g === 'source') s.group = g;
  else if (g === '0') s.group = 'link';
  s.sort = val('sort');
  s.tab = val('tab');
  s.q = val('q');
  const st = val('status');
  if (st === 'inactive' || st === 'expired' || st === 'all') s.status = st;
  const pg = Number(val('page'));
  if (Number.isInteger(pg) && pg > 1) s.page = pg;
  return s;
}

/** True when any UTM filter has a value. */
export function hasFilters(f: UtmFilters): boolean {
  return UTM_KEYS.some((k) => f[k].length > 0);
}

/** Serialises state to a query string (no leading '?'); defaults are omitted. */
export function serialize(s: ViewState, base: ViewState = DEFAULT_STATE): string {
  const parts: string[] = [];
  const add = (k: string, v: string) => parts.push(k + '=' + v);
  if (s.range === 'custom') {
    if (s.from) {
      add('from', s.from);
      if (s.to) add('to', s.to);
    }
  } else if (s.range !== base.range) {
    add('range', s.range);
  }
  if (s.campaign) add('campaign', encodeURIComponent(s.campaign));
  for (const k of UTM_KEYS) {
    if (s.filters[k].length > 0) add(k, s.filters[k].map(encodeURIComponent).join(','));
  }
  if (s.group !== base.group) add('group', s.group);
  if (s.sort) add('sort', encodeURIComponent(s.sort));
  if (s.tab) add('tab', encodeURIComponent(s.tab));
  if (s.q) add('q', encodeURIComponent(s.q));
  if (s.status) add('status', s.status);
  if (s.page > 1) add('page', String(s.page));
  return parts.join('&');
}

/** The hash path for a state at `base`: '/' or '/?range=7d&...'. */
export function viewPath(s: ViewState, base = '/', defaults: ViewState = DEFAULT_STATE): string {
  const qs = serialize(s, defaults);
  return qs ? base + '?' + qs : base;
}

/** Filter values match case-insensitively (the reports lower-case facet values and compare that way). */
export function sameValue(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}

/** Returns `s` with the values of one dimension replaced. */
export function withFilter(s: ViewState, key: UtmKey, values: string[]): ViewState {
  return { ...s, filters: { ...s.filters, [key]: values } };
}

/** Returns `s` with every UTM filter cleared. */
export function clearFilters(s: ViewState): ViewState {
  return { ...s, filters: emptyFilters() };
}

/**
 * Query for a /api/reports/{name} call: window, tz, campaign scope and the UTM
 * filters. `extra` is appended last.
 *
 * Several values go as repeated parameters, which the reports take whole. A
 * parameter sent once is still read as the older comma-separated list, so a
 * lone value has its commas (and backslashes) escaped with a backslash. Either
 * way a value that contains a comma matches exactly (#0187).
 */
/**
 * Appends one filter parameter's values: a repeated parameter per value; a
 * lone value has its commas and backslashes escaped (#0187). Shared by the
 * reports and GET /api/links, which parse filters the same way.
 */
export function appendFilterValues(q: URLSearchParams, key: string, vals: string[]): void {
  for (const v of vals) q.append(key, vals.length === 1 ? v.replace(/[\\,]/g, '\\$&') : v);
}

export function reportQuery(s: ViewState, tz: string, extra: Record<string, string> = {}): string {
  const q = new URLSearchParams();
  q.set('tz', tz);
  if (s.range === 'custom' && s.from) {
    q.set('from', s.from);
    if (s.to) q.set('to', s.to);
  } else {
    q.set('range', s.range === 'custom' || s.range === 'campaign' ? '30d' : s.range);
  }
  if (s.campaign) q.set('campaign', s.campaign);
  for (const k of UTM_KEYS) {
    appendFilterValues(q, k, s.filters[k]);
  }
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  return q.toString();
}

// ── Facets ──────────────────────────────────────────────────────────────────

export interface FacetValue {
  value: string;
  clicks: number;
}

export const FILTER_LABELS: Record<UtmKey, string> = {
  source: 'Source',
  medium: 'Medium',
  utm_campaign: 'UTM campaign',
  content: 'Content',
  placement: 'Placement',
  term: 'Term',
};

/** Display text of a filter value. */
export function valueLabel(v: string): string {
  return v === NONE ? '(none)' : v;
}

/**
 * True when every UTM campaign value is exactly the slug of one ShortLinks
 * campaign (and none is empty), so the dropdown would only repeat the focus
 * chips. Hidden then, unless a utm_campaign filter is already active.
 */
export function utmCampaignRedundant(values: FacetValue[], campaignSlugs: string[]): boolean {
  if (values.length === 0) return false;
  const slugs = new Set(campaignSlugs);
  return values.every((v) => v.value !== NONE && slugs.has(v.value));
}

export interface PrunedFilters {
  filters: UtmFilters;
  /** Values removed because no facet lists them, as "Label: value". */
  dropped: string[];
}

/**
 * Removes filter values the facets do not list (a deleted campaign, a typo in
 * a pasted link). A facet list at its limit may be incomplete, so it is left
 * alone. Each facet is computed with its own dimension's filter skipped, so
 * a selected value that has clicks always appears in its own facet.
 */
export function pruneFilters(
  filters: UtmFilters,
  facets: Partial<Record<UtmKey, FacetValue[]>>,
  limit: number,
): PrunedFilters {
  const out = emptyFilters();
  const dropped: string[] = [];
  for (const k of UTM_KEYS) {
    const list = facets[k];
    const complete = list !== undefined && list.length < limit;
    for (const v of filters[k]) {
      if (!complete || v === NONE || list!.some((f) => sameValue(f.value, v))) out[k].push(v);
      else dropped.push(`${FILTER_LABELS[k]}: ${v}`);
    }
  }
  return { filters: out, dropped };
}
