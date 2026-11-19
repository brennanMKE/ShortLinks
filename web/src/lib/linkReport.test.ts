import { describe, it, expect } from 'vitest';
import {
  isLinkPerformance,
  parseRange,
  rangeQuery,
  reportQuery,
  percentChange,
  siblingRows,
  referrerLabel,
  stripScheme,
  urlSegments,
  rangeLabel,
  toDayBuckets,
} from './linkReport';

const q = (s: string) => new URLSearchParams(s);

describe('range hash state', () => {
  it('defaults to 30d and ignores junk', () => {
    expect(parseRange(q('')).kind).toBe('30d');
    expect(parseRange(q('range=bogus')).kind).toBe('30d');
    expect(parseRange(q('range=custom&from=2026-11-01')).kind).toBe('30d');
    expect(parseRange(q('range=custom&from=2026-11-05&to=2026-11-01')).kind).toBe('30d');
  });
  it('parses presets and custom', () => {
    expect(parseRange(q('range=7d')).kind).toBe('7d');
    expect(parseRange(q('range=90d')).kind).toBe('90d');
    expect(parseRange(q('range=custom&from=2026-11-01&to=2026-11-09'))).toEqual({ kind: 'custom', from: '2026-11-01', to: '2026-11-09' });
  });
  it('round-trips through rangeQuery and omits the default', () => {
    expect(rangeQuery({ kind: '30d', from: '', to: '' })).toBe('');
    expect(rangeQuery({ kind: '7d', from: '', to: '' })).toBe('range=7d');
    const c = { kind: 'custom' as const, from: '2026-11-01', to: '2026-11-09' };
    expect(parseRange(q(rangeQuery(c)))).toEqual(c);
  });
  it('sends the chart time zone', () => {
    expect(reportQuery('k', { kind: '7d', from: '', to: '' }, 'America/Los_Angeles')).toBe('key=k&compare=previous&tz=America%2FLos_Angeles&range=7d');
  });
  it('builds the report query', () => {
    expect(reportQuery('a b', { kind: '90d', from: '', to: '' })).toBe('key=a+b&compare=previous&range=90d');
    expect(reportQuery('k', { kind: 'custom', from: '2026-11-01', to: '2026-11-09' })).toBe('key=k&compare=previous&from=2026-11-01&to=2026-11-09');
  });
});

describe('percentChange', () => {
  it('handles up, down, flat and no comparison', () => {
    expect(percentChange(122, 100)?.text).toBe('▲ 22%');
    expect(percentChange(50, 100)?.text).toBe('▼ 50%');
    expect(percentChange(100, 100)?.dir).toBe('flat');
    expect(percentChange(5, null)).toBeNull();
    expect(percentChange(0, 0)).toBeNull();
    expect(percentChange(4, 0)?.text).toBe('▲ new');
  });
});

describe('siblingRows', () => {
  it('includes this link, ranked by clicks', () => {
    const rows = siblingRows({ key: 'a', label: 'A', clicks: 90 }, [
      { key: 'b', label: 'B', campaign_slug: 'c', active: true, clicks: 52 },
      { key: 'c', label: 'C', campaign_slug: null, active: true, clicks: 120 },
    ]);
    expect(rows.map((r) => r.key)).toEqual(['c', 'a', 'b']);
    expect(rows.find((r) => r.current)?.key).toBe('a');
    expect(siblingRows({ key: 'a', label: 'A', clicks: 0 }, null)).toHaveLength(1);
  });
});

describe('display helpers', () => {
  it('labels direct referrers', () => {
    expect(referrerLabel('(direct)')).toBe('(direct / app)');
    expect(referrerLabel('mastodon.social')).toBe('mastodon.social');
  });
  it('strips the scheme and splits at slashes', () => {
    expect(stripScheme('https://go.example.com/u/k3Fz9a')).toBe('go.example.com/u/k3Fz9a');
    expect(urlSegments('go.example.com/u/k3Fz9a')).toEqual(['go.example.com/', 'u/', 'k3Fz9a']);
    expect(urlSegments('localhost:8080/u/x').join('')).toBe('localhost:8080/u/x');
  });
  it('labels ranges', () => {
    expect(rangeLabel({ kind: '7d', from: '', to: '' }, null)).toBe('7 days');
    expect(rangeLabel({ kind: 'custom', from: '2026-07-03', to: '2026-08-01' }, null)).toBe('Jul 3 – Aug 1');
  });
  it('maps report days to chart buckets', () => {
    expect(toDayBuckets([{ date: '2026-11-01', clicks: 3 }])).toEqual([{ date: '2026-11-01', count: 3 }]);
    expect(toDayBuckets(null)).toEqual([]);
  });
});

describe('isLinkPerformance', () => {
  const ok = {
    report: 'link-performance',
    window: { from: '2026-10-21', to: '2026-11-19', tz: 'UTC', partial_last_day: false },
    previous_window: null,
    data: { link: {}, totals: {}, timeseries: [], previous_timeseries: null, by_referer: [], siblings: [] },
  };
  it('accepts a real report body', () => {
    expect(isLinkPerformance(ok)).toBe(true);
  });
  it('rejects what the dev server returns in place of a report', () => {
    expect(isLinkPerformance('<!doctype html>')).toBe(false);
    expect(isLinkPerformance({})).toBe(false);
    expect(isLinkPerformance(undefined)).toBe(false);
    expect(isLinkPerformance(null)).toBe(false);
    expect(isLinkPerformance({ ...ok, window: null })).toBe(false);
    expect(isLinkPerformance({ ...ok, data: undefined })).toBe(false);
  });
});
