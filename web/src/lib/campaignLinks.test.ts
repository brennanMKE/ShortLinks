// Unit tests for the campaign Links table helpers (#0157). Pure functions.

import { describe, it, expect } from 'vitest';
import type { CampaignLinkRow } from './campaigns';
import {
  reportWindow,
  buildViewGroups,
  sortViewGroups,
  flattenGroups,
  flatRows,
  channelText,
  linkMeta,
  groupChannels,
  groupChannelsShort,
  hasAnyPlacement,
  selectedLinksCsv,
  destinationPath,
  shareOf,
  type TopContentGroup,
} from './campaignLinks';

function row(key: string, clicks: number, over: Partial<CampaignLinkRow> = {}): CampaignLinkRow {
  return {
    id: 1,
    key,
    destination_url: `https://www.example.com/guides/${key}`,
    title: `Title ${key}`,
    active: true,
    denied_reason: 0,
    created_at: '2026-11-01T00:00:00Z',
    expires_at: null,
    click_count: clicks,
    campaign_id: 1,
    utm_source: 'mastodon',
    utm_medium: 'social',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
    placement: '',
    clicksInWindow: clicks,
    shareOfTotal: 0,
    ...over,
  };
}

const groups: TopContentGroup[] = [
  {
    content_key: 'www.example.com/guides/measuring-current',
    title: 'Measuring current without blowing the fuse',
    path: '/guides/measuring-current',
    clicks: 0,
    links: [
      { key: 'a', label: 'Mastodon guide post 84', utm_source: 'mastodon', utm_medium: 'social', campaign_slug: 'c', clicks: 0 },
      { key: 'b', label: 'Bluesky guide post 85', utm_source: 'bluesky', utm_medium: 'social', campaign_slug: 'c', clicks: 0 },
      { key: 'zz', label: 'other campaign', utm_source: 'x', utm_medium: '', campaign_slug: 'other', clicks: 0 },
    ],
  },
  {
    content_key: 'www.example.com/guides/esp32',
    title: 'ESP32 deep sleep, measured',
    path: '/guides/esp32',
    clicks: 0,
    links: [{ key: 'c', label: '', utm_source: 'email', utm_medium: 'email', campaign_slug: 'c', clicks: 0 }],
  },
];

describe('reportWindow', () => {
  it('turns the half-open stats window into inclusive report days', () => {
    expect(reportWindow({ window_from: '2026-11-01', window_to: '2026-12-01' })).toEqual({ from: '2026-11-01', to: '2026-11-30' });
  });
  it('is null for a missing, malformed or zero-day window', () => {
    expect(reportWindow(undefined)).toBeNull();
    expect(reportWindow({ window_from: '', window_to: '' })).toBeNull();
    expect(reportWindow({ window_from: '2026-11-01', window_to: '2026-11-01' })).toBeNull();
    expect(reportWindow({ window_from: '2026-11-01', window_to: 'nope' })).toBeNull();
  });
});

describe('buildViewGroups', () => {
  const rows = [row('a', 90), row('b', 52), row('c', 20), row('d', 5)];
  it('groups links by the report, orders members by clicks and sums them', () => {
    const g = buildViewGroups(rows, groups);
    expect(g.map((x) => x.title)).toEqual(['Measuring current without blowing the fuse', 'ESP32 deep sleep, measured', 'Title d']);
    expect(g[0].links.map((l) => l.key)).toEqual(['a', 'b']);
    expect(g[0].links[0].label).toBe('Mastodon guide post 84');
    expect(g[0].clicks).toBe(142);
    expect(g[0].share).toBe(Math.round((142 / 167) * 100));
  });
  it('drops report members that are not in this campaign and keeps unreported links as their own group', () => {
    const g = buildViewGroups(rows, groups);
    expect(g.flatMap((x) => x.links.map((l) => l.key)).sort()).toEqual(['a', 'b', 'c', 'd']);
    expect(g[2].links[0].label).toBe('');
  });
  it('falls back to one group per link without a report', () => {
    const g = buildViewGroups(rows, null);
    expect(g).toHaveLength(4);
    expect(g[0].title).toBe('Title a');
    expect(g[0].path).toBe('/guides/a');
  });
});

