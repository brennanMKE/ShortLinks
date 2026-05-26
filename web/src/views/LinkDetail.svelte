<!--
  Link Detail view (#0035). Shows the full stats for a single link, read by key
  from the shared `selectedLinkKey` store (the Dashboard sets it when a row is
  clicked — there is no URL router, so the key lives in a store).

  On mount it loads GET /api/links/{key} via getLink, surfacing loading / error
  states (a 404 maps to a "not found" message with a Back action). It displays
  the short URL (with a copy-to-clipboard button), destination, title, status
  (active / inactive / denied-with-reason), created + expiry dates, total click
  count, and the #0030 UTM breakdown (by_source / by_medium / by_campaign,
  sorted by count desc, with a graceful empty-stats case). A Deactivate action
  calls deactivateLink and reflects the new status here and in the shared `links`
  store; it is hidden once the link is inactive/denied. A Back action returns to
  the dashboard via a store write.

  All non-trivial pure logic (UTM sorting/formatting, empty-stats detection,
  status-label derivation, date formatting) lives in lib/linkDetail.ts and is
  unit-tested there. We match the Svelte 5 runes + error-handling style of
  Dashboard.svelte / Login.svelte.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, links, selectedLinkKey } from '../lib/stores';
  import { getLink, deactivateLink, ApiError } from '../lib/api';
  import { shortUrl, linkStatus } from '../lib/links';
  import {
    isNoneBucket,
    isEmptyStats,
    utmDimensions,
    statusLabel,
    formatDate,
  } from '../lib/linkDetail';
  import type { LinkDetail } from '../lib/types';

  let loading = $state(true);
  // `notFound` is the 404 case (deleted / never existed / not owned); `loadError`
  // is any other failure. They render different copy.
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<LinkDetail | null>(null);

  let deactivating = $state(false);
  let copied = $state(false);

  // Derived display values (recomputed when `detail` changes).
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
      // Clipboard may be unavailable (insecure context / permissions); ignore.
    }
  }

  async function handleDeactivate() {
    if (!detail || !canDeactivate) return;
    const key = detail.key;
    deactivating = true;
    try {
      await deactivateLink(key);
      // Reflect the soft-delete locally without a refetch…
      if (detail) detail = { ...detail, active: false };
      // …and in the shared dashboard list so the row updates when we go back.
      links.update((cur) => cur.map((l) => (l.key === key ? { ...l, active: false } : l)));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      // Other failures leave the displayed status unchanged; the user can retry.
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
      // No link selected (e.g. a direct navigation) — treat as not found so the
      // user gets a Back action rather than a perpetual spinner.
      loading = false;
      notFound = true;
      return;
    }
    load(key);
  });
</script>

