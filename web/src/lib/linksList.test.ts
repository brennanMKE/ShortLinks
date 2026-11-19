import { describe, expect, it } from 'vitest';
import {
  acceptsNewLink,
  bulkAssignedMessage,
  groupRows,
  flattenGroups,
  groupTitles,
  groupTotals,
  totalNoun,
  isNarrowed,
  linksHrefFromOverview,
  previousCampaigns,
  sortOption,
  sumClicks,
  toListQuery,
} from './linksList';
import { linkListQueryString } from './api';
import { DEFAULT_STATE, emptyFilters, parse, serialize, type ViewState } from './viewQuery';
import type { Link } from './types';

const state = (over: Partial<ViewState> = {}): ViewState => ({ ...DEFAULT_STATE, filters: emptyFilters(), ...over });
const link = (key: string, over: Partial<Link> = {}): Link =>
  ({
    id: 1,
    key,
    title: key,
    destination_url: 'https://x.test/' + key,
    active: true,
    denied_reason: 0,
    created_at: '',
    expires_at: null,
    click_count: 1,
    campaign_id: null,
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
    placement: '',
    ...over,
  }) as Link;

describe('toListQuery', () => {
  it('defaults to active, 30-day clicks, page 1', () => {
    const q = toListQuery(state(), 'UTC');
    expect(q).toMatchObject({ page: 1, status: 'active', sort: 'clicks_window', window: '30d', tz: 'UTC' });
    expect(linkListQueryString(q)).toBe('page=1&per_page=20&status=active&sort=clicks_window&window=30d&tz=UTC&group=content');
  });
  it('maps search, campaign, source, medium, status and sort', () => {
    const v = state({
      q: ' fuse ',
      campaign: 'none',
      status: 'all',
      sort: 'title',
      page: 3,
      filters: { ...emptyFilters(), source: ['a', 'b'], medium: ['social'] },
    });
    const qs = new URLSearchParams(linkListQueryString(toListQuery(v, 'UTC')));
    expect(qs.get('q')).toBe('fuse');
    expect(qs.get('campaign')).toBe('none');
    expect(qs.getAll('source')).toEqual(['a', 'b']);
    expect(qs.get('medium')).toBe('social');
    expect(qs.get('status')).toBe('all');
    expect(qs.get('sort')).toBe('title');
    expect(qs.get('page')).toBe('3');
  });
  it('sends one param per value, escaping a lone value with a comma (#0187)', () => {
    const one = new URLSearchParams(linkListQueryString(toListQuery(state({ filters: { ...emptyFilters(), source: ['a,b'] } }), 'UTC')));
    expect(one.getAll('source')).toEqual(['a\\,b']);
    const two = new URLSearchParams(linkListQueryString(toListQuery(state({ filters: { ...emptyFilters(), medium: ['x,y', 'z'] } }), 'UTC')));
    expect(two.getAll('medium')).toEqual(['x,y', 'z']);
  });
  it('treats an unknown sort as the default', () => {
    expect(sortOption('zzz').api).toBe('clicks_window');
    expect(sortOption('newest').api).toBe('created');
  });
});

describe('hash state', () => {
  it('round-trips q, status, page and sort', () => {
    const s = state({ q: 'a&b', status: 'inactive', page: 2, sort: 'clicks' });
    expect(parse(serialize(s))).toEqual(s);
    expect(serialize(state())).toBe('');
  });
  it('ignores a bad status or page', () => {
    expect(parse('status=zzz&page=-3').status).toBe('');
    expect(parse('status=zzz&page=-3').page).toBe(1);
  });
  it('knows when the view is narrowed', () => {
    expect(isNarrowed(state())).toBe(false);
    expect(isNarrowed(state({ q: 'x' }))).toBe(true);
    expect(isNarrowed(state({ status: 'all' }))).toBe(true);
  });
  it('accepts a live new link only in the unfiltered newest-first first page', () => {
    expect(acceptsNewLink(state())).toBe(false);
    expect(acceptsNewLink(state({ sort: 'newest' }))).toBe(true);
    expect(acceptsNewLink(state({ sort: 'newest', q: 'x' }))).toBe(false);
  });
});

