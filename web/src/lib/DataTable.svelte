<script lang="ts" generics="T">
  import type { Snippet } from 'svelte';
  import {
    allState,
    nextSort,
    sortRows,
    toggleAll,
    toggleKey,
    type Column,
    type SortDir,
    type SortState,
  } from './dataTable';

  interface Props {
    columns: Column<T>[];
    rows: T[];
    /** Stable unique key per row (selection + keyed each). */
    rowKey: (row: T) => string;
    /** Cell body for every non-selection column. */
    cell: Snippet<[row: T, col: Column<T>]>;
    caption: string;
    sort?: SortState | null;
    /** Skip local sorting (the parent re-queries on `onsort`). */
    serverSort?: boolean;
    /** Direction a not-yet-sorted column starts in. */
    firstDir?: SortDir;
    onsort?: (s: SortState) => void;
    selectable?: boolean;
    selected?: Set<string>;
    /** Shown in the bulk-action bar while rows are checked. */
    bulk?: Snippet<[keys: string[]]>;
    loading?: boolean;
    skeletonRows?: number;
    empty?: Snippet;
    /** Dim a row (e.g. inactive link). */
    rowClass?: (row: T) => string;
    /** Accessible name for a row's checkbox, e.g. (r) => r.title. */
    rowLabel?: (row: T) => string;
    /** Rows that return false get no checkbox (e.g. group header rows). */
    rowSelectable?: (row: T) => boolean;
    /** Keys "select all" covers when some selectable rows are not rendered (e.g. collapsed groups). */
    selectAllKeys?: string[];
  }

  let {
    columns,
    rows,
    rowKey,
    cell,
    caption,
    sort = $bindable(null),
    serverSort = false,
    firstDir = 'asc',
    onsort,
    selectable = false,
    selected = $bindable(new Set<string>()),
    bulk,
    loading = false,
    skeletonRows = 5,
    empty,
    rowClass,
    rowLabel,
    rowSelectable,
    selectAllKeys,
  }: Props = $props();

  const shown = $derived(serverSort ? rows : sortRows(rows, columns, sort));
  const keys = $derived(selectAllKeys ?? (rowSelectable ? rows.filter(rowSelectable) : rows).map(rowKey));
  const all = $derived(allState(selected, keys));
  const fixedWidths = $derived(columns.map((c) => c.width).filter((w): w is string => !!w));
  const minWidth = $derived(`calc(16rem + ${[selectable ? '40px' : '0px', ...fixedWidths].join(' + ')})`);

  const hasMeta = $derived(columns.some((c) => c.mobile === 'meta'));

  // Drop selected keys whose rows are gone (deleted, filtered, paged away).
  $effect(() => {
    if (loading) return;
    const present = new Set(keys);
    if ([...selected].some((k) => !present.has(k))) {
      selected = new Set([...selected].filter((k) => present.has(k)));
    }
  });

  let headCheck: HTMLInputElement | undefined = $state();
  $effect(() => {
    if (headCheck) headCheck.indeterminate = all === 'some';
  });

  function clickSort(col: Column<T>) {
    const next = nextSort(sort, col.key, col.align === 'right' ? 'desc' : firstDir);
    sort = next;
    onsort?.(next);
  }

  function ariaSort(col: Column<T>): 'ascending' | 'descending' | 'none' | undefined {
    if (!col.sortable) return undefined;
    if (sort?.key !== col.key) return 'none';
    return sort.dir === 'asc' ? 'ascending' : 'descending';
  }
</script>

