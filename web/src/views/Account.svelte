<!--
  Account view (#0036). Manages the signed-in user's registered passkeys: it
  lists every credential, lets the user rename one inline, and revoke one — with
  the PRD guard that the LAST passkey cannot be revoked without adding a
  replacement first (the backend refuses that with 409
  `cannot_revoke_last_credential`, surfaced here as a clear, actionable message).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, links } from '../lib/stores';
  import { listCredentials, renameCredential, revokeCredential, logout, ApiError } from '../lib/api';
  import {
    formatDate,
    lastUsedLabel,
    validateDeviceName,
    canRevoke,
    revokeErrorMessage,
  } from '../lib/account';
  import type { Credential } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let credentials = $state<Credential[]>([]);

  let editingId = $state<number | null>(null);
  let editingName = $state('');
  let editError = $state<string | null>(null);
  let saving = $state(false);

  let revokingId = $state<number | null>(null);
  let rowErrors = $state<Record<number, string>>({});

  const revokeAllowed = $derived(canRevoke(credentials));

  async function load() {
    loading = true;
    loadError = null;
    try {
      credentials = await listCredentials();
    } catch (err) {
      if (handleAuthError(err)) return;
      loadError = 'Could not load your passkeys. Please try again.';
    } finally {
      loading = false;
    }
  }

  function handleAuthError(err: unknown): boolean {
    if (err instanceof ApiError && err.status === 401) {
      currentUser.set(null);
      currentView.set('login');
      return true;
    }
    return false;
  }

  function startEdit(cred: Credential) {
    editingId = cred.id;
    editingName = cred.device_name;
    editError = null;
  }

  function cancelEdit() {
    editingId = null;
    editingName = '';
    editError = null;
  }

  async function saveEdit(cred: Credential) {
    const result = validateDeviceName(editingName);
    if ('error' in result) {
      editError = result.error;
      return;
    }
    saving = true;
    editError = null;
    try {
      const updated = await renameCredential(cred.id, result.value);
      credentials = credentials.map((c) => (c.id === updated.id ? updated : c));
      editingId = null;
      editingName = '';
    } catch (err) {
      if (handleAuthError(err)) return;
      if (err instanceof ApiError && err.status === 404) {
        editError = 'This passkey no longer exists.';
        return;
      }
      editError = 'Could not rename this passkey. Please try again.';
    } finally {
      saving = false;
    }
  }

  async function handleRevoke(cred: Credential) {
    if (!revokeAllowed) return;
    if (!confirm(`Revoke "${cred.device_name}"? You will no longer be able to sign in with it.`)) {
      return;
    }
    revokingId = cred.id;
    clearRowError(cred.id);
    try {
      await revokeCredential(cred.id);
      credentials = credentials.filter((c) => c.id !== cred.id);
    } catch (err) {
      if (handleAuthError(err)) return;
      rowErrors = { ...rowErrors, [cred.id]: revokeErrorMessage(err) };
    } finally {
      revokingId = null;
    }
  }

  function clearRowError(id: number) {
    if (id in rowErrors) {
      const { [id]: _removed, ...rest } = rowErrors;
      rowErrors = rest;
    }
  }

  function go(view: 'dashboard' | 'admin') {
    currentView.set(view);
  }

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // Drop local state regardless of server result.
    }
    currentUser.set(null);
    links.set([]);
    currentView.set('login');
  }

  onMount(load);
</script>

<div class="app-shell account-shell">
  <header class="app-header">
    <h1 class="app-title">go.sstools.co</h1>
    <nav class="nav-tabs" aria-label="Primary">
      <button type="button" class="nav-tab" onclick={() => go('dashboard')}>Dashboard</button>
      <button type="button" class="nav-tab active" aria-current="page">Account</button>
      {#if $currentUser?.is_admin}
        <button type="button" class="nav-tab" onclick={() => go('admin')}>Admin</button>
      {/if}
    </nav>
    <Button variant="default" onclick={handleSignOut}>Sign out</Button>
  </header>

  <Panel title="Account">
    {#if $currentUser}
      <p class="text-muted">Signed in as <strong>{$currentUser.email}</strong></p>
    {/if}
  </Panel>

  <Panel title="Passkeys">
    <p class="text-muted intro">
      These are the passkeys registered to your account. Add a new passkey through
      account recovery; you cannot revoke your only passkey.
    </p>

    {#if loading}
      <p class="text-muted" role="status">Loading passkeys…</p>
    {:else if loadError}
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="primary" onclick={load}>Retry</Button>
    {:else if credentials.length === 0}
      <p class="text-muted">No passkeys found for this account.</p>
    {:else}
      <ul class="creds">
        {#each credentials as cred (cred.id)}
          <li class="cred">
            <div class="cred-main">
              {#if editingId === cred.id}
                <form
                  class="edit-form"
                  onsubmit={(e) => {
                    e.preventDefault();
                    saveEdit(cred);
                  }}
                >
                  <label class="sr-only" for={`name-${cred.id}`}>Passkey name</label>
                  <input
                    id={`name-${cred.id}`}
                    type="text"
                    bind:value={editingName}
                    disabled={saving}
                    oninput={() => {
                      editError = null;
                    }}
                    style="width: auto; flex: 1; min-width: 8rem;"
                  />
                  <Button type="submit" variant="primary" disabled={saving}>
                    {saving ? 'Saving…' : 'Save'}
                  </Button>
                  <Button variant="default" disabled={saving} onclick={cancelEdit}>Cancel</Button>
                </form>
                {#if editError}
                  <p class="text-error" role="alert">{editError}</p>
                {/if}
              {:else}
                <div class="name-row">
                  <span class="cred-name">{cred.device_name || 'Unnamed passkey'}</span>
                  <Button variant="subtle" onclick={() => startEdit(cred)}>Rename</Button>
                </div>
              {/if}

              <dl class="meta">
                <div>
                  <dt>Device</dt>
                  <dd>{cred.device_hint}</dd>
                </div>
                <div>
                  <dt>Added</dt>
                  <dd>{formatDate(cred.created_at)}</dd>
                </div>
                <div>
                  <dt>Last used</dt>
                  <dd>{lastUsedLabel(cred.last_used_at)}</dd>
                </div>
                <div>
                  <dt>Sign count</dt>
                  <dd class="num">{cred.sign_count}</dd>
                </div>
              </dl>

              {#if rowErrors[cred.id]}
                <p class="text-error" role="alert">{rowErrors[cred.id]}</p>
              {/if}
            </div>

            <div class="cred-actions">
              <Button
                variant="danger"
                disabled={!revokeAllowed || revokingId === cred.id}
                onclick={() => handleRevoke(cred)}
              >
                {revokingId === cred.id ? 'Revoking…' : 'Revoke'}
              </Button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </Panel>
</div>

<style>
  .account-shell {
    max-width: 760px;
  }
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
  .intro {
    margin: 0 0 var(--space-3);
  }
  .creds {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
  }
  .cred {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-3);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-3) var(--space-4);
    background: var(--bg-subtle);
  }
  .cred-main {
    flex: 1;
    min-width: 0;
  }
  .name-row {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-2);
  }
  .cred-name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .edit-form {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-2);
    flex-wrap: wrap;
  }
  .meta {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
    gap: var(--space-2) var(--space-4);
    margin: 0;
  }
  .meta dt {
    color: var(--text-muted);
    font-weight: 600;
    font-size: var(--fs-sm);
  }
  .meta dd {
    margin: 0;
    font-size: var(--fs-base);
    overflow-wrap: anywhere;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  .cred-actions {
    flex-shrink: 0;
  }
</style>
