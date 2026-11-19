// Hash-query state of the Overview (#0162): range, campaign focus and the
// Top content grouping. Pure (no DOM, no Svelte) so overviewQuery.test.ts
// covers it directly. #0163 widens this into the shared viewQuery module with
// the UTM filters; the keys here keep the same names and defaults.

export type RangeKey = '7d' | '30d' | '90d' | 'custom';
export type GroupBy = 'content' | 'link' | 'source';

export interface OverviewState {
  range: RangeKey;
  /** YYYY-MM-DD; only meaningful when range is 'custom'. */
  from: string;
  to: string;
  /** Campaign slug, 'none' for clicks with no campaign, '' for All. */
  campaign: string;
  group: GroupBy;
}

export const DEFAULT_STATE: OverviewState = { range: '30d', from: '', to: '', campaign: '', group: 'content' };

const DAY = /^\d{4}-\d{2}-\d{2}$/;

/** Reads the Overview state from a hash query. Unknown or malformed values fall back to defaults. */
export function parseOverviewQuery(q: URLSearchParams): OverviewState {
  const s: OverviewState = { ...DEFAULT_STATE };
  const from = q.get('from') ?? '';
  const to = q.get('to') ?? '';
  if (DAY.test(from)) {
    s.range = 'custom';
    s.from = from;
    s.to = DAY.test(to) && to >= from ? to : '';
  } else {
    const r = q.get('range');
    if (r === '7d' || r === '30d' || r === '90d') s.range = r;
  }
  s.campaign = q.get('campaign') ?? '';
  const g = q.get('group');
  if (g === 'link' || g === 'source') s.group = g;
  return s;
}

/** Serialises state to a query string (no leading '?'); defaults are omitted. */
export function serializeOverviewQuery(s: OverviewState): string {
  const q = new URLSearchParams();
  if (s.range === 'custom') {
    if (s.from) {
      q.set('from', s.from);
      if (s.to) q.set('to', s.to);
    }
  } else if (s.range !== DEFAULT_STATE.range) {
    q.set('range', s.range);
  }
  if (s.campaign) q.set('campaign', s.campaign);
  if (s.group !== DEFAULT_STATE.group) q.set('group', s.group);
  return q.toString();
}

/** The hash path for a state: '/' or '/?range=7d&...'. */
export function overviewPath(s: OverviewState): string {
  const qs = serializeOverviewQuery(s);
  return qs ? '/?' + qs : '/';
}

/** Query for a /api/reports/{name} call: window, tz and campaign scope. */
export function reportQuery(s: OverviewState, tz: string, extra: Record<string, string> = {}): string {
  const q = new URLSearchParams();
  q.set('tz', tz);
  if (s.range === 'custom' && s.from) {
    q.set('from', s.from);
    if (s.to) q.set('to', s.to);
  } else {
    q.set('range', s.range === 'custom' ? '30d' : s.range);
  }
  if (s.campaign) q.set('campaign', s.campaign);
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  return q.toString();
}
