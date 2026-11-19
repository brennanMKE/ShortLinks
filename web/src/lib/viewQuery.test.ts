import { describe, expect, it } from 'vitest';
import {
  DEFAULT_STATE,
  NONE,
  UTM_KEYS,
  clearFilters,
  emptyFilters,
  hasFilters,
  parse,
  pruneFilters,
  reportQuery,
  serialize,
  utmCampaignRedundant,
  viewPath,
  withFilter,
  type ViewState,
} from './viewQuery';
import { parseHash } from './routing';

const state = (over: Partial<ViewState> = {}): ViewState => ({ ...DEFAULT_STATE, filters: emptyFilters(), ...over });

describe('viewQuery defaults', () => {
  it('omits defaults', () => {
    expect(serialize(DEFAULT_STATE)).toBe('');
    expect(viewPath(DEFAULT_STATE)).toBe('/');
    expect(viewPath(DEFAULT_STATE, '/links')).toBe('/links');
  });
  it('parses an empty query to the defaults', () => {
    expect(parse('')).toEqual(DEFAULT_STATE);
    expect(parse('?')).toEqual(DEFAULT_STATE);
  });
  it('does not share the filters object between states', () => {
    const a = parse('');
    a.filters.source.push('x');
    expect(parse('').filters.source).toEqual([]);
    expect(DEFAULT_STATE.filters.source).toEqual([]);
  });
});

describe('viewQuery round trip', () => {
  it('round-trips every key', () => {
    const s = state({
      range: '90d',
      campaign: 'guides-2026-11',
      filters: {
        source: ['mastodon', 'bluesky'],
        medium: ['social'],
        utm_campaign: ['guides-2026-11'],
        content: ['guide-1'],
        placement: ['Mission St pole'],
        term: ['esp32'],
      },
      group: 'source',
      sort: 'change',
      tab: 'links',
    });
    expect(parse(serialize(s))).toEqual(s);
  });
  it('round-trips a custom range', () => {
    const c = state({ range: 'custom', from: '2026-10-01', to: '2026-10-31', campaign: 'none', group: 'link' });
    expect(parse(serialize(c))).toEqual(c);
    const open = state({ range: 'custom', from: '2026-10-01' });
    expect(serialize(open)).toBe('from=2026-10-01');
    expect(parse(serialize(open))).toEqual(open);
  });
  it('encodes ampersands, commas, spaces and unicode inside values', () => {
    const s = state({
      campaign: 'a&b',
      filters: { ...emptyFilters(), placement: ['18th & Texas board', 'a,b', 'café = 100%'], content: ['x#y'] },
    });
    const qs = serialize(s);
    expect(qs).toContain('placement=18th%20%26%20Texas%20board,a%2Cb,caf%C3%A9%20%3D%20100%25');
    expect(qs.split('&')).toHaveLength(3); // campaign, content, placement: the & inside values never splits
    expect(parse(qs)).toEqual(s);
  });
  it('survives the router: a value with a comma stays one value', () => {
    const s = state({ filters: { ...emptyFilters(), source: ['a,b', 'c'] } });
    const { rawQuery } = parseHash('#/?' + serialize(s));
    expect(parse(rawQuery).filters.source).toEqual(['a,b', 'c']);
  });
  it('keeps ~none as the empty bucket', () => {
    const s = state({ filters: { ...emptyFilters(), medium: [NONE, 'social'] } });
    expect(serialize(s)).toBe('medium=~none,social');
    expect(parse('medium=~none,social')).toEqual(s);
  });
  it('accepts + as a space when pasted by hand', () => {
    expect(parse('placement=18th+%26+Texas+board').filters.placement).toEqual(['18th & Texas board']);
  });
  it('orders keys stably', () => {
    const s = state({ group: 'link', filters: { ...emptyFilters(), term: ['t'], source: ['s'] } });
    expect(serialize(s)).toBe('source=s&term=t&group=link');
  });
});

