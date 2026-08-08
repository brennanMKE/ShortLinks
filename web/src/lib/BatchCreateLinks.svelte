<!--
  BatchCreateLinks — the #0105 batch-create form on the campaign detail view.
  "One destination URL, then a row per channel" (issue description): a
  shared destination URL field, a shared utm_campaign/utm_term pair
  (prefilled from the campaign's own default_utm_campaign/default_utm_term,
  editable — #0098/#0099's decided "starting point, never a lock" rule), and
  a repeatable row per channel carrying exactly the fields the issue's
  deliverable list names as PER-ROW: source, medium, content, placement, and
  an optional title. utm_campaign/utm_term deliberately do NOT vary per row
  — see lib/campaigns.ts's "Batch create (#0105)" module doc comment for why
  repeating them per row would be the very fragmentation docs/utm.md warns
  about, self-inflicted instead of caused by different authors.

  ROW 1 PREFILL (review finding 2): the campaign's default_utm_source/
  _medium/_content prefill ONLY row 1 — via lib/campaigns.ts's
  initialBatchRows, called once in onMount below — never every row. See that
  function's doc comment for why: prefilling every row would make rows 2..N
  byte-identical duplicates of row 1, and would make every untouched row
  non-blank, defeating the skip-blank-rows behavior. Rows added afterward via
  "+ Add row" always start genuinely blank (emptyBatchChannelRow()), exactly
  as before this fix.

  GUIDANCE TEXT carries only the PLACEHOLDER half of #0095's UtmField
  (lib/utmFieldGuide.ts) for the four per-row fields — not the persistent
  hint paragraph UtmField.svelte renders, which repeated across eight rows
  would be unreadable (issue note, explicitly anticipated). UtmConventions
  (the "lowercase, hyphens, no PII" line) is shown once, above every row,
  exactly like the single-create builder.

  COMPOSITION happens entirely client-side via lib/campaigns.ts's
  composeBatchRowDestinationUrl — the SAME composeUtmUrl helper single-create
  uses (issue note: "resist writing a second composition path") — so every
  row's `destination_url` sent to the server is already fully composed; the
  backend only validates and inserts, it never recomposes (see
  internal/handlers/campaigns.go's BatchCreateLinks doc comment).

  DEDUP: two rows differing only in placement compose to a BYTE-IDENTICAL
  destination_url (placement is never baked into the URL — #0099's data
  model) and must still become two separate links — the confirmed trap this
  issue exists to fix. Nothing in THIS component works around it; the fix is
  entirely server-side (links.Store.CreateLinksBatch bypasses
  CreateOrReactivateLink's dedup lookup). What this component DOES check,
  client-side, is a SEPARATE concern — an accidental exact duplicate row
  (same source+medium+content+placement) — via duplicateBatchRowIndices,
  mirroring the server's own rejection so the user sees the same decision
  without a round trip.

  ATOMIC: the whole submission either creates every non-blank row or (on any
  validation/filter failure) creates nothing — see api.ts's
  batchCreateLinksForCampaign doc comment. A blank row (added but never
  filled in) is skipped rather than creating a link with empty UTM values;
  skippedCount is surfaced in the success notice rather than silently
  dropped.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, currentView } from './stores';
  import { batchCreateLinksForCampaign, ApiError } from './api';
  import {
    emptyBatchChannelRow,
    initialBatchRows,
    nonBlankBatchRows,
    duplicateBatchRowIndices,
    buildBatchCreateRows,
    MAX_BATCH_CREATE_ROWS_PER_REQUEST,
    type BatchChannelRow,
  } from './campaigns';
  import { UTM_FIELD_GUIDES } from './utmFieldGuide';
  import type { CampaignDetail } from './types';
  import Button from './Button.svelte';
  import UtmConventions from './UtmConventions.svelte';

  interface Props {
    campaign: CampaignDetail;
    /** Called after a successful create so the parent can refresh the campaign detail (links table, counts). */
    oncreated: () => void;
  }

  let { campaign, oncreated }: Props = $props();

  let destinationUrl = $state('');
  // Prefilled ONCE from the campaign's defaults, set in onMount (below)
  // rather than in the $state initializer — reading `campaign.*` directly in
  // an initializer only captures Svelte's compile-time-visible value and
  // trips the `state_referenced_locally` warning, since a $props() value can
  // in principle change before mount. onMount runs exactly once, deliberately
  // outside reactive tracking, so this is a real one-time snapshot (matching
  // the snapshot-at-open pattern the edit forms elsewhere in this app already
  // use, e.g. LinkDetail's startEdit()) rather than a live binding — editable
  // from here on, per the "starting point, never a lock" rule.
  let sharedCampaign = $state('');
  let sharedTerm = $state('');
  // rows starts as a single blank row (matching the pre-mount snapshot
  // above); onMount replaces it with initialBatchRows(campaign) — ROW 1
  // prefilled from the campaign's default_utm_source/_medium/_content,
  // still just one row (review finding 2; see this file's header comment
  // and lib/campaigns.ts's initialBatchRows doc comment for why only row 1).
  let rows = $state<BatchChannelRow[]>([emptyBatchChannelRow()]);
  onMount(() => {
    sharedCampaign = campaign.default_utm_campaign;
    sharedTerm = campaign.default_utm_term;
    rows = initialBatchRows(campaign);
  });

  const nonBlank = $derived(nonBlankBatchRows(rows));
  const blankCount = $derived(rows.length - nonBlank.length);
  const dupeIndices = $derived(duplicateBatchRowIndices(rows));
  // destinationUrl is part of canSubmit, not just handleSubmit's guard: since
  // #0105's row-1 prefill makes row 1 non-blank at mount, nonBlank.length is
  // already 1 on an untouched form, so without this the button reads "Create 1
  // link" and is enabled before the user has typed anything — clicking it just
  // produces "Destination URL is required." (review nit 1).
  const canSubmit = $derived(
    destinationUrl.trim() !== '' && nonBlank.length > 0 && dupeIndices.size === 0,
  );

  let submitting = $state(false);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);

  function addRow() {
    if (rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST) return;
    rows = [...rows, emptyBatchChannelRow()];
  }

  function removeRow(i: number) {
    if (rows.length <= 1) return;
    rows = rows.filter((_, idx) => idx !== i);
  }

  async function handleSubmit() {
    error = null;
    notice = null;
    const dest = destinationUrl.trim();
    if (dest === '') {
      error = 'Destination URL is required.';
      return;
    }
    if (nonBlank.length === 0) {
      error = 'Add at least one row with a source, medium, content, placement, or title.';
      return;
    }
    if (dupeIndices.size > 0) {
      error =
        'Two or more rows have the same source, medium, content, and placement — make them distinct or remove the duplicate.';
      return;
    }

    const payload = buildBatchCreateRows(dest, rows, sharedCampaign, sharedTerm);
    submitting = true;
    try {
      const res = await batchCreateLinksForCampaign(campaign.slug, payload);
      const createdCount = res.links.length;
      notice =
        `Created ${createdCount} link${createdCount === 1 ? '' : 's'}.` +
        (blankCount > 0 ? ` ${blankCount} blank row${blankCount === 1 ? '' : 's'} skipped.` : '');
      destinationUrl = '';
      // Re-seed from the campaign's defaults rather than a bare empty row.
      // onMount does not run again, so resetting to emptyBatchChannelRow()
      // meant the defaults appeared on first open and never again in that
      // session — the panel behaved differently on the second batch than the
      // first, for no stated reason (review nit 2).
      rows = initialBatchRows(campaign);
      oncreated();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      error = err instanceof ApiError ? err.message : 'Could not create links. Please try again.';
    } finally {
      submitting = false;
    }
  }
</script>

<div class="batch-form">
  <UtmConventions />

  <div class="field">
    <label for="batch-destination-url">Destination URL</label>
    <input
      id="batch-destination-url"
      type="text"
      placeholder="https://example.com/summer-sale"
      bind:value={destinationUrl}
      disabled={submitting}
    />
  </div>

  <div class="shared-utm-row">
    <div class="field">
      <label for="batch-utm-campaign">Campaign <span class="text-faint">(shared by every row below)</span></label>
      <input
        id="batch-utm-campaign"
        type="text"
        placeholder={UTM_FIELD_GUIDES.utm_campaign.placeholder}
        bind:value={sharedCampaign}
        disabled={submitting}
      />
    </div>
    <div class="field">
      <label for="batch-utm-term">Term <span class="text-faint">(optional, shared)</span></label>
      <input
        id="batch-utm-term"
        type="text"
        placeholder={UTM_FIELD_GUIDES.utm_term.placeholder}
        bind:value={sharedTerm}
        disabled={submitting}
      />
    </div>
  </div>

  <div class="rows">
    {#each rows as row, i (i)}
      <div class="row-card" class:row-dupe={dupeIndices.has(i)}>
        <div class="row-header">
          <span class="row-label">Row {i + 1}</span>
          <Button
            variant="subtle"
            type="button"
            onclick={() => removeRow(i)}
            disabled={submitting || rows.length === 1}
          >
            Remove row
          </Button>
        </div>
        <div class="row-fields">
          <div class="field">
            <label for={`batch-row-${i}-source`}>Source</label>
            <input
              id={`batch-row-${i}-source`}
              type="text"
              placeholder={UTM_FIELD_GUIDES.utm_source.placeholder}
              bind:value={row.utm_source}
              disabled={submitting}
            />
          </div>
          <div class="field">
            <label for={`batch-row-${i}-medium`}>Medium</label>
            <input
              id={`batch-row-${i}-medium`}
              type="text"
              placeholder={UTM_FIELD_GUIDES.utm_medium.placeholder}
              bind:value={row.utm_medium}
              disabled={submitting}
            />
          </div>
          <div class="field">
            <label for={`batch-row-${i}-content`}>Content</label>
            <input
              id={`batch-row-${i}-content`}
              type="text"
              placeholder={UTM_FIELD_GUIDES.utm_content.placeholder}
              bind:value={row.utm_content}
              disabled={submitting}
            />
          </div>
          <div class="field">
            <label for={`batch-row-${i}-placement`}>Placement <span class="text-faint">(optional)</span></label>
            <input
              id={`batch-row-${i}-placement`}
              type="text"
              placeholder="18th & Texas board"
              bind:value={row.placement}
              disabled={submitting}
            />
          </div>
        </div>
        <div class="field">
          <label for={`batch-row-${i}-title`}>Title <span class="text-faint">(optional)</span></label>
          <input
            id={`batch-row-${i}-title`}
            type="text"
            placeholder="Flyer QR code"
            bind:value={row.title}
            disabled={submitting}
          />
        </div>
        {#if dupeIndices.has(i)}
          <p class="text-error row-dupe-note" role="alert">
            Duplicate of an earlier row (same source, medium, content, and placement).
          </p>
        {/if}
      </div>
    {/each}
  </div>

  <Button
    variant="default"
    type="button"
    onclick={addRow}
    disabled={submitting || rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST}
  >
    {rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST
      ? `Row limit reached (${MAX_BATCH_CREATE_ROWS_PER_REQUEST})`
      : '+ Add row'}
  </Button>

  <p class="text-faint batch-summary">
    {nonBlank.length} link{nonBlank.length === 1 ? '' : 's'} will be created{blankCount > 0
      ? ` — ${blankCount} blank row${blankCount === 1 ? '' : 's'} will be skipped`
      : ''}.
  </p>

  {#if error}
    <p class="text-error" role="alert">{error}</p>
  {/if}
  {#if notice}
    <p class="text-notice" role="status">{notice}</p>
  {/if}

  <Button variant="primary" type="button" onclick={handleSubmit} disabled={submitting || !canSubmit}>
    {submitting ? 'Creating…' : `Create ${nonBlank.length} link${nonBlank.length === 1 ? '' : 's'}`}
  </Button>
</div>

<style>
  .batch-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .shared-utm-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
    gap: var(--space-3);
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .row-card {
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-3);
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .row-card.row-dupe {
    border-color: var(--danger);
  }
  .row-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2);
  }
  .row-label {
    font-weight: 600;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .row-fields {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
    gap: var(--space-2) var(--space-3);
  }
  .row-dupe-note {
    margin: 0;
    font-size: var(--fs-sm);
  }
  .batch-summary {
    margin: 0;
    font-size: var(--fs-sm);
  }
</style>
