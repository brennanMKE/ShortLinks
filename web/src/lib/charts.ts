// Pure, framework-free data-shaping helpers for the inline SVG charts (#0049).
// No DOM or Svelte dependencies — every function is unit-testable with vitest.

import type { DayBucket, LinkSeries, UTMBucket } from './types';

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
 * Fill day gaps against a resolved, HALF-OPEN [windowFrom, windowTo) window —
 * `CampaignStats.window_from`/`window_to` (#0102/#0103's convention, "YYYY-
 * MM-DD", UTC). Unlike `fillDayGaps` (which takes an INCLUSIVE end and is
 * what the days-lookback-based `ClicksChart` still uses via
 * `defaultDateRange`), `windowTo` here is EXCLUSIVE: it is the first day
 * NOT in the window, not the last day that is. Subtracting one calendar day
 * before delegating to `fillDayGaps` is the one place that conversion
 * happens, so every campaign chart series fills gaps against the same
 * corrected end — getting this wrong renders a phantom trailing day with a
 * zero count that was never queried (#0103 downstream constraint 3's
 * "gotcha", generalized from the links table to every per-day series here).
 *
 * A single-day window (windowTo exactly one day after windowFrom) therefore
 * fills exactly one bucket, not two. Returns [] for a malformed window or an
 * empty/reversed one (windowFrom >= windowTo).
 */