describe('linksHrefFromOverview', () => {
  it('carries campaign, source and medium only', () => {
    const v = state({
      range: '7d',
      campaign: 'guides',
      filters: { ...emptyFilters(), source: ['mastodon'], medium: ['social'], content: ['c'] },
    });
    expect(linksHrefFromOverview(v)).toBe('/links?campaign=guides&source=mastodon&medium=social');
  });
  it('is plain /links with nothing to carry', () => {
    expect(linksHrefFromOverview(state({ range: '90d' }))).toBe('/links');
  });
});

describe('groupRows', () => {
  const ls = [
    link('a', { content_key: 'k1', label: 'Fuse' }),
    link('b', { content_key: 'k2' }),
    link('c', { content_key: 'k1' }),
  ];
  it('is flat when not grouped', () => {
    expect(groupRows(ls, false, new Set()).map((r) => r.id)).toEqual(['a', 'b', 'c']);
  });
  it('folds shared content into a group at its first link, singles stay', () => {
    const rows = groupRows(ls, true, new Set());
    expect(rows.map((r) => r.id)).toEqual(['group:k1', 'b']);
    expect(rows[0]).toMatchObject({ kind: 'group', label: 'Fuse', open: false });
  });
  it('lists the links of an expanded group', () => {
    const rows = groupRows(ls, true, new Set(['k1']));
    expect(rows.map((r) => r.id)).toEqual(['group:k1', 'a', 'c', 'b']);
    expect(rows[1]).toMatchObject({ kind: 'link', child: true });
  });
  it('does not group links without a content key', () => {
    expect(groupRows([link('x'), link('y')], true, new Set()).every((r) => r.kind === 'link')).toBe(true);
  });
  it('sums', () => {
    expect(sumClicks([link('a', { click_count: 2 }), link('b', { click_count: 3 })], (l) => l.click_count)).toBe(5);
  });
});

describe('server-grouped pages (#0189)', () => {
  const groups = [
    { content_key: 'k1', title: 'Fuse guide', path: '/g', link_count: 2, clicks: 5, links: [link('a', { content_key: 'k1' }), link('c', { content_key: 'k1' })] },
    { content_key: 'k2', title: 'Other', path: '/o', link_count: 1, clicks: 1, links: [link('b', { content_key: 'k2' })] },
  ];
  it('flattens groups in order and keeps each group together', () => {
    expect(flattenGroups(groups).map((l) => l.key)).toEqual(['a', 'c', 'b']);
  });
  it('uses the server title for the group row', () => {
    const rows = groupRows(flattenGroups(groups), true, new Set(), groupTitles(groups));
    expect(rows.map((r) => r.id)).toEqual(['group:k1', 'b']);
    expect(rows[0]).toMatchObject({ kind: 'group', label: 'Fuse guide' });
  });
  it('asks the server to group only for Group by content', () => {
    expect(toListQuery(state({ group: 'content' }), 'UTC').group).toBe('content');
    expect(toListQuery(state({ group: 'link' }), 'UTC').group).toBeUndefined();
  });
  it('uses the server totals per group', () => {
    expect(groupTotals(groups).get('k1')).toEqual({ clicks: 5, window: 0 });
  });
  it('names what the pager counts', () => {
    expect(totalNoun(true, 1)).toBe('content item');
    expect(totalNoun(true, 3)).toBe('content items');
    expect(totalNoun(false, 3)).toBe('links');
  });
});

describe('bulk helpers', () => {
  it("remembers each link's campaign for Undo", () => {
    const m = previousCampaigns([link('a', { campaign_slug: 'x' }), link('b'), link('c', { campaign_slug: 'x' })]);
    expect(m.get('x')).toEqual(['a', 'c']);
    expect(m.get('')).toEqual(['b']);
  });
  it('words the toasts', () => {
    expect(bulkAssignedMessage(1, 'Guides')).toBe('1 link added to Guides');
    expect(bulkAssignedMessage(3, '')).toBe('3 links removed from their campaign');
  });
});
