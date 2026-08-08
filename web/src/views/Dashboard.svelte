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
    listCampaigns,
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
  import type { Link, Campaign } from '../lib/types';
  import { emptyUtmParams, composeUtmUrl, isUtmEmpty, fillBlankUtmParams } from '../lib/utm';
  import type { UtmParams } from '../lib/utm';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import UtmField from '../lib/UtmField.svelte';
  import UtmConventions from '../lib/UtmConventions.svelte';
  import { APP_NAME } from '../lib/branding';

  const PER_PAGE = 20;

  // ── Create-form state ──────────────────────────────────────────────────────
  let destinationUrl = $state('');
  let title = $state('');
  let customKey = $state('');
  let expiresAt = $state('');
  let submitting = $state(false);

  // Optional expiry (#0076). expiresAt === '' means the link NEVER expires, and
  // the field is shown BLANK — no native "mm/dd/yyyy" mask. A native
  // datetime-local is layered transparently over the field purely as the picker
  // engine + value holder; our own text shows the chosen date. This looks blank
  // when empty and identical across browsers (no fragile mask-hiding CSS).
  let expiresInput = $state<HTMLInputElement | null>(null);
  const hasExpiry = $derived(expiresAt !== '');

  // Friendly display of the chosen date, e.g. "Dec 31, 2026, 11:59 PM". expiresAt
  // is local wall-clock ("YYYY-MM-DDTHH:mm"), so new Date() parses it as local.
  const expiryLabel = $derived(
    expiresAt
      ? new Date(expiresAt).toLocaleString(undefined, {
          year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
        })
      : '',
  );

  // Open the native date picker. Wired to both click and focus so it appears as
  // soon as the field is engaged (including tabbing in). showPicker needs user
  // activation and can throw — a focused input is a fine fallback.
  function openExpiryPicker() {
    try {
      expiresInput?.showPicker();
    } catch {
      /* no activation / unsupported — the focused input still accepts a date */
    }
  }

  // Clear the date: empties the field and dismisses the picker (blur closes it).
  function clearExpiry() {
    expiresAt = '';
    expiresInput?.blur();
  }

  // ── UTM builder state ───────────────────────────────────────────────────────
  let utmOpen = $state(false);
  let utmParams = $state<UtmParams>(emptyUtmParams());
  // Placement (#0099): a free-text operational label ("18th & Texas board"),
  // deliberately separate from utm_content — see docs/utm.md.
  let placement = $state('');

  // Live preview: the destination URL with UTM params baked in. When UTM fields
  // are all empty this equals destinationUrl unchanged (no stray `?` appended).
  // The third argument (previous) is a CONSTANT emptyUtmParams() on create:
  // the builder starts with nothing, so it can never have "owned" a UTM
  // param already present in a pasted destinationUrl — composeUtmUrl must
  // never delete one the builder didn't put there (#0099 review: the
  // create-path regression from an earlier, unconditional-delete version).
  const composedUrl = $derived(composeUtmUrl(destinationUrl, utmParams, emptyUtmParams()));
  const hasUtm = $derived(!isUtmEmpty(utmParams));

  // ── Campaign selection (#0099) ──────────────────────────────────────────────
  // The caller's own campaigns, for the optional "assign to campaign"
  // dropdown on the create form. Loaded once on mount; a failure here just
  // means the dropdown stays empty — it must never block link creation.
  let campaigns = $state<Campaign[]>([]);
  let selectedCampaignID = $state<number | ''>('');

  async function loadCampaigns() {
    try {
      const res = await listCampaigns();
      campaigns = res.campaigns.filter((c) => !c.archived);
    } catch {
      // Non-fatal: the create form works fine with no campaign selectable.
    }
  }

  // Selecting a campaign PREFILLS the five UTM builder fields from its
  // default_utm_* values — a starting point only. fillBlankUtmParams (#0099
  // review item 6) fills ONLY fields that are currently blank; anything the
  // author has already typed (by hand, or from a previously selected
  // campaign they then edited) is left exactly as they wrote it — the naive
  // "replace utmParams wholesale" would silently wipe that. Every field
  // stays bound to utmParams via the same bind:value the author already
  // types into either way, so nothing here locks a field. The dropdown now
  // lives OUTSIDE the collapsible UTM section (review item 5 — it used to be
  // reachable only after expanding "Campaign / UTM parameters", so a
  // campaign could never be picked first), so expanding it here on select is
  // what actually reveals the fields that were just prefilled.
  function handleCampaignSelect() {
    if (selectedCampaignID === '') return;
    const c = campaigns.find((c) => c.id === selectedCampaignID);
    if (!c) return;
    utmParams = fillBlankUtmParams(utmParams, c);
    utmOpen = true;
  }

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
    // destination_url — the destination site's own analytics still depends on
    // it. The same five UTM values ALSO go along as discrete fields (#0099),
    // plus placement and an optional campaign assignment, so the backend's
    // stored columns agree with what was baked in. See lib/utm.ts for the
    // full storage-decision rationale.
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
    if (selectedCampaignID !== '') input.campaign_id = selectedCampaignID;
    const src = utmParams.utm_source.trim();
    if (src !== '') input.utm_source = src;
    const med = utmParams.utm_medium.trim();
    if (med !== '') input.utm_medium = med;
    const camp = utmParams.utm_campaign.trim();
    if (camp !== '') input.utm_campaign = camp;
    const term = utmParams.utm_term.trim();
    if (term !== '') input.utm_term = term;
    const content = utmParams.utm_content.trim();
    if (content !== '') input.utm_content = content;
    const p = placement.trim();
    if (p !== '') input.placement = p;
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
      placement = '';
      selectedCampaignID = '';
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

  function go(view: 'campaigns' | 'account' | 'admin') {
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
    loadCampaigns();

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
    <h1 class="app-title">{APP_NAME}</h1>
    <nav class="nav-tabs" aria-label="Primary">
      <button type="button" class="nav-tab active" aria-current="page">Dashboard</button>
      <button type="button" class="nav-tab" onclick={() => go('campaigns')}>Campaigns</button>
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

      <!-- Expires (#0076): blank when no date is set (link never expires); shows
           the chosen date otherwise. The calendar button opens the native picker;
           clicking or tabbing into the field opens it too; the × (only when a date
           is set) clears it and closes the picker. A transparent native
           datetime-local is layered on top as the picker engine + value holder. -->
      <div class="field">
        <label for="expires">Expires <span class="text-faint">(optional)</span></label>
        <div class="dtfield" class:has-value={hasExpiry}>
          <span class="dtfield-text">{expiryLabel}</span>
          <input
            id="expires"
            class="dtfield-native"
            type="datetime-local"
            bind:this={expiresInput}
            bind:value={expiresAt}
            onclick={openExpiryPicker}
            onfocus={openExpiryPicker}
            disabled={submitting}
          />
          {#if hasExpiry}
            <button
              type="button"
              class="dtfield-btn dtfield-clear"
              tabindex="-1"
              disabled={submitting}
              onclick={clearExpiry}
              aria-label="Clear expiration date"
              title="Clear expiration date"
            >×</button>
          {/if}
          <button
            type="button"
            class="dtfield-btn dtfield-cal"
            tabindex="-1"
            disabled={submitting}
            onclick={openExpiryPicker}
            aria-label="Choose expiration date"
            title="Choose expiration date"
          >
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
              <rect x="2" y="3.2" width="12" height="10.8" rx="2" stroke="currentColor" stroke-width="1.4" />
              <path d="M2 6.6h12M5.5 1.8v2.6M10.5 1.8v2.6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
            </svg>
          </button>
        </div>
      </div>

      <!-- Campaign selection (#0099) — deliberately OUTSIDE the collapsible
           UTM section below (review item 5): it must be reachable without
           first expanding "Campaign / UTM parameters", so a campaign can be
           picked before the author ever opens that section. Selecting one
           prefills the (still-collapsed) UTM fields and expands the section
           to show them — see handleCampaignSelect. -->
      {#if campaigns.length > 0}
        <div class="field">
          <label for="campaign-select">Assign to campaign <span class="text-faint">(optional)</span></label>
          <select
            id="campaign-select"
            bind:value={selectedCampaignID}
            onchange={handleCampaignSelect}
            disabled={submitting}
          >
            <option value="">No campaign</option>
            {#each campaigns as c (c.id)}
              <option value={c.id}>{c.name}</option>
            {/each}
          </select>
          <p class="text-faint">
            Prefills the blank fields below from the campaign's defaults — still yours to edit.
          </p>
        </div>
      {/if}

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
            <UtmConventions />

            {#if destinationUrl.trim() !== ''}
              <div class="utm-preview">
                <p class="utm-preview-label">Destination preview</p>
                <p class="utm-preview-url" title={composedUrl}>{composedUrl}</p>
              </div>
            {/if}

            <UtmField
              fieldKey="utm_source"
              id="utm-source"
              bind:value={utmParams.utm_source}
              disabled={submitting}
            />
            <UtmField
              fieldKey="utm_medium"
              id="utm-medium"
              bind:value={utmParams.utm_medium}
              disabled={submitting}
            />
            <UtmField
              fieldKey="utm_campaign"
              id="utm-campaign"
              bind:value={utmParams.utm_campaign}
              disabled={submitting}
            />
            <UtmField
              fieldKey="utm_term"
              id="utm-term"
              bind:value={utmParams.utm_term}
              disabled={submitting}
            />
            <UtmField
              fieldKey="utm_content"
              id="utm-content"
              bind:value={utmParams.utm_content}
              disabled={submitting}
            />
            <div class="field">
              <label for="placement">Placement <span class="text-faint">(optional)</span></label>
              <input
                id="placement"
                type="text"
                placeholder={'e.g. "18th & Texas board" — a physical/operational label, not sent to the destination site'}
                bind:value={placement}
                disabled={submitting}
              />
            </div>
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
    /* Ensure a comfortable tap target on mobile */
    min-height: 40px;
    display: inline-flex;
    align-items: center;
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

  @media (max-width: 480px) {
    /* On narrow screens, let the nav-tabs row wrap below the title.
       The sign-out button stays on the same row as the title via order. */
    .nav-tabs {
      order: 3;          /* push below title + sign-out */
      flex: 0 0 100%;    /* take full width on its own line */
      padding: 0;
      flex-wrap: wrap;
    }
    .nav-tab {
      font-size: var(--fs-base);
      padding: var(--space-1) var(--space-3);
    }
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
    background: var(--danger-subtle);
    color: var(--danger);
    border-radius: var(--radius);
    font-size: var(--fs-sm);
  }
  .input-error {
    border-color: var(--danger) !important;
  }

  /* Custom Expires date field (#0076). Looks like a normal text input, but is
     BLANK when no date is set (no native "mm/dd/yyyy" mask). A native
     datetime-local is layered transparently on top (.dtfield-native, opacity 0)
     purely as the picker engine + value holder; .dtfield-text shows our own
     formatted date. The calendar button opens the picker; the × (only when set)
     clears it. Box matches the other inputs (border, radius, #0053-derived
     min-height) so the row lines up. */
  .dtfield {
    position: relative;
    display: flex;
    align-items: center;
    width: 100%;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    min-height: calc(var(--fs-base) * var(--lh) + var(--space-2) * 2 + var(--border-w) * 2);
    padding-left: var(--space-3);
  }
  /* Mirror input:focus from app.css so engaging the field looks like any input. */
  .dtfield:focus-within {
    border-color: var(--accent);
    box-shadow: 0 0 0 2px var(--accent-subtle);
  }
  .dtfield-text {
    flex: 1 1 auto;
    min-width: 0;
    font-size: var(--fs-base);
    line-height: var(--lh);
    color: var(--text);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /* The real control: invisible but full-size, on top of the text so clicks and
     focus reach it. -webkit-appearance:none keeps #0053's width behaviour. */
  .dtfield-native {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    padding: 0;
    border: none;
    background: transparent;
    opacity: 0;
    -webkit-appearance: none;
    appearance: none;
    min-width: 0;
    cursor: pointer;
    z-index: 1;
  }
  .dtfield-native:disabled {
    cursor: default;
  }
  /* Calendar / clear buttons sit above the transparent native input so they stay
     clickable. tabindex=-1 in markup keeps tab order on the input itself. */
  .dtfield-btn {
    position: relative;
    z-index: 2;
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: center;
    width: var(--space-6);
    align-self: stretch;
    padding: 0;
    border: none;
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .dtfield-btn:hover:not(:disabled) {
    color: var(--text);
  }
  .dtfield-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .dtfield-clear {
    font-size: var(--fs-lg);
    line-height: 1;
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
    margin-bottom: var(--space-3);
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
