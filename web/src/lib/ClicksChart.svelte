<!--
  ClicksChart — inline SVG line chart for clicks-over-time (#0049, #0161).

  Renders a polyline chart with:
  - Responsive viewBox (width=100% so it scales to its container).
  - Y-axis grid lines and labels using design tokens only (no hardcoded hex).
  - A crosshair read-out driven by pointer hover/tap or the arrow keys
    (date, value, previous-period value, series name), announced through a
    polite live region.
  - Explicit `from`/`to` window props (inclusive). When omitted it falls back
    to the legacy `days` look-back ending yesterday.
  - Optional grey dashed `comparison` series aligned day for day.
  - Optional `partialLast`: the last point is hollow with a dotted final
    segment, labelled "today so far" in the read-out.
  - An accessible <title>/<desc> pair plus a visually-hidden summary table.
  - Graceful handling of empty or single-point data (no broken axes, no NaN).
-->
<script lang="ts">
  import type { DayBucket, TimeseriesResult } from './types';
  import {
    pointsForWindow,
    alignComparison,
    toPolylinePoints,
    yAxisTicks,
    yCoord,
    xCoord,
    nearestIndex,
    clientToSvgX,
    stepIndex,
    longDateLabel,
    defaultDateRange,
    chartGeoForWidth,
    xLabelIndices,
  } from './charts';
  import ChartReadout, { type ReadoutRow } from './ChartReadout.svelte';

  interface Props {
    timeseries: TimeseriesResult | undefined | null;
    /** Look-back length, used only when `from`/`to` are not given. */
    days?: number;
    /** Window start, "YYYY-MM-DD" inclusive. Give both `from` and `to` to set the window explicitly. */
    from?: string;
    /** Window end, "YYYY-MM-DD" inclusive. */
    to?: string;
    /** Previous-period days, aligned day for day with the window. */
    comparison?: DayBucket[] | null;
    /** First day of the comparison data; defaults to the same number of days immediately before `from`. */
    comparisonFrom?: string;
    /** Draw the last point hollow with a dotted final segment ("today so far"). */
    partialLast?: boolean;
    title?: string;
    /** Series name shown in the read-out. */
    seriesLabel?: string;
  }

  let {
    timeseries,
    days = 30,
    from,
    to,
    comparison = null,
    comparisonFrom,
    partialLast = false,
    title = 'Clicks over time',
    seriesLabel = 'Clicks',
  }: Props = $props();

  // Narrow containers get a taller, near-1:1 geometry so labels stay legible (#0162).
  let boxWidth = $state(0);
  const geo = $derived(chartGeoForWidth(boxWidth));
  const phone = $derived(geo.width < 600);

  const dateRange = $derived<[string, string]>(from && to ? [from, to] : defaultDateRange(days));
  const rangeStart = $derived(dateRange[0]);
  const rangeEnd = $derived(dateRange[1]);
  const points = $derived(pointsForWindow(timeseries?.days, rangeStart, rangeEnd));
  const cmpPoints = $derived(comparison ? alignComparison(comparison, rangeStart, points.length, comparisonFrom) : []);
  const hasCmp = $derived(cmpPoints.length > 0);

  // With partialLast the solid line stops one point short; the final segment is dotted.
  const hollowLast = $derived(partialLast && points.length > 1);
  const solidPoints = $derived(hollowLast ? points.slice(0, -1) : points);

  const max = $derived(Math.max(...points.map((p) => p.value), ...cmpPoints.map((p) => p.value), 0));
  const polyline = $derived(toLine(solidPoints));
  const cmpPolyline = $derived(toLine(cmpPoints));
  const ticks = $derived(yAxisTicks(max, 4));

  // The y scale is shared by the line, comparison and dots, so every polyline
  // goes through one helper that always passes the same max.
  function toLine(pts: { date: string; label: string; value: number }[]): string {
    if (pts.length === 0) return '';
    // toPolylinePoints spaces x across the series it is given, so pad a
    // shortened series back to full width by computing coordinates directly.
    return pts
      .map((p, i) => `${round(xCoord(i, points.length, geo))},${round(yCoord(p.value, max, geo))}`)
      .join(' ');
  }
  function round(n: number): number {
    return Math.round(n * 100) / 100;
  }

  const dottedSegment = $derived(
    hollowLast
      ? `${round(xCoord(points.length - 2, points.length, geo))},${round(yCoord(points[points.length - 2].value, max, geo))} ` +
          `${round(xCoord(points.length - 1, points.length, geo))},${round(yCoord(points[points.length - 1].value, max, geo))}`
      : '',
  );

  const totalClicks = $derived(points.reduce((s, p) => s + p.value, 0));
  const cmpTotal = $derived(cmpPoints.reduce((s, p) => s + p.value, 0));
  const isEmpty = $derived(points.length === 0 || (totalClicks === 0 && cmpTotal === 0));

  const tickData = $derived(
    ticks.map((v) => ({ value: v, y: yCoord(v, max, geo), label: v.toLocaleString() })),
  );

  // X-axis: as many labels as fit (about 56 units each, at most 5), evenly spaced.
  const xLabels = $derived(
    points.length === 0
      ? []
      : xLabelIndices(points.length, Math.min(5, Math.floor(geo.innerW / 56))).map((i) => ({
          label: points[i].label,
          x: round(xCoord(i, points.length, geo)),
          anchor: points.length === 1 ? 'middle' : i === 0 ? 'start' : i === points.length - 1 ? 'end' : 'middle',
        })),
  );

  // ── Crosshair ──────────────────────────────────────────────────────────
  let active = $state<number | null>(null);
  // Only keyboard stepping is announced; hover/tap stay silent so a mouse sweep does not flood the live region.
  let viaKeyboard = $state(false);
  let plotEl = $state<HTMLDivElement | null>(null);

  // A new window/data set can shrink the series under a live crosshair.
  const activeIdx = $derived(active !== null && active < points.length ? active : null);
  const activePoint = $derived(activeIdx !== null ? points[activeIdx] : null);
  const activeCmp = $derived(activeIdx !== null && hasCmp ? cmpPoints[activeIdx] : null);
  const isTodayPartial = $derived(partialLast && activeIdx !== null && activeIdx === points.length - 1);

  const readoutRows = $derived.by<ReadoutRow[]>(() => {
    if (!activePoint) return [];
    const rows: ReadoutRow[] = [
      { label: seriesLabel, value: activePoint.value.toLocaleString(), color: 'var(--accent)' },
    ];
    if (activeCmp) {
      rows.push({
        label: `Previous (${activeCmp.label})`,
        value: activeCmp.value.toLocaleString(),
        color: 'var(--text-faint)',
        dashed: true,
      });
    }
    return rows;
  });
  const announceText = $derived(
    activePoint
      ? `${longDateLabel(activePoint.date)}: ${activePoint.value.toLocaleString()} ${seriesLabel.toLowerCase()}` +
          (activeCmp ? `, previous period ${activeCmp.value.toLocaleString()}` : '') +
          (isTodayPartial ? ', today so far' : '')
      : '',
  );
  const crossX = $derived(activeIdx !== null ? xCoord(activeIdx, points.length, geo) : 0);

  function onPointer(e: PointerEvent) {
    viaKeyboard = false;
    if (!plotEl || points.length === 0) return;
    const r = plotEl.getBoundingClientRect();
    active = nearestIndex(clientToSvgX(e.clientX, r.left, r.width, geo), points.length, geo);
  }
  function onPointerLeave(e: PointerEvent) {
    // Touch keeps its read-out until the next tap; only a mouse leaving hides it.
    if (e.pointerType === 'mouse') active = null;
  }
  // iOS Safari does not focus (or blur) a tap target, so onblur alone never
  // dismisses a tapped read-out; any pointerdown outside the plot clears it.
  function onOutside(e: PointerEvent) {
    if (plotEl && !plotEl.contains(e.target as Node)) active = null;
  }
  const announce = $derived(viaKeyboard ? announceText : '');
  function onKey(e: KeyboardEvent) {
    const next = stepIndex(activeIdx, e.key, points.length);
    if (next === undefined) return;
    e.preventDefault();
    viaKeyboard = true;
    active = next;
  }

  const svgId = `clicks-chart-${Math.random().toString(36).slice(2, 8)}`;
  const titleId = `${svgId}-title`;
  const descId = `${svgId}-desc`;
</script>

<svelte:window onpointerdown={onOutside} />

<div class="chart-wrap" bind:clientWidth={boxWidth}>
  {#if isEmpty}
    <div class="chart-empty text-muted" aria-label="{title}: no data yet">
      No click data in this period.
    </div>
  {:else}
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions, a11y_no_noninteractive_tabindex -->
    <div
      class="plot"
      bind:this={plotEl}
      role="application"
      aria-roledescription="chart"
      aria-label="{title}. Use the arrow keys to read each day."
      tabindex="0"
      onpointermove={onPointer}
      onpointerdown={onPointer}
      onpointerleave={onPointerLeave}
      onkeydown={onKey}
      onblur={() => (active = null)}
    >
      <svg
        role="img"
        aria-labelledby="{titleId} {descId}"
        viewBox="0 0 {geo.width} {geo.height}"
        width="100%"
        xmlns="http://www.w3.org/2000/svg"
        class="chart-svg"
        class:phone
      >
        <title id={titleId}>{title}</title>
        <desc id={descId}>{totalClicks.toLocaleString()} clicks from {rangeStart} to {rangeEnd}.</desc>

        {#each tickData as tick (tick.value)}
          <line class="grid-line" x1={geo.padLeft} y1={tick.y} x2={geo.padLeft + geo.innerW} y2={tick.y} />
          <text class="axis-label" x={geo.padLeft - 4} y={tick.y} text-anchor="end" dominant-baseline="middle"
            >{tick.label}</text
          >
        {/each}

        {#each xLabels as xl}
          <text class="axis-label" x={xl.x} y={geo.padTop + geo.innerH + 18} text-anchor={xl.anchor}>{xl.label}</text>
        {/each}

        {#if polyline}
          {@const firstX = xCoord(0, points.length, geo)}
          {@const lastX = xCoord(solidPoints.length - 1, points.length, geo)}
          {@const baseY = geo.padTop + geo.innerH}
          <polygon class="chart-area" points="{firstX},{baseY} {polyline} {lastX},{baseY}" aria-hidden="true" />
        {/if}

        {#if hasCmp}
          <polyline class="comparison-line" points={cmpPolyline} aria-hidden="true" />
        {/if}

        <polyline class="chart-line" points={polyline} aria-hidden="true" />
        {#if dottedSegment}
          <polyline class="chart-line partial-segment" points={dottedSegment} aria-hidden="true" />
        {/if}

        {#each solidPoints as p, i (p.date)}
          {#if p.value > 0}
            <circle class="chart-dot" cx={round(xCoord(i, points.length, geo))} cy={round(yCoord(p.value, max, geo))} r="3" aria-hidden="true" />
          {/if}
        {/each}
        {#if hollowLast}
          {@const lp = points[points.length - 1]}
          <circle
            class="chart-dot hollow"
            data-testid="partial-dot"
            cx={round(xCoord(points.length - 1, points.length, geo))}
            cy={round(yCoord(lp.value, max, geo))}
            r="3.5"
            aria-hidden="true"
          />
        {/if}

        {#if activePoint}
          <line
            class="crosshair"
            x1={crossX}
            y1={geo.padTop}
            x2={crossX}
            y2={geo.padTop + geo.innerH}
            aria-hidden="true"
          />
          {#if activeCmp}
            <circle class="focus-dot cmp" cx={crossX} cy={yCoord(activeCmp.value, max, geo)} r="3.5" aria-hidden="true" />
          {/if}
          <circle class="focus-dot" cx={crossX} cy={yCoord(activePoint.value, max, geo)} r="4.5" aria-hidden="true" />
        {/if}
      </svg>

      {#if activePoint}
        <ChartReadout
          leftPct={(crossX / geo.width) * 100}
          title={longDateLabel(activePoint.date)}
          note={isTodayPartial ? 'today so far' : undefined}
          rows={readoutRows}
        />
      {/if}
    </div>

    <div class="sr-only" aria-live="polite" role="status">{announce}</div>

    <!-- Screen-reader data table (visually hidden) -->
    <table class="sr-only" aria-label="{title} data">
      <caption>{title} — {rangeStart} to {rangeEnd}</caption>
      <thead>
        <tr><th>Date</th><th>Clicks</th>{#if hasCmp}<th>Previous period</th>{/if}</tr>
      </thead>
      <tbody>
        {#each points as p, i (p.date)}
          {#if p.value > 0 || (hasCmp && cmpPoints[i]?.value > 0)}
            <tr>
              <td>{p.label}</td><td>{p.value.toLocaleString()}</td>
              {#if hasCmp}<td>{(cmpPoints[i]?.value ?? 0).toLocaleString()}</td>{/if}
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .chart-wrap {
    width: 100%;
  }
  .plot {
    position: relative;
    touch-action: pan-y;
    cursor: crosshair;
    border-radius: var(--radius);
  }
  .plot:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
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
  /* Phone geometry is near 1:1 with pixels, so a 12-unit label renders at about 11px. */
  .chart-svg.phone .axis-label {
    font-size: 12px;
  }

  .chart-line {
    fill: none;
    stroke: var(--accent);
    stroke-width: 2;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .partial-segment {
    stroke-dasharray: 1 5;
  }
  .comparison-line {
    fill: none;
    stroke: var(--text-faint);
    stroke-width: 1.5;
    stroke-dasharray: 5 4;
    stroke-linejoin: round;
  }
  .chart-area {
    fill: var(--accent-subtle);
    opacity: 0.6;
  }
  .chart-dot {
    fill: var(--accent);
    stroke: var(--bg-panel);
    stroke-width: 1.5;
  }
  .chart-dot.hollow {
    fill: var(--bg-panel);
    stroke: var(--accent);
    stroke-width: 2;
  }

  .crosshair {
    stroke: var(--text-muted);
    stroke-width: 1;
    pointer-events: none;
  }
  .focus-dot {
    fill: var(--accent);
    stroke: var(--bg-panel);
    stroke-width: 2;
    pointer-events: none;
  }
  .focus-dot.cmp {
    fill: var(--text-faint);
  }
</style>
