<!--
  Campaigns list view (#0103). Two halves, mirroring Dashboard.svelte's shape:

  1. A create-campaign FORM (name required — mirrors the backend's 255-rune
     cap, internal/handlers/campaigns.go maxCampaignNameLength — optional
     description and start/end dates). On submit it POSTs to /api/campaigns
     and prepends the new campaign to the list. There is no other UI path to
     create a campaign anywhere in the app (#0098/#0099/#0102 shipped API +
     the create-form's "assign to existing campaign" dropdown only), so this
     form is what makes the rest of the feature reachable.

  2. A LIST of the user's campaigns: name, date range, link count, total
     clicks (ALL-TIME — labelled as such, since the detail view also shows a
     WINDOWED number and the two must stay visually distinct per #0102's
     "four numbers, two time scales" constraint), and a sparkline slot left
     empty for #0104. Archived campaigns are filtered out by default (AC) and
     reachable via an explicit "Show archived" checkbox — nothing is ever
     deleted from this list, so the checkbox is the only thing that changes.

  Clicking a row opens the campaign detail view via selectedCampaignSlug +
  currentView, mirroring Dashboard's openDetail(key).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, selectedCampaignSlug } from '../lib/stores';
  import { listCampaigns, createCampaign, logout, ApiError, type CreateCampaignInput } from '../lib/api';
  import { visibleCampaigns, campaignDateRangeLabel, toIsoDate } from '../lib/campaigns';
  import type { CampaignWithCounts } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import UtmField from '../lib/UtmField.svelte';
  import { APP_NAME } from '../lib/branding';

  const MAX_NAME_LENGTH = 255; // mirrors maxCampaignNameLength (internal/handlers/campaigns.go), counted in code points to match utf8.RuneCountInString.

  // ── List state ─────────────────────────────────────────────────────────
  let campaigns = $state<CampaignWithCounts[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let showArchived = $state(false);

  const visible = $derived(visibleCampaigns(campaigns, showArchived));
  const archivedCount = $derived(campaigns.filter((c) => c.archived).length);

  async function load() {
    loading = true;
    loadError = null;
    try {
      const res = await listCampaigns();
      campaigns = res.campaigns;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      loadError = 'Could not load your campaigns. Please try again.';
    } finally {
      loading = false;
    }
  }

  // ── Create form ────────────────────────────────────────────────────────
  let newName = $state('');
  let newDescription = $state('');
  let newStartsAt = $state('');
  let newEndsAt = $state('');
  let creating = $state(false);
  let createError = $state<string | null>(null);
  let utmOpen = $state(false);
  // The five default_utm_* fields — the API accepts them on POST /api/campaigns
  // and default_utm_campaign is PINNED at creation (#0098: it never changes
  // even if the campaign is renamed later), so this create form is the ONLY
  // place a campaign can ever get non-slug UTM defaults from any UI (#0103
  // nit 11 — this was previously omitted, meaning every campaign created here
  // fell back to the slug-derived default_utm_campaign forever).
  let defaultUtm = $state({
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
  });
  const hasDefaultUtm = $derived(Object.values(defaultUtm).some((v) => v.trim() !== ''));

  const nameLength = $derived([...newName.trim()].length);
  const nameTooLong = $derived(nameLength > MAX_NAME_LENGTH);
  const datesInvalid = $derived(
    newStartsAt !== '' && newEndsAt !== '' && newEndsAt < newStartsAt,
  );
  const canCreate = $derived(!creating && newName.trim() !== '' && !nameTooLong && !datesInvalid);

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
      campaigns = [{ ...created, link_count: 0, total_clicks: 0 }, ...campaigns];
      newName = '';
      newDescription = '';
      newStartsAt = '';
      newEndsAt = '';
      defaultUtm = { utm_source: '', utm_medium: '', utm_campaign: '', utm_term: '', utm_content: '' };
      utmOpen = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      createError =
        err instanceof ApiError ? err.message : 'Could not create the campaign. Please try again.';
    } finally {
      creating = false;
    }
  }

  // ── Navigation ─────────────────────────────────────────────────────────
  function openDetail(slug: string) {
    selectedCampaignSlug.set(slug);
    currentView.set('campaign-detail');
  }

  function go(view: 'dashboard' | 'account' | 'admin') {
    currentView.set(view);
  }

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // Drop local state and return to login even on failure.
    }
    currentUser.set(null);
    currentView.set('login');
  }

  onMount(load);
</script>

