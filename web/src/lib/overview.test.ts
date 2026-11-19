import { describe, expect, it } from 'vitest';
import { campaignChips, changeOf, contentRows, layoutChips, linkRows, sourceColors, sourceRows, sourceSegments } from './overview';

describe('changeOf', () => {
  it('reports direction and percent', () => {
    expect(changeOf(118, 100)).toMatchObject({ kind: 'up', pct: 18, arrow: '▲' });
    expect(changeOf(88, 100)).toMatchObject({ kind: 'down', pct: 12 });
    expect(changeOf(5, 5).kind).toBe('flat');
  });
  it('says new when the previous period was zero', () => {
    expect(changeOf(7, 0)).toMatchObject({ kind: 'new', text: 'new' });
    expect(changeOf(0, 0).kind).toBe('none');
    expect(changeOf(3, null).kind).toBe('none');
  });
});

describe('chips', () => {
  const camps = [
    { slug: null, name: 'No campaign', clicks: 50 },
    { slug: 'a', name: 'A', clicks: 10 },
    { slug: 'b', name: 'B', clicks: 90 },
    { slug: 'z', name: 'Z', clicks: 0 },
  ];
  it('orders by clicks with No campaign last and drops empty campaigns', () => {
    expect(campaignChips(camps, '').map((c) => c.value)).toEqual(['b', 'a', 'none']);
  });
  it('keeps a selected campaign with no clicks', () => {
    expect(campaignChips(camps, 'z').map((c) => c.value)).toContain('z');
  });
  it('shows five campaigns then More, promoting the selection', () => {
    const chips = ['a', 'b', 'c', 'd', 'e', 'f', 'g'].map((v) => ({ value: v, label: v, clicks: 1 }));
    expect(layoutChips(chips, '').visible).toHaveLength(5);
    expect(layoutChips(chips, '').more.map((c) => c.value)).toEqual(['f', 'g']);
    const l = layoutChips(chips, 'g');
    expect(l.visible.map((c) => c.value)).toContain('g');
    expect(l.more.map((c) => c.value)).toContain('e');
    expect(layoutChips(chips.slice(0, 5), '').more).toEqual([]);
  });
});

describe('rows', () => {
  it('folds sources beyond three into other', () => {
    const segs = sourceSegments([
      { value: 'a', clicks: 40 },
      { value: 'b', clicks: 30 },
      { value: 'c', clicks: 20 },
      { value: 'd', clicks: 6 },
      { value: 'e', clicks: 4 },
    ]);
    expect(segs.map((s) => s.label)).toEqual(['a', 'b', 'c', 'other (2)']);
    expect(segs[3].clicks).toBe(10);
  });
  it('builds content rows with link children', () => {
    const rows = contentRows([
      {
        content_key: 'x/g',
        title: 'T',
        path: '/g',
        clicks: 10,
        previous_clicks: 5,
        change_pct: 100,
        by_source: [{ value: 'mastodon', clicks: 10 }],
        links: [
          { key: 'k1', label: 'Mastodon post', utm_source: 'mastodon', utm_medium: 'social', campaign_slug: null, clicks: 10 },
        ],
      },
    ]);
    expect(rows[0].meta).toBe('/g · 1 link');
    expect(rows[0].change.kind).toBe('up');
    expect(rows[0].children[0].meta).toBe('/u/k1 · social · mastodon');
  });
  it('builds source rows including the other bucket', () => {
    const rows = sourceRows({
      dimension: 'source',
      total: 100,
      previous_total: 50,
      rows: [{ value: '~none', clicks: 60, share: 0.6, previous_clicks: 0 }],
      other: { clicks: 40, values: 2 },
    });
    expect(rows[0].title).toBe('(none)');
    expect(rows[0].change.kind).toBe('new');
    expect(rows[1].title).toBe('2 other sources');
  });
});

describe('source colours and link rows', () => {
  const by = [
    { value: 'bluesky', clicks: 10 },
    { value: 'mastodon', clicks: 50 },
    { value: 'bluesky', clicks: 5 },
    { value: 'email', clicks: 20 },
  ];
  it('ranks sources globally by total clicks', () => {
    const c = sourceColors(by);
    expect(c.get('mastodon')).toBe(1);
    expect(c.get('email')).toBe(2);
    expect(c.get('bluesky')).toBe(3);
  });
  it('keeps a source the same colour in every row', () => {
    const c = sourceColors(by);
    const a = sourceSegments([{ value: 'bluesky', clicks: 9 }, { value: 'mastodon', clicks: 1 }], c);
    const b = sourceSegments([{ value: 'bluesky', clicks: 1 }], c);
    expect(a.find((s) => s.label === 'bluesky')?.slot).toBe(3);
    expect(b[0].slot).toBe(3);
  });
  it('titles link rows by content and puts label, key and source on line two', () => {
    const rows = linkRows(
      [
        {
          key: 'k1', short_url: '', title: 'Guide — Mastodon post 82', label: 'Mastodon post 82', content_title: 'Guide',
          destination: '', campaign_slug: null, utm_source: 'mastodon', utm_medium: 'social', clicks: 5, previous_clicks: null, change_pct: null,
        },
      ],
      sourceColors(by),
    );
    expect(rows[0].title).toBe('Guide');
    expect(rows[0].meta).toBe('Mastodon post 82 · /u/k1 · mastodon');
    expect(rows[0].segments[0].slot).toBe(1);
  });
});
