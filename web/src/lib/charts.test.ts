// Unit tests for the chart data-shaping helpers in lib/charts.ts (#0049).
// No DOM or Svelte — pure function tests only.

import { describe, it, expect } from 'vitest';
import type { DayBucket, UTMBucket } from './types';
import {
  fillDayGaps,
  toTimeseriesPoints,
  toBarRows,
  toPolylinePoints,
  yAxisTicks,
  yCoord,
  defaultDateRange,
  DEFAULT_CHART_GEO,
} from './charts';

function day(date: string, count: number): DayBucket {
  return { date, count };
}

function bucket(value: string, count: number): UTMBucket {
  return { value, count };
}

// ── fillDayGaps ──────────────────────────────────────────────────────────────

describe('fillDayGaps', () => {
  it('fills missing days with zero count', () => {
    const result = fillDayGaps(
      [day('2026-01-10', 3), day('2026-01-12', 2)],
      '2026-01-10',
      '2026-01-12',
    );
    expect(result).toHaveLength(3);
    expect(result[0]).toEqual({ date: '2026-01-10', count: 3 });
    expect(result[1]).toEqual({ date: '2026-01-11', count: 0 });
    expect(result[2]).toEqual({ date: '2026-01-12', count: 2 });
  });

  it('returns a single day when start equals end', () => {
    const result = fillDayGaps([day('2026-06-01', 5)], '2026-06-01', '2026-06-01');
    expect(result).toHaveLength(1);
    expect(result[0]).toEqual({ date: '2026-06-01', count: 5 });
  });

  it('returns empty when start > end', () => {
    expect(fillDayGaps([], '2026-06-10', '2026-06-01')).toEqual([]);
  });

  it('returns all-zero days when input array is empty', () => {
    const result = fillDayGaps([], '2026-06-01', '2026-06-03');
    expect(result).toHaveLength(3);
    expect(result.every((d) => d.count === 0)).toBe(true);
  });

  it('handles a day at a month boundary', () => {
    const result = fillDayGaps(
      [day('2026-01-31', 1), day('2026-02-01', 4)],
      '2026-01-31',
      '2026-02-01',
    );
    expect(result).toHaveLength(2);
    expect(result[1].date).toBe('2026-02-01');
  });
});

// ── toTimeseriesPoints ───────────────────────────────────────────────────────

describe('toTimeseriesPoints', () => {
  it('returns empty for null/undefined/empty', () => {
    expect(toTimeseriesPoints(null)).toEqual([]);
    expect(toTimeseriesPoints(undefined)).toEqual([]);
    expect(toTimeseriesPoints([])).toEqual([]);
  });

  it('maps each day to a point with date, label, and value', () => {
    const pts = toTimeseriesPoints([day('2026-06-01', 7), day('2026-06-02', 3)]);
    expect(pts).toHaveLength(2);
    expect(pts[0].date).toBe('2026-06-01');
    expect(pts[0].value).toBe(7);
    // label is a locale-formatted string — just check it's non-empty.
    expect(pts[0].label.length).toBeGreaterThan(0);
  });
});

// ── toBarRows ────────────────────────────────────────────────────────────────

describe('toBarRows', () => {
  it('returns empty for null/undefined/empty', () => {
    expect(toBarRows(null)).toEqual([]);
    expect(toBarRows(undefined)).toEqual([]);
    expect(toBarRows([])).toEqual([]);
  });

  it('computes percentages as a proportion of the dimension total', () => {
    const rows = toBarRows([bucket('email', 4), bucket('social', 1)]);
    expect(rows).toHaveLength(2);
    expect(rows[0].pct).toBe(80);  // 4/5
    expect(rows[1].pct).toBe(20);  // 1/5
  });

  it('returns pct=0 for all rows when total is zero (no divide-by-zero)', () => {
    // This is a defensive case — zero-count buckets should not appear from the
    // server, but we guard it anyway.
    const rows = toBarRows([bucket('x', 0), bucket('y', 0)]);
    expect(rows.every((r) => r.pct === 0)).toBe(true);
  });

  it('returns 100% for a single bucket', () => {
    const rows = toBarRows([bucket('only', 10)]);
    expect(rows[0].pct).toBe(100);
  });
});

// ── toPolylinePoints ─────────────────────────────────────────────────────────