describe('sorting and flattening', () => {
  const rows = [row('a', 90), row('b', 52), row('c', 200), row('d', 5)];
  const vg = buildViewGroups(rows, groups);
  const total = 347;

  it('sorts groups by clicks both ways and by title', () => {
    expect(sortViewGroups(vg, 'clicks', 'desc').map((g) => g.clicks)).toEqual([200, 142, 5]);
    expect(sortViewGroups(vg, 'clicks', 'asc').map((g) => g.clicks)).toEqual([5, 142, 200]);
    expect(sortViewGroups(vg, 'link', 'asc').map((g) => g.title)[0]).toBe('ESP32 deep sleep, measured');
  });

  it('shows a one-link group as a plain row and a multi-link group collapsed', () => {
    const t = flattenGroups(sortViewGroups(vg, 'clicks', 'desc'), new Set(), total);
    expect(t.map((r) => r.kind)).toEqual(['link', 'group', 'link']);
    expect(t[1].id).toBe('group:www.example.com/guides/measuring-current');
  });

  it('expands a group into its links, labelled by their channel suffix', () => {
    const t = flattenGroups(sortViewGroups(vg, 'clicks', 'desc'), new Set(['www.example.com/guides/measuring-current']), total);
    expect(t.map((r) => r.id)).toEqual(['c', 'group:www.example.com/guides/measuring-current', 'a', 'b', 'd']);
    const child = t[2];
    expect(child.kind === 'link' && child.child).toBe(true);
    expect(child.kind === 'link' && child.title).toBe('Mastodon guide post 84');
  });

  it('flat rows keep the clean content title on line 1', () => {
    const t = flatRows(vg, 'clicks', 'desc', total);
    expect(t.map((r) => r.id)).toEqual(['c', 'a', 'b', 'd']);
    const first = t.find((r) => r.id === 'a');
    expect(first?.kind === 'link' && first.title).toBe('Measuring current without blowing the fuse');
  });

  it('a seven-digit click count is just a number in the row', () => {
    const big = buildViewGroups([row('a', 1234567)], null);
    expect(flatRows(big, 'clicks', 'desc', 1234567)[0].kind).toBe('link');
    expect(big[0].share).toBe(100);
  });
});

describe('cell text', () => {
  it('joins source and medium in one column', () => {
    expect(channelText({ utm_source: 'mastodon', utm_medium: 'social' })).toBe('mastodon · social');
    expect(channelText({ utm_source: 'mastodon', utm_medium: '' })).toBe('mastodon');
    expect(channelText({ utm_source: 'email', utm_medium: 'email' })).toBe('email');
    expect(channelText({ utm_source: '', utm_medium: '' })).toBe('');
  });
  it('builds line 2 and drops empty parts', () => {
    expect(linkMeta('k3Fz9a', 'Mastodon guide post 91', '/guides/x')).toBe('/u/k3Fz9a · Mastodon guide post 91 · /guides/x');
    expect(linkMeta('k3Fz9a', '', '')).toBe('/u/k3Fz9a');
  });
  it('lists distinct group sources', () => {
    expect(groupChannels([{ utm_source: 'mastodon' }, { utm_source: 'bluesky' }, { utm_source: 'mastodon' }, { utm_source: '' }])).toBe('mastodon · bluesky');
  });
  it('shortens three or more sources to a count', () => {
    expect(groupChannelsShort([{ utm_source: 'mastodon' }, { utm_source: 'bluesky' }])).toBe('mastodon · bluesky');
    expect(groupChannelsShort([{ utm_source: 'a' }, { utm_source: 'b' }, { utm_source: 'c' }])).toBe('3 channels');
    expect(groupChannelsShort([{ utm_source: '' }])).toBe('');
  });
  it('only shows Placement when some link has one', () => {
    expect(hasAnyPlacement([row('a', 1), row('b', 1)])).toBe(false);
    expect(hasAnyPlacement([row('a', 1), row('b', 1, { placement: 'Cafe' })])).toBe(true);
    expect(hasAnyPlacement([row('a', 1, { placement: '  ' })])).toBe(false);
  });
  it('parses destination paths', () => {
    expect(destinationPath('https://x.com/a/b/')).toBe('/a/b');
    expect(destinationPath('https://x.com')).toBe('/');
    expect(destinationPath('nope')).toBe('');
  });
  it('shareOf is safe against a zero total', () => {
    expect(shareOf(0, 0)).toBe(0);
    expect(shareOf(1, 3)).toBe(33);
  });
});

describe('selectedLinksCsv', () => {
  it('quotes commas, quotes and newlines', () => {
    const csv = selectedLinksCsv([row('a', 3, { title: 'Hello, "world"' })]);
    const lines = csv.trimEnd().split('\r\n');
    expect(lines[0]).toBe('key,short_url,title,destination_url,utm_source,utm_medium,placement,clicks,share_percent');
    expect(lines[1]).toContain('"Hello, ""world"""');
    expect(lines[1].startsWith('a,')).toBe(true);
  });
});
