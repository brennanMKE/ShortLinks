<!--
  AdminHome (#0177) — the "Home page" section of Admin → Settings: what a
  signed-out visitor sees (mode, title, about text, hero image) with a live
  preview of the real public page built from the UNSAVED values.

  The text settings are saved with PATCH /admin/settings, one key per call (the
  existing contract). The hero is its own upload (PUT /admin/home/hero, #0176):
  choosing or dropping a file previews it at once and nothing reaches the server
  until Upload. The server's 400 messages say exactly what to change, so they
  are shown as they come.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Button from './Button.svelte';
  import Panel from './Panel.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import { ApiError, deleteHero, getPublicHome, updateSetting, uploadHero } from './api';
  import {
    HOME_ABOUT_MAX,
    HOME_MODES,
    HOME_TITLE_MAX,
    HERO_MAX_BYTES,
    changedHomeSettings,
    homeSettingsFrom,
    previewHeroSize,
    sampleActivity,
    type HomeDraft,
  } from './homeAdmin';
  import type { HomeActivity, HomeHero, HomeInfo, HomeMode } from './home';
  import type { Setting } from './types';

  interface Props {
    settings: Setting[];
    /** Called with the full settings list after a save. */
    onSettings: (next: Setting[]) => void;
    /** Returns true when the error was an auth failure that has been handled. */
    onAuthError: (err: unknown) => boolean;
    /** The mode being edited; the Primary URL row follows it. */
    mode?: HomeMode;
  }

  let { settings, onSettings, onAuthError, mode = $bindable('default') }: Props = $props();

  const saved = $derived(homeSettingsFrom(settings));
  // The draft is set from the saved values on first load and after this
  // panel's own save, never when unrelated settings (the registrations toggle,
  // the Primary URL) change `settings` underneath it.
  let draft = $state<HomeDraft>({ mode: 'default', title: '', about: '' });
  onMount(() => {
    draft = { ...saved };
  });
  $effect(() => {
    mode = draft.mode;
  });

  const registrationsOpen = $derived(
    settings.find((s) => s.key === 'registrations_enabled')?.value === 'true',
  );
  const dirty = $derived(changedHomeSettings(saved, draft).length > 0);

  let saving = $state(false);
  let saveError = $state<string | null>(null);
  let notice = $state<string | null>(null);

  async function save() {
    saving = true;
    saveError = null;
    notice = null;
    try {
      let latest: Setting[] | null = null;
      for (const [key, value] of changedHomeSettings(saved, draft)) {
        const res = (await updateSetting(key, value)) as { settings?: Setting[] };
        if (res && Array.isArray(res.settings)) latest = res.settings;
      }
      if (latest) {
        onSettings(latest);
        draft = { ...homeSettingsFrom(latest) };
      }
      notice = 'Home page saved.';
      void loadServerHome();
    } catch (err) {
      if (onAuthError(err)) return;
      saveError =
        err instanceof ApiError && err.status === 400
          ? err.message
          : 'Could not save the home page. Please try again.';
    } finally {
      saving = false;
    }
  }

  // ── Hero ──────────────────────────────────────────────────────────────────
  let hero = $state<HomeHero | null>(null);
  let serverActivity = $state<HomeActivity | null>(null);
  let altDraft = $state('');
  let pendingFile = $state<File | null>(null);
  let pendingUrl = $state<string | null>(null);
  let pendingSize = $state<{ width: number; height: number } | null>(null);
  let heroError = $state<string | null>(null);
  let heroBusy = $state(false);
  let dragging = $state(false);
  let fileInput = $state<HTMLInputElement | null>(null);

  async function loadServerHome() {
    try {
      const info = await getPublicHome();
      hero = info.hero ?? null;
      serverActivity = info.activity ?? null;
      if (pendingFile === null) altDraft = hero?.alt ?? '';
    } catch {
      // The preview just has no hero; the settings above still work.
    }
  }
  onMount(loadServerHome);

  function clearPending() {
    if (pendingUrl) URL.revokeObjectURL(pendingUrl);
    pendingFile = null;
    pendingUrl = null;
    pendingSize = null;
    if (fileInput) fileInput.value = '';
  }
  onDestroy(clearPending);

  function pick(file: File | undefined) {
    if (!file) return;
    heroError = null;
    if (file.size > HERO_MAX_BYTES) {
      heroError = `That file is ${(file.size / 1048576).toFixed(1)} MB. Use an image under 2 MB.`;
      return;
    }
    clearPending();
    pendingFile = file;
    pendingUrl = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      pendingSize = { width: img.naturalWidth, height: img.naturalHeight };
    };
    img.onerror = () => {
      heroError = 'That file could not be read as an image. Use a PNG, JPEG or WebP file.';
      clearPending();
    };
    img.src = pendingUrl;
  }

  function onDrop(e: DragEvent) {
    e.preventDefault();
    dragging = false;
    pick(e.dataTransfer?.files?.[0]);
  }

  async function upload() {
    if (!pendingFile) return;
    heroBusy = true;
    heroError = null;
    try {
      hero = await uploadHero(pendingFile, altDraft.trim());
      clearPending();
      notice = 'Hero image uploaded.';
    } catch (err) {
      if (onAuthError(err)) return;
      heroError =
        err instanceof ApiError && err.status === 400
          ? err.message
          : 'Could not upload the image. Please try again.';
    } finally {
      heroBusy = false;
    }
  }

  let removeOpen = $state(false);

  // Runs inside ConfirmDialog: a throw keeps the dialog open with the message.
  async function removeHero() {
    heroError = null;
    try {
      await deleteHero();
    } catch (err) {
      if (onAuthError(err)) return;
      throw new Error('Could not remove the image. Please try again.');
    }
    hero = null;
    altDraft = '';
    notice = 'Hero image removed.';
  }

  // ── Preview ───────────────────────────────────────────────────────────────
  let phoneWidth = $state(false);
  const usingSample = $derived(draft.mode === 'activity' && serverActivity === null);
  // A chosen image the server would reject is previewed in the accepted 2:1 to 4:1 box.
  const previewHero = $derived<HomeHero | null>(
    pendingUrl
      ? {
          url: pendingUrl,
          alt: altDraft,
          ...previewHeroSize(pendingSize?.width ?? 0, pendingSize?.height ?? 0),
        }
      : hero,
  );
  let PublicHomeView = $state<typeof import('./PublicHome.svelte').default | null>(null);
  onMount(async () => {
    PublicHomeView = (await import('./PublicHome.svelte')).default;
  });
  const previewInfo = $derived<HomeInfo>({
    redirect_url: '',
    registrations_enabled: registrationsOpen,
    mode: draft.mode,
    title: draft.title,
    about: draft.about,
    hero: previewHero,
    activity: draft.mode === 'activity' ? (serverActivity ?? sampleActivity(new Date())) : undefined,
  });
</script>

<Panel title="Home page">
  <p class="text-muted lead">
    What a signed-out visitor sees at <code>/</code>. The sign-in page at <code>#/login</code> is
    always available.
  </p>

  <fieldset class="modes">
    <legend class="field-name">Mode</legend>
    {#each HOME_MODES as m (m.value)}
      <label class="mode">
        <input type="radio" name="home-mode" value={m.value} bind:group={draft.mode} />
        <span>{m.label}</span>
      </label>
    {/each}
    {#if draft.mode === 'activity'}
      <p class="warn" role="note">
        Titles and short URLs of the 3 most-clicked links on this site will be public. Top links are
        drawn from campaign links only.
      </p>
    {/if}
  </fieldset>

  <div class="field">
    <label class="field-name" for="home-title">Title</label>
    <input
      id="home-title"
      type="text"
      maxlength={HOME_TITLE_MAX}
      placeholder="Defaults to this site's host"
      disabled={draft.mode === 'default'}
      bind:value={draft.title}
    />
    <span class="count" class:over={draft.title.length >= HOME_TITLE_MAX}>
      {draft.title.length} / {HOME_TITLE_MAX}
    </span>
  </div>

  <div class="field">
    <label class="field-name" for="home-about">About text</label>
    <textarea
      id="home-about"
      rows="6"
      maxlength={HOME_ABOUT_MAX}
      disabled={draft.mode === 'default'}
      bind:value={draft.about}
    ></textarea>
    <span class="count" class:over={draft.about.length >= HOME_ABOUT_MAX}>
      {draft.about.length.toLocaleString()} / {HOME_ABOUT_MAX.toLocaleString()}
    </span>
    <p class="text-muted hint">
      Plain text. A blank line starts a new paragraph, and https:// addresses become links. No HTML
      or Markdown.
    </p>
  </div>

  <div class="actions">
    <Button variant="primary" disabled={saving || !dirty} onclick={save}>
      {saving ? 'Saving…' : 'Save home page'}
    </Button>
    {#if dirty}<span class="text-muted">Unsaved changes</span>{/if}
  </div>
  {#if saveError}<p class="text-error" role="alert">{saveError}</p>{/if}
  {#if notice}<p class="text-notice" role="status">{notice}</p>{/if}

  <h3 class="sub">Hero image</h3>
  <p class="text-muted hint">
    For the image author: a wide banner, <strong>2400×800</strong> (3:1), PNG or JPEG (WebP is
    accepted too), under 2 MB, between 1200×400 and 4000×2000 and 2:1 to 4:1 in shape. Keep the
    subject in the middle third, because phones show a 4:3 centre crop. No text in the image, so
    it stays readable at any width. It must work on both the light and dark page backgrounds.
    Export it the right way up: EXIF rotation is not applied, and all metadata is removed.
  </p>

  <div
    class="drop"
    class:dragging
    role="group"
    aria-label="Hero image"
    ondragover={(e) => {
      e.preventDefault();
      dragging = true;
    }}
    ondragleave={() => (dragging = false)}
    ondrop={onDrop}
  >
    {#if pendingUrl || hero}
      <img
        class="thumb"
        src={pendingUrl ?? hero?.url}
        alt={pendingUrl ? 'Selected image preview' : 'Current hero image'}
      />
    {/if}
    <div class="drop-body">
      <p class="drop-line">
        {#if pendingFile}
          <strong>{pendingFile.name}</strong>
          ({(pendingFile.size / 1024).toFixed(0)} KB{pendingSize
            ? `, ${pendingSize.width}×${pendingSize.height}`
            : ''})
        {:else if hero}
          Current image, {hero.width}×{hero.height}.
        {:else}
          Drag an image here, or choose a file.
        {/if}
      </p>
      <input
        bind:this={fileInput}
        class="file"
        type="file"
        accept="image/png,image/jpeg,image/webp"
        aria-label="Choose hero image"
        onchange={(e) => pick(e.currentTarget.files?.[0])}
      />
      <Button onclick={() => fileInput?.click()} disabled={heroBusy}>
        {hero || pendingFile ? 'Choose a different image' : 'Choose image'}
      </Button>
    </div>
  </div>

  <div class="field alt">
    <label class="field-name" for="hero-alt">Alt text</label>
    <input
      id="hero-alt"
      type="text"
      maxlength="200"
      disabled={pendingFile === null}
      placeholder={pendingFile ? "Describe the image for people who can't see it" : 'Choose an image to set its alt text'}
      bind:value={altDraft}
    />
    <span class="count">{altDraft.length} / 200</span>
  </div>

  <div class="actions">
    <Button
      variant="primary"
      disabled={heroBusy || pendingFile === null || altDraft.trim() === ''}
      onclick={upload}
    >
      {heroBusy && pendingFile ? 'Uploading…' : 'Upload image'}
    </Button>
    {#if pendingFile}
      <Button variant="subtle" disabled={heroBusy} onclick={clearPending}>Cancel</Button>
    {/if}
    {#if hero && !pendingFile}
      <Button variant="danger" disabled={heroBusy} onclick={() => (removeOpen = true)}>Remove image</Button>
    {/if}
  </div>
  {#if heroError}<p class="text-error" role="alert">{heroError}</p>{/if}

  <div class="preview-head">
    <h3 class="sub">Preview</h3>
    <div class="seg" role="group" aria-label="Preview width">
      <button type="button" class:on={!phoneWidth} aria-pressed={!phoneWidth} onclick={() => (phoneWidth = false)}>Desktop</button>
      <button type="button" class:on={phoneWidth} aria-pressed={phoneWidth} onclick={() => (phoneWidth = true)}>Phone</button>
    </div>
  </div>
  {#if draft.mode === 'default'}
    <p class="text-muted">
      In this mode visitors see the sign-in screen, or are sent to the Primary URL when registration
      is off. There is no page to preview.
    </p>
  {:else}
    {#if usingSample}
      <p class="text-muted hint">
        The preview uses sample numbers. Save this mode to see this site's real activity.
      </p>
    {/if}
    <div class="frame" class:phone={phoneWidth} data-testid="home-preview">
      {#if PublicHomeView}
        <PublicHomeView info={previewInfo} host={window.location.host} preview />
      {/if}
    </div>
  {/if}
</Panel>

<ConfirmDialog
  open={removeOpen}
  title="Remove hero image?"
  message="The home page will no longer show an image. You can upload a new one at any time."
  confirmLabel="Remove image"
  busyLabel="Removing…"
  onconfirm={removeHero}
  oncancel={() => (removeOpen = false)}
/>

<style>
  .lead {
    margin: 0 0 var(--space-4);
  }
  .field-name {
    font-weight: 600;
    display: block;
    margin-bottom: var(--space-1);
  }
  fieldset.modes {
    border: 0;
    margin: 0 0 var(--space-4);
    padding: 0;
    min-width: 0;
  }
  .modes legend {
    padding: 0;
  }
  .mode {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-1) 0;
    min-height: 32px;
    cursor: pointer;
    font-weight: 400;
    color: var(--text);
  }
  .warn {
    margin: var(--space-2) 0 0;
    padding: var(--space-2) var(--space-3);
    border: var(--border-w) solid var(--warning);
    border-radius: var(--radius);
    font-size: var(--fs-sm);
  }
  .field {
    margin: 0 0 var(--space-4);
  }
  .field input[type='text'],
  .field textarea {
    width: 100%;
  }
  .field textarea {
    resize: vertical;
    font-family: var(--font);
  }
  .count {
    display: block;
    text-align: right;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .count.over {
    color: var(--warning);
  }
  .hint {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
  }
  .actions {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-2);
  }
  .sub {
    font-size: var(--fs-md);
    font-weight: 600;
    margin: var(--space-5) 0 var(--space-2);
  }
  .drop {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-4);
    margin: var(--space-3) 0;
    padding: var(--space-4);
    border: 2px dashed var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-subtle);
  }
  .drop.dragging {
    border-color: var(--accent);
    background: var(--accent-subtle);
  }
  .thumb {
    width: 240px;
    max-width: 100%;
    aspect-ratio: 3 / 1;
    object-fit: cover;
    border-radius: var(--radius);
    border: var(--border-w) solid var(--border);
  }
  .drop-body {
    flex: 1 1 14rem;
    min-width: 0;
  }
  .drop-line {
    margin: 0 0 var(--space-2);
    overflow-wrap: anywhere;
  }
  .file {
    display: none;
  }
  .alt {
    margin-top: var(--space-3);
  }
  .preview-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
  }
  .preview-head .sub {
    margin-bottom: var(--space-2);
  }
  .seg {
    display: inline-flex;
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .seg button {
    font: inherit;
    font-size: var(--fs-sm);
    padding: var(--space-1) var(--space-3);
    min-height: 32px;
    border: 0;
    background: var(--bg-subtle);
    color: var(--text);
    cursor: pointer;
  }
  .seg button.on {
    background: var(--accent);
    color: var(--accent-text);
  }
  .frame {
    /* Positioned so the chart's visually hidden table (position: absolute) is clipped here. */
    position: relative;
    margin-top: var(--space-2);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-page);
    overflow: hidden;
  }
  .frame.phone {
    max-width: 390px;
    margin-inline: auto;
  }
  @media (max-width: 899px) {
    .mode {
      min-height: 40px;
    }
  }
</style>
