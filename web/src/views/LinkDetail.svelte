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

  An Edit action (#0099) opens a form that REPOPULATES the UTM builder from the
  link's discrete utm_*/placement columns via utmParamsFromLink (lib/utm.ts) —
  closing the #0048 limitation this view used to be unable to offer at all. A
  link created before that migration has all-NULL columns, which utmParamsFromLink
  maps to empty fields rather than erroring. Saving PATCHes destination_url (the
  builder's re-composed URL) together with the five discrete fields and
  placement, so the two stay in lockstep the same way create does.

  QR code (#0106): SVG/PNG download links sit right next to the Short URL
  row, since that's the exact value the code encodes — see docs/campaigns.md
  for the library/error-correction/DPI decisions and internal/qr's package
  doc comment for why it's the short URL, never destination_url.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, links, selectedLinkKey } from '../lib/stores';
  import { getLink, updateLink, deactivateLink, ApiError } from '../lib/api';
  import { shortUrl, qrSvgUrl, qrPngUrl, linkStatus, isValidHttpUrl } from '../lib/links';
  import {
    isEmptyStats,
    utmDimensions,
    statusLabel,
    formatDate,
  } from '../lib/linkDetail';
  import { emptyUtmParams, composeUtmUrl, isUtmEmpty, utmParamsFromLink } from '../lib/utm';
  import type { UtmParams } from '../lib/utm';
  import type { LinkDetail } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import ClicksChart from '../lib/ClicksChart.svelte';
  import UTMBarChart from '../lib/UTMBarChart.svelte';
  import UtmField from '../lib/UtmField.svelte';
  import UtmConventions from '../lib/UtmConventions.svelte';

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

  // ── Edit mode (#0099) ────────────────────────────────────────────────────────
  let editing = $state(false);
  let editTitle = $state('');
  let editDestinationUrl = $state('');
  let editUtmParams = $state<UtmParams>(emptyUtmParams());
  // editUtmParamsAtStart is a SNAPSHOT of editUtmParams taken once in
  // startEdit and never touched again — composeUtmUrl's `previous` argument
  // (review fix: an earlier version deleted any blank UTM key already baked
  // into editDestinationUrl, unconditionally. For a pre-migration link
  // (all-NULL columns), editUtmParams starts as emptyUtmParams(), so a
  // TITLE-ONLY save — the UTM builder never touched — would strip any
  // ?utm_source=... the link's destination_url already carried from an
  // external source. Passing this snapshot instead of editUtmParams itself
  // means composeUtmUrl only deletes a key the BUILDER demonstrably owned at
  // the start of this edit session, never one it never saw).
  let editUtmParamsAtStart = $state<UtmParams>(emptyUtmParams());
  let editUtmOpen = $state(false);
  let editPlacement = $state('');
  let saving = $state(false);
  let saveError = $state<string | null>(null);

  const editComposedUrl = $derived(composeUtmUrl(editDestinationUrl, editUtmParams, editUtmParamsAtStart));
  const editUrlInvalid = $derived(
    editDestinationUrl.trim() !== '' && !isValidHttpUrl(editDestinationUrl),
  );
  const canSaveEdit = $derived(!saving && editDestinationUrl.trim() !== '' && !editUrlInvalid);

  // Repopulate every field from the CURRENT detail — including the UTM builder
  // from utmParamsFromLink, the point of #0099's edit-repopulation fix.
  function startEdit() {
    if (!detail) return;
    editTitle = detail.title;
    editDestinationUrl = detail.destination_url;
    editUtmParams = utmParamsFromLink(detail);
    // A second, independent call (utmParamsFromLink returns a fresh object
    // every time) so later edits to editUtmParams can never alias this
    // snapshot.
    editUtmParamsAtStart = utmParamsFromLink(detail);
    editPlacement = detail.placement;
    editUtmOpen = !isUtmEmpty(editUtmParams);
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
      const updated = await updateLink(detail.key, {
        title: editTitle.trim(),
        destination_url: editComposedUrl || editDestinationUrl.trim(),
        utm_source: editUtmParams.utm_source.trim(),
        utm_medium: editUtmParams.utm_medium.trim(),
        utm_campaign: editUtmParams.utm_campaign.trim(),
        utm_term: editUtmParams.utm_term.trim(),
        utm_content: editUtmParams.utm_content.trim(),
        placement: editPlacement.trim(),
      });
      // Merge rather than replace: `updated` (a plain Link) carries no
      // utm_stats/timeseries/campaign_name/campaign_slug, which detail's
      // extra LinkDetail fields still hold and PATCH cannot have changed.
      detail = { ...detail, ...updated };
      links.update((cur) => cur.map((l) => (l.key === updated.key ? { ...l, ...updated } : l)));
      editing = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      saveError = 'Could not save changes. Please try again.';
    } finally {
      saving = false;
    }
  }

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

        <dt>QR code</dt>
        <dd>
          <div class="row">
            <a class="qr-link" href={qrSvgUrl(detail.key)} download>Download SVG</a>
            <a class="qr-link" href={qrPngUrl(detail.key)} download>Download PNG</a>
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

        <dt>Campaign</dt>
        <dd>{detail.campaign_name || 'None'}</dd>

        <dt>Placement</dt>
        <dd>{detail.placement || '—'}</dd>
      </dl>

      {#if loadError}
        <p class="text-error" role="alert">{loadError}</p>
      {/if}

      <div class="row" style="margin-top: var(--space-3); gap: var(--space-2);">
        <Button variant="default" onclick={startEdit} disabled={editing}>Edit</Button>
        {#if canDeactivate}
          <Button
            variant="danger"
            disabled={deactivating}
            onclick={handleDeactivate}
          >
            {deactivating ? 'Deactivating…' : 'Deactivate link'}
          </Button>
        {/if}
      </div>
    </Panel>

    {#if editing}
      <Panel title="Edit link">
        <form
          onsubmit={(e) => {
            e.preventDefault();
            saveEdit();
          }}
        >
          <div class="field">
            <label for="edit-title">Title</label>
            <input id="edit-title" type="text" bind:value={editTitle} disabled={saving} />
          </div>

          <div class="field">
            <label for="edit-dest-url">Destination URL</label>
            <input
              id="edit-dest-url"
              type="url"
              inputmode="url"
              bind:value={editDestinationUrl}
              disabled={saving}
              required
              aria-invalid={editUrlInvalid}
              class:input-error={editUrlInvalid}
            />
            {#if editUrlInvalid}
              <p class="text-warn" role="status">Enter a valid http(s) URL.</p>
            {/if}
          </div>

          <div class="utm-section">
            <button
              type="button"
              class="utm-toggle"
              aria-expanded={editUtmOpen}
              onclick={() => { editUtmOpen = !editUtmOpen; }}
            >
              <span class="utm-toggle-chevron" class:open={editUtmOpen}>▶</span>
              Campaign / UTM parameters
            </button>

            {#if editUtmOpen}
              <div class="utm-fields">
                <UtmConventions />

                {#if editDestinationUrl.trim() !== ''}
                  <div class="utm-preview">
                    <p class="utm-preview-label">Destination preview</p>
                    <p class="utm-preview-url" title={editComposedUrl}>{editComposedUrl}</p>
                  </div>
                {/if}

                <UtmField fieldKey="utm_source" id="edit-utm-source" bind:value={editUtmParams.utm_source} disabled={saving} />
                <UtmField fieldKey="utm_medium" id="edit-utm-medium" bind:value={editUtmParams.utm_medium} disabled={saving} />
                <UtmField fieldKey="utm_campaign" id="edit-utm-campaign" bind:value={editUtmParams.utm_campaign} disabled={saving} />
                <UtmField fieldKey="utm_term" id="edit-utm-term" bind:value={editUtmParams.utm_term} disabled={saving} />
                <UtmField fieldKey="utm_content" id="edit-utm-content" bind:value={editUtmParams.utm_content} disabled={saving} />
                <div class="field">
                  <label for="edit-placement">Placement <span class="text-faint">(optional)</span></label>
                  <input id="edit-placement" type="text" bind:value={editPlacement} disabled={saving} />
                </div>
              </div>
            {/if}
          </div>

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
    flex-wrap: wrap;
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
  .qr-link {
    color: var(--accent);
    text-decoration: underline;
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

  @media (max-width: 480px) {
    /* Stack the dt/dd pairs vertically so labels don't compete with values */
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
    /* UTM grid: already auto-fit minmax(14rem) — 1 col at 375px; no change needed */
  }

  /* ── Edit form (#0099) — mirrors Dashboard.svelte's UTM builder styling ──── */
  .input-error {
    border-color: var(--danger) !important;
  }
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
