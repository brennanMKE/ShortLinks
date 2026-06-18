// Pure, framework-free data-shaping helpers for the inline SVG charts (#0049).
// No DOM or Svelte dependencies — every function is unit-testable with vitest.

import type { DayBucket, UTMBucket } from './types';

// ── Timeseries (clicks-over-time) ──────────────────────────────────────────

/**
 * A normalised point in the clicks-over-time chart. `label` is the short
 * display string (e.g. "Jun 1"); `value` is the raw count for tooltip/a11y.
 */
export interface TimeseriesPoint {
  date: string;   // "YYYY-MM-DD" — chart key and aria label
  label: string;  // short display string, e.g. "Jun 1"
  value: number;
}

/**
 * Fill date gaps in the server-returned sparse day buckets so the chart line
 * is continuous. The server only returns days with ≥1 click; this function
 * inserts zero-count entries for every calendar day in [startDate, endDate].
 * Both dates are "YYYY-MM-DD" strings (UTC). Returns an empty array when
 * startDate > endDate.
 */
export function fillDayGaps(
  days: DayBucket[],
  startDate: string,
  endDate: string,
): DayBucket[] {
  if (startDate > endDate) return [];

  // Build a lookup: date → count.
  const byDate = new Map<string, number>(days.map((d) => [d.date, d.count]));

  const result: DayBucket[] = [];
  // Walk calendar days from start to end.
  const start = parseDateUTC(startDate);
  const end = parseDateUTC(endDate);
  if (!start || !end) return [];

  const cur = new Date(start);
  while (cur <= end) {
    const key = toDateString(cur);
    result.push({ date: key, count: byDate.get(key) ?? 0 });
    cur.setUTCDate(cur.getUTCDate() + 1);
  }
  return result;
}

/**
 * Convert sparse DayBucket[] (from the API) into TimeseriesPoint[] ready for
 * charting. If days is empty or null, returns []. Fills no gaps — use
 * fillDayGaps first when you want a continuous axis.
 */
export function toTimeseriesPoints(days: DayBucket[] | undefined | null): TimeseriesPoint[] {
  if (!days || days.length === 0) return [];
  return days.map((d) => ({
    date: d.date,
    label: shortDateLabel(d.date),
    value: d.count,
  }));
}

/**
 * Compute the default date range for the 30-day chart window as [startDate,
 * endDate] "YYYY-MM-DD" strings (UTC). The window ends at yesterday (the API
 * returns clicks < today's midnight so today is always incomplete) and runs
 * back `days` calendar days. Returns today - days ... today - 1.
 */
export function defaultDateRange(days = 30): [string, string] {
  const now = new Date();
  const end = new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - 1));
  const start = new Date(end);
  start.setUTCDate(start.getUTCDate() - (days - 1));
  return [toDateString(start), toDateString(end)];
}

// ── UTM bar-chart breakdown ─────────────────────────────────────────────────

/**
 * One row in the proportional UTM bar chart. `pct` is [0,100] and is safe
 * against divide-by-zero (returns 0 when total is 0).
 */
export interface BarRow {
  value: string;
  count: number;
  pct: number;   // 0–100, safe for <bar width={pct}%>
}

/**
 * Convert a UTMBucket[] into BarRow[]: adds a `pct` field (percentage of the
 * total for that dimension, [0,100]). Safe with an empty or null input (returns
 * []). Each bucket's pct is calculated against the sum of all counts in the
 * passed array (not the link's total click count), so the bars always fill to
 * 100% of the dimension.
 */
export function toBarRows(buckets: UTMBucket[] | undefined | null): BarRow[] {
  if (!buckets || buckets.length === 0) return [];
  const total = buckets.reduce((s, b) => s + b.count, 0);
  return buckets.map((b) => ({
    value: b.value,
    count: b.count,
    pct: total === 0 ? 0 : Math.round((b.count / total) * 100),
  }));
}

