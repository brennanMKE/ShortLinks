<!--
  API Tokens panel (Account view). Lets the signed-in user mint a named token
  for a script or agent, see their tokens' status and last use, and revoke one.
  The secret is shown exactly once, right after creation, and lives only in
  this component's state until the user dismisses it — it is never put in a
  store, URL, or browser storage, and the server cannot return it again.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser } from './stores';
  import { listApiTokens, createApiToken, revokeApiToken, ApiError } from './api';
  import {
    EXPIRY_CHOICES,
    DEFAULT_EXPIRY,
    expiresAtFor,
    validateTokenName,
    createErrorMessage,
    expiryLabel,
    statusLabel,
    splitTokens,
    expiryWarning,
    curlExample,
    type ExpiryChoice,
  } from './apiTokens';
  import { formatDate, lastUsedLabel } from './account';
  import type { ApiToken } from './types';
  import Button from './Button.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import Panel from './Panel.svelte';
  import AccountRowMenu from './AccountRowMenu.svelte';

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let tokens = $state<ApiToken[]>([]);
  const split = $derived(splitTokens(tokens));

  let showForm = $state(false);
  let name = $state('');
  let readOnly = $state(false);
  let expiry = $state<ExpiryChoice>(DEFAULT_EXPIRY);
  let customDate = $state('');
  let formError = $state<string | null>(null);
  let creating = $state(false);

  // The one-time secret. Cleared by dismissSecret().
  let newSecret = $state<string | null>(null);
  let newTokenName = $state('');
  let copied = $state<'token' | 'curl' | null>(null);

  let revokingId = $state<number | null>(null);
  let rowErrors = $state<Record<number, string>>({});

  const baseUrl = $derived($currentUser?.base_url || window.location.origin);
  const tomorrow = new Date(Date.now() + 24 * 60 * 60 * 1000).toISOString().slice(0, 10);

  function handleAuthError(err: unknown): boolean {
    if (err instanceof ApiError && err.status === 401) {
      currentUser.set(null);
      return true;
    }
    return false;
  }

  async function load() {
    loading = true;
    loadError = null;
    try {
      tokens = await listApiTokens();
    } catch (err) {
      if (handleAuthError(err)) return;
      loadError = 'Could not load your API tokens. Please try again.';
    } finally {
      loading = false;
    }
  }

  function openForm() {
    showForm = true;
    name = '';
    readOnly = false;
    expiry = DEFAULT_EXPIRY;
    customDate = '';
    formError = null;
  }

  async function handleCreate(e: SubmitEvent) {
    e.preventDefault();
    const n = validateTokenName(name);
    if ('error' in n) {
      formError = n.error;
      return;
    }
    const exp = expiresAtFor(expiry, customDate);
    if ('error' in exp) {
      formError = exp.error;
      return;
    }
    creating = true;
    formError = null;
    try {
      const created = await createApiToken({ name: n.value, read_only: readOnly, expires_at: exp.value });
      tokens = [created.api_token, ...tokens];
      newSecret = created.token;
      newTokenName = created.api_token.name;
      showForm = false;
    } catch (err) {
      if (handleAuthError(err)) return;
      formError = createErrorMessage(err);
    } finally {
      creating = false;
    }
  }

  async function copy(text: string, which: 'token' | 'curl') {
    try {
      await navigator.clipboard.writeText(text);
      copied = which;
      setTimeout(() => {
        if (copied === which) copied = null;
      }, 1500);
    } catch {
      // Clipboard may be unavailable (insecure context / permissions); the
      // value is still selectable in the field.
    }
  }

  function dismissSecret() {
    newSecret = null;
    newTokenName = '';
    copied = null;
  }

  // #0160: Revoke asks first via ConfirmDialog; confirming runs performRevoke.
  let revokeTarget = $state<ApiToken | null>(null);

  function handleRevoke(token: ApiToken) {
    revokeTarget = token;
  }

  async function performRevoke(token: ApiToken) {
    revokingId = token.id;
    if (token.id in rowErrors) {
      const { [token.id]: _removed, ...rest } = rowErrors;
      rowErrors = rest;
    }
    try {
      const revoked = await revokeApiToken(token.id);
      tokens = tokens.map((t) => (t.id === revoked.id ? revoked : t));
    } catch (err) {
      if (handleAuthError(err)) return;
      if (err instanceof ApiError && err.status === 404) {
        await load();
        return;
      }
      throw new Error('Could not revoke this token. Please try again.');
    } finally {
      revokingId = null;
    }
  }

  onMount(load);
