<!--
  CampaignMatrix (#0164) — the Content tab's content x channel table from the
  campaign-performance report (#0143). One row per content group, one column
  per source, medium or placement (chosen with the segmented control, kept in
  the hash by the parent), clicks in each cell shaded in proportion to the
  largest cell, and a dash where no link has clicks for that pairing.
  Clicking a cell opens that link; clicking a title expands the row to its
  links. Pointing at a row (or focusing inside it) reports its link keys so
  the chart above can highlight that content's line. On phones the table
  turns into one card per content with a chip per channel.
-->
<script lang="ts">
  import type { MatrixRow, CampaignPerformanceData } from './reports';
  import {
    MATRIX_BYS,
    cellKey,
    columnLabel,
    filterRows,
    matrixMax,
    rowKeys,
    shadePct,
    type MatrixBy,
  } from './campaignView';

  export interface ChildLink {
    key: string;
    title: string;
    /** Clicks in the window, or null when unknown. */
    clicks: number | null;
    meta: string;
  }

  interface Props {
    matrix: CampaignPerformanceData['matrix'];
    totalClicks: number;
    by: MatrixBy;
    /** Hash href (with the leading '#') for switching the columns. */
    byHref: (by: MatrixBy) => string;
    childLinks: (keys: string[]) => ChildLink[];
    onhover?: (keys: string[]) => void;
  }

  let { matrix, totalClicks, by, byHref, childLinks, onhover }: Props = $props();

  let filter = $state('');
  let expanded = $state<Set<string>>(new Set());

  const shown = $derived(filterRows(matrix.rows, filter));
  const max = $derived(matrixMax(matrix.rows));
  const hiddenRows = $derived(matrix.total_rows - matrix.rows.length);

  function toggle(id: string) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expanded = next;
  }
  function hover(row: MatrixRow | null) {
    onhover?.(row ? rowKeys(row) : []);
  }
  function shade(clicks: number): string {
    const p = shadePct(clicks, max);
    return p > 0 ? `background: color-mix(in srgb, var(--accent) ${p}%, transparent)` : '';
  }
  const byLabel = $derived(MATRIX_BYS.find((b) => b.key === by)?.label.toLowerCase() ?? by);
</script>

<div class="matrix-wrap">
  <div class="controls">
    <div class="by" role="group" aria-label="Columns">
      <span class="by-label">Columns</span>
      {#each MATRIX_BYS as b (b.key)}
        <a class="chip" class:on={by === b.key} href={byHref(b.key)} aria-current={by === b.key ? 'true' : undefined}>{b.label}</a>
      {/each}
    </div>
    <label class="filter">
      <span class="sr-only">Filter content</span>
      <input type="search" placeholder="Filter content…" bind:value={filter} />
    </label>
  </div>

  {#if matrix.rows.length === 0}
    <p class="text-muted empty">No clicks in this window, so there is nothing to compare yet.</p>
  {:else}
    <table class="matrix">
      <caption class="sr-only">Clicks by content and {byLabel}</caption>
      <thead>
        <tr>
          <th scope="col" class="rank"><span class="sr-only">Rank</span>#</th>
          <th scope="col" class="title-col">Content</th>
          {#each matrix.columns as col (col)}
            <th scope="col" class="num" title={columnLabel(col)}>{columnLabel(col)}</th>
          {/each}
          <th scope="col" class="num total-col">Total ▼</th>
        </tr>
      </thead>
      <tbody>
        {#each shown as row (row.content_key)}
          {@const kids = expanded.has(row.content_key) ? childLinks(rowKeys(row)) : []}
          <tr
            class="mrow"
            onpointerenter={() => hover(row)}
            onpointerleave={() => hover(null)}
            onfocusin={() => hover(row)}
            onfocusout={() => hover(null)}
          >
            <td class="rank">{matrix.rows.indexOf(row) + 1}</td>
            <td class="title-col">
              <button
                type="button"
                class="title-btn"
                aria-expanded={expanded.has(row.content_key)}
                onclick={() => toggle(row.content_key)}
              >
                <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" class:open={expanded.has(row.content_key)}
                  ><path d="M6 3.5l4.5 4.5L6 12.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg
                >
                <span class="title-text">{row.title}</span>
              </button>
            </td>
            {#each matrix.columns as col (col)}
              {@const cell = row.cells[col]}
              {#if cell}
                <td class="num cell" data-label={columnLabel(col)} style={shade(cell.clicks)}>
                  <a
                    href={'#/links/' + encodeURIComponent(cellKey(cell))}
                    title={cell.keys && cell.keys.length > 1
                      ? `${cell.keys.length} links; opens the one with the most clicks`
                      : `Open ${cellKey(cell)}`}
                    aria-label="{row.title}, {columnLabel(col)}: {cell.clicks.toLocaleString()} clicks"
                    >{cell.clicks.toLocaleString()}</a
                  >
                </td>
              {:else}
                <td class="num cell empty" data-label={columnLabel(col)}><span aria-label="no clicks">—</span></td>
              {/if}
            {/each}
            <td class="num total-col" data-label="Total">{row.total.toLocaleString()}</td>
          </tr>
          {#each kids as kid (kid.key)}
            <tr class="child">
              <td class="rank"></td>
              <td class="title-col" colspan={matrix.columns.length + 1}>
                <a class="kid-title" href={'#/links/' + encodeURIComponent(kid.key)}>{kid.title}</a>
                <span class="kid-meta">{kid.meta}</span>
              </td>
              <td class="num total-col">{kid.clicks === null ? '' : kid.clicks.toLocaleString()}</td>
            </tr>
          {/each}
        {:else}
          <tr><td colspan={matrix.columns.length + 3} class="text-muted none">No content matches "{filter}".</td></tr>
        {/each}
      </tbody>
      <tfoot>
        <tr class="all">
          <th scope="row" class="rank"></th>
          <th scope="row" class="title-col">All content</th>
          {#each matrix.columns as col (col)}
            <td class="num" data-label={columnLabel(col)}>{(matrix.column_totals[col] ?? 0).toLocaleString()}</td>
          {/each}
          <td class="num total-col" data-label="Total">{totalClicks.toLocaleString()}</td>
        </tr>
      </tfoot>
    </table>
    {#if hiddenRows > 0}
      <p class="text-faint note">Showing the top {matrix.rows.length} of {matrix.total_rows} content groups; the totals count them all.</p>
    {/if}
  {/if}
</div>

<style>
  .matrix-wrap {
    min-width: 0;
  }
  .controls {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .by {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1);
  }
  .by-label {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    margin-right: var(--space-1);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    min-height: 30px;
    padding: 0 var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: 16px;
    background: var(--bg-panel);
    color: var(--text);
    text-decoration: none;
  }
  .chip:hover {
    background: var(--bg-subtle);
  }
  .chip.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .filter {
    margin: 0;
    flex: 0 1 15rem;
  }
  .filter input {
    width: 100%;
  }
  .empty,
  .note,
  .none {
    padding: var(--space-3) 0;
  }
  .note {
    margin: 0;
    font-size: var(--fs-sm);
  }

  .matrix {
    width: 100%;
    border-collapse: collapse;
    table-layout: auto;
  }
  th,
  td {
    padding: var(--space-2) var(--space-2);
    border-bottom: var(--border-w) solid var(--border);
    vertical-align: middle;
  }
  thead th {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
    text-align: left;
    white-space: nowrap;
    max-width: 9rem;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  thead th.num {
    text-align: right;
  }
  .rank {
    width: 2rem;
    color: var(--text-faint);
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .title-col {
    min-width: 12rem;
    text-align: left;
  }
  .title-btn {
    display: flex;
    align-items: flex-start;
    gap: var(--space-2);
    width: 100%;
    padding: 0;
    border: none;
    background: none;
    color: var(--text);
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .title-btn svg {
    flex: none;
    margin-top: 4px;
    color: var(--text-muted);
    transition: transform 0.1s;
  }
  .title-btn svg.open {
    transform: rotate(90deg);
  }
  .title-btn:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  .title-text {
    overflow-wrap: anywhere;
  }
  .cell a {
    display: block;
    color: var(--text);
    font-weight: 600;
    text-decoration: none;
  }
  .cell a:hover {
    text-decoration: underline;
  }
  .cell.empty {
    color: var(--text-faint);
  }
  .total-col {
    font-weight: 600;
    white-space: nowrap;
  }
  .mrow:hover {
    background: var(--bg-subtle);
  }
  .child td {
    background: var(--bg-subtle);
    font-size: var(--fs-sm);
  }
  .child .title-col {
    padding-left: var(--space-5);
  }
  .kid-title {
    color: var(--accent);
    overflow-wrap: anywhere;
  }
  .kid-meta {
    display: block;
    color: var(--text-muted);
    overflow-wrap: anywhere;
  }
  tfoot th,
  tfoot td {
    border-top: 2px solid var(--border-strong);
    border-bottom: none;
    font-weight: 600;
    text-align: left;
  }
  tfoot td.num {
    text-align: right;
  }

  /* Phones: one card per content, a chip per channel. */
  @media (max-width: 640px) {
    .matrix,
    .matrix tbody,
    .matrix tfoot,
    .matrix tr,
    .matrix td,
    .matrix th {
      display: block;
    }
    .matrix thead {
      display: none;
    }
    .matrix tr {
      padding: var(--space-3) 0;
      border-bottom: var(--border-w) solid var(--border);
    }
    .matrix td,
    .matrix th {
      border: none;
      padding: 0;
    }
    .rank {
      display: none !important;
    }
    .title-col {
      min-width: 0;
      margin-bottom: var(--space-2);
    }
    .matrix .cell,
    .matrix .mrow td.total-col,
    .matrix tfoot td {
      display: inline-block;
      margin: 0 var(--space-1) var(--space-1) 0;
      padding: 2px var(--space-2);
      border-radius: 12px;
      text-align: left;
      border: var(--border-w) solid var(--border);
    }
    .matrix .cell::before,
    .matrix .mrow td.total-col::before,
    .matrix tfoot td::before {
      content: attr(data-label) ' ';
      color: var(--text-muted);
      font-weight: 400;
    }
    .matrix .cell a {
      display: inline;
    }
    .matrix .cell.empty {
      display: none;
    }
    .matrix .mrow td.total-col {
      background: var(--bg-subtle);
    }
    .child {
      padding: var(--space-2) 0 var(--space-2) var(--space-4);
    }
    .child .title-col {
      padding-left: 0;
      margin: 0;
    }
    .child td.total-col {
      display: block;
      margin: 0;
      padding: 0;
      border: none;
    }
  }
</style>