// ── SVG geometry helpers ────────────────────────────────────────────────────

/**
 * The geometry contract for the SVG line/bar chart. All coordinates are in SVG
 * user units within a viewBox of `0 0 width height`.
 */
export interface ChartGeometry {
  width: number;
  height: number;
  padLeft: number;
  padRight: number;
  padTop: number;
  padBottom: number;
  /** Inner drawable area width (width - padLeft - padRight). */
  innerW: number;
  /** Inner drawable area height (height - padTop - padBottom). */
  innerH: number;
}

/** Default geometry for the clicks-over-time chart. */
export const DEFAULT_CHART_GEO: ChartGeometry = {
  width: 600,
  height: 180,
  padLeft: 36,
  padRight: 12,
  padTop: 12,
  padBottom: 32,
  innerW: 600 - 36 - 12,
  innerH: 180 - 12 - 32,
};

/**
 * Map an array of TimeseriesPoint[] to SVG polyline coordinates.
 * Returns an empty string ("") when points is empty (safe for the `points`
 * attribute of <polyline>).
 *
 * x is distributed evenly across innerW; y is scaled linearly to [0,innerH]
 * with 0 at the bottom (SVG y=innerH → chart y=0, SVG y=0 → chart y=max).
 * When there is only one point it is placed at the horizontal centre.
 */
export function toPolylinePoints(
  points: TimeseriesPoint[],
  geo: ChartGeometry = DEFAULT_CHART_GEO,
): string {
  if (points.length === 0) return '';
  const max = Math.max(...points.map((p) => p.value), 1); // guard divide-by-zero
  const { padLeft, padTop, innerW, innerH } = geo;

  return points
    .map((p, i) => {
      const x = padLeft + (points.length === 1 ? innerW / 2 : (i / (points.length - 1)) * innerW);
      const y = padTop + innerH - (p.value / max) * innerH;
      return `${round(x)},${round(y)}`;
    })
    .join(' ');
}

/**
 * Y-axis grid line values for the chart: up to `count` evenly-spaced ticks
 * from 0 to max (inclusive). Safe with a max of 0 (returns [0]). Ticks are
 * rounded to a human-friendly value (power of 10 multiple).
 */
export function yAxisTicks(max: number, count = 4): number[] {
  if (max <= 0) return [0];
  const step = Math.ceil(max / count);
  const ticks: number[] = [];
  for (let v = 0; v <= max; v += step) {
    ticks.push(v);
  }
  // Always include 0 and max.
  if (ticks[ticks.length - 1] < max) ticks.push(max);
  return ticks;
}

/**
 * Map a data value to its SVG y coordinate within the chart geometry. Safe
 * against max=0 (returns the baseline).
 */
export function yCoord(value: number, max: number, geo: ChartGeometry = DEFAULT_CHART_GEO): number {
  const { padTop, innerH } = geo;
  if (max <= 0) return padTop + innerH;
  return padTop + innerH - (value / max) * innerH;
}

// ── Private utilities ───────────────────────────────────────────────────────

/** Parse "YYYY-MM-DD" as a UTC Date. Returns null on invalid input. */
function parseDateUTC(s: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s);
  if (!m) return null;
  return new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]));
}

/** Format a UTC Date as "YYYY-MM-DD". */
function toDateString(d: Date): string {
  const y = d.getUTCFullYear();
  const mo = String(d.getUTCMonth() + 1).padStart(2, '0');
  const da = String(d.getUTCDate()).padStart(2, '0');
  return `${y}-${mo}-${da}`;
}

/**
 * Short display label for a "YYYY-MM-DD" date string, e.g. "Jun 1".
 * Falls back to the raw string on parse failure.
 */
function shortDateLabel(iso: string): string {
  const d = parseDateUTC(iso);
  if (!d) return iso;
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

/** Round to two decimal places to keep SVG coordinates compact. */
function round(n: number): number {
  return Math.round(n * 100) / 100;
}