describe('toPolylinePoints', () => {
  const geo = DEFAULT_CHART_GEO;

  it('returns empty string for empty points', () => {
    expect(toPolylinePoints([])).toBe('');
  });

  it('places a single point at horizontal centre', () => {
    const pts = [{ date: '2026-06-01', label: 'Jun 1', value: 5 }];
    const result = toPolylinePoints(pts, geo);
    // x should be padLeft + innerW/2
    const expectedX = geo.padLeft + geo.innerW / 2;
    expect(result).toContain(`${Math.round(expectedX * 100) / 100},`);
  });

  it('maps max-value point to y=padTop', () => {
    const pts = [
      { date: '2026-06-01', label: 'Jun 1', value: 10 },
      { date: '2026-06-02', label: 'Jun 2', value: 5 },
    ];
    const result = toPolylinePoints(pts, geo);
    const pairs = result.split(' ');
    expect(pairs).toHaveLength(2);
    // First point (max=10) should be at y=padTop.
    const [, y0] = pairs[0].split(',').map(Number);
    expect(y0).toBeCloseTo(geo.padTop, 1);
  });

  it('places zero-value point at baseline (padTop + innerH)', () => {
    const pts = [
      { date: '2026-06-01', label: 'Jun 1', value: 10 },
      { date: '2026-06-02', label: 'Jun 2', value: 0 },
    ];
    const result = toPolylinePoints(pts, geo);
    const pairs = result.split(' ');
    const [, y1] = pairs[1].split(',').map(Number);
    expect(y1).toBeCloseTo(geo.padTop + geo.innerH, 1);
  });

  it('handles all-zero values without NaN (guards divide-by-zero)', () => {
    const pts = [
      { date: '2026-06-01', label: 'Jun 1', value: 0 },
      { date: '2026-06-02', label: 'Jun 2', value: 0 },
    ];
    const result = toPolylinePoints(pts, geo);
    expect(result).not.toContain('NaN');
  });
});

// ── yAxisTicks ───────────────────────────────────────────────────────────────

describe('yAxisTicks', () => {
  it('returns [0] when max is 0', () => {
    expect(yAxisTicks(0)).toEqual([0]);
  });

  it('always includes 0', () => {
    const ticks = yAxisTicks(100, 4);
    expect(ticks[0]).toBe(0);
  });

  it('always includes or covers max', () => {
    const ticks = yAxisTicks(7, 4);
    expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(7);
  });

  it('returns reasonable tick count', () => {
    const ticks = yAxisTicks(100, 4);
    expect(ticks.length).toBeGreaterThanOrEqual(2);
    expect(ticks.length).toBeLessThanOrEqual(6);
  });
});

// ── yCoord ───────────────────────────────────────────────────────────────────

describe('yCoord', () => {
  const geo = DEFAULT_CHART_GEO;

  it('maps max value to padTop (top of chart)', () => {
    expect(yCoord(10, 10, geo)).toBeCloseTo(geo.padTop, 1);
  });

  it('maps zero to padTop + innerH (baseline)', () => {
    expect(yCoord(0, 10, geo)).toBeCloseTo(geo.padTop + geo.innerH, 1);
  });

  it('returns baseline when max is 0 (no divide-by-zero)', () => {
    expect(yCoord(0, 0, geo)).toBeCloseTo(geo.padTop + geo.innerH, 1);
  });
});

// ── defaultDateRange ─────────────────────────────────────────────────────────

describe('defaultDateRange', () => {
  it('returns two "YYYY-MM-DD" strings', () => {
    const [start, end] = defaultDateRange(30);
    expect(/^\d{4}-\d{2}-\d{2}$/.test(start)).toBe(true);
    expect(/^\d{4}-\d{2}-\d{2}$/.test(end)).toBe(true);
  });

  it('start is before end', () => {
    const [start, end] = defaultDateRange(30);
    expect(start < end).toBe(true);
  });

  it('window spans roughly the requested number of days', () => {
    const [start, end] = defaultDateRange(30);
    const diff =
      (new Date(end).getTime() - new Date(start).getTime()) / (1000 * 60 * 60 * 24);
    expect(diff).toBeCloseTo(29, 0); // 30-day window = 29 intervals
  });
});
