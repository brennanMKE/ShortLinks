<!--
  Account view (#0036). Manages the signed-in user's registered passkeys: it
  lists every credential, lets the user rename one inline, and revoke one — with
  the PRD guard that the LAST passkey cannot be revoked without adding a
  replacement first (the backend refuses that with 409
  `cannot_revoke_last_credential`, surfaced here as a clear, actionable message).

  There is no logged-in "add passkey" endpoint in the routes — per the PRD a new
  credential is added through the account-recovery flow — so this view is
  list / rename / revoke only, matching the issue's acceptance criteria.

  On mount it loads GET /account/credentials via listCredentials, surfacing
  loading / error / empty states. Each row shows the device name (inline
  editable), the AAGUID-derived device hint (computed server-side, returned as
  `device_hint`), created + last-used dates, and the sign count. Rename calls
  PATCH /account/credentials/{id}; Revoke calls DELETE /account/credentials/{id}.

  All non-trivial pure logic (date / last-used formatting, rename validation,
  the last-credential guard, and the revoke-failure → message mapping) lives in
  lib/account.ts and is unit-tested there. We match the Svelte 5 runes + error
  handling style of LinkDetail.svelte / Dashboard.svelte, and the topbar / tabs
  for nav consistency with the other views.
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

  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let credentials = $state<Credential[]>([]);

  // The id of the credential whose name is being edited inline, plus the draft
  // value and a per-edit error. Only one row is editable at a time.
  let editingId = $state<number | null>(null);
  let editingName = $state('');
  let editError = $state<string | null>(null);
  let saving = $state(false);

  // Per-credential transient state keyed by id: a revoke in flight, and the most
  // recent revoke error to show on that row (e.g. the 409 last-credential copy).
  let revokingId = $state<number | null>(null);
  let rowErrors = $state<Record<number, string>>({});

  // The last passkey cannot be revoked (PRD). The backend enforces this with a
  // 409; we also disable the control client-side for immediate feedback.
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

  // Returns true (and routes to login) when the error is a 401 session expiry,
  // so callers can early-return; false otherwise.
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
      // Update the row in place with the authoritative server record.
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
      // The 409 last-credential case yields the clear, actionable message; any
      // other failure gets the generic retry copy (see lib/account.ts).
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
      // Even if the server call fails, drop local state and return to login.
    }
    currentUser.set(null);
    links.set([]);
    currentView.set('login');
  }

  onMount(load);
</script>

