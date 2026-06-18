<!--
  UTMBarChart — horizontal proportional bar chart for one UTM dimension (#0049).

  Each row is a UTM value (e.g. "email", "social") with a filled bar whose
  width is proportional to its share of that dimension's total, plus a count.

  Accessibility: the underlying table is visible (not hidden), so it doubles as
  the accessible fallback. The bars are aria-hidden decorations.
  Design tokens only — no hardcoded colours.
-->
<script lang="ts">
  import type { UTMBucket } from './types';
  import { toBarRows } from './charts';
  import { NONE_BUCKET } from './linkDetail';

  interface Props {
    buckets: UTMBucket[] | undefined | null;
    dimension: string;  // e.g. "source"
    label: string;      // e.g. "Source"
  }

  let { buckets, dimension, label }: Props = $props();

  const rows = $derived(toBarRows(buckets));
  const isEmpty = $derived(rows.length === 0);
</script>

<div class="utm-chart" aria-label="{label} breakdown">
  {#if isEmpty}
    <p class="text-faint no-data">No data.</p>
  {:else}
    <table class="bar-table" aria-label="{label} breakdown by count">
      <caption class="sr-only">{label} UTM dimension breakdown</caption>
      <tbody>
        {#each rows as row (row.value)}
          {@const displayValue = row.value === NONE_BUCKET ? '(none)' : row.value}
          {@const isNone = row.value === NONE_BUCKET}
          <tr>
            <td class="bar-label" class:none={isNone} title={displayValue}>
              {displayValue}
            </td>
            <td class="bar-cell" aria-hidden="true">
              <div class="bar-track">
                <div class="bar-fill" style="width: {row.pct}%"></div>
              </div>
            </td>
            <td class="bar-count">{row.count.toLocaleString()}</td>
            <td class="bar-pct sr-only">{row.pct}%</td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .utm-chart {
    width: 100%;
  }
  .no-data {
    font-size: var(--fs-sm);
    margin: 0;
  }
  .bar-table {
    width: 100%;
    border-collapse: collapse;
    font-size: var(--fs-sm);
  }
  /* Override global table styles for the bar table */
  .bar-table :global(thead th) {
    display: none;
  }
  .bar-table tr {
    background: none !important;
  }
  .bar-table td {
    padding: var(--space-1) var(--space-2) var(--space-1) 0;
    border: none;
    vertical-align: middle;
  }
  .bar-label {
    width: 6rem;
    max-width: 6rem;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text);
    font-size: var(--fs-sm);
  }
  .bar-label.none {
    color: var(--text-faint);
    font-style: italic;
  }
  .bar-cell {
    width: 100%;
    padding-right: var(--space-2);
  }
  .bar-track {
    background: var(--bg-subtle);
    border-radius: var(--radius);
    height: 10px;
    width: 100%;
    overflow: hidden;
  }
  .bar-fill {
    background: var(--accent);
    height: 100%;
    border-radius: var(--radius);
    min-width: 2px; /* Always show a sliver for non-zero rows */
    transition: width 0.2s ease;
  }
  .bar-count {
    font-variant-numeric: tabular-nums;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    white-space: nowrap;
    text-align: right;
    padding-left: var(--space-2);
  }
</style>