export function fillDayGapsInWindow(
  days: DayBucket[],
  windowFrom: string,
  windowTo: string,
): DayBucket[] {
  const from = parseDateUTC(windowFrom);
  const to = parseDateUTC(windowTo);
  if (!from || !to || from.getTime() >= to.getTime()) return [];
  const inclusiveEnd = new Date(to);
  inclusiveEnd.setUTCDate(inclusiveEnd.getUTCDate() - 1);
  return fillDayGaps(days, windowFrom, toDateString(inclusiveEnd));
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

// ── Campaign multi-series chart (#0104) ─────────────────────────────────────
//
// CampaignSeriesByLink (internal/clicks/stats.go) already caps the series
// count at 6 named links + one folded "Other" (seriesByLinkCap) — that cap is
// NOT re-implemented here. This section only shapes what the backend already
// returned: per-series gap-filling and a stable color assignment.

/**
 * Number of individually-colored named-series slots (`--series-1` …
 * `--series-6` in app.css). This is the size of the color palette, not a
 * restatement of internal/clicks/stats.go's `seriesByLinkCap` — the two
 * happen to agree today because the cap was chosen from the same "5–8 named
 * series" range the palette was validated for, but if the query layer's cap
 * ever changes, this only needs to change if the new cap exceeds the
 * validated palette size (buildCampaignSeries wraps defensively either way).
 */
export const CAMPAIGN_SERIES_COLOR_COUNT = 6;

/**
 * CSS custom-property reference for a named series' color slot (0-based).
 * Wraps modulo CAMPAIGN_SERIES_COLOR_COUNT as a defensive fallback only —
 * the backend never sends more than 6 named series — so a future drift
 * degrades to a repeated color rather than an undefined CSS variable.
 */
export function campaignSeriesColorVar(slot: number): string {
  const n = ((slot % CAMPAIGN_SERIES_COLOR_COUNT) + CAMPAIGN_SERIES_COLOR_COUNT) % CAMPAIGN_SERIES_COLOR_COUNT;
  return `var(--series-${n + 1})`;
}

/**
 * CSS custom-property reference for the synthetic "Other" fold series.
 * Deliberately outside the 1..6 named palette (it resolves to --text-faint,
 * not a hue) so the fold reads as a muted aggregate rather than a seventh
 * identity — the AC's "Other is visually distinguishable from named links
 * and is not mistaken for one."
 */
export const CAMPAIGN_OTHER_SERIES_COLOR_VAR = 'var(--series-other)';

/** One series in the campaign per-link clicks-over-time chart, shaped for rendering. */
export interface CampaignSeriesChartData {
  /** Stable render key: the link's key, or 'other' for the folded series. */
  key: string;
  linkId: number | null;
  label: string;
  isOther: boolean;
  points: TimeseriesPoint[];
  total: number;
  /** CSS `var(--series-N)` / `var(--series-other)` reference — never a raw hex. */
  colorVar: string;
}

/**
 * Shapes CampaignRollup.series_by_link into chart-ready series: gap-filled
 * per series against the shared [windowFrom, windowTo) window (#0103
 * downstream constraint 3 — "LinkSeries.Days omits zero-click days, same as
 * TimeseriesResult — the frontend fills gaps, and must do it PER series"),
 * plus a color assignment that stays STABLE across renders of the same
 * campaign.
 *
 * Stability mechanism: named (non-"Other") series are sorted by `link_id`
 * ascending — a property of the link itself that never changes — and slots
 * are handed out in THAT order, never in the order `series` arrives in
 * (which is the backend's rank-by-click-total-in-window order, and can
 * reorder between two fetches of the same unchanged links as their totals
 * shift). So the same set of top-N link ids always maps to the same color
 * slot regardless of which one currently has more clicks — "series colors
 * need to stay stable across renders... or the chart appears to change
 * meaning when data refreshes." The OUTPUT array keeps the backend's
 * original (rank) order, since that is the useful order for a legend —
 * only the color assignment is decoupled from it.
 *
 * Returns [] for null/undefined/empty input.
 */
export function buildCampaignSeries(
  series: LinkSeries[] | undefined | null,
  windowFrom: string,
  windowTo: string,
): CampaignSeriesChartData[] {
  if (!series || series.length === 0) return [];

  const namedIds = series
    .filter((s) => !s.is_other && s.link_id !== null)
    .map((s) => s.link_id as number)
    .sort((a, b) => a - b);
  const colorByLinkId = new Map<number, string>();
  namedIds.forEach((id, i) => colorByLinkId.set(id, campaignSeriesColorVar(i)));

  return series.map((s) => {
    const filled = fillDayGapsInWindow(s.days, windowFrom, windowTo);
    const points = toTimeseriesPoints(filled);
    const total = points.reduce((sum, p) => sum + p.value, 0);
    const colorVar = s.is_other
      ? CAMPAIGN_OTHER_SERIES_COLOR_VAR
      : (s.link_id !== null ? colorByLinkId.get(s.link_id) : undefined) ?? CAMPAIGN_OTHER_SERIES_COLOR_VAR;
    return {
      key: s.is_other ? 'other' : s.key,
      linkId: s.link_id,
      label: s.is_other ? 'Other' : s.title || s.key,
      isOther: s.is_other,
      points,
      total,
      colorVar,
    };
  });
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
 * x is distributed evenly across innerW (via xCoord); y is scaled linearly
 * to [0,innerH] with 0 at the bottom (SVG y=innerH → chart y=0, SVG y=0 →
 * chart y=max). When there is only one point it is placed at the horizontal
 * centre.
 *
 * `max` is the scale this series' y-coordinates are computed against.
 * Defaults to the series' OWN peak (`undefined` → self-derived, matching the
 * original behaviour this parameter was added to) — correct for
 * ClicksChart's single series, which has no other series to share a scale
 * with. A multi-series chart (CampaignClicksChart's per-link mode) MUST pass
 * the same shared max for every series it draws — passing each series its
 * own self-derived max independently would normalize every line to its own
 * peak, so a 2-click-a-day series would draw identically to a 60-click-a-day
 * one (#0104 review finding 1: "every polyline is normalized to its own
 * peak... every series touches the top of the plot regardless of value",
 * caught only because the same points ALSO render as dots via yCoord+the
 * shared max prop in CampaignClicksChart, which don't agree with the line —
 * see buildCampaignSeries/CampaignClicksChart.svelte).
 */
export function toPolylinePoints(
  points: TimeseriesPoint[],
  geo: ChartGeometry = DEFAULT_CHART_GEO,
  max?: number,
): string {
  if (points.length === 0) return '';
  // Math.max(..., 1) guards divide-by-zero for both the self-derived path
  // (all-zero series) and an explicit max of 0 (e.g. a shared max computed
  // from an all-zero window).
  const effectiveMax = Math.max(max ?? Math.max(...points.map((p) => p.value)), 1);
  const { padTop, innerH } = geo;

  return points
    .map((p, i) => {
      const x = xCoord(i, points.length, geo);
      const y = padTop + innerH - (p.value / effectiveMax) * innerH;
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

/**
 * Map a point index to its SVG x coordinate within the chart geometry:
 * evenly distributed across innerW, with a single point (n===1) placed at
 * the horizontal centre rather than at padLeft. This is the one x-position
 * formula every chart in this file/its consuming components uses — it was
 * previously hand-inlined identically in four places (toPolylinePoints,
 * CampaignClicksChart.svelte's xLabels calc, its seriesX() helper, and its
 * area-polygon firstX/lastX) and was the real drift vector between the two
 * chart components (#0104 review finding 7).
 */
export function xCoord(i: number, n: number, geo: ChartGeometry = DEFAULT_CHART_GEO): number {
  const { padLeft, innerW } = geo;
  return padLeft + (n === 1 ? innerW / 2 : (i / (n - 1)) * innerW);
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