<div class="detail">
  <header class="topbar">
    <button type="button" class="back" onclick={goDashboard}>&larr; Dashboard</button>
    <h1 class="wordmark">Link detail</h1>
  </header>

  {#if loading}
    <p class="muted" role="status">Loading link…</p>
  {:else if notFound}
    <section class="card">
      <h2>Link not found</h2>
      <p class="muted">This link no longer exists, or you don't have access to it.</p>
      <button type="button" class="primary" onclick={goDashboard}>Back to dashboard</button>
    </section>
  {:else if loadError && !detail}
    <section class="card">
      <p class="error" role="alert">{loadError}</p>
      {#if $selectedLinkKey}
        <button type="button" class="primary" onclick={() => load($selectedLinkKey!)}>Retry</button>
      {/if}
    </section>
  {:else if detail}
    <section class="card">
      <div class="title-row">
        <h2>{detail.title || 'Untitled link'}</h2>
        <span class="badge {status}">{statusLabel(detail)}</span>
      </div>

      <dl class="fields">
        <dt>Short URL</dt>
        <dd class="short-url-row">
          <a class="short-url" href={shareUrl} target="_blank" rel="noreferrer">{shareUrl}</a>
          <button type="button" class="copy" onclick={copyShortUrl}>
            {copied ? 'Copied!' : 'Copy'}
          </button>
        </dd>

        <dt>Destination</dt>
        <dd>
          <a class="dest" href={detail.destination_url} target="_blank" rel="noreferrer">
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
        <p class="error" role="alert">{loadError}</p>
      {/if}

      {#if canDeactivate}
        <button
          type="button"
          class="danger"
          disabled={deactivating}
          onclick={handleDeactivate}
        >
          {deactivating ? 'Deactivating…' : 'Deactivate link'}
        </button>
      {/if}
    </section>

    <section class="card">
      <h2>UTM breakdown</h2>
      {#if noStats}
        <p class="muted">No click data yet — share this link to start collecting stats.</p>
      {:else}
        <div class="utm-grid">
          {#each dimensions as dim (dim.dimension)}
            <div class="utm-dim">
              <h3>{dim.label}</h3>
              {#if dim.buckets.length === 0}
                <p class="muted small">No data.</p>
              {:else}
                <table class="utm-table">
                  <tbody>
                    {#each dim.buckets as b (b.value)}
                      <tr>
                        <td class="utm-value" class:none={isNoneBucket(b)}>
                          {isNoneBucket(b) ? '(none)' : b.value}
                        </td>
                        <td class="num">{b.count}</td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .detail {
    max-width: 48rem;
    margin: 0 auto;
    padding: 1rem;
  }
  .topbar {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }
  .wordmark {
    font-size: 1.125rem;
    margin: 0;
  }
  .back {
    background: none;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    padding: 0.375rem 0.75rem;
    cursor: pointer;
    font-size: 0.875rem;
  }
  .card {
    border: 1px solid #e2e2e2;
    border-radius: 0.5rem;
    padding: 1.25rem;
    margin-bottom: 1.5rem;
  }
  .card h2 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1rem;
  }
  .title-row h2 {
    margin: 0;
  }
  .fields {
    display: grid;
    grid-template-columns: 9rem 1fr;
    gap: 0.5rem 1rem;
    margin: 0 0 1rem;
  }
  .fields dt {
    color: #666;
    font-weight: 600;
    font-size: 0.8125rem;
    align-self: center;
  }
  .fields dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .short-url-row {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    flex-wrap: wrap;
  }
  .short-url {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    color: #1f6feb;
    word-break: break-all;
  }
  .dest {
    color: #1f6feb;
    word-break: break-all;
  }
  .copy {
    border: 1px solid #1f6feb;
    background: #fff;
    color: #1f6feb;
    border-radius: 0.375rem;
    padding: 0.25rem 0.625rem;
    font-size: 0.8125rem;
    cursor: pointer;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  .muted {
    color: #888;
  }
  .muted.small {
    font-size: 0.8125rem;
  }
  .badge {
    display: inline-block;
    padding: 0.125rem 0.5rem;
    border-radius: 1rem;
    font-size: 0.75rem;
    font-weight: 600;
    white-space: nowrap;
  }
  .badge.active {
    background: #e6f4ea;
    color: #1a7f37;
  }
  .badge.inactive {
    background: #f0f0f0;
    color: #777;
  }
  .badge.denied {
    background: #fbe9e7;
    color: #c0362c;
  }
  .primary {
    margin-top: 0.5rem;
    padding: 0.5rem 0.875rem;
    border: none;
    border-radius: 0.375rem;
    background: #1f6feb;
    color: #fff;
    font-size: 0.9375rem;
    cursor: pointer;
  }
  .danger {
    margin-top: 0.5rem;
    padding: 0.5rem 0.875rem;
    border: 1px solid #c0362c;
    border-radius: 0.375rem;
    background: #fff;
    color: #c0362c;
    font-size: 0.9375rem;
    cursor: pointer;
  }
  .danger:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .utm-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
    gap: 1.25rem;
  }
  .utm-dim h3 {
    margin: 0 0 0.5rem;
    font-size: 0.9375rem;
  }
  .utm-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.875rem;
  }
  .utm-table td {
    padding: 0.25rem 0.25rem;
    border-bottom: 1px solid #eee;
  }
  .utm-table td.num {
    text-align: right;
  }
  .utm-value {
    overflow-wrap: anywhere;
  }
  .utm-value.none {
    color: #999;
    font-style: italic;
  }
  .error {
    color: #c0362c;
    font-size: 0.875rem;
    margin: 0.5rem 0;
  }
</style>