<div class="app-shell">
  <header class="app-header">
    <h1 class="app-title">{APP_NAME}</h1>
    <nav class="nav-tabs" aria-label="Primary">
      <button type="button" class="nav-tab" onclick={() => go('dashboard')}>Dashboard</button>
      <button type="button" class="nav-tab active" aria-current="page">Campaigns</button>
      <button type="button" class="nav-tab" onclick={() => go('account')}>Account</button>
      {#if $currentUser?.is_admin}
        <button type="button" class="nav-tab" onclick={() => go('admin')}>Admin</button>
      {/if}
    </nav>
    <Button variant="default" onclick={handleSignOut}>Sign out</Button>
  </header>

  <Panel title="Create a campaign">
    <form
      onsubmit={(e) => {
        e.preventDefault();
        handleCreate();
      }}
    >
      <div class="field">
        <label for="campaign-name">Name</label>
        <input
          id="campaign-name"
          type="text"
          placeholder="Summer fair"
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

      <div class="utm-section">
        <button type="button" class="utm-toggle" aria-expanded={utmOpen} onclick={() => (utmOpen = !utmOpen)}>
          <span class="utm-toggle-chevron" class:open={utmOpen}>▶</span>
          Default UTM parameters
          {#if hasDefaultUtm && !utmOpen}<span class="badge">filled</span>{/if}
        </button>
        {#if utmOpen}
          <div class="utm-fields">
            <p class="text-faint utm-intro">
              Prefills the UTM builder whenever this campaign is selected on a link — every value stays
              editable there, nothing here locks a field. <span class="mono">default_utm_campaign</span> is fixed
              at creation and will not change even if this campaign is renamed later.
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

      <Button type="submit" variant="primary" disabled={!canCreate}>
        {creating ? 'Creating…' : 'Create campaign'}
      </Button>
    </form>
  </Panel>

  <Panel title="Your campaigns" noPadding={!loading && !loadError}>
    {#if loading}
      <p class="text-muted">Loading your campaigns…</p>
    {:else if loadError}
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="subtle" onclick={load}>Retry</Button>
    {:else}
      {#if archivedCount > 0}
        <div class="archive-toggle">
          <label class="row">
            <input type="checkbox" bind:checked={showArchived} />
            Show archived ({archivedCount})
          </label>
        </div>
      {/if}

      {#if visible.length === 0}
        <p class="text-muted empty-msg">
          {campaigns.length === 0
            ? 'No campaigns yet — create your first one above.'
            : 'No campaigns to show — try "Show archived".'}
        </p>
      {:else}
        <div class="table-scroll">
          <table>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Date range</th>
                <th scope="col">Links</th>
                <th scope="col">Total clicks <span class="text-faint">(all-time)</span></th>
                <th scope="col">Trend</th>
              </tr>
            </thead>
            <tbody>
              {#each visible as c (c.id)}
                <tr>
                  <td>
                    <button type="button" class="row-link campaign-name" onclick={() => openDetail(c.slug)}>
                      {c.name}
                    </button>
                    {#if c.archived}<span class="badge badge-muted">Archived</span>{/if}
                  </td>
                  <td class="text-muted">{campaignDateRangeLabel(c)}</td>
                  <td class="num">{c.link_count}</td>
                  <td class="num">{c.total_clicks}</td>
                  <td class="text-faint sparkline-slot"><!-- Sparkline: #0104 --> —</td>
                </tr>
              {/each}
            </tbody>
          </table>
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
    .nav-tabs {
      order: 3;
      flex: 0 0 100%;
      padding: 0;
      flex-wrap: wrap;
    }
    .nav-tab {
      font-size: var(--fs-base);
      padding: var(--space-1) var(--space-3);
    }
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

  /* ── Default UTM parameters (#0103 nit 11) — mirrors Dashboard.svelte's
     UTM builder toggle so the two collapsible sections look/behave the same. */
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
  .utm-intro {
    margin: 0 0 var(--space-3);
    font-size: var(--fs-sm);
  }
  .mono {
    font-family: var(--font-mono);
  }

  .archive-toggle {
    padding: var(--space-3) var(--space-4);
    border-bottom: var(--border-w) solid var(--border);
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .archive-toggle input {
    width: auto;
  }

  .empty-msg {
    padding: var(--space-4);
  }

  /*
   * The row itself is no longer the click target (#0103 fix 5 — a bare
   * `<tr onclick>` with no tabindex/role/key handler was never reachable by
   * keyboard; 16 tab stops in the reviewer's run never landed on a row).
   * The name cell's button IS the click target, natively focusable and
   * Enter/Space-activated with no extra ARIA plumbing needed.
   */
  .row-link {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    font-weight: 600;
    color: inherit;
    cursor: pointer;
    text-align: left;
  }
  .row-link:hover,
  .row-link:focus-visible {
    color: var(--accent);
    text-decoration: underline;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
  }
  .sparkline-slot {
    min-width: 60px;
  }
</style>
