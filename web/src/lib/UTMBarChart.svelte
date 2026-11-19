<!--
  UTMBarChart — horizontal proportional bar chart for one UTM dimension (#0049).

  Each row is a UTM value (e.g. "email", "social") with a filled bar whose
  width is proportional to its share of that dimension's total, plus a count.

  Accessibility: the underlying table is visible (not hidden), so it doubles as
  the accessible fallback. The bars are aria-hidden decorations.
  Design tokens only — no hardcoded colours.

  Selection (#0161): when the parent passes `onselect`, each row's label
  becomes a real <button> (and the bar a click target) that calls it with
  { dimension, value } — the raw bucket value, so "(none)" arrives as
  NONE_BUCKET. Without `onselect` the rows stay plain text, so views with no
  filter to apply look unchanged.
-->
<script lang="ts">
  import type { UTMBucket } from './types';
  import { toBarRows } from './charts';
  import { NONE_BUCKET } from './linkDetail';

  interface Props {
    buckets: UTMBucket[] | undefined | null;
    dimension: string;  // e.g. "source"
    label: string;      // e.g. "Source"
    /** Called when a bar is clicked / activated. Omit where no filter exists. */
    onselect?: (detail: { dimension: string; value: string }) => void;
  }

  let { buckets, dimension, label, onselect }: Props = $props();

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
          <tr class:selectable={!!onselect}>
            <td class="bar-label" class:none={isNone} title="{displayValue}: {row.count.toLocaleString()} ({row.pct}%)">
              {#if onselect}
                <button
                  type="button"
                  class="bar-btn"
                  aria-label="Filter {label} to {displayValue}, {row.count.toLocaleString()} clicks"
                  onclick={() => onselect({ dimension, value: row.value })}
                >{displayValue}</button>
              {:else}
                {displayValue}
              {/if}
            </td>
            <td class="bar-cell" aria-hidden="true">
              <!-- svelte-ignore a11y_click_events_have_key_events -->
              <!-- svelte-ignore a11y_no_static_element_interactions -->
              <div
                class="bar-track"
                class:clickable={!!onselect}
                onclick={onselect ? () => onselect({ dimension, value: row.value }) : undefined}
              >
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
  .bar-btn {
    all: unset;
    box-sizing: border-box;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    cursor: pointer;
    color: var(--accent);
    border-radius: var(--radius);
  }
  .bar-btn:hover {
    text-decoration: underline;
  }
  .bar-btn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .bar-track.clickable {
    cursor: pointer;
  }
  .bar-table tr.selectable:hover .bar-fill {
    background: var(--accent-hover);
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