</script>

<ConfirmDialog
  open={revokeTarget !== null}
  title="Revoke API token?"
  message={`Anything using "${revokeTarget?.name ?? ''}" stops working immediately. This cannot be undone.`}
  confirmLabel="Revoke token"
  busyLabel="Revoking…"
  oncancel={() => (revokeTarget = null)}
  onconfirm={() => (revokeTarget ? performRevoke(revokeTarget) : undefined)}
/>

{#snippet tokenRow(token: ApiToken)}
  {@const warning = expiryWarning(token)}
  <li class="token" class:inactive={token.status !== 'active'}>
    <div class="token-main">
      <div class="token-line">
        <span class="token-name">{token.name}</span>
        {#if token.read_only}<span class="tag">Read-only</span>{/if}
        {#if token.status !== 'active'}<span class="tag">{statusLabel(token)}</span>{/if}
        <code class="mono prefix">{token.prefix}…</code>
        <span class="text-muted last-used">
          {#if token.status === 'revoked' && token.revoked_at}
            Revoked {formatDate(token.revoked_at)}
          {:else if token.status === 'expired'}
            {expiryLabel(token)}
          {:else if token.last_used_at}
            Used {lastUsedLabel(token.last_used_at)}{token.last_used_ip ? ` from ${token.last_used_ip}` : ''}
          {:else}
            Never used
          {/if}
        </span>
      </div>
      {#if warning}
        <div class="tag tag-warn" title="Expires {formatDate(token.expires_at)}">⚠ {warning}</div>
      {/if}
      {#if rowErrors[token.id]}
        <p class="text-error" role="alert">{rowErrors[token.id]}</p>
      {/if}
    </div>
    {#if token.status === 'active'}
      <AccountRowMenu
        label="Actions for {token.name}"
        busy={revokingId === token.id}
        items={[{ label: 'Revoke', danger: true, onselect: () => handleRevoke(token) }]}
      />
    {/if}
  </li>
{/snippet}

<Panel title="API tokens">
  <p class="text-muted intro">
    An API token lets a script or agent use the API as you: create campaigns and
    short links, read stats, and so on. It cannot manage your passkeys, other
    tokens, or admin settings. Treat a token like a password and revoke it
    if it might be exposed.
  </p>

  {#if newSecret}
    <div class="secret" role="status">
      <p class="secret-title">
        Token <strong>{newTokenName}</strong> created. Copy it now; you won't be able to see it again.
      </p>
      <div class="secret-row">
        <label class="sr-only" for="new-token-secret">New API token</label>
        <input
          id="new-token-secret"
          class="mono"
          type="text"
          readonly
          value={newSecret}
          autocomplete="off"
          spellcheck="false"
          onfocus={(e) => (e.currentTarget as HTMLInputElement).select()}
        />
        <Button variant="primary" onclick={() => copy(newSecret!, 'token')}>
          {copied === 'token' ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <p class="text-muted small">Send it in the <code>Authorization</code> header:</p>
      <div class="secret-row">
        <code class="mono example">{curlExample(baseUrl, newSecret)}</code>
        <Button variant="default" onclick={() => copy(curlExample(baseUrl, newSecret!), 'curl')}>
          {copied === 'curl' ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <Button variant="default" onclick={dismissSecret}>Done, I've saved it</Button>
    </div>
  {/if}

  {#if showForm}
    <form class="create-form" onsubmit={handleCreate}>
      <div class="field">
        <label for="token-name">Name</label>
        <input
          id="token-name"
          type="text"
          bind:value={name}
          placeholder="e.g. marketing-agent"
          maxlength="100"
          disabled={creating}
          autocomplete="off"
        />
      </div>
      <div class="field">
        <label for="token-expiry">Expiration</label>
        <select id="token-expiry" bind:value={expiry} disabled={creating}>
          {#each EXPIRY_CHOICES as c (c.value)}
            <option value={c.value}>{c.label}</option>
          {/each}
        </select>
      </div>
      {#if expiry === 'custom'}
        <div class="field">
          <label for="token-expiry-date">Expires on</label>
          <input id="token-expiry-date" type="date" min={tomorrow} bind:value={customDate} disabled={creating} />
        </div>
      {/if}
      {#if expiry === 'never'}
        <p class="text-muted small">This token will work until you revoke it.</p>
      {/if}
      <label class="check">
        <input type="checkbox" bind:checked={readOnly} disabled={creating} />
        Read-only (can view links, campaigns, and stats but not change anything)
      </label>
      {#if formError}
        <p class="text-error" role="alert">{formError}</p>
      {/if}
      <div class="form-actions">
        <Button type="submit" variant="primary" disabled={creating}>
          {creating ? 'Creating…' : 'Create token'}
        </Button>
        <Button variant="default" disabled={creating} onclick={() => (showForm = false)}>Cancel</Button>
      </div>
    </form>
  {:else}
    <Button variant="primary" onclick={openForm}>New API token</Button>
  {/if}

  {#if loading}
    <p class="text-muted" role="status">Loading API tokens…</p>
  {:else if loadError}
    <p class="text-error" role="alert">{loadError}</p>
    <Button variant="primary" onclick={load}>Retry</Button>
  {:else if tokens.length > 0}
    {#if split.active.length > 0}
      <ul class="tokens">
        {#each split.active as token (token.id)}
          {@render tokenRow(token)}
        {/each}
      </ul>
    {:else}
      <p class="text-muted small">No active tokens.</p>
    {/if}
    {#if split.inactive.length > 0}
      <details class="inactive-tokens">
        <summary>
          {split.inactive.length} revoked or expired {split.inactive.length === 1 ? 'token' : 'tokens'}
        </summary>
        <ul class="tokens">
          {#each split.inactive as token (token.id)}
            {@render tokenRow(token)}
          {/each}
        </ul>
      </details>
    {/if}
  {/if}
  <p class="text-muted small docs-link">
    Using a token from an agent?
    <a href="https://github.com/brennanMKE/ShortLinks/blob/main/docs/mcp.md" target="_blank" rel="noopener">
      Read the MCP setup guide
    </a>.
  </p>
</Panel>

<style>
  .intro {
    margin: 0 0 var(--space-3);
  }
  .small {
    font-size: var(--fs-sm);
    margin: var(--space-2) 0;
  }
  .mono {
    font-family: var(--font-mono);
  }
  .secret {
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-subtle);
    padding: var(--space-3) var(--space-4);
    margin-bottom: var(--space-3);
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
    align-items: flex-start;
  }
  .secret-title {
    margin: 0;
  }
  .secret-row {
    display: flex;
    gap: var(--space-2);
    align-items: center;
    width: 100%;
    min-width: 0;
  }
  .secret-row input {
    flex: 1;
    min-width: 0;
  }
  .example {
    flex: 1;
    min-width: 0;
    overflow-wrap: anywhere;
    font-size: var(--fs-sm);
  }
  .create-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    max-width: 28rem;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
  }
  .field label {
    font-weight: 600;
    font-size: var(--fs-sm);
  }
  .check {
    display: flex;
    gap: var(--space-2);
    align-items: flex-start;
    font-size: var(--fs-sm);
  }
  .check input {
    margin-top: 0.2em;
  }
  .form-actions {
    display: flex;
    gap: var(--space-2);
  }
  .tokens {
    list-style: none;
    margin: var(--space-3) 0 0;
    padding: 0;
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .token {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-subtle);
  }
  .token + .token {
    border-top: var(--border-w) solid var(--border);
  }
  .token.inactive {
    opacity: 0.7;
  }
  .token-main {
    flex: 1;
    min-width: 0;
  }
  .token-line {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-3);
  }
  .token-name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .prefix {
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .last-used {
    font-size: var(--fs-sm);
  }
  .tag {
    display: inline-block;
    font-size: var(--fs-sm);
    border: var(--border-w) solid var(--border-strong);
    border-radius: 999px;
    padding: 0 var(--space-2);
    color: var(--text-muted);
  }
  .tag-warn {
    margin-top: var(--space-1);
    color: var(--warning);
    border-color: var(--warning);
  }
  .inactive-tokens {
    margin-top: var(--space-3);
  }
  .inactive-tokens summary {
    cursor: pointer;
    color: var(--text-muted);
    font-size: var(--fs-sm);
  }
  .docs-link {
    margin-top: var(--space-3);
  }
  .docs-link a {
    color: var(--accent);
  }
  @media (max-width: 899px) {
    /* Padding on an inline link widens its tap area without moving the text. */
    .docs-link a {
      padding: 12px 2px;
    }
  }
  @media (max-width: 480px) {
    .secret-row {
      flex-direction: column;
      align-items: stretch;
    }
  }
</style>
