<!--
  TopContent (#0162) — the ranked list under the Overview chart, built on the
  shared table pieces from #0155 (DataTable, TwoLineCell, InlineBar, RowMenu).

  Columns: rank, identity (title over a muted second line; the flexible
  column, so it takes the spare width), by-source stacked bar, clicks with an
  inline bar, change, and a ⋯ menu on link rows. A content row expands to its
  member links, which DataTable renders as extra rows (`child`). Below 900px
  DataTable turns the rows into cards and the title wraps to two lines.
  `loading` dims the rows while a newer set is on its way (no flash of text).
-->
<script lang="ts">
  import type { TopRow, ChildRow } from './overview';
  import type { Column } from './dataTable';
  import type { MenuItem } from './rowMenu';
  import { shortUrl } from './links';
  import { navigate } from './hashRouter.svelte';
  import DataTable from './DataTable.svelte';
  import TwoLineCell from './TwoLineCell.svelte';
  import InlineBar from './InlineBar.svelte';
  import RowMenu from './RowMenu.svelte';
  import SourceBar from './SourceBar.svelte';

  interface Props {
    rows: TopRow[];
    /** Identity column header: "Content", "Link" or "Source". */
    firstHeader: string;
    showSources?: boolean;
    pageSize?: number;
    /** Total rows the server has, for the "Showing the top N of M" note. */
    total?: number;
    allLinksHref: string;
    /** Dim the rows while a newer set loads. */
    loading?: boolean;
  }

  let { rows, firstHeader, showSources = true, pageSize = 10, total, allLinksHref, loading = false }: Props = $props();

  interface Flat {
    id: string;
    kind: 'parent' | 'child';
    rank: number;
    top: TopRow;
    child?: ChildRow;
  }

  let shown = $state(10);
  // A new grouping or scope starts at one page again.
  let lastRows: TopRow[] | null = null;
  $effect(() => {
    if (rows !== lastRows) {
      lastRows = rows;
      shown = pageSize;
    }
  });

  let expanded = $state<Record<string, boolean>>({});

  const visible = $derived(rows.slice(0, shown));
  const remaining = $derived(Math.max(0, rows.length - shown));
  const max = $derived(Math.max(0, ...rows.map((r) => r.clicks)));

  const flat = $derived.by<Flat[]>(() => {
    const out: Flat[] = [];
    visible.forEach((r, i) => {
      out.push({ id: r.id, kind: 'parent', rank: i + 1, top: r });
      if (expanded[r.id]) {
        for (const c of r.children) out.push({ id: r.id + '>' + c.key, kind: 'child', rank: 0, top: r, child: c });
      }
    });
    return out;
  });

  const columns = $derived<Column<Flat>[]>([
    { key: 'rank', label: '#', width: '2.75rem', align: 'right', mobile: 'hide' },
    { key: 'id', label: firstHeader, mobile: 'title' },
    ...(showSources ? [{ key: 'src', label: 'By source', width: '13rem', mobile: 'meta' as const }] : []),
    { key: 'clicks', label: 'Clicks', width: '6.5rem', align: 'right' as const, inset: true, mobile: 'metric' as const },
    { key: 'chg', label: 'Change', width: '5.5rem', align: 'right' as const, mobile: 'actions' as const },
    { key: 'menu', label: 'Actions', width: '6.75rem', hideLabel: true, mobile: 'hide' as const },
  ]);

  async function copy(key: string) {
    try {
      await navigator.clipboard?.writeText(shortUrl(key));
    } catch {
      // Clipboard unavailable (insecure context or permissions); nothing to do.
    }
  }

  function menuFor(key: string): MenuItem[] {
    return [
      { label: 'Copy short URL', onselect: () => copy(key) },
      { label: 'View link', onselect: () => navigate('/links/' + encodeURIComponent(key)) },
      { label: 'Open short URL', href: shortUrl(key) },
    ];
  }

  /** The /u/ key a row's menu acts on: a child, or a Group-by-Link row. */
  function keyOf(r: Flat): string | undefined {
    return r.kind === 'child' ? r.child?.key : r.top.linkKey;
  }
</script>

{#snippet cell(r: Flat, col: Column<Flat>)}
  {#if col.key === 'rank'}
    {#if r.kind === 'parent'}<span class="rank">{r.rank}</span>{/if}
  {:else if col.key === 'id'}
    {#if r.kind === 'child' && r.child}
      <div class="idcell child-cell">
        <TwoLineCell
          title={r.child.label}
          meta={r.child.meta}
          href={'#/links/' + encodeURIComponent(r.child.key)}
          wrap
        />
      </div>
    {:else}
      <div class="idcell">
        {#if r.top.children.length > 0}
          <button
            type="button"
            class="exp"
            aria-expanded={!!expanded[r.id]}
            aria-label="{expanded[r.id] ? 'Hide' : 'Show'} links for {r.top.title}"
            onclick={() => (expanded[r.id] = !expanded[r.id])}>{expanded[r.id] ? '▾' : '▸'}</button
          >
        {/if}
        <TwoLineCell
          title={r.top.title}
          meta={r.top.meta}
          href={r.top.linkKey ? '#/links/' + encodeURIComponent(r.top.linkKey) : undefined}
          wrap
        />
      </div>
    {/if}
  {:else if col.key === 'src'}
    {#if r.kind === 'parent'}<SourceBar segments={r.top.segments} widthPct={max > 0 ? Math.max(2, Math.round((r.top.clicks / max) * 100)) : 0} />{/if}
  {:else if col.key === 'clicks'}
    <InlineBar value={r.kind === 'child' ? (r.child?.clicks ?? 0) : r.top.clicks} {max} />
  {:else if col.key === 'chg'}
    {#if r.kind === 'parent' && r.top.change.kind !== 'none'}
      <span class="chg {r.top.change.kind}" title={r.top.change.title}>
        {#if r.top.change.arrow}<span aria-hidden="true">{r.top.change.arrow}</span>{/if}{r.top.change.text}
        <span class="sr-only">{r.top.change.kind === 'up' ? 'up' : r.top.change.kind === 'down' ? 'down' : ''}</span>
      </span>
    {:else if r.kind === 'child' && r.child}
      <!-- Phone cards drop the menu column, so the child's menu moves here. -->
      <span class="mob-only"><RowMenu items={menuFor(r.child.key)} label="Actions for {r.child.label}" /></span>
    {/if}
  {:else if col.key === 'menu'}
    {@const k = keyOf(r)}
    {#if k}
      <RowMenu items={menuFor(k)} label="Actions for {r.kind === 'child' ? r.child?.label : r.top.title}" copyText={shortUrl(k)} />
    {/if}
  {/if}
{/snippet}

<div class="top" class:loading aria-busy={loading}>
  <DataTable
    {columns}
    rows={flat}
    rowKey={(r) => r.id}
    {cell}
    caption="Top {firstHeader.toLowerCase()} by clicks"
    rowClass={(r) => (r.kind === 'child' ? 'top-child' : '')}
  />
  <div class="foot">
    {#if remaining > 0}
      <button type="button" class="more" onclick={() => (shown += pageSize)}>Show {Math.min(pageSize, remaining)} more</button>
      <span class="sep" aria-hidden="true">·</span>
    {/if}
    <a class="text-link" href={'#' + allLinksHref}>View all links →</a>
    {#if total !== undefined && total > rows.length}
      <span class="text-muted tail">Showing the top {rows.length} of {total.toLocaleString()}.</span>
    {/if}
  </div>
</div>

<style>
  .top.loading {
    opacity: 0.55;
    transition: opacity 0.15s;
  }
  .rank {
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .idcell {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    min-width: 0;
  }
  .child-cell {
    padding-left: 28px;
  }
  .exp {
    flex: 0 0 auto;
    width: 28px;
    height: 28px;
    margin-left: -6px;
    border: none;
    background: none;
    color: var(--text-muted);
    cursor: pointer;
    border-radius: var(--radius);
    font-size: 12px;
  }
  .exp:hover {
    background: var(--bg-header);
    color: var(--text);
  }
  .chg {
    font-size: var(--fs-sm);
    font-weight: 600;
    white-space: nowrap;
  }
  .chg.up {
    color: var(--success);
  }
  .chg.down {
    color: var(--danger);
  }
  .chg.new {
    color: var(--accent);
  }
  .chg.flat {
    color: var(--text-muted);
  }
  .mob-only {
    display: none;
  }
  :global(tr.top-child) {
    background: var(--bg-subtle);
  }
  :global(tr.top-child td) {
    height: 44px;
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-3) var(--space-4);
    font-size: var(--fs-base);
    border-top: var(--border-w) solid var(--border);
  }
  .more {
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
  }
  .tail {
    margin-left: auto;
    font-size: var(--fs-sm);
  }
  @media (max-width: 899px) {
    .mob-only {
      display: inline-flex;
    }
    .child-cell {
      padding-left: 12px;
    }
    .foot {
      padding: var(--space-3);
    }
  }
</style>
