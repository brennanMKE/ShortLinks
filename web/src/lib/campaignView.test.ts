import { describe, it, expect } from 'vitest';
import {
  parseCampaignView,
  patchCampaignQuery,
  campaignPath,
  statsRange,
  addDays,
  columnLabel,
  cellKey,
  rowKeys,
  filterRows,
  matrixMax,
  shadePct,
  idleLinks,
  topContentTile,
  comparisonRow,
  campaignStatusOf,
  DEFAULT_VIEW,
} from './campaignView';
import type { MatrixRow } from './reports';

const rows: MatrixRow[] = [
  {
    content_key: 'a',
    title: 'ESP32 deep sleep',
    total: 212,
    cells: { mastodon: { clicks: 118, keys: ['k1', 'k2'] }, email: { clicks: 33, key: 'k3' } },
  },
  { content_key: 'b', title: 'Soldering iron tips', total: 16, cells: { bluesky: { clicks: 16, key: 'k4' } } },
];

describe('parseCampaignView', () => {
  it('defaults', () => {
    expect(parseCampaignView('')).toEqual(DEFAULT_VIEW);
  });
  it('reads tab, range, by and group', () => {
    expect(parseCampaignView('tab=links&range=7d&by=medium&group=link')).toMatchObject({ tab: 'links', range: '7d', by: 'medium', group: 'link' });
  });
  it('reads range=campaign as the default', () => {
    expect(parseCampaignView('range=campaign').range).toBe('campaign');
  });
  it('accepts the older group=0 as a flat list', () => {
    expect(parseCampaignView('tab=links&group=0').group).toBe('link');
  });
  it('treats from as a custom range and drops a to before from', () => {
    expect(parseCampaignView('from=2026-11-01&to=2026-11-10')).toMatchObject({ range: 'custom', from: '2026-11-01', to: '2026-11-10' });
    expect(parseCampaignView('from=2026-11-10&to=2026-11-01')).toMatchObject({ range: 'custom', to: '' });
  });
  it('ignores unknown or malformed values', () => {
    expect(parseCampaignView('tab=nope&range=bogus&by=x&from=yesterday')).toEqual(DEFAULT_VIEW);
  });
});

describe('patchCampaignQuery', () => {
  it('omits defaults', () => {
    expect(patchCampaignQuery('', {})).toBe('');
    expect(patchCampaignQuery('tab=links', { tab: 'content' })).toBe('');
  });
  it('a non-default range is written, the campaign range is not', () => {
    expect(patchCampaignQuery('', { range: '30d' })).toBe('range=30d');
    expect(patchCampaignQuery('range=30d', { range: 'campaign' })).toBe('');
  });
  it('rewrites the older group=0 as group=link and keeps the other state', () => {
    expect(patchCampaignQuery('group=0&tab=links', { range: '30d' })).toBe('range=30d&group=link&tab=links');
  });
  it('a preset range clears a custom window', () => {
    expect(patchCampaignQuery('from=2026-11-01&to=2026-11-10', { range: '7d' })).toBe('range=7d');
  });
  it('round-trips a custom range, the tab and by', () => {
    const qs = patchCampaignQuery('', { range: 'custom', from: '2026-11-01', to: '2026-11-09', tab: 'channels', by: 'placement' });
    expect(parseCampaignView(qs)).toMatchObject({ tab: 'channels', range: 'custom', from: '2026-11-01', to: '2026-11-09', by: 'placement' });
  });
  it('builds a path', () => {
    expect(campaignPath('guides 1', '', { tab: 'links' })).toBe('/campaigns/guides%201?tab=links');
    expect(campaignPath('g', '')).toBe('/campaigns/g');
  });
});

