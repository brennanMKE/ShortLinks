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

  The list is kept in the shared `links` store so the SSE issue (#0034) can
  prepend live link.created events to it — see the clearly-marked SEAM below.
  This issue loads the initial list via REST only; SSE is NOT implemented here.

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
  import type { Link } from '../lib/types';

  const PER_PAGE = 20;

  // ── Create-form state ──────────────────────────────────────────────────────
  let destinationUrl = $state('');
  let title = $state('');
  let customKey = $state('');
  let expiresAt = $state(''); // <input type="datetime-local"> value (local time).
  let submitting = $state(false);

  // The contextual banner shown after the most recent create attempt, and the
  // per-field inline errors (cleared on edit / next submit).
  let notice = $state<CreateNotice | null>(null);
  let keyError = $state<string | null>(null);
  let urlError = $state<string | null>(null);

  // Whether the entered URL fails client-side validation (gates submit, mirrors
  // the server's http(s) check; the server is still authoritative).
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

  // Copy-button feedback: the key whose short URL was most recently copied.
  let copiedKey = $state<string | null>(null);

  async function loadPage(p: number) {
    loading = true;
    loadError = null;
    try {
      const res = await listLinks(p, PER_PAGE);
      // The list is the shared store so #0034 can prepend live events to it.
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
    const input: CreateLinkInput = { destination_url: destinationUrl.trim() };
    const t = title.trim();
    if (t !== '') input.title = t;
    const k = customKey.trim();
    if (k !== '') input.key = k;
    const e = expiresAt.trim();
    if (e !== '') {
      // <input type="datetime-local"> gives a local "YYYY-MM-DDTHH:mm"; convert
      // to an RFC 3339 instant (UTC, with offset) for the server's expires_at.
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
      // Prepend the returned link to the shared store. On an active-duplicate the
      // link already exists in the list; replace any existing row with the same
      // key so we don't show it twice.
      links.update((cur) => [created, ...cur.filter((l) => l.key !== created.key)]);
      // Clear the form except keep nothing sticky; a fresh form invites the next.
      destinationUrl = '';
      title = '';
      customKey = '';
      expiresAt = '';
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
      // Reflect the soft-delete in the store row without a refetch.
      links.update((cur) =>
        cur.map((l) => (l.key === key ? { ...l, active: false } : l)),
      );
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
      }
      // Other failures leave the row unchanged; the user can retry.
    } finally {
      const { [key]: _removed, ...rest } = deactivating;
      deactivating = rest;
    }
  }

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // Even if the server call fails, drop local state and return to login.
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

    // ────────────────────────────────────────────────────────────────────────
    // SEAM #0034 (SSE): subscribe to /api/events here and prepend link.created
    // events to the links store; close on unmount.
    //
    // #0034 will open `new EventSource('/api/events')`, listen for the
    // 'link.created' event, JSON.parse the payload into a Link, and prepend it to
    // the shared `links` store (deduping by key, exactly like handleCreate above
    // does). The returned cleanup function from this onMount is where #0034 will
    // call eventSource.close() so the stream is torn down when the Dashboard
    // unmounts. This issue (#0033) loads the list via REST only — no SSE here.
    // ────────────────────────────────────────────────────────────────────────
  });
</script>

