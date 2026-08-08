<!--
  Campaign Detail view (#0103). Reads which campaign to show from the shared
  `selectedCampaignSlug` store (CampaignsList sets it when a row is opened —
  there is no URL router, mirroring LinkDetail/selectedLinkKey).

  Two API calls back this view, on purpose:
    - GET /api/campaigns/{slug}       (getCampaign)      — metadata, the
      ALL-TIME link_count/total_clicks, every currently-assigned link, and
      (when a stats provider is wired) WINDOWED CampaignStats for the
      summary numbers.
    - GET /api/campaigns/{slug}/stats (getCampaignStats)  — the same window's
      per-link breakdown (`by_link`), which the first endpoint deliberately
      omits (#0098 downstream constraint 8: campaignDetailView is additive
      over campaignView, not a full CampaignRollup). The links table's
      "clicks in window" and "share of campaign total" columns come from
      here, matched to each link by key (lib/campaigns.ts buildLinkRows).
  A failure fetching the stats call is NON-FATAL to the page — the links
  table still renders with 0/0% in that case rather than blocking on a
  request the campaign/links data does not need.

  FOUR NUMBERS, TWO TIME SCALES (#0102 constraint 2): the summary shows
  link_count/total_clicks (ALL-TIME) beside stats.click_count (WINDOWED,
  labelled with the resolved window) — see types.ts's CampaignDetail doc
  comment. They are never reconciled into one number.

  Header: name, description, date range, and Edit/Archive controls. The edit
  form states that the slug never changes (#0098 downstream constraint 4).

  Links table: short key, title, destination, source, medium, placement,
  windowed clicks, share of total, created date — sortable by clicks
  (stable, lib/campaigns.ts sortLinkRows), each row linking to the existing
  link-detail view. "Copy all short URLs" copies every row's short URL.

  Assign/unassign against #0099's endpoints: the assign form accepts pasted
  keys or short URLs (lib/campaigns.ts parseKeysInput), chunks anything over
  the server's 50-key cap (chunkKeys), and surfaces partial success/failure
  since AssignLinks is documented non-atomic across keys.

  Charts (#0104) are a deliberate empty slot below the summary.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, selectedCampaignSlug, selectedLinkKey } from '../lib/stores';
  import {
    getCampaign,
    getCampaignStats,
    updateCampaign,
    assignLinksToCampaign,
    unassignLinkFromCampaign,
    ApiError,
  } from '../lib/api';
  import {
    campaignDateRangeLabel,
    windowLabel,
    windowDayCount,
    clicksPerDayAverage,
    buildLinkRows,
    unlistedClickCount,
    sortLinkRows,
    chunkKeys,
    parseKeysInput,
    copyAllShortUrlsText,
    toDateInput,
    toIsoDate,
    joinSentences,
  } from '../lib/campaigns';
  import { formatDate } from '../lib/linkDetail';
  import type { CampaignDetail, LinkBucket } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';

  const MAX_NAME_LENGTH = 255;

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<CampaignDetail | null>(null);
  let byLink = $state<LinkBucket[]>([]);

  // ── Links table ────────────────────────────────────────────────────────
  const rows = $derived(buildLinkRows(detail?.links ?? [], byLink));
  let sortDirection = $state<'desc' | 'asc'>('desc');
  const sortedRows = $derived(sortLinkRows(rows, sortDirection));
  // Clicks in byLink attributed to a link no longer assigned to this
  // campaign — excluded from both the numerator and denominator of "Share
  // of listed links" above, so it is surfaced separately rather than
  // silently making the visible shares look like they cover everything
  // (#0103 fix 3).
  const unlistedClicks = $derived(unlistedClickCount(detail?.links ?? [], byLink));

  function toggleSort() {
    sortDirection = sortDirection === 'desc' ? 'asc' : 'desc';
  }

  let copiedAll = $state(false);
  async function copyAll() {
    const text = copyAllShortUrlsText(sortedRows);
    if (text === '') return;
    try {
      await navigator.clipboard.writeText(text);
      copiedAll = true;
      setTimeout(() => {
        copiedAll = false;
      }, 1500);
    } catch {
      // Clipboard may be unavailable; ignore.
    }
  }

  function openLink(key: string) {
    selectedLinkKey.set(key);
    currentView.set('link-detail');
  }

  // ── Load ───────────────────────────────────────────────────────────────
  // load() also runs as a REFRESH after assign/unassign, not just on first
  // mount. `loading` — which swaps the whole view for "Loading campaign…" —
  // is only touched on the initial load (detail still null); a refresh
  // keeps the existing table/summary rendered with stale data until the new
  // detail arrives, rather than blanking the page on every edit (#0103 nit
  // 10 — a full-page spinner on every assign/unassign read as broken).
  async function load(slug: string) {
    const initial = detail === null;
    if (initial) loading = true;
    notFound = false;
    loadError = null;
    try {
      detail = await getCampaign(slug);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      if (err instanceof ApiError && err.status === 404) {
        notFound = true;
        if (initial) loading = false;
        return;
      }
      loadError = 'Could not load this campaign. Please try again.';
      if (initial) loading = false;
      return;
    }

    // Non-fatal: the links table degrades to 0 clicks / 0% share rather than
    // blocking the whole page on a second request (#0102 constraint 5: dev
    // mode's stats provider always returns empty/zero here anyway).
    try {
      const rollup = await getCampaignStats(slug);
      byLink = rollup.by_link;
    } catch {
      byLink = [];
    }

    if (initial) loading = false;
  }

  function goList() {
    selectedCampaignSlug.set(null);
    currentView.set('campaigns');
  }

  // ── Edit ───────────────────────────────────────────────────────────────
  let editing = $state(false);
  let editName = $state('');
  let editDescription = $state('');
  let editStartsAt = $state('');
  let editEndsAt = $state('');
  let saving = $state(false);
  let saveError = $state<string | null>(null);

  const editNameLength = $derived([...editName.trim()].length);
  const editNameTooLong = $derived(editNameLength > MAX_NAME_LENGTH);
  const editDatesInvalid = $derived(
    editStartsAt !== '' && editEndsAt !== '' && editEndsAt < editStartsAt,
  );
  const canSaveEdit = $derived(
    !saving && editName.trim() !== '' && !editNameTooLong && !editDatesInvalid,
  );

  function startEdit() {
    if (!detail) return;
    editName = detail.name;
    editDescription = detail.description;
    editStartsAt = toDateInput(detail.starts_at);
    editEndsAt = toDateInput(detail.ends_at);
    saveError = null;
    editing = true;
  }

  function cancelEdit() {
    editing = false;
    saveError = null;
  }

  async function saveEdit() {
    if (!detail || !canSaveEdit) return;
    saving = true;
    saveError = null;
    try {
      const updated = await updateCampaign(detail.slug, {
        name: editName.trim(),
        description: editDescription.trim(),
        starts_at: toIsoDate(editStartsAt),
        ends_at: toIsoDate(editEndsAt),
      });
      detail = { ...detail, ...updated };
      editing = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      saveError = err instanceof ApiError ? err.message : 'Could not save changes. Please try again.';
    } finally {
      saving = false;
    }
  }

  // ── Archive / unarchive ──────────────────────────────────────────────────
  let archiving = $state(false);
  async function handleArchiveToggle() {
    if (!detail) return;
    archiving = true;
    try {
      const updated = await updateCampaign(detail.slug, { archived: !detail.archived });
      detail = { ...detail, ...updated };
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      loadError = 'Could not update the campaign. Please try again.';
    } finally {
      archiving = false;
    }
  }

  // ── Assign links (#0099) ──────────────────────────────────────────────────
  let assignInput = $state('');
  let assigning = $state(false);
  let assignError = $state<string | null>(null);
  let assignNotice = $state<string | null>(null);
  const parsedKeys = $derived(parseKeysInput(assignInput));

  async function handleAssign() {
    if (!detail || parsedKeys.length === 0) return;
    const slug = detail.slug;
    assigning = true;
    assignError = null;
    assignNotice = null;
    const chunks = chunkKeys(parsedKeys);
    let succeeded = 0;
    let redirected = false;
    try {
      for (const chunk of chunks) {
        // Count success from the RESPONSE's links array, not chunk.length —
        // the server's own record of what actually landed, rather than an
        // assumption that a non-throwing request assigned every key it sent.
        const res = await assignLinksToCampaign(slug, chunk);
        succeeded += res.links.length;
      }
      assignNotice = `Assigned ${succeeded} link${succeeded === 1 ? '' : 's'}.`;
      assignInput = '';
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        redirected = true;
      } else {
        const reason = err instanceof ApiError ? err.message : 'Could not reach the server.';
        // AssignLinks (internal/handlers/campaigns.go) is documented NOT
        // atomic across keys: it stops at the first failing key, leaving
        // every key before it — INCLUDING keys earlier in the SAME request —
        // already assigned. A failure partway through a single chunk (e.g.
        // spr001, nosuchkey, spr002: spr001 lands, nosuchkey 404s, spr002 is
        // never attempted) never returns a links array for that chunk, so
        // `succeeded` here can only count fully-completed prior chunks — it
        // is a lower bound, not the true count. The unconditional reload
        // below is what makes the table correct regardless; this message is
        // just the best available explanation of what happened.
        // joinSentences (not a bare template-literal space) because `reason`
        // is the server's message verbatim and is not guaranteed to end in
        // punctuation — e.g. "link not found: nosuchkey" — which previously
        // ran straight into the next sentence with no separator at all
        // (#0103 fix 4).
        assignError =
          succeeded > 0
            ? joinSentences(
                reason,
                `${succeeded} link${succeeded === 1 ? ' was' : 's were'} assigned before the failure; the rest were not.`,
              )
            : joinSentences(
                reason,
                'Some links in this batch may have been assigned before the failure — the list below reflects the current state.',
              );
      }
    } finally {
      assigning = false;
    }
    // Refresh outside the try/finally so a redirect-to-login never triggers
    // an extra load() the destroyed view can't use. Always reload after a
    // non-401 error too (not just on success / succeeded > 0): AssignLinks'
    // non-atomic, mid-chunk failures can leave links assigned server-side
    // that `succeeded` never counted (see comment above), so only a reload
    // — never the client's own tally — can show the true state (#0103 fix
    // 2; previously gated on succeeded > 0, so this exact partial-failure
    // case reported "0 links" and never refreshed).
    if (!redirected) {
      await load(slug);
    }
  }

  // ── Unassign ─────────────────────────────────────────────────────────────
  let unassigning = $state<Record<string, boolean>>({});
  let unassignError = $state<string | null>(null);

  async function handleUnassign(key: string) {
    if (!detail) return;
    const slug = detail.slug;
    unassigning = { ...unassigning, [key]: true };
    unassignError = null;
    try {
      await unassignLinkFromCampaign(slug, key);
      await load(slug);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      unassignError = 'Could not remove this link. Please try again.';
    } finally {
      const { [key]: _removed, ...rest } = unassigning;
      unassigning = rest;
    }
  }

  onMount(() => {
    const slug = $selectedCampaignSlug;
    if (!slug) {
      loading = false;
      notFound = true;
      return;
    }
    load(slug);
  });
</script>

<div class="app-shell detail-shell">
  <header class="app-header">
    <Button onclick={goList}>&larr; Campaigns</Button>
    <h1 class="app-title">Campaign detail</h1>
  </header>

  {#if loading}
    <p class="text-muted" role="status">Loading campaign…</p>
  {:else if notFound}
    <Panel title="Campaign not found">
      <p class="text-muted">This campaign no longer exists, or you don't have access to it.</p>
      <Button variant="primary" onclick={goList}>Back to campaigns</Button>
    </Panel>
  {:else if loadError && !detail}
    <Panel>
      <p class="text-error" role="alert">{loadError}</p>
      {#if $selectedCampaignSlug}
        <Button variant="primary" onclick={() => load($selectedCampaignSlug!)}>Retry</Button>
      {/if}
    </Panel>
  {:else if detail}
    <!-- Header -->
    <Panel>
      <div class="title-row">
        <h2 class="detail-title">{detail.name}</h2>
        {#if detail.archived}<span class="badge badge-muted">Archived</span>{/if}
      </div>
      {#if detail.description}
        <p class="text-muted description">{detail.description}</p>
      {/if}

      <dl class="fields">
        <dt>Date range</dt>
        <dd>{campaignDateRangeLabel(detail)}</dd>

        <dt>Short URL slug</dt>
        <dd>
          <span class="mono">{detail.slug}</span>
          <span class="text-faint slug-note">— fixed at creation; renaming this campaign will not change it.</span>
        </dd>
      </dl>

      {#if loadError}
        <p class="text-error" role="alert">{loadError}</p>
      {/if}

      <div class="row" style="gap: var(--space-2); margin-top: var(--space-3);">
        <Button variant="default" onclick={startEdit} disabled={editing}>Edit</Button>
        <Button
          variant={detail.archived ? 'default' : 'danger'}
          disabled={archiving}
          onclick={handleArchiveToggle}
        >
          {archiving ? 'Saving…' : detail.archived ? 'Unarchive campaign' : 'Archive campaign'}
        </Button>
      </div>
    </Panel>

    {#if editing}
      <Panel title="Edit campaign">
        <form
          onsubmit={(e) => {
            e.preventDefault();
            saveEdit();
          }}
        >
          <div class="field">
            <label for="edit-campaign-name">Name</label>
            <input
              id="edit-campaign-name"
              type="text"
              bind:value={editName}
              disabled={saving}
              required
              aria-invalid={editNameTooLong}
              class:input-error={editNameTooLong}
            />
            {#if editNameTooLong}
              <p class="text-error" role="alert">Name must be at most {MAX_NAME_LENGTH} characters.</p>
            {/if}
          </div>

          <div class="field">
            <label for="edit-campaign-description">Description <span class="text-faint">(optional)</span></label>
            <input id="edit-campaign-description" type="text" bind:value={editDescription} disabled={saving} />
          </div>

          <div class="date-row">
            <div class="field">
              <label for="edit-campaign-starts">Starts <span class="text-faint">(optional)</span></label>
              <input id="edit-campaign-starts" type="date" bind:value={editStartsAt} disabled={saving} />
            </div>
            <div class="field">
              <label for="edit-campaign-ends">Ends <span class="text-faint">(optional)</span></label>
              <input
                id="edit-campaign-ends"
                type="date"
                bind:value={editEndsAt}
                disabled={saving}
                aria-invalid={editDatesInvalid}
                class:input-error={editDatesInvalid}
              />
            </div>
          </div>
          {#if editDatesInvalid}
            <p class="text-error" role="alert">End date must not be before the start date.</p>
          {/if}

          <p class="text-faint slug-edit-note">
            The URL slug (<span class="mono">{detail.slug}</span>) is fixed and will not change, even if you rename the campaign.
          </p>

          {#if saveError}
            <p class="text-error" role="alert">{saveError}</p>
          {/if}

          <div class="row" style="gap: var(--space-2);">
            <Button type="submit" variant="primary" disabled={!canSaveEdit}>
              {saving ? 'Saving…' : 'Save changes'}
            </Button>
            <Button type="button" variant="default" onclick={cancelEdit} disabled={saving}>Cancel</Button>
          </div>
        </form>
      </Panel>
    {/if}

    <!-- Summary -->
    <Panel title="Summary">
      <div class="summary-grid">
        <div class="stat">
          <span class="stat-label">Links</span>
          <span class="stat-value">{detail.link_count}</span>
          <span class="stat-sub text-faint">current membership</span>
        </div>
        <div class="stat">
          <span class="stat-label">Total clicks</span>
          <span class="stat-value">{detail.total_clicks}</span>
          <span class="stat-sub text-faint">all-time</span>
        </div>
        <div class="stat">
          <span class="stat-label">Clicks in window</span>
          <span class="stat-value">{detail.stats?.click_count ?? 0}</span>
          <span class="stat-sub text-faint">{windowLabel(detail.stats)}</span>
        </div>
        <div class="stat">
          <span class="stat-label">Clicks / day</span>
          <span class="stat-value">
            {clicksPerDayAverage(detail.stats?.click_count ?? 0, windowDayCount(detail.stats))}
          </span>
          <span class="stat-sub text-faint">average over window</span>
        </div>
        {#if (detail.stats?.excluded_bot_count ?? 0) > 0}
          <div class="stat stat-warn">
            <span class="stat-label">Bots excluded</span>
            <span class="stat-value">{detail.stats?.excluded_bot_count}</span>
            <span class="stat-sub text-faint">not counted above</span>
          </div>
        {/if}
      </div>
    </Panel>

    <!-- Chart slot — #0104 -->
    <Panel title="Clicks over time">
      <p class="text-muted">Charts land here in #0104.</p>
    </Panel>

    <!-- Links table -->
    <Panel title="Links" noPadding>
      <div class="links-toolbar row spread">
        <span class="text-muted">
          {sortedRows.length} link{sortedRows.length === 1 ? '' : 's'}
        </span>
        <div class="row toolbar-actions">
          <!--
            Visible sort control for the stacked-card layout (#0103 fix 1):
            below the breakpoint the <thead> — and the sort button that lives
            inside it — is sr-only-clipped, not display:none, so aria-sort
            keeps working for assistive tech, but a SIGHTED mobile user had
            no way to reach "sortable by clicks" (an explicit AC) at all.
            This button is hidden above the breakpoint (CSS) and drives the
            exact same `sortDirection` state as the header button — one
            source of truth, two entry points.
          -->
          <span class="mobile-sort-toggle">
            <Button
              variant="subtle"
              onclick={toggleSort}
              aria-label={`Sort by clicks, currently ${sortDirection === 'desc' ? 'highest first' : 'lowest first'}`}
            >
              Sort: Clicks {sortDirection === 'desc' ? '▼' : '▲'}
            </Button>
          </span>
          <Button variant="subtle" onclick={copyAll} disabled={sortedRows.length === 0}>
            {copiedAll ? 'Copied!' : 'Copy all short URLs'}
          </Button>
        </div>
      </div>

      {#if unassignError}
        <p class="text-error links-toolbar-error" role="alert">{unassignError}</p>
      {/if}

      {#if sortedRows.length === 0}
        <p class="text-muted empty-msg">No links assigned yet — add one below.</p>
      {:else}
        <div class="table-scroll">
          <table class="links-table">
            <thead>
              <tr>
                <th scope="col">Short key</th>
                <th scope="col">Title</th>
                <th scope="col">Destination</th>
                <th scope="col">Source</th>
                <th scope="col">Medium</th>
                <th scope="col">Placement</th>
                <th scope="col" aria-sort={sortDirection === 'desc' ? 'descending' : 'ascending'}>
                  <button type="button" class="sort-btn" onclick={toggleSort}>
                    Clicks {sortDirection === 'desc' ? '▼' : '▲'}
                  </button>
                </th>
                <th scope="col">Share of listed links</th>
                <th scope="col">Created</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {#each sortedRows as row (row.key)}
                <tr>
                  <td class="mono" data-label="Short key">
                    <button type="button" class="link-key-btn" onclick={() => openLink(row.key)}>{row.key}</button>
                  </td>
                  <td class="title-cell" data-label="Title" title={row.title}>{row.title || '—'}</td>
                  <td class="dest-cell" data-label="Destination" title={row.destination_url}>{row.destination_url}</td>
                  <td class="text-muted" data-label="Source">{row.utm_source || '—'}</td>
                  <td class="text-muted" data-label="Medium">{row.utm_medium || '—'}</td>
                  <td class="placement-cell" data-label="Placement" title={row.placement}>{row.placement || '—'}</td>
                  <td class="num" data-label="Clicks">{row.clicksInWindow}</td>
                  <td class="num" data-label="Share">{row.shareOfTotal}%</td>
                  <td class="text-muted" data-label="Created">{formatDate(row.created_at)}</td>
                  <td class="actions-cell" data-label="Actions">
                    <Button
                      variant="danger"
                      disabled={unassigning[row.key]}
                      onclick={() => handleUnassign(row.key)}
                    >
                      {unassigning[row.key] ? '…' : 'Remove'}
                    </Button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
        {#if unlistedClicks > 0}
          <p class="text-faint unlisted-note">
            {unlistedClicks} click{unlistedClicks === 1 ? '' : 's'} in this window came from links no longer
            assigned to this campaign and are not listed above.
          </p>
        {/if}
      {/if}
    </Panel>

    <!-- Assign -->
    <Panel title="Assign links">
      <div class="field">
        <label for="assign-keys">Link keys or short URLs</label>
        <textarea
          id="assign-keys"
          rows="3"
          placeholder={'abc123, https://go.sstools.co/u/def456'}
          bind:value={assignInput}
          disabled={assigning}
        ></textarea>
        <p class="text-faint">
          Separate multiple with commas, spaces, or new lines. Sent in batches of up to 50.
        </p>
      </div>

      {#if assignError}
        <p class="text-error" role="alert">{assignError}</p>
      {/if}
      {#if assignNotice}
        <p class="text-notice" role="status">{assignNotice}</p>
      {/if}

      <Button variant="primary" onclick={handleAssign} disabled={assigning || parsedKeys.length === 0}>
        {assigning
          ? 'Assigning…'
          : parsedKeys.length > 0
            ? `Assign ${parsedKeys.length} link${parsedKeys.length === 1 ? '' : 's'}`
            : 'Assign links'}
      </Button>
    </Panel>
  {/if}
</div>

<style>
  .detail-shell {
    max-width: 960px;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-2);
    flex-wrap: wrap;
  }
  .detail-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0;
  }
  .description {
    margin: 0 0 var(--space-3);
  }
  .fields {
    display: grid;
    grid-template-columns: 9rem 1fr;
    gap: var(--space-2) var(--space-4);
    margin: 0 0 var(--space-2);
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
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
  }
  .slug-note,
  .slug-edit-note {
    display: block;
    font-size: var(--fs-sm);
    margin-top: var(--space-1);
  }
  .input-error {
    border-color: var(--danger) !important;
  }
  .date-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }

  /* ── Summary stat grid ──────────────────────────────────────────────── */
  .summary-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
    gap: var(--space-4);
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .stat-label {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .stat-value {
    font-size: var(--fs-xl);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .stat-sub {
    font-size: var(--fs-sm);
  }
  .stat-warn .stat-value {
    color: var(--warning);
  }

  /* ── Links table ────────────────────────────────────────────────────── */
  .links-toolbar {
    padding: var(--space-3) var(--space-4);
    border-bottom: var(--border-w) solid var(--border);
  }
  .links-toolbar-error {
    padding: 0 var(--space-4) var(--space-3);
  }
  .toolbar-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  /*
   * Hidden above the stacked-card breakpoint — the header's own sort button
   * (inside <thead>) is visible and sufficient on desktop. Shown below it in
   * the @media (max-width: 900px) block further down, alongside the switch
   * to the stacked-card layout (#0103 fix 1).
   */
  .mobile-sort-toggle {
    display: none;
  }
  .empty-msg {
    padding: var(--space-4);
  }
  .sort-btn {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    text-transform: inherit;
    letter-spacing: inherit;
    color: inherit;
    cursor: pointer;
  }
  .link-key-btn {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    font-family: var(--font-mono);
    color: var(--accent);
    cursor: pointer;
    text-decoration: underline;
  }
  /*
   * Column-priority decision (#0103 fix 6): the links table is the widest
   * thing in the app (10 columns) and, unconstrained, ran to ~1100px —
   * wider than even the desktop .table-scroll container (926px), so
   * Created/Remove were cut off at EVERY width, not just narrow ones.
   * Title/Destination/Placement are the three free-text columns with real
   * room to give back, so they take the squeeze here; Short key, Clicks,
   * and Share are the numbers/identifier the page exists to show and are
   * left unconstrained. Placement gets its own (tighter) class rather than
   * reusing .dest-cell — the two were accidentally sharing one 200px budget,
   * which was most of the desktop overflow on its own.
   */
  .title-cell {
    max-width: 120px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dest-cell {
    max-width: 150px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .placement-cell {
    max-width: 90px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .actions-cell {
    text-align: right;
    white-space: nowrap;
  }
  .unlisted-note {
    padding: 0 var(--space-4) var(--space-3);
    font-size: var(--fs-sm);
  }

  @media (max-width: 480px) {
    .fields {
      grid-template-columns: 1fr;
      gap: var(--space-1);
    }
    .fields dt {
      margin-top: var(--space-2);
    }
    .fields dt:first-child {
      margin-top: 0;
    }
    .date-row {
      grid-template-columns: 1fr;
    }
    .summary-grid {
      grid-template-columns: repeat(2, 1fr);
    }
  }

  /*
   * Stacked-card breakpoint for the links table (#0103 fix 2): originally
   * 480px, but a real-browser measurement at 481px found
   * documentElement.scrollWidth 855 against clientWidth 481 (and
   * .table-scroll 924 against 447) — the ten-column table still forced
   * horizontal panning across a wide 481px–~1000px band even though desktop
   * (above that band) and the old sub-480px stacked layout were both clean.
   * Raised to 900px so nothing between "phone" and "narrow laptop" pans.
   * This is a SEPARATE @media block from the 480px one above — fields/
   * date-row/summary-grid don't overflow and stay stacking at the original
   * 480px; only the table layout moves.
   *
   * Because more of the viewport range now uses the stacked layout, the
   * visible .mobile-sort-toggle in .links-toolbar (see above) matters more,
   * not less — it is the ONLY sighted way to reach "sortable by clicks"
   * anywhere in this wider band, since the <thead> (and the sort button
   * living inside it) is still sr-only-clipped here, not display:none.
   */
  @media (max-width: 900px) {
    .mobile-sort-toggle {
      display: inline-flex;
    }

    /*
     * Ten columns can't fit any priority scheme below this breakpoint
     * without either hiding data the AC requires (Clicks/Share/Remove all
     * reachable) or forcing a horizontal scroll with no affordance under
     * macOS overlay scrollbars (the reviewer's original complaint). Instead
     * of picking which columns to drop, the table becomes a stacked card
     * per link: every field stays present, in the SAME priority order as
     * the desktop columns (short key/title/destination first, clicks/
     * share/created next, actions last), just read top-to-bottom instead
     * of left-to-right. This guarantees Clicks, Share, and Remove are
     * always reachable with page (vertical) scrolling only — never
     * horizontal.
     */
    .table-scroll {
      overflow-x: visible;
    }
    .links-table thead {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
    }
    .links-table,
    .links-table tbody,
    .links-table tr {
      display: block;
      width: 100%;
    }
    .links-table tbody tr {
      border: var(--border-w) solid var(--border);
      border-radius: var(--radius);
      margin: var(--space-3);
      padding: var(--space-1) 0;
    }
    .links-table tbody tr:last-child {
      margin-bottom: var(--space-3);
    }
    .links-table td {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: var(--space-3);
      padding: var(--space-2) var(--space-3);
      border-bottom: none;
      max-width: none;
      overflow-wrap: anywhere;
      white-space: normal;
      text-align: right;
    }
    .links-table td::before {
      content: attr(data-label);
      flex: 0 0 auto;
      font-weight: 600;
      font-size: var(--fs-sm);
      color: var(--text-muted);
      text-align: left;
    }
    .links-table .actions-cell {
      justify-content: flex-end;
    }
    .links-table .actions-cell::before {
      content: none;
    }
  }
</style>