<div class="account">
  <header class="topbar">
    <h1 class="wordmark">go.sstools.co</h1>
    <nav class="tabs" aria-label="Primary">
      <button type="button" class="tab" onclick={() => go('dashboard')}>Dashboard</button>
      <button type="button" class="tab active" aria-current="page">Account</button>
      {#if $currentUser?.is_admin}
        <button type="button" class="tab" onclick={() => go('admin')}>Admin</button>
      {/if}
    </nav>
    <button type="button" class="signout" onclick={handleSignOut}>Sign out</button>
  </header>

  <section class="card">
    <h2>Account</h2>
    {#if $currentUser}
      <p class="muted email">Signed in as <strong>{$currentUser.email}</strong></p>
    {/if}
  </section>

  <section class="card">
    <h2>Passkeys</h2>
    <p class="muted intro">
      These are the passkeys registered to your account. Add a new passkey through
      account recovery; you cannot revoke your only passkey.
    </p>

    {#if loading}
      <p class="muted" role="status">Loading passkeys…</p>
    {:else if loadError}
      <p class="error" role="alert">{loadError}</p>
      <button type="button" class="primary" onclick={load}>Retry</button>
    {:else if credentials.length === 0}
      <p class="muted">No passkeys found for this account.</p>
    {:else}
      <ul class="creds">
        {#each credentials as cred (cred.id)}
          <li class="cred">
            <div class="cred-main">
              {#if editingId === cred.id}
                <form
                  class="edit"
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
                  />
                  <button type="submit" class="primary small" disabled={saving}>
                    {saving ? 'Saving…' : 'Save'}
                  </button>
                  <button type="button" class="ghost small" disabled={saving} onclick={cancelEdit}>
                    Cancel
                  </button>
                </form>
                {#if editError}
                  <p class="error small" role="alert">{editError}</p>
                {/if}
              {:else}
                <div class="name-row">
                  <span class="name">{cred.device_name || 'Unnamed passkey'}</span>
                  <button type="button" class="ghost small" onclick={() => startEdit(cred)}>
                    Rename
                  </button>
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
                <p class="error small" role="alert">{rowErrors[cred.id]}</p>
              {/if}
            </div>

            <div class="cred-actions">
              <button
                type="button"
                class="danger small"
                disabled={!revokeAllowed || revokingId === cred.id}
                title={revokeAllowed
                  ? 'Revoke this passkey'
                  : 'You cannot revoke your only passkey. Add another passkey first.'}
                onclick={() => handleRevoke(cred)}
              >
                {revokingId === cred.id ? 'Revoking…' : 'Revoke'}
              </button>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</div>

<style>
  .account {
    max-width: 48rem;
    margin: 0 auto;
    padding: 1rem;
  }
  .topbar {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }
  .wordmark {
    font-size: 1.125rem;
    margin: 0;
  }
  .tabs {
    display: flex;
    gap: 0.5rem;
    flex: 1;
  }
  .tab {
    background: none;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    padding: 0.375rem 0.75rem;
    cursor: pointer;
    font-size: 0.875rem;
  }
  .tab.active {
    background: #1f6feb;
    color: #fff;
    border-color: #1f6feb;
    cursor: default;
  }
  .signout {
    background: none;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    padding: 0.375rem 0.75rem;
    cursor: pointer;
    font-size: 0.875rem;
  }
  .card {
    border: 1px solid #e2e2e2;
    border-radius: 0.5rem;
    padding: 1.25rem;
    margin-bottom: 1.5rem;
  }
  .card h2 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
  }
  .email {
    margin: 0;
  }
  .intro {
    margin: 0 0 1rem;
    font-size: 0.875rem;
  }
  .creds {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  .cred {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    border: 1px solid #eee;
    border-radius: 0.5rem;
    padding: 1rem;
  }
  .cred-main {
    flex: 1;
    min-width: 0;
  }
  .name-row {
    display: flex;
    align-items: center;
    gap: 0.625rem;
    margin-bottom: 0.75rem;
  }
  .name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .edit {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.5rem;
    flex-wrap: wrap;
  }
  .edit input {
    flex: 1;
    min-width: 8rem;
    padding: 0.375rem 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 0.9375rem;
  }
  .meta {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
    gap: 0.5rem 1rem;
    margin: 0;
  }
  .meta dt {
    color: #666;
    font-weight: 600;
    font-size: 0.75rem;
  }
  .meta dd {
    margin: 0;
    font-size: 0.875rem;
    overflow-wrap: anywhere;
  }
  .num {
    font-variant-numeric: tabular-nums;
  }
  .cred-actions {
    flex-shrink: 0;
  }
  .muted {
    color: #888;
  }
  .primary {
    margin-top: 0.5rem;
    padding: 0.5rem 0.875rem;
    border: none;
    border-radius: 0.375rem;
    background: #1f6feb;
    color: #fff;
    font-size: 0.9375rem;
    cursor: pointer;
  }
  .ghost {
    background: #fff;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    cursor: pointer;
  }
  .danger {
    border: 1px solid #c0362c;
    border-radius: 0.375rem;
    background: #fff;
    color: #c0362c;
    cursor: pointer;
  }
  .danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .small {
    padding: 0.3125rem 0.625rem;
    font-size: 0.8125rem;
    margin-top: 0;
  }
  .primary.small:disabled,
  .ghost.small:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .error {
    color: #c0362c;
    font-size: 0.875rem;
    margin: 0.5rem 0;
  }
  .error.small {
    font-size: 0.8125rem;
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }
</style>
