<!--
  NewLinkForm (#0158) — the create-link form and its success step, shared by
  the dialog (NewLinkDialog.svelte, opened by "+ New link" on desktop) and the
  full page at #/new (views/NewLink.svelte, used on phones).

  Step 1 asks for Destination URL, Title, Campaign and Channel, with a More
  disclosure for alias, expiry, the five UTM fields and placement, and a live
  "Will redirect to" preview. Step 2 shows the short URL with Copy and QR,
  "Create another for this page" (keeps URL, title, campaign; pick the next
  channel) and Done. A duplicate response says "You already have a link for
  this" and shows the existing link. Pure logic lives in lib/newLink.ts;
  validation and error mapping in lib/links.ts. See docs/ui-redesign.md 3.7.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, links } from './stores';
  import { createLink, listCampaigns, ApiError, type CreateLinkInput } from './api';
  import {
    qrPngUrl,
    isValidHttpUrl,
    isValidKey,
    MAX_KEY_LENGTH,
    noticeForCreated,
    noticeForError,
    type CreateNotice,
  } from './links';
  import type { Campaign } from './types';
  import { emptyUtmParams, composeUtmUrl, fillBlankUtmParams } from './utm';
  import type { UtmParams } from './utm';
  import { CHANNELS, applyChannel, carryOver, nextChannel } from './newLink';
  import Button from './Button.svelte';
  import UtmField from './UtmField.svelte';
  import UtmConventions from './UtmConventions.svelte';

  interface Props {
    /** Destination to prefill (a pasted URL). */
    initialUrl?: string;
    /** Called by Cancel and Done. */
    onclose: () => void;
    /** 'dialog' hides the page chrome; 'page' shows a heading. */
    mode?: 'dialog' | 'page';
  }
  let { initialUrl = '', onclose, mode = 'dialog' }: Props = $props();

  // ── Form state ──────────────────────────────────────────────────────────────
  let destinationUrl = $state(untrackedInitial());
  let title = $state('');
  let customKey = $state('');
  let expiresAt = $state('');
  let placement = $state('');
  let utmParams = $state<UtmParams>(emptyUtmParams());
  let channel = $state('');
  let selectedCampaignID = $state<number | ''>('');
  let moreOpen = $state(false);
  let submitting = $state(false);
  let step = $state<'form' | 'done'>('form');

  function untrackedInitial(): string {
    return initialUrl;
  }

  let notice = $state<CreateNotice | null>(null);
  let keyError = $state<string | null>(null);
  let urlError = $state<string | null>(null);

  // Optional expiry (#0076): expiresAt === '' means the link never expires and
  // the field shows blank. A transparent native datetime-local is layered over
  // our own text purely as the picker engine + value holder.
  let expiresInput = $state<HTMLInputElement | null>(null);
  const hasExpiry = $derived(expiresAt !== '');
  const expiryLabel = $derived(
    expiresAt
      ? new Date(expiresAt).toLocaleString(undefined, {
          year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
        })
      : '',
  );
  function openExpiryPicker() {
    try {
      expiresInput?.showPicker();
    } catch {
      /* no activation / unsupported — the focused input still accepts a date */
    }
  }
  function clearExpiry() {
    expiresAt = '';
    expiresInput?.blur();
  }

  // ── Campaigns ───────────────────────────────────────────────────────────────
  let campaigns = $state<Campaign[]>([]);
  const selectedCampaign = $derived(
    selectedCampaignID === '' ? null : (campaigns.find((c) => c.id === selectedCampaignID) ?? null),
  );

  async function loadCampaigns() {
    try {
      const res = await listCampaigns();
      campaigns = res.campaigns.filter((c) => !c.archived);
    } catch {
      // Non-fatal: the form works with no campaign selectable.
    }
  }

  // Picking a campaign fills only the BLANK UTM fields from its defaults
  // (fillBlankUtmParams, #0099 item 6); a chosen channel is then re-applied so
  // the campaign's own default source/medium never overrides it.
  function handleCampaignSelect() {
    const c = selectedCampaign;
    if (c) utmParams = fillBlankUtmParams(utmParams, c);
    if (channel) utmParams = applyChannel(utmParams, channel, c);
  }

  // Channel shortcut: fills utm_source / utm_medium.
  function handleChannelSelect() {
    utmParams = applyChannel(utmParams, channel, selectedCampaign);
  }

  // ── Derived ─────────────────────────────────────────────────────────────────
  // The previous-params argument is a constant empty set: the builder starts
  // with nothing, so it never "owned" a UTM param already in a pasted URL.
  const composedUrl = $derived(composeUtmUrl(destinationUrl, utmParams, emptyUtmParams()));
  const urlInvalid = $derived(destinationUrl.trim() !== '' && !isValidHttpUrl(destinationUrl));
  const keyInvalid = $derived(!isValidKey(customKey));
  const canSubmit = $derived(
    !submitting && destinationUrl.trim() !== '' && !urlInvalid && !keyInvalid,
  );
  const hasMore = $derived(
    customKey.trim() !== '' || expiresAt !== '' || placement.trim() !== '' ||
      utmParams.utm_campaign.trim() !== '' || utmParams.utm_term.trim() !== '' ||
      utmParams.utm_content.trim() !== '',
  );

  function buildInput(): CreateLinkInput {
    // The composed URL (destination + UTM baked in) is what gets stored, and
    // the same values also go as discrete fields (#0099) so the columns agree.
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
    for (const key of ['utm_source', 'utm_medium', 'utm_campaign', 'utm_term', 'utm_content'] as const) {
      const v = utmParams[key].trim();
      if (v !== '') input[key] = v;
    }
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
      nextPick = nextChannel(channel);
      step = 'done';
    } catch (err) {
      const n = noticeForError(err);
      notice = n;
      if (n.kind === 'error') {
        if (n.field === 'key') {
          keyError = n.message;
          moreOpen = true;
        } else if (n.field === 'destination_url') urlError = n.message;
        else if (err instanceof ApiError && err.status === 401) currentUser.set(null);
      }
    } finally {
      submitting = false;
    }
  }

  // ── Success step ────────────────────────────────────────────────────────────
  let nextPick = $state('');
  let copied = $state(false);
  let showQr = $state(false);
  let resultEl = $state<HTMLElement | null>(null);

  // Move focus to Copy when the success step appears (the form's focused
  // control is gone), so Enter/Space copies and keyboard users aren't lost.
  $effect(() => {
    if (step === 'done') resultEl?.querySelector('button')?.focus();
  });

  async function copyShortUrl(url: string) {
    try {
      await navigator.clipboard.writeText(url);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      // Clipboard may be unavailable (insecure context / permissions); ignore.
    }
  }

  function createAnother() {
    const kept = carryOver(
      { destinationUrl, title, campaignId: selectedCampaignID, utmParams, channel },
      nextPick,
    );
    destinationUrl = kept.destinationUrl;
    title = kept.title;
    selectedCampaignID = kept.campaignId;
    // Re-apply the campaign's default utm_content etc. to the cleared fields.
    utmParams = kept.utmParams;
    if (selectedCampaign) utmParams = fillBlankUtmParams(utmParams, selectedCampaign);
    utmParams = applyChannel(utmParams, kept.channel, selectedCampaign);
    channel = kept.channel;
    customKey = '';
    expiresAt = '';
    placement = '';
    notice = null;
    copied = false;
    showQr = false;
    step = 'form';
  }

  onMount(() => {
    loadCampaigns();
  });
