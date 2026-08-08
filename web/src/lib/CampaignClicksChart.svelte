<!--
  CampaignClicksChart — inline SVG multi-series line chart for a campaign's
  clicks over time (#0104), toggling between a single combined total and a
  per-link breakdown.

  Deliberately a SEPARATE component from ClicksChart.svelte rather than an
  extension of it (see this file's companion note in the #0104 report):
  ClicksChart's window model is `days` back from yesterday
  (lib/charts.ts's defaultDateRange) — right for a link, which has no
  campaign-style resolved window. A campaign's window is the server-resolved
  [windowFrom, windowTo) from CampaignStats.window_from/window_to (#0103
  downstream constraint 1: label the axis from THESE fields, not the
  campaign's nominal starts_at/ends_at, or the caption disagrees with the
  summary above it). Bolting an optional multi-series/legend/window-prop mode
  onto ClicksChart would have made the single-series 30-day link chart carry
  logic it never uses. The two components DO share their SVG geometry
  helpers (toPolylinePoints/yAxisTicks/yCoord/DEFAULT_CHART_GEO from
  lib/charts.ts) so the visual language stays one product, not two idioms.

  Renders ALWAYS (never hides the SVG behind an empty-state message the way
  ClicksChart does for a link with no data at all) — the #0104 AC requires a
  campaign with zero clicks in the window to still show axes, not a
  collapsed element, since "no clicks yet" is an expected, common state for
  a campaign chart the way it usually isn't for an established link.

  series_by_link (CampaignRollup, capped + "Other" in the query layer per
  #0102) is shaped by lib/charts.ts's buildCampaignSeries: per-series gap
  filling against the shared window, and colors that stay stable across
  renders by keying off link_id rather than the backend's rank order (see
  that function's doc comment).
-->
<script lang="ts">
  import type { LinkSeries, TimeseriesResult } from './types';
  import {
    fillDayGapsInWindow,
    toTimeseriesPoints,
    toPolylinePoints,
    buildCampaignSeries,
    yAxisTicks,
    yCoord,
    xCoord,
    DEFAULT_CHART_GEO,
    type CampaignSeriesChartData,
  } from './charts';
  import Button from './Button.svelte';

  interface Props {
    /** Combined clicks-over-time for the whole campaign (CampaignRollup/CampaignDetail's `timeseries`). */
    timeseries: TimeseriesResult | undefined | null;
    /** Capped + "Other"-folded per-link series (CampaignRollup's `series_by_link`). */
    seriesByLink: LinkSeries[] | undefined | null;
    /** CampaignStats.window_from — "YYYY-MM-DD", UTC, inclusive. */
    windowFrom: string;
    /** CampaignStats.window_to — "YYYY-MM-DD", UTC, EXCLUSIVE (half-open). */
    windowTo: string;
    title?: string;
  }

  let { timeseries, seriesByLink, windowFrom, windowTo, title = 'Clicks over time' }: Props = $props();

  const geo = DEFAULT_CHART_GEO;

  let mode = $state<'combined' | 'per-link'>('combined');

  const combinedFilled = $derived(fillDayGapsInWindow(timeseries?.days ?? [], windowFrom, windowTo));
  const combinedPoints = $derived(toTimeseriesPoints(combinedFilled));
  const combinedTotal = $derived(combinedPoints.reduce((s, p) => s + p.value, 0));
  const combinedSeries = $derived<CampaignSeriesChartData>({
    key: 'combined',
    linkId: null,
    label: 'All links',
    isOther: false,
    points: combinedPoints,
    total: combinedTotal,
    colorVar: 'var(--accent)',
  });

  const linkSeries = $derived(buildCampaignSeries(seriesByLink, windowFrom, windowTo));
  const hasPerLinkData = $derived(linkSeries.length > 0);

  // Per-link mode only actually applies when there is per-link data to show;
  // a stale toggle (e.g. flipped before the second request resolved) falls
  // back to combined rather than rendering nothing.
  const activeSeries = $derived(mode === 'per-link' && hasPerLinkData ? linkSeries : [combinedSeries]);
  const showLegend = $derived(mode === 'per-link' && activeSeries.length > 1);

  // Every series in activeSeries was filled against the SAME [windowFrom,
  // windowTo) window, so they share the same point count and date sequence
  // — safe to read the x-axis and the accessible table's rows from whichever
  // series happens to be first.
  const axisPoints = $derived(activeSeries[0]?.points ?? []);

  const maxValue = $derived(
    Math.max(0, ...activeSeries.flatMap((s) => s.points.map((p) => p.value))),
  );
  const ticks = $derived(yAxisTicks(maxValue, 4));
  const tickData = $derived(
    ticks.map((v) => ({ value: v, y: yCoord(v, maxValue, geo), label: v.toLocaleString() })),
  );

  // X-axis: show roughly 5 evenly-spaced date labels to avoid crowding — same
  // spacing rule as ClicksChart, with one addition: when the window length
  // isn't evenly divisible by the step, ClicksChart's original algorithm
  // unconditionally APPENDS the last day, which can land it one or two days
  // after the previous label — close enough to visually collide ("Aug
  // 6Aug 7" overlapping into unreadable text, seen at a 31-day window in
  // #0104 browser verification). Replacing the last regular label instead of
  // appending, whenever it would land within half a step of the final day,
  // keeps the "always show the last day" guarantee without the overlap.
  const xLabelCount = 5;
  const xLabels = $derived(
    axisPoints.length === 0
      ? []
      : (() => {
          const step = Math.max(1, Math.floor(axisPoints.length / (xLabelCount - 1)));
          const indices: number[] = [];
          for (let i = 0; i < axisPoints.length; i += step) indices.push(i);
          const lastIdx = axisPoints.length - 1;
          const lastPicked = indices[indices.length - 1];
          if (lastPicked !== lastIdx) {
            if (lastIdx - lastPicked < step / 2) {
              indices[indices.length - 1] = lastIdx;
            } else {
              indices.push(lastIdx);
            }
          }
          return indices.map((i) => {
            const x = xCoord(i, axisPoints.length, geo);
            // The first/last labels sit AT the chart's inner edges, where a
            // centered ("middle") anchor pushes half the text past the
            // padding into .chart-wrap's overflow:hidden and clips it
            // (visible on the rightmost date at a ~31-day window in browser
            // verification). Anchoring them start/end instead keeps the
            // full label inside the chart's own padding.
            const anchor =
              axisPoints.length === 1
                ? 'middle' // single-day window: the lone point sits at horizontal center, with room on both sides
                : i === 0
                  ? 'start'
                  : i === axisPoints.length - 1
                    ? 'end'
                    : 'middle';
            return { label: axisPoints[i].label, x: Math.round(x * 100) / 100, anchor };
          });
        })(),
  );

  function seriesX(points: { value: number }[], i: number): number {
    return xCoord(i, points.length, geo);
  }

  const rangeStart = $derived(axisPoints[0]?.date ?? windowFrom);
  const rangeEnd = $derived(axisPoints[axisPoints.length - 1]?.date ?? windowFrom);
  const isZeroWindow = $derived(combinedTotal === 0);

  // Built as one value (rather than interpolated inline across a line break
  // in the template) so the <desc> text never picks up a stray newline +
  // indentation as literal whitespace before the comma (#0104 review finding
  // 6 — rendered as "...to 2026-07-31\n      , split across 3 series.").
  const seriesSuffix = $derived(
    mode === 'per-link' && hasPerLinkData ? `, split across ${activeSeries.length} series` : '',
  );

  const svgId = `campaign-clicks-chart-${Math.random().toString(36).slice(2, 8)}`;
  const titleId = `${svgId}-title`;
  const descId = `${svgId}-desc`;
</script>

<div class="chart-wrap">
  {#if hasPerLinkData}
    <div class="mode-toggle" role="group" aria-label="Chart series">
      <Button
        variant={mode === 'combined' ? 'primary' : 'subtle'}
        aria-pressed={mode === 'combined'}
        onclick={() => (mode = 'combined')}
      >
        Combined
      </Button>
      <Button
        variant={mode === 'per-link' ? 'primary' : 'subtle'}
        aria-pressed={mode === 'per-link'}
        onclick={() => (mode = 'per-link')}
      >
        Per link
      </Button>
    </div>
  {/if}

  <svg
    role="img"
    aria-labelledby="{titleId} {descId}"
    viewBox="0 0 {geo.width} {geo.height}"
    width="100%"
    xmlns="http://www.w3.org/2000/svg"
    class="chart-svg"
  >
    <title id={titleId}>{title}</title>
    <desc id={descId}>{combinedTotal.toLocaleString()} clicks from {rangeStart} to {rangeEnd}{seriesSuffix}.</desc>

    <!-- Grid lines (horizontal, one per y-axis tick) — rendered even at zero
         so the chart never collapses to a blank box (#0104 AC). -->
    {#each tickData as tick (tick.value)}
      <line class="grid-line" x1={geo.padLeft} y1={tick.y} x2={geo.padLeft + geo.innerW} y2={tick.y} />
      <text class="axis-label" x={geo.padLeft - 4} y={tick.y} text-anchor="end" dominant-baseline="middle"
        >{tick.label}</text
      >
    {/each}

    <!-- X-axis labels -->
    {#each xLabels as xl}
      <text class="axis-label" x={xl.x} y={geo.padTop + geo.innerH + 18} text-anchor={xl.anchor}>{xl.label}</text>
    {/each}

    {#each activeSeries as s (s.key)}
      {@const poly = toPolylinePoints(s.points, geo, maxValue)}
      {#if mode === 'combined' && poly}
        {@const firstX = xCoord(0, s.points.length, geo)}
        {@const lastX = xCoord(s.points.length - 1, s.points.length, geo)}
        {@const baseY = geo.padTop + geo.innerH}
        <polygon class="chart-area" points="{firstX},{baseY} {poly} {lastX},{baseY}" aria-hidden="true" />
      {/if}

      <polyline
        class="chart-line"
        class:other-line={s.isOther}
        style="stroke: {s.colorVar}"
        points={poly}
        aria-hidden="true"
      />

      {#each s.points as p, i (p.date)}
        {#if p.value > 0}
          <circle
            class="chart-dot"
            class:other-dot={s.isOther}
            style="fill: {s.colorVar}"
            cx={Math.round(seriesX(s.points, i) * 100) / 100}
            cy={Math.round(yCoord(p.value, maxValue, geo) * 100) / 100}
            r="3"
            aria-hidden="true"
          >
            <title>{s.label} — {p.label}: {p.value.toLocaleString()} clicks</title>
          </circle>
        {/if}
      {/each}
    {/each}
  </svg>

  {#if showLegend}
    <ul class="chart-legend" aria-hidden="true">
      {#each activeSeries as s (s.key)}
        <li class:legend-other={s.isOther}>
          <span class="legend-swatch" style="background: {s.colorVar}"></span>
          <span class="legend-label" title={s.label}>{s.label}</span>
          <span class="legend-total">{s.total.toLocaleString()}</span>
        </li>
      {/each}
    </ul>
  {/if}

  {#if isZeroWindow}
    <p class="text-muted chart-note">No clicks in this window.</p>
  {/if}

  <!-- Screen-reader data table (visually hidden) — one column per active
       series, doubling as the legend's accessible/visible-text fallback
       (dataviz relief channel for the light-mode series whose line color
       alone sits below 3:1 contrast). -->
  <table class="sr-only" aria-label="{title} data">
    <caption>{title} — {rangeStart} to {rangeEnd}</caption>
    <thead>
      <tr>
        <th>Date</th>
        {#each activeSeries as s (s.key)}<th>{s.label}</th>{/each}
      </tr>
    </thead>
    <tbody>
      {#each axisPoints as p, i (p.date)}
        <tr>
          <td>{p.label}</td>
          {#each activeSeries as s (s.key)}<td>{s.points[i]?.value ?? 0}</td>{/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .chart-wrap {
    width: 100%;
    overflow: hidden;
  }
  .mode-toggle {
    display: flex;
    gap: var(--space-2);
    margin-bottom: var(--space-3);
  }
  .chart-svg {
    display: block;
    width: 100%;
    overflow: visible;
  }
  .chart-note {
    padding: var(--space-2) 0 0;
    font-size: var(--fs-sm);
  }

  .grid-line {
    stroke: var(--border);
    stroke-width: 1;
    stroke-dasharray: 3 3;
  }
  .axis-label {
    font-size: 10px;
    fill: var(--text-faint);
    font-family: var(--font-mono);
  }

  .chart-line {
    fill: none;
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  /* "Other" is dashed on top of its own muted color — two independent
     signals (not color alone) that it is a fold, not a named link. */
  .other-line {
    stroke-dasharray: 4 3;
  }
  .chart-area {
    fill: var(--accent-subtle);
    opacity: 0.6;
  }
  .chart-dot {
    stroke: var(--bg-panel);
    stroke-width: 1.5;
  }
  .other-dot {
    opacity: 0.85;
  }

  /* ── Legend ──────────────────────────────────────────────────────────── */
  .chart-legend {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2) var(--space-4);
    margin: var(--space-3) 0 0;
    padding: 0;
    list-style: none;
  }
  .chart-legend li {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    min-width: 0;
    font-size: var(--fs-sm);
  }
  .legend-swatch {
    flex: 0 0 auto;
    width: 10px;
    height: 10px;
    border-radius: 2px;
  }
  .legend-other .legend-swatch {
    border: 1px dashed var(--text-faint);
    background: transparent !important;
  }
  .legend-label {
    max-width: 9rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text);
  }
  .legend-other .legend-label {
    color: var(--text-faint);
    font-style: italic;
  }
  .legend-total {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }

  @media (max-width: 480px) {
    .legend-label {
      max-width: 6.5rem;
    }
    .chart-legend {
      gap: var(--space-1) var(--space-3);
    }
  }
</style>
