import { describe, expect, it } from 'vitest';
import {
  DEFAULT_STATE,
  overviewPath,
  parseOverviewQuery,
  reportQuery,
  serializeOverviewQuery,
  type OverviewState,
} from './overviewQuery';

const parse = (s: string) => parseOverviewQuery(new URLSearchParams(s));

describe('overviewQuery', () => {
  it('omits defaults', () => {
    expect(serializeOverviewQuery(DEFAULT_STATE)).toBe('');
    expect(overviewPath(DEFAULT_STATE)).toBe('/');
  });
  it('round-trips every key', () => {
    const s: OverviewState = { range: '7d', from: '', to: '', campaign: 'guides-2026-11', group: 'source' };
    expect(parse(serializeOverviewQuery(s))).toEqual(s);
    const c: OverviewState = { range: 'custom', from: '2026-10-01', to: '2026-10-31', campaign: 'none', group: 'link' };
    expect(parse(serializeOverviewQuery(c))).toEqual(c);
  });
  it('ignores malformed values', () => {
    expect(parse('range=5y&group=x&from=nope')).toEqual(DEFAULT_STATE);
    expect(parse('from=2026-10-10&to=2026-10-01').to).toBe('');
  });
  it('builds report queries with tz and scope', () => {
    expect(reportQuery(DEFAULT_STATE, 'UTC')).toBe('tz=UTC&range=30d');
    const q = new URLSearchParams(
      reportQuery({ ...DEFAULT_STATE, campaign: 'a b', range: '90d' }, 'America/Los_Angeles', { limit: '5' }),
    );
    expect(q.get('campaign')).toBe('a b');
    expect(q.get('range')).toBe('90d');
    expect(q.get('limit')).toBe('5');
  });
});
