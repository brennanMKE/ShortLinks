import { describe, it, expect } from 'vitest';
import { formatRelativeDate } from './relativeDate';

const now = new Date('2026-11-18T12:00:00Z');
const ago = (ms: number) => new Date(now.getTime() - ms);
const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

describe('formatRelativeDate thresholds', () => {
  it('under a minute is "just now"', () => {
    expect(formatRelativeDate(ago(30_000), now)).toBe('just now');
  });
  it('minutes and hours', () => {
    expect(formatRelativeDate(ago(5 * MIN), now)).toBe('5m ago');
    expect(formatRelativeDate(ago(59 * MIN), now)).toBe('59m ago');
    expect(formatRelativeDate(ago(HOUR), now)).toBe('1h ago');
    expect(formatRelativeDate(ago(23 * HOUR), now)).toBe('23h ago');
  });
  it('days up to 6', () => {
    expect(formatRelativeDate(ago(DAY), now)).toBe('1d ago');
    expect(formatRelativeDate(ago(3 * DAY + HOUR), now)).toBe('3d ago');
    expect(formatRelativeDate(ago(7 * DAY - 1), now)).toBe('6d ago');
  });
  it('7 days and older is absolute, never relative', () => {
    expect(formatRelativeDate(ago(7 * DAY), now)).toBe('Nov 11');
    expect(formatRelativeDate(ago(30 * DAY), now)).toBe('Oct 19');
  });
  it('adds the year for a different calendar year', () => {
    expect(formatRelativeDate(new Date('2025-03-02T12:00:00Z'), now)).toBe('Mar 2, 2025');
  });
  it('future dates (clock skew) are absolute; invalid is empty', () => {
    expect(formatRelativeDate(new Date(now.getTime() + DAY), now)).toBe('Nov 19');
    expect(formatRelativeDate('not a date', now)).toBe('');
  });
  it('accepts ISO strings', () => {
    expect(formatRelativeDate('2026-11-15T12:00:00Z', now)).toBe('3d ago');
  });
});
