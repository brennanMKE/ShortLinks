<!--
  LinksList view (#0153) — the user's links table, reachable at #/links. Moved
  out of the old Dashboard unchanged (#0033, #0034, #0035): REST list via
  listLinks with ?page pagination, live link.created events over SSE
  prepended to the shared `links` store, row click opens /links/:key,
  deactivate updates the row in place. #0154 replaces the table itself.
-->
<script lang="ts">
  // Dashboard embeds this view under its own heading and passes false.
  let { showTitle = true }: { showTitle?: boolean } = $props();
  import { onMount } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import { listLinks, deactivateLink, ApiError } from '../lib/api';
  import { navigate } from '../lib/hashRouter.svelte';
  import Link from '../lib/Link.svelte';
  import { shortUrl, linkStatus, destinationDomain, deniedReasonLabel } from '../lib/links';
  import { subscribeLinks, prependUniqueByKey } from '../lib/events';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';

  const PER_PAGE = 20;

  // ── List state ──────────────────────────────────────────────────────────────
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let page = $state(1);
  let total = $state(0);
  let perPage = $state(PER_PAGE);
  let deactivating = $state<Record<string, boolean>>({});

  const totalPages = $derived(Math.max(1, Math.ceil(total / perPage)));
  const hasPrev = $derived(page > 1);
  const hasNext = $derived(page < totalPages);

  let copiedKey = $state<string | null>(null);

  async function loadPage(p: number) {
    loading = true;
    loadError = null;
    try {
      const res = await listLinks(p, PER_PAGE);
      links.set(res.links);
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

  async function copyShortUrl(key: string) {
    const url = shortUrl(key);
    try {
      await navigator.clipboard.writeText(url);
      copiedKey = key;
      setTimeout(() => {
        if (copiedKey === key) copiedKey = null;
      }, 1500);
    } catch {
      // Clipboard may be unavailable (insecure context / permissions); ignore.
    }
  }

  // ── Row interactions ────────────────────────────────────────────────────────
  function openDetail(key: string) {
    navigate('/links/' + encodeURIComponent(key));
  }

  async function handleDeactivate(key: string) {
    deactivating = { ...deactivating, [key]: true };
    try {
      await deactivateLink(key);
      links.update((cur) =>
        cur.map((l) => (l.key === key ? { ...l, active: false } : l)),
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
      }
    } finally {
      const { [key]: _removed, ...rest } = deactivating;
      deactivating = rest;
    }
  }

  function formatDate(iso: string): string {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return iso;
    return d.toLocaleDateString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
    });
  }

  onMount(() => {
    loadPage(1);

    // #0034 SSE live updates: open the /api/events stream and prepend each
    // link.created event to the shared store.
    const unsubscribe = subscribeLinks((link) => {
      links.update((cur) => prependUniqueByKey(cur, link));
    });

    return unsubscribe;
  });
</script>

<div class="app-shell">
{#if showTitle}<h1 class="page-title">Links</h1>{/if}
<Panel title="Your links" noPadding={$links.length > 0 && !loading && !loadError}>
    {#if loading}
      <p class="text-muted">Loading your links…</p>
    {:else if loadError}
      <p class="text-error" role="alert">
        {loadError}
      </p>
      <Button variant="subtle" onclick={() => loadPage(page)}>Retry</Button>
    {:else if $links.length === 0}
      <p class="text-muted">No links yet — create your first one above.</p>
    {:else}
      <div class="table-scroll">
        <table>
          <thead>
            <tr>
              <th scope="col">Short URL</th>
              <th scope="col">Destination</th>
              <th scope="col">Title</th>
              <th scope="col">Clicks</th>
              <th scope="col">Status</th>
              <th scope="col">Created</th>
              <th scope="col"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each $links as link (link.key)}
              {@const status = linkStatus(link)}
              <tr
                class="link-row"
                onclick={(e) => {
                  if ((e.target as Element).closest('a')) return;
                  openDetail(link.key);
                }}
              >
                <td class="short-cell">
                  <Link to={'/links/' + encodeURIComponent(link.key)} class="mono short-key-link">/u/{link.key}</Link>
                  <Button
                    variant="subtle"
                    onclick={(e) => {
                      e.stopPropagation();
                      copyShortUrl(link.key);
                    }}
                  >
                    {copiedKey === link.key ? 'Copied!' : 'Copy'}
                  </Button>
                </td>
                <td class="dest-cell text-muted" title={link.destination_url}>
                  {destinationDomain(link.destination_url)}
                </td>
                <td>{link.title || '—'}</td>
                <td class="num">{link.click_count}</td>
                <td>
                  <span
                    class="badge"
                    class:badge-success={status === 'active'}
                    class:badge-danger={status === 'denied'}
                    class:badge-muted={status === 'inactive'}
                  >
                    {#if status === 'denied'}
                      Denied{link.denied_reason > 0 ? `: ${deniedReasonLabel(link.denied_reason)}` : ''}
                    {:else if status === 'inactive'}
                      Inactive
                    {:else}
                      Active
                    {/if}
                  </span>
                </td>
                <td class="text-muted">{formatDate(link.created_at)}</td>
                <td class="actions-cell">
                  {#if status === 'active'}
                    <Button
                      variant="danger"
                      disabled={deactivating[link.key]}
                      onclick={(e) => {
                        e.stopPropagation();
                        handleDeactivate(link.key);
                      }}
                    >
                      {deactivating[link.key] ? 'Deactivating…' : 'Deactivate'}
                    </Button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>

      {#if totalPages > 1}
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
  .short-cell {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    white-space: nowrap;
  }
  /*
   * No local `.mono` rule here: Dashboard's only monospace text is the
   * Link below, and a scoped rule can never reach an element Link.svelte
   * renders in ITS OWN template (see app.css's ".nav-tab" comment) — Svelte
   * would flag it "unused CSS" since nothing in THIS file's own markup
   * carries a bare "mono" class. Styling instead lives entirely on
   * :global(.short-key-link), a name unique to this one Link so it can
   * never leak onto another file's own (separately scoped) ".mono" text —
   * e.g. CampaignDetail's slug/key cells, which must stay plain monospace,
   * not link-colored.
   */
  :global(.short-key-link) {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    color: var(--accent);
    text-decoration: underline;
  }
  .dest-cell {
    max-width: 200px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .actions-cell {
    text-align: right;
    white-space: nowrap;
  }
  .link-row {
    cursor: pointer;
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