describe('viewQuery parse robustness', () => {
  it('ignores malformed values', () => {
    expect(parse('range=5y&group=x&from=nope')).toEqual(DEFAULT_STATE);
    expect(parse('from=2026-10-10&to=2026-10-01').to).toBe('');
  });
  it('drops empty and duplicate list entries', () => {
    expect(parse('source=a,,a,%20,b').filters.source).toEqual(['a', 'b']);
    expect(parse('source=').filters.source).toEqual([]);
  });
  it('keeps a malformed escape literally instead of throwing', () => {
    expect(parse('content=100%').filters.content).toEqual(['100%']);
  });
  it('takes the first of a repeated key and ignores unknown keys', () => {
    expect(parse('range=7d&range=90d&zzz=1').range).toBe('7d');
  });
});

describe('filter helpers', () => {
  it('withFilter and clearFilters do not mutate', () => {
    const s = state();
    const t = withFilter(s, 'source', ['a']);
    expect(s.filters.source).toEqual([]);
    expect(t.filters.source).toEqual(['a']);
    expect(hasFilters(t.filters)).toBe(true);
    expect(hasFilters(clearFilters(t).filters)).toBe(false);
  });
});

describe('reportQuery', () => {
  it('builds report queries with tz and scope', () => {
    expect(reportQuery(DEFAULT_STATE, 'UTC')).toBe('tz=UTC&range=30d');
    const q = new URLSearchParams(
      reportQuery(state({ campaign: 'a b', range: '90d' }), 'America/Los_Angeles', { limit: '5' }),
    );
    expect(q.get('campaign')).toBe('a b');
    expect(q.get('range')).toBe('90d');
    expect(q.get('limit')).toBe('5');
  });
  it('sends each filter value as a repeated parameter and omits sort, tab and group', () => {
    const s = state({
      group: 'link',
      sort: 'x',
      tab: 'y',
      filters: { ...emptyFilters(), source: ['mastodon', 'bluesky'], medium: [NONE] },
    });
    const q = new URLSearchParams(reportQuery(s, 'UTC'));
    expect(q.getAll('source')).toEqual(['mastodon', 'bluesky']);
    expect(q.getAll('medium')).toEqual([NONE]);
    expect(q.has('group')).toBe(false);
    expect(q.has('sort')).toBe(false);
    expect(q.has('tab')).toBe(false);
  });
  it('uses the report parameter names for every UTM key', () => {
    const f = emptyFilters();
    for (const k of UTM_KEYS) f[k] = ['v'];
    const q = new URLSearchParams(reportQuery(state({ filters: f }), 'UTC'));
    for (const k of UTM_KEYS) expect(q.get(k)).toBe('v');
  });
});

describe('utmCampaignRedundant', () => {
  const fv = (...v: string[]) => v.map((value) => ({ value, clicks: 1 }));
  it('is true when every value is a ShortLinks campaign slug', () => {
    expect(utmCampaignRedundant(fv('a', 'b'), ['a', 'b', 'c'])).toBe(true);
  });
  it('is false for a value with no campaign, the empty bucket, or no values', () => {
    expect(utmCampaignRedundant(fv('a', 'events-2026'), ['a'])).toBe(false);
    expect(utmCampaignRedundant(fv('a', NONE), ['a'])).toBe(false);
    expect(utmCampaignRedundant([], ['a'])).toBe(false);
  });
});

describe('pruneFilters', () => {
  const facets = { source: [{ value: 'mastodon', clicks: 3 }], medium: [{ value: 'social', clicks: 3 }] };
  it('drops values no facet lists, with a label for the notice', () => {
    const f = { ...emptyFilters(), source: ['mastodon', 'gone'], medium: [NONE] };
    const r = pruneFilters(f, facets, 50);
    expect(r.filters.source).toEqual(['mastodon']);
    expect(r.filters.medium).toEqual([NONE]);
    expect(r.dropped).toEqual(['Source: gone']);
  });
  it('matches case-insensitively, as the reports do', () => {
    const f = { ...emptyFilters(), source: ['Mastodon'] };
    expect(pruneFilters(f, facets, 50).dropped).toEqual([]);
  });
  it('keeps values when the facet list is at its limit or absent', () => {
    const f = { ...emptyFilters(), source: ['x'], content: ['y'] };
    expect(pruneFilters(f, { source: [{ value: 'a', clicks: 1 }] }, 1).dropped).toEqual([]);
    expect(pruneFilters(f, {}, 50).dropped).toEqual([]);
  });
});