<div class="dashboard">
  <header class="topbar">
    <h1 class="wordmark">go.sstools.co</h1>
    <nav class="tabs" aria-label="Primary">
      <button type="button" class="tab active" aria-current="page">Dashboard</button>
      <button type="button" class="tab" onclick={() => go('account')}>Account</button>
      {#if $currentUser?.is_admin}
        <button type="button" class="tab" onclick={() => go('admin')}>Admin</button>
      {/if}
    </nav>
    <button type="button" class="signout" onclick={handleSignOut}>Sign out</button>
  </header>

  <section class="create card">
    <h2>Create a short link</h2>
    <form
      onsubmit={(e) => {
        e.preventDefault();
        handleCreate();
      }}
    >
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
      />
      {#if urlInvalid}
        <p class="field-hint" role="status">Enter a valid http(s) URL.</p>
      {/if}
      {#if urlError}
        <p class="error" role="alert">{urlError}</p>
      {/if}

      <label for="title">Title <span class="optional">(optional)</span></label>
      <input
        id="title"
        type="text"
        placeholder="A human-readable label"
        bind:value={title}
        disabled={submitting}
      />

      <label for="custom-key">Custom alias <span class="optional">(optional)</span></label>
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
      />
      {#if keyError}
        <p class="error" role="alert">{keyError}</p>
      {/if}

      <label for="expires">Expires <span class="optional">(optional)</span></label>
      <input
        id="expires"
        type="datetime-local"
        bind:value={expiresAt}
        disabled={submitting}
      />

      <button type="submit" disabled={!canSubmit}>
        {submitting ? 'Creating…' : 'Create link'}
      </button>
    </form>

    {#if notice}
      {#if notice.kind === 'created' || notice.kind === 'duplicate'}
        {@const resultKey = notice.link.key}
        <div class="result" role="status">
          {#if notice.kind === 'duplicate'}
            <p class="notice">{notice.message}</p>
          {:else}
            <p class="success-label">Your short link is ready:</p>
          {/if}
          <div class="short-url-row">
            <a class="short-url" href={notice.shortUrl} target="_blank" rel="noreferrer">
              {notice.shortUrl}
            </a>
            <button type="button" class="copy" onclick={() => copyShortUrl(resultKey)}>
              {copiedKey === resultKey ? 'Copied!' : 'Copy'}
            </button>
          </div>
        </div>
      {:else if notice.kind === 'denied'}
        <p class="error denied" role="alert">{notice.message}</p>
      {:else if notice.field === null}
        <p class="error" role="alert">{notice.message}</p>
      {/if}
    {/if}
  </section>

  <section class="list card">
    <h2>Your links</h2>

    {#if loading}
      <p class="muted">Loading your links…</p>
    {:else if loadError}
      <p class="error" role="alert">
        {loadError}
        <button type="button" class="link" onclick={() => loadPage(page)}>Retry</button>
      </p>
    {:else if $links.length === 0}
      <p class="muted">No links yet — create your first one above.</p>
    {:else}
      <table class="links-table">
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
            <tr class="row" onclick={() => openDetail(link.key)}>
              <td class="short-cell">
                <span class="short-key">/u/{link.key}</span>
                <button
                  type="button"
                  class="copy small"
                  title="Copy short URL"
                  onclick={(e) => {
                    e.stopPropagation();
                    copyShortUrl(link.key);
                  }}
                >
                  {copiedKey === link.key ? 'Copied!' : 'Copy'}
                </button>
              </td>
              <td class="dest" title={link.destination_url}>{destinationDomain(link.destination_url)}</td>
              <td>{link.title || '—'}</td>
              <td class="num">{link.click_count}</td>
              <td>
                <span class="badge {status}">
                  {#if status === 'denied'}
                    Denied{link.denied_reason > 0 ? `: ${deniedReasonLabel(link.denied_reason)}` : ''}
                  {:else if status === 'inactive'}
                    Inactive
                  {:else}
                    Active
                  {/if}
                </span>
              </td>
              <td class="muted">{formatDate(link.created_at)}</td>
              <td class="actions">
                {#if status === 'active'}
                  <button
                    type="button"
                    class="link danger"
                    disabled={deactivating[link.key]}
                    onclick={(e) => {
                      e.stopPropagation();
                      handleDeactivate(link.key);
                    }}
                  >
                    {deactivating[link.key] ? 'Deactivating…' : 'Deactivate'}
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>

      {#if totalPages > 1}
        <div class="pager">
          <button type="button" disabled={!hasPrev} onclick={() => loadPage(page - 1)}>
            Previous
          </button>
          <span class="page-info">Page {page} of {totalPages} ({total} links)</span>
          <button type="button" disabled={!hasNext} onclick={() => loadPage(page + 1)}>
            Next
          </button>
        </div>
      {/if}
    {/if}
  </section>
</div>

<style>
  .dashboard {
    max-width: 56rem;
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
  .tabs {
    display: flex;
    gap: 0.5rem;
    flex: 1;
  }
  .tab {
    background: none;
    border: none;
    padding: 0.375rem 0.5rem;
    border-radius: 0.375rem;
    cursor: pointer;
    color: #444;
    font-size: 0.9375rem;
  }
  .tab.active {
    background: #eef3fb;
    color: #1f6feb;
    font-weight: 600;
  }
  .signout {
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
  form {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }
  label {
    font-weight: 600;
    font-size: 0.875rem;
    margin-top: 0.5rem;
  }
  .optional {
    font-weight: 400;
    color: #888;
  }
  input {
    padding: 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 1rem;
  }
  input[aria-invalid='true'] {
    border-color: #c0362c;
  }
  button[type='submit'] {
    margin-top: 1rem;
    padding: 0.5rem;
    border: none;
    border-radius: 0.375rem;
    background: #1f6feb;
    color: #fff;
    font-size: 1rem;
    cursor: pointer;
  }
  button[type='submit']:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .result {
    margin-top: 1rem;
    padding: 0.75rem;
    border: 1px solid #d0e3ff;
    background: #f5f9ff;
    border-radius: 0.375rem;
  }
  .success-label {
    margin: 0 0 0.5rem;
    font-weight: 600;
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
  .copy {
    border: 1px solid #1f6feb;
    background: #fff;
    color: #1f6feb;
    border-radius: 0.375rem;
    padding: 0.25rem 0.625rem;
    font-size: 0.8125rem;
    cursor: pointer;
  }
  .copy.small {
    padding: 0.125rem 0.5rem;
    font-size: 0.75rem;
  }
  .links-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.875rem;
  }
  .links-table th,
  .links-table td {
    text-align: left;
    padding: 0.5rem 0.5rem;
    border-bottom: 1px solid #eee;
    vertical-align: middle;
  }
  .links-table th {
    color: #666;
    font-weight: 600;
    font-size: 0.8125rem;
  }
  .row {
    cursor: pointer;
  }
  .row:hover {
    background: #fafbfc;
  }
  .short-cell {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    white-space: nowrap;
  }
  .short-key {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  }
  .dest {
    max-width: 14rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .muted {
    color: #888;
  }
  .badge {
    display: inline-block;
    padding: 0.125rem 0.5rem;
    border-radius: 1rem;
    font-size: 0.75rem;
    font-weight: 600;
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
  .actions {
    text-align: right;
    white-space: nowrap;
  }
  .link {
    background: none;
    border: none;
    color: #1f6feb;
    cursor: pointer;
    padding: 0;
    font-size: 0.875rem;
  }
  .link.danger {
    color: #c0362c;
  }
  .link:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-top: 1rem;
    justify-content: center;
  }
  .pager button {
    padding: 0.375rem 0.75rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    background: #fff;
    cursor: pointer;
  }
  .pager button:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .page-info {
    color: #666;
    font-size: 0.875rem;
  }
  .error {
    color: #c0362c;
    font-size: 0.875rem;
    margin: 0.25rem 0 0;
  }
  .error.denied {
    margin-top: 1rem;
    padding: 0.75rem;
    border: 1px solid #f3c6c0;
    background: #fbe9e7;
    border-radius: 0.375rem;
  }
  .notice {
    color: #1a7f37;
    margin: 0 0 0.5rem;
    font-weight: 600;
  }
  .field-hint {
    color: #b06000;
    font-size: 0.8125rem;
    margin: 0.125rem 0 0;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
