<script lang="ts">
  import { onMount } from 'svelte';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import RowMenu from '../lib/RowMenu.svelte';
  import InlineBar from '../lib/InlineBar.svelte';
  import RelativeDate from '../lib/RelativeDate.svelte';
  import { listLinks } from '../lib/api';
  import { shortUrl } from '../lib/links';
  import { columnMax, type Column } from '../lib/dataTable';
  import type { Link } from '../lib/types';

  let links = $state<Link[]>([]);
  let loading = $state(true);
  let selected = $state(new Set<string>());
  const skeleton = new URLSearchParams(location.search).has('skeleton');

  onMount(async () => {
    if (skeleton) return;
    links = (await listLinks(1, 100)).links ?? [];
    if (new URLSearchParams(location.search).has('fake')) {
      // Dev aid: spread clicks and ages so bars and relative dates are visible.
      const day = 86_400_000;
      links = links.map((l, i) => ({
        ...l,
        click_count: [412, 96, 1280, 7, 0, 33, 250][i % 7],
        created_at: new Date(Date.now() - [2e5, 3e6, 4 * day, 6 * day, 12 * day, 90 * day, 400 * day][i % 7]).toISOString(),
        active: i !== 3,
      }));
    }
    loading = false;
  });

  const columns: Column<Link>[] = [
    { key: 'title', label: 'Link', sortable: true, value: (l) => l.title || l.key, mobile: 'title' },
    { key: 'dest', label: 'Destination', width: '11rem' },
    { key: 'clicks', label: 'Clicks', width: '8rem', align: 'right', sortable: true, value: (l) => l.click_count, mobile: 'metric' },
    { key: 'created', label: 'Created', width: '8rem', sortable: true, value: (l) => l.created_at },
    { key: 'actions', label: 'Actions', width: '6rem', hideLabel: true, align: 'right', mobile: 'actions' },
  ];
  const max = $derived(columnMax(links, (l) => l.click_count));
  function path(u: string) {
    try { const x = new URL(u); return x.pathname + x.search; } catch { return u; }
  }
</script>

<main class="demo">
  <h1 class="page-title">Shared table pieces</h1>
  <p class="text-muted">Dev-only gallery for #0155. Add <code>?skeleton</code> to see loading rows.</p>
  <div class="panel">
    <DataTable
      {columns}
      rows={links}
      rowKey={(l) => l.key}
      caption="Links"
      selectable
      bind:selected
      {loading}
      rowLabel={(l) => l.title || l.key}
    >
      {#snippet cell(l, col)}
        {#if col.key === 'title'}
          <TwoLineCell title={l.title || l.key} meta={`/u/${l.key} · ${path(l.destination_url)}`} href={`#/links/${l.key}`} dim={!l.active} tag={l.active ? '' : 'Inactive'} />
        {:else if col.key === 'dest'}
          <span class="text-muted">{path(l.destination_url)}</span>
        {:else if col.key === 'clicks'}
          <InlineBar value={l.click_count} {max} />
        {:else if col.key === 'created'}
          <RelativeDate date={l.created_at} />
        {:else}
          <RowMenu
            label={`Actions for ${l.title || l.key}`}
            copyText={shortUrl(l.key)}
            items={[
              { label: 'Copy short URL', onselect: () => navigator.clipboard?.writeText(shortUrl(l.key)) },
              { label: 'Open destination', href: l.destination_url },
              { label: 'QR (SVG)', onselect: () => {} },
              { label: 'Deactivate', danger: true, onselect: () => {} },
            ]}
          />
        {/if}
      {/snippet}
      {#snippet bulk(keys)}
        <button type="button" class="btn-link" onclick={() => (selected = new Set())}>Deactivate {keys.length}</button>
      {/snippet}
      {#snippet empty()}<span class="text-muted">No links yet.</span>{/snippet}
    </DataTable>
  </div>
</main>

<style>
  .demo { max-width: 1040px; margin: 0 auto; padding: var(--space-5) var(--space-4); }
  .panel { background: var(--bg-panel); border: var(--border-w) solid var(--border); border-radius: var(--radius); overflow: hidden; }
</style>
