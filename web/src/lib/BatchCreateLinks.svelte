<!--
  BatchCreateLinks (#0168, redesigns #0105) — create one link per channel for a
  campaign, as a grid. Shared fields on top (Destination, Title; plus the
  campaign/term pair, tucked under "More"), then one line per link: Source,
  Medium, Label, Placement and a remove button.

  The Title is ONE content title for the whole batch; each row's Label goes to
  utm_content. That is the structural fix for titles like "... - Mastodon guide
  post 84": the channel lives in the UTM fields, not the title.

  Keyboard: Tab walks the cells; Enter moves to the same column on the next row
  (adding a row at the end). Pasting tab-separated text into any cell fills rows
  from that cell (lib/batchGrid.ts). The last-used channel set is remembered per
  browser and offered in the "Channels" select. Each submit sends an
  Idempotency-Key (#0147) that is reused for an identical retry, so a double
  submit or a timeout-resubmit cannot create two sets.

  Composition, the duplicate-row check and blank-row skipping are unchanged from
  #0105 (lib/campaigns.ts). After a create, the dialog becomes a result list with
  Copy all and the campaign's QR zip.
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { currentUser } from './stores';
  import { batchCreateLinksForCampaign, ApiError } from './api';
  import {
    emptyBatchChannelRow,
    initialBatchRows,
    nonBlankBatchRows,
    duplicateBatchRowIndices,
    buildBatchCreateRows,
    campaignQrZipUrl,
    MAX_BATCH_CREATE_ROWS_PER_REQUEST,
    type BatchChannelRow,
  } from './campaigns';
  import {
    GRID_COLUMNS,
    isGridPaste,
    parseTsv,
    stripHeaderRow,
    applyPaste,
    loadChannelPreset,
    saveChannelPreset,
    rowsFromPreset,
    keyForPayload,
    type ChannelPreset,
  } from './batchGrid';
  import { shortUrl } from './links';
  import { UTM_FIELD_GUIDES } from './utmFieldGuide';
  import type { CampaignDetail, Link } from './types';
  import Button from './Button.svelte';

  interface Props {
    campaign: CampaignDetail;
    /** Called after a successful create so the parent can refresh the campaign detail (links table, counts). */
    oncreated: () => void;
    /** Close the surrounding dialog (Cancel / Done). */
    onclose: () => void;
  }

  let { campaign, oncreated, onclose }: Props = $props();

  let destinationUrl = $state('');
  let title = $state('');
  let sharedCampaign = $state('');
  let sharedTerm = $state('');
  let rows = $state<BatchChannelRow[]>([emptyBatchChannelRow()]);
  let preset = $state<ChannelPreset[]>([]);
  let gridEl: HTMLDivElement | undefined = $state();

  // Snapshot the campaign's defaults once at mount (editable from here on,
  // "starting point, never a lock" — #0098/#0099). A remembered channel set
  // wins over the campaign's single default row.
  onMount(() => {
    sharedCampaign = campaign.default_utm_campaign;
    sharedTerm = campaign.default_utm_term;
    preset = loadChannelPreset();
    rows = preset.length > 0 ? rowsFromPreset(preset) : initialBatchRows(campaign);
  });

  const nonBlank = $derived(nonBlankBatchRows(rows));
  const blankCount = $derived(rows.length - nonBlank.length);
  const dupeIndices = $derived(duplicateBatchRowIndices(rows));
  const canSubmit = $derived(
    destinationUrl.trim() !== '' && nonBlank.length > 0 && dupeIndices.size === 0,
  );

  let submitting = $state(false);
  let error = $state<string | null>(null);
  let created = $state<Link[] | null>(null);
  let copied = $state(false);
  // The Idempotency-Key of the last attempt, reused while the payload is unchanged.
  let attempt: { key: string; body: string } | null = null;

  const presetLabel = $derived(preset.map((p) => p.utm_source || p.utm_medium).join(', '));

  function addRow() {
    if (rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST) return;
    rows = [...rows, emptyBatchChannelRow()];
  }

  function removeRow(i: number) {
    rows = rows.length <= 1 ? [emptyBatchChannelRow()] : rows.filter((_, idx) => idx !== i);
  }

  function usePreset(e: Event) {
    const v = (e.currentTarget as HTMLSelectElement).value;
    if (v === 'last') rows = rowsFromPreset(preset);
    else if (v === 'campaign') rows = initialBatchRows(campaign);
    else if (v === 'blank') rows = [emptyBatchChannelRow()];
    (e.currentTarget as HTMLSelectElement).value = '';
  }

  async function focusCell(r: number, c: number) {
    await tick();
    gridEl?.querySelector<HTMLInputElement>(`input[data-r="${r}"][data-c="${c}"]`)?.focus();
  }

  function onCellKeydown(e: KeyboardEvent, r: number, c: number) {
    if (e.key !== 'Enter' || e.isComposing) return;
    e.preventDefault();
    if (e.shiftKey) return;
    if (r + 1 >= rows.length) {
      if (rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST) return;
      addRow();
    }
    void focusCell(r + 1, c);
  }

  function onCellPaste(e: ClipboardEvent, r: number, c: number) {
    const text = e.clipboardData?.getData('text/plain') ?? '';
    if (!isGridPaste(text)) return; // an ordinary single-cell paste
    e.preventDefault();
    const matrix = stripHeaderRow(parseTsv(text));
    rows = applyPaste(rows, r, c, matrix, MAX_BATCH_CREATE_ROWS_PER_REQUEST);
  }

  async function handleSubmit() {
    error = null;
    const dest = destinationUrl.trim();
    if (dest === '') {
      error = 'Destination URL is required.';
      return;
    }
    if (nonBlank.length === 0) {
      error = 'Add at least one row with a source, medium, label or placement.';
      return;
    }
    if (dupeIndices.size > 0) {
      error =
        'Two or more rows have the same source, medium, label and placement. Make them distinct or remove the duplicate.';
      return;
    }

    const payload = buildBatchCreateRows(dest, rows, sharedCampaign, sharedTerm, title);
    attempt = keyForPayload(attempt, JSON.stringify(payload));
    submitting = true;
    try {
      const res = await batchCreateLinksForCampaign(campaign.slug, payload, attempt.key);
      saveChannelPreset(nonBlank);
      preset = loadChannelPreset();
      created = res.links;
      attempt = null;
      oncreated();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      error = err instanceof ApiError ? err.message : 'Could not create links. Please try again.';
    } finally {
      submitting = false;
    }
  }

  function again() {
    created = null;
    copied = false;
    destinationUrl = '';
    title = '';
    rows = preset.length > 0 ? rowsFromPreset(preset) : initialBatchRows(campaign);
  }

  async function copyAll() {
    if (!created) return;
    try {
      await navigator.clipboard?.writeText(created.map((l) => shortUrl(l.key)).join('\n'));
      copied = true;
    } catch {
      error = 'Could not copy. Select the links and copy them by hand.';
    }
  }

  function rowLabel(l: Link): string {
    return [l.utm_source, l.utm_content, l.placement].filter(Boolean).join(' · ');
  }
</script>

{#if created}
  <div class="batch-form">
    <p class="result-head" role="status">
      Created {created.length} link{created.length === 1 ? '' : 's'}{title.trim() ? ` for “${title.trim()}”` : ''}.
    </p>
    <ul class="result-list">
      {#each created as l (l.id)}
        <li>
          <span class="result-url">{shortUrl(l.key)}</span>
          <span class="text-faint result-meta">{rowLabel(l)}</span>
        </li>
      {/each}
    </ul>
    {#if error}<p class="text-error" role="alert">{error}</p>{/if}
    <div class="actions">
      <Button variant="default" onclick={copyAll}>{copied ? 'Copied' : 'Copy all'}</Button>
      <a class="btn-link" href={campaignQrZipUrl(campaign.slug)} download>Campaign QR zip</a>
      <span class="spacer"></span>
      <span class="end-actions">
        <Button variant="default" onclick={again}>Create more</Button>
        <Button variant="primary" onclick={onclose}>Done</Button>
      </span>
    </div>
  </div>
{:else}
  <form
    class="batch-form"
    onsubmit={(e) => {
      e.preventDefault();
      void handleSubmit();
    }}
  >
    <div class="field">
      <label for="batch-destination-url">Destination</label>
      <input
        id="batch-destination-url"
        type="text"
        inputmode="url"
        placeholder="https://example.com/guides/measuring-current"
        bind:value={destinationUrl}
        disabled={submitting}
      />
    </div>
    <div class="field">
      <label for="batch-title">Title</label>
      <input
        id="batch-title"
        type="text"
        placeholder="Measuring current without blowing the fuse"
        bind:value={title}
        disabled={submitting}
      />
      <p class="hint">Name the content, not the channel. Every link in this batch gets this title.</p>
    </div>

    <details class="more">
      <summary>Campaign and term (shared by every row)</summary>
      <div class="shared-utm-row">
        <div class="field">
          <label for="batch-utm-campaign">utm_campaign</label>
          <input
            id="batch-utm-campaign"
            type="text"
            placeholder={UTM_FIELD_GUIDES.utm_campaign.placeholder}
            bind:value={sharedCampaign}
            disabled={submitting}
          />
        </div>
        <div class="field">
          <label for="batch-utm-term">utm_term <span class="text-faint">(optional)</span></label>
          <input
            id="batch-utm-term"
            type="text"
            placeholder={UTM_FIELD_GUIDES.utm_term.placeholder}
            bind:value={sharedTerm}
            disabled={submitting}
          />
        </div>
      </div>
    </details>

    <div class="grid" bind:this={gridEl} role="group" aria-label="Links to create">
      <div class="grid-head" aria-hidden="true">
        <span>Source</span>
        <span>Medium</span>
        <span>Label</span>
        <span>Placement</span>
        <span></span>
      </div>
      {#each rows as row, i (i)}
        <div class="grid-row" class:row-dupe={dupeIndices.has(i)}>
          {#each GRID_COLUMNS as col, c (col)}
            <div class="cell">
              <label class="cell-label sr-only" for={`batch-${i}-${col}`}>
                {col === 'utm_source' ? 'Source' : col === 'utm_medium' ? 'Medium' : col === 'utm_content' ? 'Label' : 'Placement'}<span class="sr-only">, row {i + 1}</span>
              </label>
              <input
                id={`batch-${i}-${col}`}
                type="text"
                data-r={i}
                data-c={c}
                autocomplete="off"
                autocapitalize="off"
                placeholder={col === 'placement'
                  ? 'optional'
                  : col === 'utm_content'
                    ? 'guide post 84'
                    : UTM_FIELD_GUIDES[col].placeholder}
                bind:value={row[col]}
                disabled={submitting}
                onkeydown={(e) => onCellKeydown(e, i, c)}
                onpaste={(e) => onCellPaste(e, i, c)}
              />
            </div>
          {/each}
          <button
            type="button"
            class="remove"
            aria-label={`Remove row ${i + 1}`}
            onclick={() => removeRow(i)}
            disabled={submitting || (rows.length === 1 && nonBlankBatchRows(rows).length === 0)}>✕</button
          >
          {#if dupeIndices.has(i)}
            <p class="text-error dupe-note" role="alert">
              Same source, medium, label and placement as an earlier row.
            </p>
          {/if}
        </div>
      {/each}
    </div>

    <div class="grid-tools">
      <Button
        variant="default"
        onclick={addRow}
        disabled={submitting || rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST}
      >
        {rows.length >= MAX_BATCH_CREATE_ROWS_PER_REQUEST
          ? `Row limit reached (${MAX_BATCH_CREATE_ROWS_PER_REQUEST})`
          : '+ Add row'}
      </Button>
      <select class="preset" aria-label="Channels" onchange={usePreset} disabled={submitting}>
        <option value="">Channels…</option>
        {#if preset.length > 0}
          <option value="last">Last used: {presetLabel}</option>
        {/if}
        <option value="campaign">Campaign defaults</option>
        <option value="blank">Blank</option>
      </select>
      <span class="text-faint tip">Paste rows from a spreadsheet. Enter moves down.</span>
    </div>

    {#if error}
      <p class="text-error" role="alert">{error}</p>
    {/if}

    <div class="actions">
      <span class="text-faint summary"
        >{nonBlank.length} link{nonBlank.length === 1 ? '' : 's'}{blankCount > 0
          ? `, ${blankCount} blank skipped`
          : ''}</span
      >
      <span class="spacer"></span>
      <Button variant="default" onclick={onclose} disabled={submitting}>Cancel</Button>
      <Button variant="primary" type="submit" disabled={submitting || !canSubmit}>
        {submitting ? 'Creating…' : `Create ${nonBlank.length} link${nonBlank.length === 1 ? '' : 's'}`}
      </Button>
    </div>
  </form>
{/if}

<style>
  .batch-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    min-width: 0;
  }
  .batch-form .field {
    margin-bottom: 0;
  }
  .hint {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
    color: var(--text-faint);
  }
  .more summary {
    cursor: pointer;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .shared-utm-row {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
    gap: var(--space-3);
    margin-top: var(--space-2);
  }
  .grid {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    min-width: 0;
  }
  .grid-head,
  .grid-row {
    display: grid;
    grid-template-columns: 1fr 1fr 1.2fr 1.4fr 2rem;
    gap: var(--space-2);
    align-items: center;
  }
  .grid-head {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
  }
  .grid-row {
    row-gap: var(--space-1);
  }
  .grid-row.row-dupe input {
    border-color: var(--danger);
  }
  .cell {
    min-width: 0;
  }
  .cell input {
    width: 100%;
    min-width: 0;
  }
  .remove {
    font: inherit;
    width: 2rem;
    height: 2rem;
    padding: 0;
    border: var(--border-w) solid transparent;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .remove:hover:not(:disabled) {
    background: var(--danger-hover-bg);
    color: var(--danger);
  }
  .remove:disabled {
    opacity: 0.35;
    cursor: default;
  }
  .dupe-note {
    grid-column: 1 / -1;
    margin: 0;
  }
  .grid-tools {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2) var(--space-3);
  }
  .preset {
    width: auto;
    max-width: 100%;
  }
  .tip {
    font-size: var(--fs-sm);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }
  .spacer {
    flex: 1;
  }
  /* Keep Create more and Done together on one line, even on a phone. */
  .end-actions {
    display: inline-flex;
    gap: var(--space-2);
    flex-wrap: nowrap;
    margin-left: auto;
  }
  .summary {
    font-size: var(--fs-sm);
  }
  .result-head {
    margin: 0;
    font-weight: 600;
  }
  .result-list {
    list-style: none;
    margin: 0;
    padding: 0;
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    max-height: 18rem;
    overflow: auto;
  }
  .result-list li {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-3);
    justify-content: space-between;
    padding: var(--space-2) var(--space-3);
    border-bottom: var(--border-w) solid var(--border);
  }
  .result-list li:last-child {
    border-bottom: none;
  }
  .result-url {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }
  .result-meta {
    font-size: var(--fs-sm);
  }

  /* Phone: a row becomes a 2x2 block of labelled cells with the remove button
     beside it; the column header row is dropped. */
  @media (max-width: 600px) {
    .grid-head {
      display: none;
    }
    .grid-row {
      grid-template-columns: 1fr 1fr 2rem;
      padding: var(--space-2);
      border: var(--border-w) solid var(--border);
      border-radius: var(--radius);
    }
    .grid-row .cell:nth-child(1),
    .grid-row .cell:nth-child(3) {
      grid-column: 1;
    }
    .grid-row .cell:nth-child(2),
    .grid-row .cell:nth-child(4) {
      grid-column: 2;
    }
    .remove {
      grid-column: 3;
      grid-row: 1;
      align-self: start;
    }
    .cell-label.sr-only {
      position: static;
      width: auto;
      height: auto;
      margin: 0 0 2px;
      overflow: visible;
      clip: auto;
      white-space: normal;
      display: block;
      font-size: var(--fs-sm);
      color: var(--text-muted);
    }
  }
</style>
