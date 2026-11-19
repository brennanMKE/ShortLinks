<!--
  Campaigns list view (#0103, redesigned in #0166 per docs/ui-redesign.md 3.5).

  The list is the page: a DataTable ranked by recent activity
  (GET /api/campaigns?sort=activity, #0146) with columns Campaign (name and a
  status tag, then the description or "Ended 10 days ago"), Dates, Links,
  30 days (InlineBar, all-time total as a second line) and Trend (Sparkline of
  the API's 30 daily counts). "+ New campaign" opens a <dialog> (name and
  dates; description and default UTM parameters under "More"). "Show archived
  (N)" is a toggle. Below 900px DataTable turns rows into cards.

  default_utm_campaign is PINNED at creation (#0098: it never changes even if
  the campaign is renamed later), so the dialog is the only place a campaign
  can get non-slug UTM defaults (#0103 nit 11).

  A campaign opens /campaigns/{slug} (#0120).
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { currentUser } from '../lib/stores';
  import { listCampaigns, createCampaign, ApiError, type CreateCampaignInput } from '../lib/api';
  import {
    visibleCampaigns,
    toIsoDate,
    compactDateRange,
    campaignTimingLine,
    campaignSubline,
    campaignStatusLabel,
    sparklineDays,
  } from '../lib/campaigns';
  import type { CampaignWithCounts } from '../lib/types';
  import type { Column } from '../lib/dataTable';
  import { navigate } from '../lib/hashRouter.svelte';
  import { getChartTimeZone } from '../lib/preferences';
  import Button from '../lib/Button.svelte';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import InlineBar from '../lib/InlineBar.svelte';
  import Sparkline from '../lib/Sparkline.svelte';
  import UtmField from '../lib/UtmField.svelte';

  const MAX_NAME_LENGTH = 255; // mirrors maxCampaignNameLength (internal/handlers/campaigns.go), counted in code points to match utf8.RuneCountInString.

  // ── List state ─────────────────────────────────────────────────────────
  let campaigns = $state<CampaignWithCounts[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let showArchived = $state(false);

  const visible = $derived(visibleCampaigns(campaigns, showArchived));
  const archivedCount = $derived(campaigns.filter((c) => c.archived).length);
  const maxClicks = $derived(Math.max(0, ...visible.map((c) => c.clicks_30d)));
  const now = new Date();

  const columns: Column<CampaignWithCounts>[] = [
    { key: 'name', label: 'Campaign', mobile: 'title' },
    { key: 'dates', label: 'Dates', width: '150px', mobile: 'meta' },
    { key: 'links', label: 'Links', width: '70px', align: 'right' },
    { key: 'clicks', label: '30 days', width: '160px', align: 'right', mobile: 'metric' },
    { key: 'trend', label: 'Trend', width: '150px' },
  ];

  async function load() {
    loading = true;
    loadError = null;
    try {
      const res = await listCampaigns('activity', getChartTimeZone());
      campaigns = res.campaigns;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      loadError = 'Could not load your campaigns. Please try again.';
    } finally {
      loading = false;
    }
  }

  // ── Create dialog ──────────────────────────────────────────────────────
  let dialogEl: HTMLDialogElement | undefined = $state();
  let nameInput: HTMLInputElement | undefined = $state();
  let newName = $state('');
  let newDescription = $state('');
  let newStartsAt = $state('');
  let newEndsAt = $state('');
  let creating = $state(false);
  let createError = $state<string | null>(null);
  let moreOpen = $state(false);
  // The five default_utm_* fields — the API accepts them on POST /api/campaigns.
  let defaultUtm = $state({
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
  });
  const hasMore = $derived(
    newDescription.trim() !== '' || Object.values(defaultUtm).some((v) => v.trim() !== ''),
  );

  const nameLength = $derived([...newName.trim()].length);
  const nameTooLong = $derived(nameLength > MAX_NAME_LENGTH);
  const datesInvalid = $derived(
    newStartsAt !== '' && newEndsAt !== '' && newEndsAt < newStartsAt,
  );
  const canCreate = $derived(!creating && newName.trim() !== '' && !nameTooLong && !datesInvalid);

  async function openDialog() {
    createError = null;
    dialogEl?.showModal();
    await tick();
    nameInput?.focus();
  }
  function closeDialog() {
    dialogEl?.close();
  }

  async function handleCreate() {
    if (!canCreate) return;
    creating = true;
    createError = null;
    try {
      const input: CreateCampaignInput = { name: newName.trim() };
      const d = newDescription.trim();
      if (d !== '') input.description = d;
      const starts = toIsoDate(newStartsAt);
      if (starts) input.starts_at = starts;
      const ends = toIsoDate(newEndsAt);
      if (ends) input.ends_at = ends;
      const src = defaultUtm.utm_source.trim();
      if (src !== '') input.default_utm_source = src;
      const med = defaultUtm.utm_medium.trim();
      if (med !== '') input.default_utm_medium = med;
      const camp = defaultUtm.utm_campaign.trim();
      if (camp !== '') input.default_utm_campaign = camp;
      const term = defaultUtm.utm_term.trim();
      if (term !== '') input.default_utm_term = term;
      const content = defaultUtm.utm_content.trim();
      if (content !== '') input.default_utm_content = content;

      const created = await createCampaign(input);
      newName = '';
      newDescription = '';
      newStartsAt = '';
      newEndsAt = '';
      defaultUtm = { utm_source: '', utm_medium: '', utm_campaign: '', utm_term: '', utm_content: '' };
      moreOpen = false;
      closeDialog();
      // A new campaign has no clicks and would sort last by activity, out of
      // sight; open it instead.
      navigate('/campaigns/' + encodeURIComponent(created.slug));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      createError =
        err instanceof ApiError ? err.message : 'Could not create the campaign. Please try again.';
    } finally {
      creating = false;
    }
  }

  onMount(load);
</script>

<div class="app-shell">
  <div class="page-head">
    <h1 class="page-title">Campaigns</h1>
    <div class="page-actions">
      {#if archivedCount > 0}
        <button
          type="button"
          class="toggle-btn"
          class:on={showArchived}
          onclick={() => (showArchived = !showArchived)}
        >
          {showArchived ? 'Hide' : 'Show'} archived ({archivedCount})
        </button>
      {/if}
      <Button variant="primary" onclick={openDialog}>+ New campaign</Button>
    </div>
  </div>

  {#if loadError}
    <div class="error-box">
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="subtle" onclick={load}>Retry</Button>
    </div>
  {:else}
    <div class="table-panel">
      <DataTable
        {columns}
        rows={visible}
        rowKey={(c) => String(c.id)}
        caption="Campaigns, most recently active first"
        {loading}
        rowClass={(c) => (c.archived ? 'row-archived' : '')}
      >
        {#snippet cell(c, col)}
          {#if col.key === 'name'}
            <TwoLineCell
              title={c.name}
              meta={campaignSubline(c, now)}
              href={'#/campaigns/' + encodeURIComponent(c.slug)}
              tag={campaignStatusLabel(c.status)}
              dim={c.status === 'archived'}
            />
          {:else if col.key === 'dates'}
            <div class="two">
              <span class="l1">{compactDateRange(c, now)}</span>
              {#if campaignTimingLine(c, now)}<span class="l2">{campaignTimingLine(c, now)}</span>{/if}
            </div>
          {:else if col.key === 'links'}
            <span class="num">{c.link_count.toLocaleString('en-US')}</span>
          {:else if col.key === 'clicks'}
            <div class="two">
              <InlineBar value={c.clicks_30d} max={maxClicks} />
              <span class="l2 r">{c.total_clicks.toLocaleString('en-US')} total</span>
            </div>
          {:else if col.key === 'trend'}
            <Sparkline days={sparklineDays(c.sparkline, now)} partialLast label="Last 30 days" />
          {/if}
        {/snippet}
        {#snippet empty()}
          {#if campaigns.length === 0}
            <p class="text-muted">No campaigns yet. Create your first one with + New campaign.</p>
          {:else}
            <p class="text-muted">No campaigns to show. Try Show archived.</p>
          {/if}
        {/snippet}
      </DataTable>
    </div>
  {/if}
</div>

<dialog
  bind:this={dialogEl}
  class="new-dialog"
  aria-labelledby="new-campaign-title"
  onclose={() => (createError = null)}
  oncancel={(e) => {
    if (creating) e.preventDefault();
  }}
  onclick={(e) => {
    if (e.target === dialogEl && !creating) closeDialog();
  }}
>
  <form
    class="dialog-form"
    onsubmit={(e) => {
      e.preventDefault();
      handleCreate();
    }}
  >
    <h2 id="new-campaign-title" class="dialog-title">New campaign</h2>

    <div class="field">
      <label for="campaign-name">Name</label>
      <input
        id="campaign-name"
        type="text"
        placeholder="Summer fair"
        bind:this={nameInput}
        bind:value={newName}
        disabled={creating}
        required
        aria-invalid={nameTooLong}
        class:input-error={nameTooLong}
      />
      {#if nameTooLong}
        <p class="text-error" role="alert">Name must be at most {MAX_NAME_LENGTH} characters.</p>
      {/if}
    </div>

    <div class="date-row">
      <div class="field">
        <label for="campaign-starts">Starts <span class="text-faint">(optional)</span></label>
        <input id="campaign-starts" type="date" bind:value={newStartsAt} disabled={creating} />
      </div>
      <div class="field">
        <label for="campaign-ends">Ends <span class="text-faint">(optional)</span></label>
        <input
          id="campaign-ends"
          type="date"
          bind:value={newEndsAt}
          disabled={creating}
          aria-invalid={datesInvalid}
          class:input-error={datesInvalid}
        />
      </div>
    </div>
    {#if datesInvalid}
      <p class="text-error" role="alert">End date must not be before the start date.</p>
    {/if}

    <div class="more-section">
      <button type="button" class="more-toggle" aria-expanded={moreOpen} onclick={() => (moreOpen = !moreOpen)}>
        <span class="more-chevron" class:open={moreOpen} aria-hidden="true">▶</span>
        More
        {#if hasMore && !moreOpen}<span class="badge">filled</span>{/if}
      </button>
      {#if moreOpen}
        <div class="more-fields">
          <div class="field">
            <label for="campaign-description">Description <span class="text-faint">(optional)</span></label>
            <input
              id="campaign-description"
              type="text"
              placeholder="What this campaign promotes"
              bind:value={newDescription}
              disabled={creating}
            />
          </div>
          <p class="text-faint more-intro">
            Default UTM parameters prefill the UTM builder whenever this campaign is selected on a link; every
            value stays editable there. <span class="mono">default_utm_campaign</span> is fixed at creation and will
            not change even if this campaign is renamed later.
          </p>
          <UtmField fieldKey="utm_source" id="campaign-default-utm-source" bind:value={defaultUtm.utm_source} disabled={creating} />
          <UtmField fieldKey="utm_medium" id="campaign-default-utm-medium" bind:value={defaultUtm.utm_medium} disabled={creating} />
          <UtmField fieldKey="utm_campaign" id="campaign-default-utm-campaign" bind:value={defaultUtm.utm_campaign} disabled={creating} />
          <UtmField fieldKey="utm_term" id="campaign-default-utm-term" bind:value={defaultUtm.utm_term} disabled={creating} />
          <UtmField fieldKey="utm_content" id="campaign-default-utm-content" bind:value={defaultUtm.utm_content} disabled={creating} />
        </div>
      {/if}
    </div>

    {#if createError}
      <p class="text-error" role="alert">{createError}</p>
    {/if}

    <div class="dialog-actions">
      <Button type="submit" variant="primary" disabled={!canCreate}>
        {creating ? 'Creating…' : 'Create campaign'}
      </Button>
      <Button disabled={creating} onclick={closeDialog}>Cancel</Button>
    </div>
  </form>
</dialog>

<style>
  .page-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    flex-wrap: wrap;
    margin-bottom: var(--space-4);
  }
  .page-actions {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  .toggle-btn {
    background: none;
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-1) var(--space-3);
    font: inherit;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    cursor: pointer;
    min-height: 32px;
  }
  .toggle-btn:hover {
    color: var(--text);
    background: var(--bg-subtle);
  }
  .toggle-btn.on {
    background: var(--accent-subtle);
    color: var(--text);
  }
  .table-panel {
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .error-box {
    padding: var(--space-4);
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }

  /* Two-line cells for Dates and 30 days (TwoLineCell covers the first column). */
  .two {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .l1 {
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .l2 {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .l2.r {
    text-align: right;
    padding-right: var(--space-2);
    font-variant-numeric: tabular-nums;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  :global(tr.row-archived) .l1 {
    color: var(--text-muted);
  }

  /* ── New campaign dialog ── */
  .new-dialog {
    width: min(480px, calc(100vw - 2 * var(--space-4)));
    max-height: calc(100dvh - 2 * var(--space-4));
    overflow-y: auto;
    /* Padding lives on .dialog-form, not here: a click in the dialog's own
       padding targets the <dialog> and would count as a backdrop click. */
    padding: 0;
    background: var(--bg-panel);
    color: var(--text);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .new-dialog::backdrop {
    background: rgba(0, 0, 0, 0.4);
  }
  .dialog-form {
    padding: var(--space-5);
  }
  .dialog-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0 0 var(--space-4);
  }
  .dialog-actions {
    display: flex;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }
  .date-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  @media (max-width: 480px) {
    .date-row {
      grid-template-columns: 1fr;
    }
  }
  .input-error {
    border-color: var(--danger) !important;
  }

  .more-section {
    margin-bottom: var(--space-3);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .more-toggle {
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
  .more-toggle:hover {
    background: var(--bg-header);
    color: var(--text);
  }
  .more-chevron {
    font-size: 9px;
    display: inline-block;
    transition: transform 0.15s ease;
    color: var(--text-faint);
  }
  .more-chevron.open {
    transform: rotate(90deg);
  }
  .more-fields {
    padding: var(--space-3) var(--space-3) var(--space-2);
    border-top: var(--border-w) solid var(--border);
  }
  .more-intro {
    margin: 0 0 var(--space-3);
    font-size: var(--fs-sm);
  }
  .mono {
    font-family: var(--font-mono);
  }
</style>
