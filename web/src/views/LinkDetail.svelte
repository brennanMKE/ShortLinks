<!--
  Link Detail view (#0035, #0049). Shows the full stats for a single link, read
  by key from the shared `selectedLinkKey` store (the Dashboard sets it when a
  row is clicked — there is no URL router, so the key lives in a store).

  On mount it loads GET /api/links/{key} via getLink, surfacing loading / error
  states (a 404 maps to a "not found" message with a Back action). It displays
  the short URL (with a copy-to-clipboard button), destination, title, status
  (active / inactive / denied-with-reason), created + expiry dates, total click
  count, a clicks-over-time line chart (#0049), and the #0030 UTM breakdown
  (by_source / by_medium / by_campaign, now as horizontal bar charts with
  proportional widths). A Deactivate action calls deactivateLink and reflects the
  new status here and in the shared `links` store; it is hidden once the link is
  inactive/denied. A Back action returns to the dashboard via a store write.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, links, selectedLinkKey } from '../lib/stores';
  import { getLink, deactivateLink, ApiError } from '../lib/api';
  import { shortUrl, linkStatus } from '../lib/links';
  import {
    isEmptyStats,
    utmDimensions,
    statusLabel,
    formatDate,
  } from '../lib/linkDetail';
  import type { LinkDetail } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import ClicksChart from '../lib/ClicksChart.svelte';
  import UTMBarChart from '../lib/UTMBarChart.svelte';

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<LinkDetail | null>(null);

  let deactivating = $state(false);
  let copied = $state(false);

  const status = $derived(detail ? linkStatus(detail) : 'active');
  const shareUrl = $derived(detail ? shortUrl(detail.key) : '');
  const dimensions = $derived(detail ? utmDimensions(detail.utm_stats) : []);
  const noStats = $derived(detail ? isEmptyStats(detail.utm_stats) : true);
  const canDeactivate = $derived(status === 'active');

  async function load(key: string) {
    loading = true;
    notFound = false;
    loadError = null;
    detail = null;
    try {
      detail = await getLink(key);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      if (err instanceof ApiError && err.status === 404) {
        notFound = true;
        return;
      }
      loadError = 'Could not load this link. Please try again.';
    } finally {
      loading = false;
    }
  }

  async function copyShortUrl() {
    if (!shareUrl) return;
    try {
      await navigator.clipboard.writeText(shareUrl);
      copied = true;
      setTimeout(() => {
        copied = false;
      }, 1500);
    } catch {
      // Clipboard may be unavailable; ignore.
    }
  }

  async function handleDeactivate() {
    if (!detail || !canDeactivate) return;
    const key = detail.key;
    deactivating = true;
    try {
      await deactivateLink(key);
      if (detail) detail = { ...detail, active: false };
      links.update((cur) => cur.map((l) => (l.key === key ? { ...l, active: false } : l)));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      loadError = 'Could not deactivate this link. Please try again.';
    } finally {
      deactivating = false;
    }
  }

  function goDashboard() {
    selectedLinkKey.set(null);
    currentView.set('dashboard');
  }

  onMount(() => {
    const key = $selectedLinkKey;
    if (!key) {
      loading = false;
      notFound = true;
      return;
    }
    load(key);
  });
</script>

<div class="app-shell detail-shell">
  <header class="app-header">
    <Button onclick={goDashboard}>&larr; Dashboard</Button>
    <h1 class="app-title">Link detail</h1>
  </header>

  {#if loading}
    <p class="text-muted" role="status">Loading link…</p>
  {:else if notFound}
    <Panel title="Link not found">
      <p class="text-muted">This link no longer exists, or you don't have access to it.</p>
      <Button variant="primary" onclick={goDashboard}>Back to dashboard</Button>
    </Panel>
  {:else if loadError && !detail}
    <Panel>
      <p class="text-error" role="alert">{loadError}</p>
      {#if $selectedLinkKey}
        <Button variant="primary" onclick={() => load($selectedLinkKey!)}>Retry</Button>
      {/if}
    </Panel>
  {:else if detail}
    <Panel>
      <div class="title-row">
        <h2 class="detail-title">{detail.title || 'Untitled link'}</h2>
        <span
          class="badge"
          class:badge-success={status === 'active'}
          class:badge-danger={status === 'denied'}
          class:badge-muted={status === 'inactive'}
        >
          {statusLabel(detail)}
        </span>
      </div>

      <dl class="fields">
        <dt>Short URL</dt>
        <dd>
          <div class="row">
            <a class="short-url" href={shareUrl} target="_blank" rel="noreferrer">{shareUrl}</a>
            <Button variant="subtle" onclick={copyShortUrl}>
              {copied ? 'Copied!' : 'Copy'}
            </Button>
          </div>
        </dd>

        <dt>Destination</dt>
        <dd>
          <a class="dest-link" href={detail.destination_url} target="_blank" rel="noreferrer">
            {detail.destination_url}
          </a>
        </dd>

        <dt>Total clicks</dt>
        <dd class="num">{detail.click_count}</dd>

        <dt>Created</dt>
        <dd>{formatDate(detail.created_at)}</dd>

        <dt>Expires</dt>
        <dd>{formatDate(detail.expires_at)}</dd>
      </dl>

      {#if loadError}
        <p class="text-error" role="alert">{loadError}</p>
      {/if}

      {#if canDeactivate}
        <div style="margin-top: var(--space-3);">
          <Button
            variant="danger"
            disabled={deactivating}
            onclick={handleDeactivate}
          >
            {deactivating ? 'Deactivating…' : 'Deactivate link'}
          </Button>
        </div>
      {/if}
    </Panel>

    <Panel title="Clicks over time">
      <ClicksChart timeseries={detail.timeseries} days={30} title="Clicks over the last 30 days" />
    </Panel>

    <Panel title="UTM breakdown">
      {#if noStats}
        <p class="text-muted">No click data yet — share this link to start collecting stats.</p>
      {:else}
        <div class="utm-grid">
          {#each dimensions as dim (dim.dimension)}
            <div class="utm-dim">
              <h3 class="utm-dim-title">{dim.label}</h3>
              <UTMBarChart buckets={dim.buckets} dimension={dim.dimension} label={dim.label} />
            </div>
          {/each}
        </div>
      {/if}
    </Panel>
  {/if}
</div>

<style>
  .detail-shell {
    max-width: 760px;
  }
  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-4);
    margin-bottom: var(--space-4);
  }
  .detail-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0;
  }
  .fields {
    display: grid;
    grid-template-columns: 9rem 1fr;
    gap: var(--space-2) var(--space-4);
    margin: 0 0 var(--space-3);
  }
  .fields dt {
    color: var(--text-muted);
    font-weight: 600;
    font-size: var(--fs-sm);
    align-self: center;
  }
  .fields dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .short-url {
    font-family: var(--font-mono);
    color: var(--accent);
    word-break: break-all;
  }
  .dest-link {
    color: var(--accent);
    word-break: break-all;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  .utm-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--space-5);
  }
  .utm-dim-title {
    margin: 0 0 var(--space-2);
    font-size: var(--fs-md);
    font-weight: 600;
  }
</style>
