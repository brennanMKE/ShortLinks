<!--
  LinksList view (#0153, rebuilt in #0156) — the user's links table at #/links,
  on the shared DataTable / TwoLineCell / RowMenu / InlineBar pieces (#0155).

  Columns: select, Link (two lines: title, then `/u/key · source · channel ·
  destination path`), Campaign, Clicks (all time, with a bar), ⋯. The old
  Destination-hostname, Status and Created columns are gone; an inactive or
  denied link is dimmed and carries a tag instead of a status pill. Only the
  title links to the detail page. Behaviour kept from #0033–#0035: REST list
  with ?page pagination and live link.created events over SSE prepended to the
  shared `links` store.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import { listLinks, listCampaigns, deactivateLink, ApiError } from '../lib/api';
  import { navigate, getRoute } from '../lib/hashRouter.svelte';
  import { shortUrl, linkStatus, linkMeta, qrSvgUrl, qrPngUrl, deniedReasonLabel } from '../lib/links';
  import { subscribeLinks, prependUniqueByKey } from '../lib/events';
  import { columnMax, type Column } from '../lib/dataTable';
  import type { MenuItem } from '../lib/rowMenu';
  import type { Link } from '../lib/types';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import RowMenu from '../lib/RowMenu.svelte';
  import InlineBar from '../lib/InlineBar.svelte';
  import Button from '../lib/Button.svelte';
  import ConfirmDialog from '../lib/ConfirmDialog.svelte';
  import Panel from '../lib/Panel.svelte';
  import { pastedUrl } from '../lib/newLink';
  import { openNewLink } from '../lib/newLinkDialog';

  const PER_PAGE = 20;

  // ── List state ──────────────────────────────────────────────────────────────
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let page = $state(1);
  let total = $state(0);
  let perPage = $state(PER_PAGE);
  let campaignNames = $state<Record<number, string>>({});

  const totalPages = $derived(Math.max(1, Math.ceil(total / perPage)));
  const hasPrev = $derived(page > 1);
  const hasNext = $derived(page < totalPages);
  const maxClicks = $derived(columnMax($links, (l) => l.click_count));

  const columns: Column<Link>[] = [
    { key: 'link', label: 'Link', sortable: true, value: (l) => l.title || l.key, mobile: 'title' },
    { key: 'campaign', label: 'Campaign', width: '10rem', mobile: 'meta' },
    { key: 'clicks', label: 'Clicks', width: '8rem', align: 'right', inset: true, sortable: true, value: (l) => l.click_count, mobile: 'metric' },
    { key: 'actions', label: 'Actions', width: '7rem', hideLabel: true, align: 'right', mobile: 'actions' },
  ];

  async function loadPage(p: number) {
    loading = true;
    loadError = null;
    try {
      const res = await listLinks(p, PER_PAGE);
      links.set(res.links ?? []);
      page = res.page;
      perPage = res.per_page;
      total = res.total;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        // App.svelte's guard effect reacts to currentUser going null and
        // bounces to /login, preserving this route as the redirect target
        // (#0120) — no navigation call needed here.
        currentUser.set(null);
        return;
      }
      loadError = 'Could not load your links. Please try again.';
    } finally {
      loading = false;
    }
  }

  // Campaign names for the Campaign column. Best effort: the list endpoint
  // only carries campaign_id, and a failure just leaves the column blank.
  async function loadCampaignNames() {
    try {
      const res = await listCampaigns();
      const map: Record<number, string> = {};
      for (const c of res.campaigns ?? []) map[c.id] = c.name;
      campaignNames = map;
    } catch {
      /* column stays empty */
    }
  }

  // ── Row actions ─────────────────────────────────────────────────────────────
  async function copyShortUrl(key: string) {
    try {
      await navigator.clipboard.writeText(shortUrl(key));
    } catch {
      // Clipboard may be unavailable (insecure context / permissions); ignore.
    }
  }

  function detailHref(key: string): string {
    return '#/links/' + encodeURIComponent(key);
  }

  // #0160: Deactivate asks first via ConfirmDialog (until #0165 adds Undo).
  let deactivateTarget = $state<Link | null>(null);

  function handleDeactivate(link: Link) {
    deactivateTarget = link;
  }

  async function performDeactivate(key: string) {
    try {
      await deactivateLink(key);
      links.update((cur) => cur.map((l) => (l.key === key ? { ...l, active: false } : l)));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      throw new Error('Could not deactivate this link. Please try again.');
    }
  }

  function menuItems(link: Link) {
    const items: MenuItem[] = [
      { label: 'Open destination', href: link.destination_url },
      { label: 'Copy short URL', onselect: () => copyShortUrl(link.key) },
      { label: 'QR code (SVG)', href: qrSvgUrl(link.key) },
      { label: 'QR code (PNG)', href: qrPngUrl(link.key) },
      { label: 'Edit', onselect: () => navigate('/links/' + encodeURIComponent(link.key)) },
    ];
    if (link.active && link.denied_reason === 0) {
      items.push({ label: 'Deactivate', danger: true, onselect: () => void handleDeactivate(link) });
    }
    return items;
  }

  function tagFor(link: Link): string {
    const s = linkStatus(link);
    if (s === 'denied') return link.denied_reason > 0 ? `Denied: ${deniedReasonLabel(link.denied_reason)}` : 'Denied';
    return s === 'inactive' ? 'Inactive' : '';
  }

  // Pasting a URL while no input is focused opens New link prefilled (#0158).
  function onPaste(e: ClipboardEvent) {
    // LinksList also renders inside the Overview (#/); only the Links page takes pastes.
    if (getRoute().path !== '/links') return;
    const t = e.target as HTMLElement | null;
    const tag = (t?.tagName ?? '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t?.isContentEditable) return;
    if (document.querySelector('dialog[open]')) return;
    const url = pastedUrl(e.clipboardData?.getData('text/plain'));
    if (!url) return;
    e.preventDefault();
    openNewLink(url);
  }


  onMount(() => {
    loadPage(1);
    loadCampaignNames();

    // #0034 SSE live updates: open the /api/events stream and prepend each
    // link.created event to the shared store.
    const unsubscribe = subscribeLinks((link) => {
      links.update((cur) => prependUniqueByKey(cur, link));
    });

    return unsubscribe;
  });
</script>

<ConfirmDialog
  open={deactivateTarget !== null}
  title="Deactivate link?"
  message={`"${deactivateTarget?.title || deactivateTarget?.key || ''}" will stop redirecting. Its click history is kept.`}
  confirmLabel="Deactivate link"
  busyLabel="Deactivating…"
  oncancel={() => (deactivateTarget = null)}
  onconfirm={() => (deactivateTarget ? performDeactivate(deactivateTarget.key) : undefined)}
/>

<svelte:window onpaste={onPaste} />

<div class="app-shell">
<h1 class="page-title">Links</h1>
<Panel title="Your links" noPadding={!loadError}>
  {#if loadError}
    <div class="err">
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="subtle" onclick={() => loadPage(page)}>Retry</Button>
    </div>
  {:else}
    <DataTable
      {columns}
      rows={$links}
      rowKey={(l) => l.key}
      caption="Your links"
      {loading}
      rowClass={(l) => (linkStatus(l) === 'active' ? '' : 'is-inactive')}
      rowLabel={(l) => l.title || l.key}
    >
      {#snippet cell(l, col)}
        {#if col.key === 'link'}
          <TwoLineCell
            title={l.title || l.key}
            meta={linkMeta(l)}
            href={detailHref(l.key)}
            dim={linkStatus(l) !== 'active'}
            tag={tagFor(l)}
          />
        {:else if col.key === 'campaign'}
          {#if l.campaign_id !== null && campaignNames[l.campaign_id]}
            <span title={campaignNames[l.campaign_id]}>{campaignNames[l.campaign_id]}</span>
          {:else}
            <span class="text-muted">—</span>
          {/if}
        {:else if col.key === 'clicks'}
          <InlineBar value={l.click_count} max={maxClicks} />
        {:else}
          <RowMenu label={`Actions for ${l.title || l.key}`} copyText={shortUrl(l.key)} items={menuItems(l)} />
        {/if}
      {/snippet}
      {#snippet empty()}
        <span class="text-muted">No links yet — use + New link to create your first one.</span>
      {/snippet}
    </DataTable>

    {#if !loading && totalPages > 1}
      <div class="pager">
        <Button disabled={!hasPrev} onclick={() => loadPage(page - 1)}>Previous</Button>
        <span class="text-muted">Page {page} of {totalPages} ({total} links)</span>
        <Button disabled={!hasNext} onclick={() => loadPage(page + 1)}>Next</Button>
      </div>
    {/if}
  {/if}
</Panel>
</div>

<style>
  .err {
    padding: var(--space-4);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-4);
    justify-content: center;
    border-top: var(--border-w) solid var(--border);
    flex-wrap: wrap;
  }
</style>