</script>

<div class="nl" class:nl-page={mode === 'page'}>
  {#if step === 'form'}
    <h2 class="nl-title nl-form-title" id="new-link-title">New link</h2>
    <form
      onsubmit={(e) => {
        e.preventDefault();
        handleCreate();
      }}
    >
      <div class="field">
        <label for="dest-url">Destination URL</label>
        <!-- svelte-ignore a11y_autofocus -->
        <input
          id="dest-url"
          type="url"
          inputmode="url"
          placeholder="https://example.com/page"
          bind:value={destinationUrl}
          oninput={() => (urlError = null)}
          disabled={submitting}
          required
          autofocus={mode === 'dialog'}
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
          placeholder="Measuring current without blowing the fuse"
          bind:value={title}
          disabled={submitting}
        />
        <p class="text-faint hint">Use the content's title. The channel goes in Channel.</p>
      </div>

      <div class="pair">
        <div class="field">
          <label for="campaign-select">Campaign <span class="text-faint">(optional)</span></label>
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
        </div>
        <div class="field">
          <label for="channel-select">Channel <span class="text-faint">(optional)</span></label>
          <select
            id="channel-select"
            bind:value={channel}
            onchange={handleChannelSelect}
            disabled={submitting}
          >
            <option value="">None</option>
            {#each CHANNELS as c (c.id)}
              <option value={c.id}>{c.label}</option>
            {/each}
          </select>
        </div>
      </div>

      <div class="more">
        <button
          type="button"
          class="more-toggle"
          aria-expanded={moreOpen}
          aria-controls="new-link-more"
          onclick={() => (moreOpen = !moreOpen)}
        >
          <span class="chev" class:open={moreOpen}>▶</span>
          More
          <span class="text-faint more-sub">custom alias · expiry · UTM fields · placement</span>
          {#if hasMore && !moreOpen}<span class="badge">filled</span>{/if}
        </button>

        {#if moreOpen}
          <div class="more-body" id="new-link-more">
            <div class="field">
              <label for="custom-key">Custom alias <span class="text-faint">(optional)</span></label>
              <input
                id="custom-key"
                type="text"
                placeholder="e.g. launch"
                bind:value={customKey}
                oninput={() => (keyError = null)}
                disabled={submitting}
                aria-invalid={keyInvalid || keyError !== null}
                class:input-error={keyInvalid || keyError !== null}
              />
              {#if keyInvalid}
                <p class="text-warn" role="status">
                  Up to {MAX_KEY_LENGTH} characters, letters and numbers plus - and _.
                </p>
              {/if}
              {#if keyError}
                <p class="text-error" role="alert">{keyError}</p>
              {/if}
            </div>

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

            <UtmConventions />
            <UtmField fieldKey="utm_source" id="utm-source" bind:value={utmParams.utm_source} disabled={submitting} />
            <UtmField fieldKey="utm_medium" id="utm-medium" bind:value={utmParams.utm_medium} disabled={submitting} />
            <UtmField fieldKey="utm_campaign" id="utm-campaign" bind:value={utmParams.utm_campaign} disabled={submitting} />
            <UtmField fieldKey="utm_term" id="utm-term" bind:value={utmParams.utm_term} disabled={submitting} />
            <UtmField fieldKey="utm_content" id="utm-content" bind:value={utmParams.utm_content} disabled={submitting} />

            <div class="field">
              <label for="placement">Placement <span class="text-faint">(optional)</span></label>
              <input
                id="placement"
                type="text"
                placeholder="18th & Texas board"
                bind:value={placement}
                disabled={submitting}
              />
              <p class="text-faint hint">A physical or operational label; not sent to the destination site.</p>
            </div>
          </div>
        {/if}
      </div>

      {#if destinationUrl.trim() !== '' && !urlInvalid}
        <div class="preview">
          <span class="preview-label">Will redirect to</span>
          <span class="preview-url" title={composedUrl} data-testid="redirect-preview">{composedUrl}</span>
        </div>
      {/if}

      {#if notice && notice.kind === 'denied'}
        <div class="denied-box" role="alert">{notice.message}</div>
      {:else if notice && notice.kind === 'error' && notice.field === null}
        <p class="text-error" role="alert">{notice.message}</p>
      {/if}

      <div class="actions">
        <Button variant="subtle" onclick={onclose} disabled={submitting}>Cancel</Button>
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {submitting ? 'Creating…' : 'Create link'}
        </Button>
      </div>
    </form>
  {:else if notice && (notice.kind === 'created' || notice.kind === 'duplicate')}
    {@const key = notice.link.key}
    {@const url = notice.shortUrl}
    <h2 class="nl-title" id="new-link-title">
      {notice.kind === 'duplicate' ? 'You already have a link for this' : 'Link ready'}
    </h2>
    <div class="result" role="status" bind:this={resultEl}>
      <a class="short-url" href={url} target="_blank" rel="noreferrer">{url}</a>
      <div class="row">
        <Button variant="subtle" onclick={() => copyShortUrl(url)}>{copied ? 'Copied!' : 'Copy'}</Button>
        <Button variant="subtle" aria-expanded={showQr} onclick={() => (showQr = !showQr)}>QR</Button>
      </div>
    </div>
    {#if showQr}
      <div class="qr">
        <img src={qrPngUrl(key)} alt="QR code for {url}" width="160" height="160" />
        <a class="btn-link" href={qrPngUrl(key)} download>Download PNG</a>
      </div>
    {/if}
    <div class="actions another">
      <label class="another-pick">
        <span class="text-faint">Next channel</span>
        <select bind:value={nextPick} aria-label="Channel for the next link">
          {#each CHANNELS as c (c.id)}
            <option value={c.id}>{c.label}</option>
          {/each}
        </select>
      </label>
      <Button onclick={createAnother}>Create another for this page</Button>
      <Button variant="primary" onclick={onclose}>Done</Button>
    </div>
  {/if}
</div>

<style>
  .nl-title {
    margin: 0 0 var(--space-4);
    font-size: var(--fs-lg);
    font-weight: 600;
  }
  /* On the page the view's own h1 already says "New link"; the success heading stays. */
  .nl-page .nl-form-title {
    display: none;
  }
  .hint {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
  }
  .pair {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  .more {
    margin-bottom: var(--space-3);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .more-toggle {
    min-height: 40px;
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
  .more-sub {
    font-weight: 400;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .chev {
    font-size: 9px;
    display: inline-block;
    transition: transform 0.15s ease;
    color: var(--text-faint);
  }
  .chev.open {
    transform: rotate(90deg);
  }
  .more-body {
    padding: var(--space-3) var(--space-3) var(--space-1);
    border-top: var(--border-w) solid var(--border);
    background: var(--bg-panel);
  }
  .preview {
    display: flex;
    flex-direction: column;
    gap: 2px;
    margin-bottom: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-subtle);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .preview-label {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
  }
  .preview-url {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    color: var(--accent);
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 3;
    line-clamp: 3;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
  .result {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    flex-wrap: wrap;
    padding: var(--space-3) var(--space-4);
    border: var(--border-w) solid var(--border);
    background: var(--accent-subtle);
    border-radius: var(--radius);
    margin-bottom: var(--space-4);
  }
  .short-url {
    font-family: var(--font-mono);
    font-size: var(--fs-md);
    color: var(--accent);
    overflow-wrap: anywhere;
  }
  .qr {
    display: flex;
    align-items: flex-end;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .qr img {
    background: #fff;
    padding: var(--space-2);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .another {
    align-items: center;
  }
  .another-pick {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-right: auto;
  }
  .denied-box {
    margin-bottom: var(--space-3);
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

  /* Custom Expires field (#0076): blank when unset, native picker layered on top. */
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

  @media (max-width: 600px) {
    .pair {
      grid-template-columns: 1fr;
      gap: 0;
    }
    .more-sub {
      display: none;
    }
    .another-pick {
      flex: 0 0 100%;
      margin-right: 0;
    }
  }
</style>