<div class="dt-wrap">
  {#if selectable && selected.size > 0}
    <div class="bulk-bar" role="region" aria-label="Bulk actions">
      <span class="bulk-count">{selected.size} selected</span>
      {#if bulk}{@render bulk([...selected])}{/if}
      <button type="button" class="bulk-clear" onclick={() => (selected = new Set())}>Clear</button>
    </div>
  {/if}

  <div class="dt-scroll">
    <table class="dt" class:selectable class:no-meta={!hasMeta} style:min-width={minWidth} aria-busy={loading}>
      <caption class="sr-only">{caption}</caption>
      <colgroup>
        {#if selectable}<col style="width: 40px" />{/if}
        {#each columns as col (col.key)}
          <col style:width={col.width} />
        {/each}
      </colgroup>
      <thead>
        <tr>
          {#if selectable}
            <th class="sel" scope="col">
              <input
                bind:this={headCheck}
                type="checkbox"
                aria-label="Select all rows"
                checked={all === 'all'}
                disabled={loading || rows.length === 0}
                onchange={() => (selected = toggleAll(selected, keys))}
              />
            </th>
          {/if}
          {#each columns as col (col.key)}
            <th scope="col" class:right={col.align === 'right'} class:inset={col.inset} aria-sort={ariaSort(col)}>
              {#if col.sortable}
                <button type="button" class="sort-btn" class:active={sort?.key === col.key} onclick={() => clickSort(col)}>
                  <span class:sr-only={col.hideLabel}>{col.label}</span>
                  <span class="arrow" aria-hidden="true">{sort?.key === col.key ? (sort.dir === 'asc' ? '▲' : '▼') : ''}</span>
                </button>
              {:else}
                <span class:sr-only={col.hideLabel}>{col.label}</span>
              {/if}
            </th>
          {/each}
        </tr>
      </thead>
      <tbody>
        {#if loading}
          {#each Array(skeletonRows) as _, i (i)}
            <tr class="skeleton" aria-hidden="true">
              {#if selectable}<td class="sel"><span class="sk sk-box"></span></td>{/if}
              {#each columns as col, ci (col.key)}
                <td class="m-{col.mobile ?? 'hide'}" class:right={col.align === 'right'}>
                  <span class="sk" style:width={ci === 0 || col.mobile === 'title' ? '70%' : '50%'}></span>
                  {#if col.mobile === 'title'}<span class="sk sk-sub"></span>{/if}
                </td>
              {/each}
            </tr>
          {/each}
        {:else if rows.length === 0}
          <tr class="empty-row">
            <td colspan={columns.length + (selectable ? 1 : 0)}>
              {#if empty}{@render empty()}{:else}<span class="text-muted">Nothing to show.</span>{/if}
            </td>
          </tr>
        {:else}
          {#each shown as row (rowKey(row))}
            {@const k = rowKey(row)}
            <tr class={rowClass?.(row) ?? ''} class:is-selected={selected.has(k)}>
              {#if selectable}
                <td class="sel">
                  {#if !rowSelectable || rowSelectable(row)}
                    <input type="checkbox" aria-label={rowLabel ? `Select ${rowLabel(row)}` : 'Select row'} checked={selected.has(k)} onchange={() => (selected = toggleKey(selected, k))} />
                  {/if}
                </td>
              {/if}
              {#each columns as col (col.key)}
                <td class="m-{col.mobile ?? 'hide'}" class:right={col.align === 'right'}>
                  {@render cell(row, col)}
                </td>
              {/each}
            </tr>
          {/each}
        {/if}
      </tbody>
    </table>
  </div>
</div>

<style>
  .dt-wrap { width: 100%; }
  /* relative: keeps .sr-only header labels inside the scroller. */
  .dt-scroll { position: relative; overflow-x: auto; }

  /* Fixed layout + <colgroup>: widths come from the columns, the one column
     without a width takes the rest. No per-cell max-width hacks. */
  .dt {
    table-layout: fixed;
    width: 100%;
    border-collapse: collapse;
  }
  .dt th { white-space: nowrap; }
  .dt td { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dt th { vertical-align: middle; }
  .dt th.right,
  .dt td.right { text-align: right; }
  .dt th.inset { padding-right: calc(var(--space-3) + var(--space-2)); }
  .dt td.sel,
  .dt th.sel { padding-right: 0; }
  .dt td { height: 52px; }
  .dt tr.is-selected td { background: var(--accent-subtle); }
  .empty-row td { height: auto; padding: var(--space-5) var(--space-3); text-align: center; white-space: normal; }
  .empty-row:hover { background: none; }

  .sort-btn {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 0;
    border: none;
    background: none;
    font: inherit;
    font-weight: inherit;
    letter-spacing: inherit;
    text-transform: inherit;
    color: inherit;
    line-height: inherit;
    cursor: pointer;
  }
  .sort-btn:hover,
  .sort-btn.active { color: var(--text); }
  .sort-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .arrow { font-size: 9px; line-height: 1; min-width: 9px; }
  th.right .sort-btn { flex-direction: row-reverse; }

  input[type='checkbox'] { width: 16px; height: 16px; margin: 0; cursor: pointer; accent-color: var(--accent); }

  .bulk-bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--accent-subtle);
    border-bottom: var(--border-w) solid var(--border);
    font-size: var(--fs-base);
  }
  .bulk-count { font-weight: 600; }
  .bulk-clear {
    margin-left: auto;
    border: none;
    background: none;
    color: var(--accent);
    font: inherit;
    cursor: pointer;
  }

  /* Skeleton rows */
  .sk {
    display: block;
    height: 12px;
    border-radius: 3px;
    background: linear-gradient(90deg, var(--bg-subtle), var(--bg-header), var(--bg-subtle));
    background-size: 200% 100%;
    animation: sk-shimmer 1.2s linear infinite;
  }
  .sk-box { width: 16px; height: 16px; }
  .sk-sub { width: 40%; height: 10px; margin-top: 6px; }
  td.right .sk { margin-left: auto; }
  @keyframes sk-shimmer { to { background-position: -200% 0; } }
  @media (prefers-reduced-motion: reduce) { .sk { animation: none; } }

  /* Phone: rows become cards (title, meta line, metric, ⋯). #0173 finishes
     the per-view card treatment. */
  @media (max-width: 899px) {
    .dt-scroll { overflow: visible; }
    .dt { min-width: 0 !important; table-layout: auto; }
    .dt colgroup,
    .dt thead { display: none; }
    .dt,
    .dt tbody { display: block; }
    .dt tbody tr {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      grid-template-areas: 'title metric' 'meta actions';
      column-gap: var(--space-3);
      align-items: center;
      padding: var(--space-3);
      border-bottom: var(--border-w) solid var(--border);
    }
    .dt.selectable tbody tr {
      grid-template-columns: auto minmax(0, 1fr) auto;
      grid-template-areas: 'sel title metric' 'sel meta actions';
    }
    .dt tbody td { display: block; padding: 0; height: auto; border: none; min-width: 0; }
    .dt td.sel { grid-area: sel; padding-right: var(--space-3); min-width: 16px; box-sizing: content-box; }
    .dt td.m-title { grid-area: title; }
    .dt td.m-meta { grid-area: meta; font-size: var(--fs-sm); color: var(--text-muted); }
    .dt td.m-metric { grid-area: metric; text-align: right; min-width: 4.5rem; }
    .dt td.m-actions { grid-area: actions; justify-self: end; }
    .dt.no-meta tbody tr { grid-template-areas: 'title metric' 'title actions'; }
    .dt.no-meta.selectable tbody tr { grid-template-areas: 'sel title metric' 'sel title actions'; }
    .dt td.m-hide { display: none; }
    .dt tbody tr:last-child { border-bottom: none; }
    .dt tr.empty-row { display: block; }
    .dt tr.empty-row td { display: block; }
  }
</style>
