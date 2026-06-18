<!--
  ClicksChart — inline SVG line chart for clicks-over-time (#0049).

  Renders a polyline chart with:
  - Responsive viewBox (width=100% so it scales to its container).
  - Y-axis grid lines and labels using design tokens only (no hardcoded hex).
  - An accessible <title>/<desc> pair inside the SVG plus a visually-hidden
    summary table so screen readers see the underlying numbers.
  - Graceful handling of empty or single-point data (no broken axes, no NaN).
-->
<script lang="ts">
  import type { TimeseriesResult } from './types';
  import {
    fillDayGaps,
    toTimeseriesPoints,
    toPolylinePoints,
    yAxisTicks,
    yCoord,
    defaultDateRange,
    DEFAULT_CHART_GEO,
  } from './charts';

  interface Props {
    timeseries: TimeseriesResult | undefined | null;
    /** Number of days in the look-back window (must match the API's default). */
    days?: number;
    title?: string;
  }

  let { timeseries, days = 30, title = 'Clicks over time' }: Props = $props();

  const geo = DEFAULT_CHART_GEO;

  // Fill day gaps so the chart is continuous across the full window.
  // Use $derived so `days` is tracked reactively.
  const dateRange = $derived(defaultDateRange(days));
  const rangeStart = $derived(dateRange[0]);
  const rangeEnd = $derived(dateRange[1]);
  const filled = $derived(fillDayGaps(timeseries?.days ?? [], rangeStart, rangeEnd));
  const points = $derived(toTimeseriesPoints(filled));
  const polyline = $derived(toPolylinePoints(points, geo));
  const max = $derived(Math.max(...points.map((p) => p.value), 0));
  const ticks = $derived(yAxisTicks(max, 4));

  // Accessible summary: e.g. "30 days, 142 total clicks"
  const totalClicks = $derived(points.reduce((s, p) => s + p.value, 0));
  const isEmpty = $derived(points.length === 0 || totalClicks === 0);

  // Build tick Y coords and labels.
  const tickData = $derived(
    ticks.map((v) => ({
      value: v,
      y: yCoord(v, max, geo),
      label: v.toLocaleString(),
    })),
  );

  // X-axis: show roughly 5 evenly-spaced date labels to avoid crowding.
  const xLabelCount = 5;
  const xLabels = $derived(
    points.length === 0
      ? []
      : (() => {
          const step = Math.max(1, Math.floor(points.length / (xLabelCount - 1)));
          const indices: number[] = [];
          for (let i = 0; i < points.length; i += step) indices.push(i);
          // Always include the last point.
          if (indices[indices.length - 1] !== points.length - 1) {
            indices.push(points.length - 1);
          }
          return indices.map((i) => {
            const x =
              geo.padLeft +
              (points.length === 1 ? geo.innerW / 2 : (i / (points.length - 1)) * geo.innerW);
            return { label: points[i].label, x: Math.round(x * 100) / 100 };
          });
        })(),
  );

  const svgId = `clicks-chart-${Math.random().toString(36).slice(2, 8)}`;
  const titleId = `${svgId}-title`;
  const descId = `${svgId}-desc`;
</script>

<div class="chart-wrap">
  {#if isEmpty}
    <div class="chart-empty text-muted" aria-label="{title}: no data yet">
      No click data in this period.
    </div>
  {:else}
    <!-- Accessible SVG: described by title + desc, with a hidden data table below. -->
    <svg
      role="img"
      aria-labelledby="{titleId} {descId}"
      viewBox="0 0 {geo.width} {geo.height}"
      width="100%"
      xmlns="http://www.w3.org/2000/svg"
      class="chart-svg"
    >
      <title id={titleId}>{title}</title>
      <desc id={descId}>{totalClicks.toLocaleString()} clicks over the last {days} days ({rangeStart} to {rangeEnd}).</desc>

      <!-- Grid lines (horizontal, one per y-axis tick) -->
      {#each tickData as tick (tick.value)}
        <line
          class="grid-line"
          x1={geo.padLeft}
          y1={tick.y}
          x2={geo.padLeft + geo.innerW}
          y2={tick.y}
        />
        <!-- Y-axis label -->
        <text
          class="axis-label"
          x={geo.padLeft - 4}
          y={tick.y}
          text-anchor="end"
          dominant-baseline="middle"
        >{tick.label}</text>
      {/each}

      <!-- X-axis labels -->
      {#each xLabels as xl}
        <text
          class="axis-label"
          x={xl.x}
          y={geo.padTop + geo.innerH + 18}
          text-anchor="middle"
        >{xl.label}</text>
      {/each}

      <!-- Area fill under the line -->
      {#if polyline}
        {@const firstPt = points[0]}
        {@const lastPt = points[points.length - 1]}
        {@const firstX = geo.padLeft + (points.length === 1 ? geo.innerW / 2 : 0)}
        {@const lastX = geo.padLeft + geo.innerW}
        {@const baseY = geo.padTop + geo.innerH}
        <polygon
          class="chart-area"
          points="{firstX},{baseY} {polyline} {lastX},{baseY}"
          aria-hidden="true"
        />
      {/if}

      <!-- Line -->
      <polyline class="chart-line" points={polyline} aria-hidden="true" />

      <!-- Dots at each data point (optional, helps with single-point) -->
      {#each points as p, i (p.date)}
        {@const x = geo.padLeft + (points.length === 1 ? geo.innerW / 2 : (i / (points.length - 1)) * geo.innerW)}
        {@const y = yCoord(p.value, max, geo)}
        {#if p.value > 0}
          <circle
            class="chart-dot"
            cx={Math.round(x * 100) / 100}
            cy={Math.round(y * 100) / 100}
            r="3"
            aria-hidden="true"
          >
            <title>{p.label}: {p.value.toLocaleString()} clicks</title>
          </circle>
        {/if}
      {/each}
    </svg>

    <!-- Screen-reader data table (visually hidden) -->
    <table class="sr-only" aria-label="{title} data">
      <caption>{title} — {rangeStart} to {rangeEnd}</caption>
      <thead>
        <tr><th>Date</th><th>Clicks</th></tr>
      </thead>
      <tbody>
        {#each points as p (p.date)}
          {#if p.value > 0}
            <tr><td>{p.label}</td><td>{p.value.toLocaleString()}</td></tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .chart-wrap {
    width: 100%;
    overflow: hidden;
  }
  .chart-svg {
    display: block;
    width: 100%;
    overflow: visible;
  }
  .chart-empty {
    padding: var(--space-5) 0;
    font-size: var(--fs-sm);
  }

  /* Grid lines — use token for border colour */
  .grid-line {
    stroke: var(--border);
    stroke-width: 1;
    stroke-dasharray: 3 3;
  }

  /* Axis labels */
  .axis-label {
    font-size: 10px;
    fill: var(--text-faint);
    font-family: var(--font-mono);
  }

  /* Line */
  .chart-line {
    fill: none;
    stroke: var(--accent);
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }

  /* Area fill */
  .chart-area {
    fill: var(--accent-subtle);
    opacity: 0.6;
  }

  /* Dots */
  .chart-dot {
    fill: var(--accent);
    stroke: var(--bg-panel);
    stroke-width: 1.5;
  }
</style>
