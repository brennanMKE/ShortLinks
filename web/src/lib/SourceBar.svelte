<!--
  SourceBar (#0162) — a stacked by-source bar with a compact legend. The
  track is `widthPct` of the cell (clicks relative to the largest row, so rank
  reads without the number); segments split it by source. Colour is paired
  with a legend entry, and the whole bar has an aria-label listing the values.
-->
<script lang="ts">
  import type { Segment } from './overview';

  interface Props {
    segments: Segment[];
    /** Track width as a percentage of the cell. */
    widthPct?: number;
  }

  let { segments, widthPct = 100 }: Props = $props();

  const color = (slot: number) => (slot === 0 ? 'var(--series-other)' : `var(--series-${((slot - 1) % 6) + 1})`);
  const summary = $derived(segments.map((s) => `${s.label} ${s.clicks.toLocaleString()}`).join(', '));
</script>

{#if segments.length > 0}
  <div class="src">
    <div class="track" style="width: {widthPct}%" role="img" aria-label="Clicks by source: {summary}">
      {#each segments as s (s.label)}
        <span class="seg" style="flex: {s.pct} 1 0; background: {color(s.slot)}" title="{s.label}: {s.clicks.toLocaleString()}"></span>
      {/each}
    </div>
    <ul class="legend" aria-hidden="true">
      {#each segments as s (s.label)}
        <li>
          <span class="dot" style="background: {color(s.slot)}"></span>
          <span class="lname">{s.label}</span>
          <span class="lnum">{s.clicks.toLocaleString()}</span>
        </li>
      {/each}
    </ul>
  </div>
{/if}

<style>
  .src {
    min-width: 0;
  }
  .track {
    display: flex;
    gap: 1px;
    height: 8px;
    border-radius: 4px;
    overflow: hidden;
    min-width: 6px;
  }
  .seg {
    min-width: 2px;
  }
  .legend {
    display: flex;
    flex-wrap: wrap;
    gap: 0 var(--space-3);
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    line-height: 1.35;
  }
  .legend li {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    min-width: 0;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex: 0 0 auto;
  }
  .lname {
    max-width: 11ch;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .lnum {
    font-variant-numeric: tabular-nums;
    color: var(--text);
  }
</style>
