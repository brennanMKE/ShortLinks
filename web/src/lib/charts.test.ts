// Unit tests for the chart data-shaping helpers in lib/charts.ts (#0049).
// No DOM or Svelte — pure function tests only.

import { describe, it, expect } from 'vitest';
import type { DayBucket, LinkSeries, UTMBucket } from './types';
import {
  fillDayGaps,
  fillDayGapsInWindow,
  toTimeseriesPoints,
  toBarRows,
  toPolylinePoints,
  yAxisTicks,
  yCoord,
  xCoord,
  defaultDateRange,
  DEFAULT_CHART_GEO,
  campaignSeriesColorVar,
  CAMPAIGN_SERIES_COLOR_COUNT,
  CAMPAIGN_OTHER_SERIES_COLOR_VAR,
  buildCampaignSeries,
  nearestIndex,
  clientToSvgX,
  stepIndex,
  pointsForWindow,
  alignComparison,
  shiftDate,
  spreadLabels,
  truncateLabel,
  geoWithPadRight,
} from './charts';

function day(date: string, count: number): DayBucket {
  return { date, count };
}

function bucket(value: string, count: number): UTMBucket {
  return { value, count };
}

function linkSeries(overrides: Partial<LinkSeries> = {}): LinkSeries {
  return {
    link_id: 1,
    key: 'abc123',
    title: 'Flyer A',
    is_other: false,
    days: [],
    ...overrides,
  };
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

// ── fillDayGapsInWindow ──────────────────────────────────────────────────────

describe('fillDayGapsInWindow', () => {
  it('fills a multi-day half-open window, excluding windowTo itself', () => {
    // [2026-06-01, 2026-06-04) => three inclusive days: 06-01, 06-02, 06-03.
    // A phantom 06-04 bucket would mean the exclusive-end conversion was
    // skipped (#0103 downstream constraint 3's gotcha).
    const result = fillDayGapsInWindow(
      [day('2026-06-01', 2), day('2026-06-03', 5)],
      '2026-06-01',
      '2026-06-04',
    );
    expect(result.map((d) => d.date)).toEqual(['2026-06-01', '2026-06-02', '2026-06-03']);
    expect(result).toEqual([
      { date: '2026-06-01', count: 2 },
      { date: '2026-06-02', count: 0 },
      { date: '2026-06-03', count: 5 },
    ]);
  });

  it('a single-day window (windowTo one day after windowFrom) fills exactly one bucket', () => {
    const result = fillDayGapsInWindow([day('2026-06-01', 7)], '2026-06-01', '2026-06-02');
    expect(result).toEqual([{ date: '2026-06-01', count: 7 }]);
  });

  it('returns [] when windowFrom === windowTo (an empty half-open window)', () => {
    expect(fillDayGapsInWindow([], '2026-06-01', '2026-06-01')).toEqual([]);
  });

  it('returns [] when windowFrom is after windowTo', () => {
    expect(fillDayGapsInWindow([], '2026-06-05', '2026-06-01')).toEqual([]);
  });

  it('returns [] for malformed date strings', () => {
    expect(fillDayGapsInWindow([], '', '')).toEqual([]);
    expect(fillDayGapsInWindow([], 'not-a-date', '2026-06-01')).toEqual([]);
  });

  it('handles the exclusive end crossing a month boundary', () => {
    // [2026-01-31, 2026-02-02) => inclusive days 01-31, 02-01.
    const result = fillDayGapsInWindow([], '2026-01-31', '2026-02-02');
    expect(result.map((d) => d.date)).toEqual(['2026-01-31', '2026-02-01']);
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

  // #0104 review finding 1: scaling was PRIVATE to this function — every
  // caller got a polyline normalized to that series' OWN max, with no way to
  // pass a shared one. That's invisible with a single series (ClicksChart,
  // where "my own max" and "the chart's max" are the same number), but wrong
  // for a multi-series chart: CampaignClicksChart computed one shared
  // maxValue for its y-axis, ticks, AND every dot's cy — but had no third
  // argument to hand toPolylinePoints, so every LINE still scaled to its own
  // peak while the DOTS for those same points sat wherever the shared scale
  // put them. A 2-clicks/day series drew identically to a 60-clicks/day one.
  // No existing test here could have caught this: none of them render two
  // series side by side, because there was no shared-scale parameter to
  // exercise.
  it('scales an explicit shared max across the series, not the series\' own peak', () => {
    const dominant = [
      { date: '2026-06-01', label: 'Jun 1', value: 60 },
      { date: '2026-06-02', label: 'Jun 2', value: 30 },
    ];
    const tiny = [
      { date: '2026-06-01', label: 'Jun 1', value: 2 },
      { date: '2026-06-02', label: 'Jun 2', value: 1 },
    ];
    const sharedMax = 60; // dominant's own max, shared across both series

    const dominantY0 = Number(toPolylinePoints(dominant, geo, sharedMax).split(' ')[0].split(',')[1]);
    const tinyY0 = Number(toPolylinePoints(tiny, geo, sharedMax).split(' ')[0].split(',')[1]);

    // On the shared scale, dominant's first point (== the max) sits at the
    // very top of the plot...
    expect(dominantY0).toBeCloseTo(geo.padTop, 1);
    // ...and tiny's first point (2/60 of the shared max) sits far below it —
    // NOT at the top the way it would if `tiny` were scaled against its own
    // private max of 2 (the pre-fix behaviour, and what a mutation reverting
    // the `max` parameter reproduces: this assertion is the one that fails).
    expect(tinyY0).toBeGreaterThan(dominantY0 + geo.innerH * 0.5);
  });

  it('defaults to each series\' own max when no shared max is passed (unchanged self-derived behaviour)', () => {
    // No third argument — this must still behave exactly as it did before
    // the `max` parameter was added, which is what makes ClicksChart's
    // existing single-series usage safe to leave untouched.
    const pts = [{ date: '2026-06-01', label: 'Jun 1', value: 2 }];
    const y = Number(toPolylinePoints(pts, geo).split(',')[1]);
    expect(y).toBeCloseTo(geo.padTop, 1); // this point IS its own max
  });
});

// ── xCoord ───────────────────────────────────────────────────────────────────

describe('xCoord', () => {
  const geo = DEFAULT_CHART_GEO;

  it('places a single point at horizontal centre', () => {
    expect(xCoord(0, 1, geo)).toBeCloseTo(geo.padLeft + geo.innerW / 2, 5);
  });

  it('places the first of several points at padLeft', () => {
    expect(xCoord(0, 3, geo)).toBeCloseTo(geo.padLeft, 5);
  });

  it('places the last of several points at padLeft + innerW', () => {
    expect(xCoord(2, 3, geo)).toBeCloseTo(geo.padLeft + geo.innerW, 5);
  });

  it('distributes points evenly in between', () => {
    expect(xCoord(1, 3, geo)).toBeCloseTo(geo.padLeft + geo.innerW / 2, 5);
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

// ── campaignSeriesColorVar ───────────────────────────────────────────────────

describe('campaignSeriesColorVar', () => {
  it('returns a distinct --series-N var() for each of the 6 named slots', () => {
    const vars = Array.from({ length: CAMPAIGN_SERIES_COLOR_COUNT }, (_, i) => campaignSeriesColorVar(i));
    expect(vars).toEqual([
      'var(--series-1)',
      'var(--series-2)',
      'var(--series-3)',
      'var(--series-4)',
      'var(--series-5)',
      'var(--series-6)',
    ]);
    expect(new Set(vars).size).toBe(CAMPAIGN_SERIES_COLOR_COUNT);
  });

  it('wraps modulo CAMPAIGN_SERIES_COLOR_COUNT past the 6th slot (defensive fallback)', () => {
    expect(campaignSeriesColorVar(6)).toBe('var(--series-1)');
    expect(campaignSeriesColorVar(7)).toBe('var(--series-2)');
  });

  it('never returns the "Other" color for a named slot', () => {
    for (let i = 0; i < 10; i++) {
      expect(campaignSeriesColorVar(i)).not.toBe(CAMPAIGN_OTHER_SERIES_COLOR_VAR);
    }
  });
});

// ── buildCampaignSeries ──────────────────────────────────────────────────────

describe('buildCampaignSeries', () => {
  const from = '2026-06-01';
  const to = '2026-06-04'; // exclusive => 06-01, 06-02, 06-03

  it('returns [] for null/undefined/empty input', () => {
    expect(buildCampaignSeries(null, from, to)).toEqual([]);
    expect(buildCampaignSeries(undefined, from, to)).toEqual([]);
    expect(buildCampaignSeries([], from, to)).toEqual([]);
  });

  it('fills gaps PER series against the shared window, independently', () => {
    const series = [
      linkSeries({ link_id: 1, key: 'aaa', title: 'A', days: [day('2026-06-01', 3)] }),
      linkSeries({ link_id: 2, key: 'bbb', title: 'B', days: [day('2026-06-03', 5)] }),
    ];
    const result = buildCampaignSeries(series, from, to);
    expect(result).toHaveLength(2);
    // Series A has a click on 06-01 but nothing on 06-02/06-03 — those must
    // be zero-filled independently of series B, which is the inverse shape.
    expect(result[0].points.map((p) => [p.date, p.value])).toEqual([
      ['2026-06-01', 3],
      ['2026-06-02', 0],
      ['2026-06-03', 0],
    ]);
    expect(result[1].points.map((p) => [p.date, p.value])).toEqual([
      ['2026-06-01', 0],
      ['2026-06-02', 0],
      ['2026-06-03', 5],
    ]);
  });

  it('computes each series total from its own filled points', () => {
    const series = [linkSeries({ days: [day('2026-06-01', 3), day('2026-06-03', 4)] })];
    const result = buildCampaignSeries(series, from, to);
    expect(result[0].total).toBe(7);
  });

  it('labels a named series by title, falling back to key when title is blank', () => {
    const series = [
      linkSeries({ link_id: 1, key: 'abc123', title: 'Flyer A' }),
      linkSeries({ link_id: 2, key: 'def456', title: '' }),
    ];
    const result = buildCampaignSeries(series, from, to);
    expect(result[0].label).toBe('Flyer A');
    expect(result[1].label).toBe('def456');
    expect(result.every((s) => !s.isOther)).toBe(true);
  });

  it('assigns named series distinct, non-"Other" colors', () => {
    const series = [
      linkSeries({ link_id: 1, key: 'a' }),
      linkSeries({ link_id: 2, key: 'b' }),
      linkSeries({ link_id: 3, key: 'c' }),
    ];
    const result = buildCampaignSeries(series, from, to);
    const colors = result.map((s) => s.colorVar);
    expect(new Set(colors).size).toBe(3);
    expect(colors.every((c) => c !== CAMPAIGN_OTHER_SERIES_COLOR_VAR)).toBe(true);
  });

  it('marks the is_other row as Other, with the dedicated Other color and label', () => {
    const series = [
      linkSeries({ link_id: 1, key: 'a' }),
      linkSeries({ link_id: null, key: '', title: 'Other', is_other: true, days: [day('2026-06-02', 9)] }),
    ];
    const result = buildCampaignSeries(series, from, to);
    const other = result.find((s) => s.isOther);
    expect(other).toBeDefined();
    expect(other?.key).toBe('other');
    expect(other?.label).toBe('Other');
    expect(other?.colorVar).toBe(CAMPAIGN_OTHER_SERIES_COLOR_VAR);
    expect(other?.total).toBe(9);
  });

  it('keeps the OUTPUT in the backend-provided (rank) order, not id order', () => {
    // Backend ranks by click total desc; id 2 (higher total) arrives first.
    const series = [
      linkSeries({ link_id: 2, key: 'b', days: [day('2026-06-01', 10)] }),
      linkSeries({ link_id: 1, key: 'a', days: [day('2026-06-01', 1)] }),
    ];
    const result = buildCampaignSeries(series, from, to);
    expect(result.map((s) => s.key)).toEqual(['b', 'a']);
  });

  it('color assignment is STABLE across two fetches even when the rank order flips', () => {
    // Fetch 1: link 1 leads (rank order: 1, 2).
    const fetch1 = [
      linkSeries({ link_id: 1, key: 'a', days: [day('2026-06-01', 10)] }),
      linkSeries({ link_id: 2, key: 'b', days: [day('2026-06-01', 1)] }),
    ];
    // Fetch 2 (later poll, same two links, link 2 has since overtaken):
    // backend now ranks 2 before 1.
    const fetch2 = [
      linkSeries({ link_id: 2, key: 'b', days: [day('2026-06-01', 20)] }),
      linkSeries({ link_id: 1, key: 'a', days: [day('2026-06-01', 10)] }),
    ];
    const colors1 = new Map(buildCampaignSeries(fetch1, from, to).map((s) => [s.linkId, s.colorVar]));
    const colors2 = new Map(buildCampaignSeries(fetch2, from, to).map((s) => [s.linkId, s.colorVar]));
    expect(colors1.get(1)).toBe(colors2.get(1));
    expect(colors1.get(2)).toBe(colors2.get(2));
  });

  it('every named series plus Other reconciles to the campaign total (sum of all Days)', () => {
    const series = [
      linkSeries({ link_id: 1, key: 'a', days: [day('2026-06-01', 3), day('2026-06-02', 2)] }),
      linkSeries({ link_id: 2, key: 'b', days: [day('2026-06-03', 4)] }),
      linkSeries({ link_id: null, key: '', title: 'Other', is_other: true, days: [day('2026-06-01', 1)] }),
    ];
    const result = buildCampaignSeries(series, from, to);
    const total = result.reduce((sum, s) => sum + s.total, 0);
    expect(total).toBe(10);
  });
});

// ── Read-out / crosshair helpers (#0161) ────────────────────────────────────

describe('nearestIndex', () => {
  const geo = DEFAULT_CHART_GEO;
  it('returns -1 for no points and 0 for a single point', () => {
    expect(nearestIndex(100, 0)).toBe(-1);
    expect(nearestIndex(500, 1)).toBe(0);
  });
  it('snaps to the point whose xCoord is closest', () => {
    const n = 30;
    for (const i of [0, 1, 7, 15, 28, 29]) {
      expect(nearestIndex(xCoord(i, n, geo), n)).toBe(i);
      // A nudge less than half a step still lands on the same point.
      expect(nearestIndex(xCoord(i, n, geo) + 3, n)).toBe(i);
    }
  });
  it('picks the nearer neighbour between two points', () => {
    const n = 11;
    const mid = (xCoord(4, n) + xCoord(5, n)) / 2;
    expect(nearestIndex(mid - 1, n)).toBe(4);
    expect(nearestIndex(mid + 1, n)).toBe(5);
  });
  it('clamps pointers in the padding to the first/last point', () => {
    expect(nearestIndex(-50, 10)).toBe(0);
    expect(nearestIndex(0, 10)).toBe(0);
    expect(nearestIndex(9999, 10)).toBe(9);
  });
});

describe('clientToSvgX', () => {
  it('scales the pixel offset into viewBox units', () => {
    expect(clientToSvgX(150, 100, 300)).toBe(100); // 50/300 * 600
    expect(clientToSvgX(100, 100, 300)).toBe(0);
  });
  it('is safe for a zero-width box', () => {
    expect(clientToSvgX(5, 0, 0)).toBe(0);
  });
});

describe('stepIndex', () => {
  it('starts from the first point on ArrowRight and the last on ArrowLeft', () => {
    expect(stepIndex(null, 'ArrowRight', 30)).toBe(0);
    expect(stepIndex(null, 'ArrowLeft', 30)).toBe(29);
  });
  it('steps by one and clamps at both ends', () => {
    expect(stepIndex(5, 'ArrowRight', 30)).toBe(6);
    expect(stepIndex(5, 'ArrowLeft', 30)).toBe(4);
    expect(stepIndex(29, 'ArrowRight', 30)).toBe(29);
    expect(stepIndex(0, 'ArrowLeft', 30)).toBe(0);
  });
  it('jumps a week with PageUp/PageDown and to the ends with Home/End', () => {
    expect(stepIndex(10, 'PageUp', 30)).toBe(17);
    expect(stepIndex(10, 'PageDown', 30)).toBe(3);
    expect(stepIndex(28, 'PageUp', 30)).toBe(29);
    expect(stepIndex(10, 'Home', 30)).toBe(0);
    expect(stepIndex(10, 'End', 30)).toBe(29);
  });
  it('Escape hides the crosshair', () => {
    expect(stepIndex(3, 'Escape', 30)).toBeNull();
  });
  it('ignores other keys and empty charts', () => {
    expect(stepIndex(3, 'a', 30)).toBeUndefined();
    expect(stepIndex(3, 'Tab', 30)).toBeUndefined();
    expect(stepIndex(null, 'ArrowRight', 0)).toBeUndefined();
  });
});

describe('pointsForWindow / shiftDate / alignComparison', () => {
  it('fills an inclusive window', () => {
    const pts = pointsForWindow([day('2026-11-02', 4)], '2026-11-01', '2026-11-03');
    expect(pts.map((p) => p.value)).toEqual([0, 4, 0]);
    expect(pointsForWindow(null, '2026-11-01', '2026-11-02')).toHaveLength(2);
  });
  it('shifts dates across month boundaries', () => {
    expect(shiftDate('2026-11-01', -1)).toBe('2026-10-31');
    expect(shiftDate('2026-12-31', 1)).toBe('2027-01-01');
    expect(shiftDate('nope', 3)).toBe('nope');
  });
  it('aligns the previous period day for day, defaulting to the n days before from', () => {
    const prev = [day('2026-10-30', 9), day('2026-10-31', 8), day('2026-11-02', 1)];
    const cmp = alignComparison(prev, '2026-11-01', 3);
    expect(cmp.map((p) => p.date)).toEqual(['2026-10-29', '2026-10-30', '2026-10-31']);
    expect(cmp.map((p) => p.value)).toEqual([0, 9, 8]);
  });
  it('honours an explicit comparisonFrom and returns [] without data', () => {
    const cmp = alignComparison([day('2026-09-01', 5)], '2026-11-01', 2, '2026-09-01');
    expect(cmp.map((p) => p.value)).toEqual([5, 0]);
    expect(alignComparison(null, '2026-11-01', 3)).toEqual([]);
  });
});

describe('spreadLabels / truncateLabel / geoWithPadRight', () => {
  it('leaves well-separated labels alone', () => {
    expect(spreadLabels([20, 60, 100], 13, 10, 150)).toEqual([20, 60, 100]);
  });
  it('pushes colliding labels apart while keeping their order', () => {
    const out = spreadLabels([50, 50, 51], 13, 10, 150);
    expect(out[1] - out[0]).toBeGreaterThanOrEqual(13);
    expect(out[2] - out[1]).toBeGreaterThanOrEqual(13);
  });
  it('stays within bounds when pushed past the bottom edge', () => {
    const out = spreadLabels([148, 149, 150], 13, 10, 150);
    expect(Math.max(...out)).toBeLessThanOrEqual(150);
    expect(out[1] - out[0]).toBeGreaterThanOrEqual(13);
  });
  it('truncates with an ellipsis only when needed', () => {
    expect(truncateLabel('short', 14)).toBe('short');
    expect(truncateLabel('a very long link title', 10)).toBe('a very lo…');
  });
  it('recomputes innerW for a wider right pad', () => {
    const g = geoWithPadRight(112);
    expect(g.innerW).toBe(600 - 36 - 112);
    expect(g.padRight).toBe(112);
  });
});
