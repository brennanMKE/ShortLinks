<!--
  ChartReadout — the small floating read-out the line charts show next to
  their crosshair (#0161). Purely presentational: the parent owns the
  crosshair index and passes the already-formatted rows. Positioned by
  percentage of the chart box, flipping to the left of the crosshair past the
  midpoint so it never leaves the chart. aria-hidden: the chart wrapper
  announces the same text through its own live region.
-->
<script lang="ts" module>
  export interface ReadoutRow {
    label: string;
    value: string;
    /** CSS color for the swatch, e.g. `var(--series-1)`. */
    color?: string;
    /** Draw the swatch dashed (comparison series). */
    dashed?: boolean;
  }
</script>

<script lang="ts">
  interface Props {
    /** Crosshair x as a percentage [0,100] of the chart box width. */
    leftPct: number;
    title: string;
    rows: ReadoutRow[];
    /** Small trailing note, e.g. "today so far". */
    note?: string;
  }

  let { leftPct, title, rows, note }: Props = $props();
  const flip = $derived(leftPct > 55);
</script>

<div
  class="readout"
  class:flip
  style="left: {leftPct}%"
  aria-hidden="true"
  data-testid="chart-readout"
>
  <div class="readout-title">{title}{#if note}<span class="readout-note">&nbsp;· {note}</span>{/if}</div>
  {#each rows as r}
    <div class="readout-row">
      {#if r.color}
        <span class="swatch" class:dashed={r.dashed} style="--sw: {r.color}"></span>
      {/if}
      <span class="readout-label">{r.label}</span>
      <span class="readout-value">{r.value}</span>
    </div>
  {/each}
</div>

<style>
  .readout {
    position: absolute;
    top: 4px;
    transform: translateX(12px);
    z-index: 2;
    min-width: 8rem;
    max-width: 14rem;
    padding: var(--space-2) var(--space-3);
    background: var(--bg-panel);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 2px 8px rgb(0 0 0 / 0.15);
    font-size: var(--fs-sm);
    pointer-events: none;
  }
  .readout.flip {
    transform: translateX(calc(-100% - 12px));
  }
  /* Phone widths: the read-out must not swallow the chart it describes. */
  @media (max-width: 480px) {
    .readout {
      min-width: 0;
      max-width: 11rem;
      padding: var(--space-1) var(--space-2);
      font-size: 12px;
      transform: translateX(8px);
    }
    .readout.flip {
      transform: translateX(calc(-100% - 8px));
    }
  }
  .readout-title {
    white-space: nowrap;
    font-weight: 600;
    color: var(--text);
    margin-bottom: 2px;
  }
  .readout-note {
    font-weight: 400;
    color: var(--text-muted);
  }
  .readout-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
  }
  .readout-label {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--text-muted);
  }
  .readout-value {
    font-variant-numeric: tabular-nums;
    color: var(--text);
    font-weight: 600;
  }
  .swatch {
    flex: 0 0 auto;
    width: 12px;
    height: 0;
    border-top: 3px solid var(--sw);
  }
  .swatch.dashed {
    border-top: 2px dashed var(--sw);
  }
</style>