describe('statsRange', () => {
  const today = '2026-11-18';
  const v = DEFAULT_VIEW;
  const dated = (starts: string, ends: string) => ({ starts_at: starts + 'T00:00:00Z', ends_at: ends + 'T00:00:00Z' });

  it('an ended campaign includes its last day (to is exclusive)', () => {
    expect(statsRange(v, today, dated('2026-10-01', '2026-10-31'))).toEqual({ from: '2026-10-01', to: '2026-11-01' });
  });
  it('a running campaign includes today', () => {
    expect(statsRange(v, today, dated('2026-11-01', '2026-11-30'))).toEqual({ from: '2026-11-01', to: '2026-11-19' });
  });
  it('a one-day campaign gets a one-day window', () => {
    expect(statsRange(v, today, dated('2026-11-05', '2026-11-05'))).toEqual({ from: '2026-11-05', to: '2026-11-06' });
    expect(statsRange(v, today, dated('2026-11-18', '2026-11-18'))).toEqual({ from: '2026-11-18', to: '2026-11-19' });
  });
  it('a campaign that has not started gets its first day', () => {
    expect(statsRange(v, today, dated('2026-12-01', '2026-12-31'))).toEqual({ from: '2026-12-01', to: '2026-12-02' });
  });
  it('leaves the window to the server unless the campaign has both dates', () => {
    expect(statsRange(v, today, { starts_at: '2026-11-01T00:00:00Z', ends_at: null })).toEqual({});
    expect(statsRange(v, today, null)).toEqual({});
  });
  it('ends presets today, exclusive', () => {
    expect(statsRange({ ...v, range: '7d' }, today)).toEqual({ from: '2026-11-11', to: today });
    expect(statsRange({ ...v, range: '30d' }, today)).toEqual({ from: '2026-10-19', to: today });
  });
  it('makes a custom to exclusive, and no end means through today', () => {
    expect(statsRange({ ...v, range: 'custom', from: '2026-11-01', to: '2026-11-30' }, today)).toEqual({ from: '2026-11-01', to: '2026-12-01' });
    expect(statsRange({ ...v, range: 'custom', from: '2026-11-01', to: '' }, today)).toEqual({ from: '2026-11-01', to: '2026-11-19' });
    expect(statsRange({ ...v, range: 'custom', from: '2026-11-01', to: today }, today)).toEqual({ from: '2026-11-01', to: '2026-11-19' });
  });
  it('adds days across months', () => {
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01');
  });
});

describe('matrix helpers', () => {
  it('labels columns', () => {
    expect(columnLabel('other')).toBe('Other');
    expect(columnLabel('~none')).toBe('(none)');
    expect(columnLabel('mastodon')).toBe('mastodon');
  });
  it('picks the cell link', () => {
    expect(cellKey({ clicks: 1, key: 'a' })).toBe('a');
    expect(cellKey({ clicks: 2, keys: ['b', 'c'] })).toBe('b');
    expect(cellKey(undefined)).toBe('');
  });
  it('collects a row\'s link keys once', () => {
    expect(rowKeys(rows[0]).sort()).toEqual(['k1', 'k2', 'k3']);
  });
  it('filters by title', () => {
    expect(filterRows(rows, 'SOLDER')).toHaveLength(1);
    expect(filterRows(rows, '  ')).toHaveLength(2);
  });
  it('shades in proportion to the largest cell', () => {
    expect(matrixMax(rows)).toBe(118);
    expect(shadePct(0, 118)).toBe(0);
    expect(shadePct(118, 118)).toBe(45);
    expect(shadePct(1, 118)).toBeLessThan(shadePct(60, 118));
    expect(shadePct(5, 0)).toBe(0);
  });
});

describe('campaignStatusOf', () => {
  const t = '2026-11-18';
  it('derives the status from the dates', () => {
    expect(campaignStatusOf({ archived: true, starts_at: null, ends_at: null }, t)).toBe('archived');
    expect(campaignStatusOf({ archived: false, starts_at: '2026-12-01T00:00:00Z', ends_at: null }, t)).toBe('scheduled');
    expect(campaignStatusOf({ archived: false, starts_at: '2026-10-01T00:00:00Z', ends_at: '2026-11-17T00:00:00Z' }, t)).toBe('ended');
    expect(campaignStatusOf({ archived: false, starts_at: '2026-11-01T00:00:00Z', ends_at: '2026-11-18T00:00:00Z' }, t)).toBe('live');
    expect(campaignStatusOf({ archived: false, starts_at: null, ends_at: null }, t)).toBe('live');
  });
});

describe('tiles', () => {
  it('counts idle links', () => {
    expect(idleLinks(42, 33)).toBe(9);
    expect(idleLinks(3, 5)).toBe(0);
  });
  it('names the top content with its share', () => {
    expect(topContentTile(rows, 902)).toEqual({ title: 'ESP32 deep sleep', clicks: 212, sharePct: 24 });
    expect(topContentTile([], 0)).toBeNull();
  });
  it('finds the campaign in a comparison', () => {
    const r = { slug: 'x' } as never;
    expect(comparisonRow([r], 'x')).toBe(r);
    expect(comparisonRow([], 'x')).toBeNull();
    expect(comparisonRow(undefined, 'x')).toBeNull();
  });
});
