<!--
  Dashboard view (#0033). Two halves:

  1. A create-link FORM (destination URL required + http(s)-validated, optional
     title, optional custom alias `key`, optional expiry). On submit it POSTs to
     /api/links via createLink. The response carries the full link object plus a
     `duplicate` boolean: on success we prepend it to the `links` store and show
     the generated short URL with a copy button; on `duplicate:true` we show the
     "already shortened" notice (still surfacing the returned link); a 422
     url_denied shows the denial reason; a 409 (alias taken) / 400 (bad URL) show
     an inline field error. All of that mapping lives in pure, tested helpers in
     lib/links.ts.

  2. A LIST of the user's links, loaded on mount via listLinks (REST). Each row
     shows the short URL (with copy), destination domain, title, created date,
     click count, and an active/denied/inactive badge; clicking a row opens the
     link-detail view (#0035) by setting selectedLinkKey + currentView; a
     deactivate action calls deactivateLink and updates the row in place.
     Pagination via ?page=/?per_page= (the server's params).

  The list is kept in the shared `links` store; on mount we subscribe to the
  /api/events SSE stream (#0034) and prepend live link.created events to it
  (deduped by key), closing the stream on unmount — see onMount below.

  Navigation tabs (Dashboard, Account, Admin-when-admin) and Sign out live in the
  header; we match Login.svelte's Svelte 5 runes + error-handling style.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import {
    currentView,
    currentUser,
    links,
    selectedLinkKey,
  } from '../lib/stores';
  import {
    listLinks,
    createLink,
    deactivateLink,
    logout,
    ApiError,
    type CreateLinkInput,
  } from '../lib/api';
  import {
    shortUrl,
    isValidHttpUrl,
    noticeForCreated,
    noticeForError,
    linkStatus,
    destinationDomain,
    deniedReasonLabel,
    type CreateNotice,
  } from '../lib/links';
  import { subscribeLinks, prependUniqueByKey } from '../lib/events';
  import type { Link } from '../lib/types';
  import { emptyUtmParams, composeUtmUrl, isUtmEmpty } from '../lib/utm';
  import type { UtmParams } from '../lib/utm';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';

  const PER_PAGE = 20;

  // ── Create-form state ──────────────────────────────────────────────────────
  let destinationUrl = $state('');
  let title = $state('');
  let customKey = $state('');
  let expiresAt = $state('');
  let submitting = $state(false);

  // ── UTM builder state ───────────────────────────────────────────────────────
  let utmOpen = $state(false);
  let utmParams = $state<UtmParams>(emptyUtmParams());

  // Live preview: the destination URL with UTM params baked in. When UTM fields
  // are all empty this equals destinationUrl unchanged (no stray `?` appended).
  const composedUrl = $derived(composeUtmUrl(destinationUrl, utmParams));
  const hasUtm = $derived(!isUtmEmpty(utmParams));

  let notice = $state<CreateNotice | null>(null);
  let keyError = $state<string | null>(null);
  let urlError = $state<string | null>(null);

  const urlInvalid = $derived(destinationUrl.trim() !== '' && !isValidHttpUrl(destinationUrl));
  const canSubmit = $derived(!submitting && destinationUrl.trim() !== '' && !urlInvalid);

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
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      loadError = 'Could not load your links. Please try again.';
    } finally {
      loading = false;
    }
  }

  // ── Create submit ─────────────────────────────────────────────────────────
  function buildInput(): CreateLinkInput {
    // Use the composed URL (destination + UTM params baked in) as the stored
    // destination_url. UTM values are NOT stored as discrete fields — they are
    // merged into the URL before submission. See lib/utm.ts for rationale.
    const input: CreateLinkInput = { destination_url: composedUrl || destinationUrl.trim() };
    const t = title.trim();
    if (t !== '') input.title = t;
    const k = customKey.trim();
    if (k !== '') input.key = k;
    const e = expiresAt.trim();
    if (e !== '') {
      const d = new Date(e);
      if (!Number.isNaN(d.getTime())) input.expires_at = d.toISOString();
    }
    return input;
  }

  async function handleCreate() {
    if (!canSubmit) return;
    submitting = true;
    notice = null;
    keyError = null;
    urlError = null;
    try {
      const created = await createLink(buildInput());
      notice = noticeForCreated(created);
      links.update((cur) => [created, ...cur.filter((l) => l.key !== created.key)]);
      destinationUrl = '';
      title = '';
      customKey = '';
      expiresAt = '';
      utmParams = emptyUtmParams();
      utmOpen = false;
    } catch (err) {
      const n = noticeForError(err);
      notice = n;
      if (n.kind === 'error') {
        if (n.field === 'key') keyError = n.message;
        else if (n.field === 'destination_url') urlError = n.message;
        else if (err instanceof ApiError && err.status === 401) {
          currentUser.set(null);
          currentView.set('login');
        }
      }
    } finally {
      submitting = false;
    }
  }

  // ── Copy short URL to clipboard ─────────────────────────────────────────────
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
    selectedLinkKey.set(key);
    currentView.set('link-detail');
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
        currentView.set('login');
      }
    } finally {
      const { [key]: _removed, ...rest } = deactivating;
      deactivating = rest;
    }
  }

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // Drop local state and return to login even on failure.
    }
    currentUser.set(null);
    links.set([]);
    currentView.set('login');
  }

  function go(view: 'account' | 'admin') {
    currentView.set(view);
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
  <header class="app-header">
    <h1 class="app-title">go.sstools.co</h1>
    <nav class="nav-tabs" aria-label="Primary">
      <button type="button" class="nav-tab active" aria-current="page">Dashboard</button>
      <button type="button" class="nav-tab" onclick={() => go('account')}>Account</button>
      {#if $currentUser?.is_admin}
        <button type="button" class="nav-tab" onclick={() => go('admin')}>Admin</button>
      {/if}
    </nav>
    <Button variant="default" onclick={handleSignOut}>Sign out</Button>
  </header>

  <Panel title="Create a short link">
    <form
      onsubmit={(e) => {
        e.preventDefault();
        handleCreate();
      }}
    >
      <div class="field">
        <label for="dest-url">Destination URL</label>
        <input
          id="dest-url"
          type="url"
          inputmode="url"
          placeholder="https://example.com/page"
          bind:value={destinationUrl}
          oninput={() => {
            urlError = null;
          }}
          disabled={submitting}
          required
          aria-invalid={urlInvalid || urlError !== null}
          class:input-error={urlInvalid || urlError !== null}
        />
        {#if urlInvalid}
          <p class="text-warn" role="status">Enter a valid http(s) URL.</p>
        {/if}
        {#if urlError}
          <p class="text-error" role="alert">{urlError}</p>
        {/if}
      </div>

      <div class="field">
        <label for="title">Title <span class="text-faint">(optional)</span></label>
        <input
          id="title"
          type="text"
          placeholder="A human-readable label"
          bind:value={title}
          disabled={submitting}
        />
      </div>

      <div class="field">
        <label for="custom-key">Custom alias <span class="text-faint">(optional)</span></label>
        <input
          id="custom-key"
          type="text"
          placeholder="e.g. launch"
          bind:value={customKey}
          oninput={() => {
            keyError = null;
          }}
          disabled={submitting}
          aria-invalid={keyError !== null}
          class:input-error={keyError !== null}
        />
        {#if keyError}
          <p class="text-error" role="alert">{keyError}</p>
        {/if}
      </div>

      <div class="field">
        <label for="expires">Expires <span class="text-faint">(optional)</span></label>
        <input
          id="expires"
          type="datetime-local"
          bind:value={expiresAt}
          disabled={submitting}
        />
      </div>

      <!-- UTM builder — collapsible section (#0048) -->
      <div class="utm-section">
        <button
          type="button"
          class="utm-toggle"
          aria-expanded={utmOpen}
          onclick={() => { utmOpen = !utmOpen; }}
        >
          <span class="utm-toggle-chevron" class:open={utmOpen}>▶</span>
          Campaign / UTM parameters
          {#if hasUtm && !utmOpen}
            <span class="badge">filled</span>
          {/if}
        </button>

        {#if utmOpen}
          <div class="utm-fields">
            <div class="field">
              <label for="utm-source">Source <span class="text-faint">(utm_source)</span></label>
              <input
                id="utm-source"
                type="text"
                placeholder="e.g. newsletter, google, twitter"
                bind:value={utmParams.utm_source}
                disabled={submitting}
              />
            </div>
            <div class="field">
              <label for="utm-medium">Medium <span class="text-faint">(utm_medium)</span></label>
              <input
                id="utm-medium"
                type="text"
                placeholder="e.g. email, cpc, social"
                bind:value={utmParams.utm_medium}
                disabled={submitting}
              />
            </div>
            <div class="field">
              <label for="utm-campaign">Campaign <span class="text-faint">(utm_campaign)</span></label>
              <input
                id="utm-campaign"
                type="text"
                placeholder="e.g. spring-launch, black-friday"
                bind:value={utmParams.utm_campaign}
                disabled={submitting}
              />
            </div>
            <div class="field">
              <label for="utm-term">Term <span class="text-faint">(utm_term, optional)</span></label>
              <input
                id="utm-term"
                type="text"
                placeholder="e.g. running+shoes"
                bind:value={utmParams.utm_term}
                disabled={submitting}
              />
            </div>
            <div class="field">
              <label for="utm-content">Content <span class="text-faint">(utm_content, optional)</span></label>
              <input
                id="utm-content"
                type="text"
                placeholder="e.g. hero-cta, sidebar-link"
                bind:value={utmParams.utm_content}
                disabled={submitting}
              />
            </div>

            {#if destinationUrl.trim() !== ''}
              <div class="utm-preview">
                <p class="utm-preview-label">Destination preview</p>
                <p class="utm-preview-url" title={composedUrl}>{composedUrl}</p>
              </div>
            {/if}
          </div>
        {/if}
      </div>

      <Button type="submit" variant="primary" disabled={!canSubmit}>
        {submitting ? 'Creating…' : 'Create link'}
      </Button>
    </form>

    {#if notice}
      {#if notice.kind === 'created' || notice.kind === 'duplicate'}
        {@const resultKey = notice.link.key}
        <div class="result-box" role="status">
          {#if notice.kind === 'duplicate'}
            <p class="text-notice result-label">{notice.message}</p>
          {:else}
            <p class="result-label">Your short link is ready:</p>
          {/if}
          <div class="row">
            <a class="short-url" href={notice.shortUrl} target="_blank" rel="noreferrer">
              {notice.shortUrl}
            </a>
            <Button variant="subtle" onclick={() => copyShortUrl(resultKey)}>
              {copiedKey === resultKey ? 'Copied!' : 'Copy'}
            </Button>
          </div>
        </div>
      {:else if notice.kind === 'denied'}
        <div class="denied-box" role="alert">{notice.message}</div>
      {:else if notice.field === null}
        <p class="text-error" role="alert">{notice.message}</p>
      {/if}
    {/if}
  </Panel>

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
            <tr class="link-row" onclick={() => openDetail(link.key)}>
              <td class="short-cell">
                <span class="mono">/u/{link.key}</span>
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
  .nav-tabs {
    display: flex;
    gap: var(--space-1);
    flex: 1;
    padding: 0 var(--space-2);
  }
  .nav-tab {
    background: none;
    border: none;
    padding: var(--space-1) var(--space-2);
    border-radius: var(--radius);
    cursor: pointer;
    color: var(--text-muted);
    font-size: var(--fs-md);
    font-family: var(--font);
  }
  .nav-tab.active {
    background: var(--accent-subtle);
    color: var(--accent);
    font-weight: 600;
  }
  .nav-tab:hover:not(.active) {
    background: var(--bg-subtle);
    color: var(--text);
  }
  .short-cell {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    white-space: nowrap;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
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
  .result-box {
    margin-top: var(--space-4);
    padding: var(--space-3) var(--space-4);
    border: var(--border-w) solid var(--border);
    background: var(--accent-subtle);
    border-radius: var(--radius);
  }
  .result-label {
    margin: 0 0 var(--space-2);
    font-weight: 600;
  }
  .short-url {
    font-family: var(--font-mono);
    color: var(--accent);
    word-break: break-all;
  }
  .denied-box {
    margin-top: var(--space-4);
    padding: var(--space-3) var(--space-4);
    border: var(--border-w) solid var(--border);
    background: #fdecea;
    color: var(--danger);
    border-radius: var(--radius);
    font-size: var(--fs-sm);
  }
  .input-error {
    border-color: var(--danger) !important;
  }
  .pager {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-4);
    justify-content: center;
    border-top: var(--border-w) solid var(--border);
  }

  /* ── UTM builder (#0048) ─────────────────────────────────────────────────── */
  .utm-section {
    margin-bottom: var(--space-3);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }

  .utm-toggle {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    background: var(--bg-subtle);
    border: none;
    padding: var(--space-2) var(--space-3);
    font-family: var(--font);
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
    cursor: pointer;
    text-align: left;
  }

  .utm-toggle:hover {
    background: var(--bg-header);
    color: var(--text);
  }

  .utm-toggle-chevron {
    font-size: 9px;
    display: inline-block;
    transition: transform 0.15s ease;
    color: var(--text-faint);
  }

  .utm-toggle-chevron.open {
    transform: rotate(90deg);
  }

  .utm-fields {
    padding: var(--space-3) var(--space-3) var(--space-2);
    border-top: var(--border-w) solid var(--border);
    background: var(--bg-panel);
  }

  .utm-preview {
    margin-top: var(--space-2);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-subtle);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }

  .utm-preview-label {
    margin: 0 0 var(--space-1);
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
  }

  .utm-preview-url {
    margin: 0;
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    color: var(--accent);
    word-break: break-all;
    overflow-wrap: anywhere;
  }
</style>
