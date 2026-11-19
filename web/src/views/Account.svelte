<!--
  Account view (#0036). Manages the signed-in user's registered passkeys: it
  lists every credential, lets the user rename one inline, and revoke one — with
  the PRD guard that the LAST passkey cannot be revoked without adding a
  replacement first (the backend refuses that with 409
  `cannot_revoke_last_credential`, surfaced here as a clear, actionable message).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import {
    listCredentials,
    renameCredential,
    revokeCredential,
    logoutAll,
    ApiError,
  } from '../lib/api';
  import {
    syncLabel,
    formatMonthYear,
    validateDeviceName,
    canRevoke,
    revokeErrorMessage,
    ONLY_PASSKEY_NOTE,
  } from '../lib/account';
  import type { Credential } from '../lib/types';
  import { replace } from '../lib/hashRouter.svelte';
  import Button from '../lib/Button.svelte';
  import ConfirmDialog from '../lib/ConfirmDialog.svelte';
  import Panel from '../lib/Panel.svelte';
  import AccountRowMenu from '../lib/AccountRowMenu.svelte';
  import RelativeDate from '../lib/RelativeDate.svelte';
  import ApiTokens from '../lib/ApiTokens.svelte';
  import Preferences from '../lib/Preferences.svelte';

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
      // App.svelte's guard effect reacts to currentUser going null and
      // bounces to /login, preserving this route as the redirect target
      // (#0120) — no navigation call needed here.
      currentUser.set(null);
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

  // #0160: Revoke asks first via ConfirmDialog; confirming runs performRevoke.
  let revokeTarget = $state<Credential | null>(null);
  let signOutAllPrompt = $state(false);

  function handleRevoke(cred: Credential) {
    if (!revokeAllowed) return;
    revokeTarget = cred;
  }

  async function performRevoke(cred: Credential) {
    if (!revokeAllowed) return;
    revokingId = cred.id;
    clearRowError(cred.id);
    try {
      await revokeCredential(cred.id);
      credentials = credentials.filter((c) => c.id !== cred.id);
    } catch (err) {
      if (handleAuthError(err)) return;
      throw new Error(revokeErrorMessage(err));
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

  let signingOutAll = $state(false);
  let signOutAllError = $state<string | null>(null);

  // "Sign out everywhere" (#0094): revokes every session for this account —
  // this browser, any other browser, and the iPhone app — but never touches
  // enrolled passkeys. Destructive and not undoable, so it goes through a
  // confirmation step first, same as revoking a passkey.
  //
  // Unlike handleSignOut, a failure here must NOT be presented as success: the
  // whole point of the button is the guarantee that nothing is still
  // authenticated, and on a failed request the server-side transaction rolled
  // back, so every session — including the one on the device the user is
  // trying to lock out — is still live. Only a 401 (the session backing this
  // very request is already gone) legitimately takes the signed-out branch.
  function handleSignOutEverywhere() {
    signOutAllPrompt = true;
  }

  async function performSignOutEverywhere() {
    signingOutAll = true;
    signOutAllError = null;
    try {
      await logoutAll();
    } catch (err) {
      if (handleAuthError(err)) return;
      throw new Error('Could not sign out everywhere. Your other sessions may still be active. Please try again.');
    } finally {
      signingOutAll = false;
    }
    currentUser.set(null);
    links.set([]);
    replace('/login');
  }

  onMount(load);
</script>

<ConfirmDialog
  open={revokeTarget !== null}
  title="Remove passkey?"
  message={`You will no longer be able to sign in with "${revokeTarget?.device_name ?? ''}".`}
  confirmLabel="Remove passkey"
  busyLabel="Removing…"
  oncancel={() => (revokeTarget = null)}
  onconfirm={() => (revokeTarget ? performRevoke(revokeTarget) : undefined)}
/>
<ConfirmDialog
  open={signOutAllPrompt}
  title="Sign out everywhere?"
  message="This immediately signs out every device and app signed in to this account, including the iPhone app. Your passkeys are not affected; you can sign back in right away."
  confirmLabel="Sign out everywhere"
  busyLabel="Signing out…"
  oncancel={() => (signOutAllPrompt = false)}
  onconfirm={performSignOutEverywhere}
/>

<div class="app-shell account-shell">
  <h1 class="page-title">Account</h1>
  <Panel title="Account">
    {#if $currentUser}
      <p class="text-muted">Signed in as <strong>{$currentUser.email}</strong></p>
      <p class="text-muted intro">
        If a session is live somewhere you no longer control — a lost device, a
        shared computer — sign out everywhere to end every session at once,
        including the iPhone app. Your passkeys keep working; nothing about your
        account changes.
      </p>
      <Button variant="danger" disabled={signingOutAll} onclick={handleSignOutEverywhere}>
        {signingOutAll ? 'Signing out everywhere…' : 'Sign out everywhere'}
      </Button>
      {#if signOutAllError}
        <p class="text-error" role="alert">{signOutAllError}</p>
      {/if}
    {/if}
  </Panel>

  <Panel title="Passkeys">
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
                <div class="cred-line">
                  <span class="cred-name" title={cred.device_hint}>
                    {cred.device_name || 'Unnamed passkey'}
                  </span>
                  {#if cred.current}<span class="tag tag-current">This device</span>{/if}
                  <span class="tag">{syncLabel(cred)}</span>
                  <span class="cred-used text-muted">
                    {#if cred.last_used_at}
                      Used <RelativeDate date={cred.last_used_at} />
                    {:else}
                      Never used
                    {/if}
                    · Added {formatMonthYear(cred.created_at)}
                  </span>
                </div>
              {/if}

              {#if rowErrors[cred.id]}
                <p class="text-error" role="alert">{rowErrors[cred.id]}</p>
              {/if}
            </div>

            {#if editingId !== cred.id}
              <AccountRowMenu
                label={`Actions for ${cred.device_name || 'passkey'}`}
                busy={revokingId === cred.id}
                items={[
                  { label: 'Rename', onselect: () => startEdit(cred) },
                  {
                    label: 'Remove',
                    danger: true,
                    disabled: !revokeAllowed,
                    onselect: () => handleRevoke(cred),
                  },
                ]}
              />
            {/if}
          </li>
        {/each}
      </ul>
      {#if !revokeAllowed}
        <p class="text-muted note" data-testid="only-passkey-note">{ONLY_PASSKEY_NOTE}</p>
      {/if}
    {/if}
  </Panel>

  <ApiTokens />

  <Preferences />
</div>

<style>
  .account-shell {
    max-width: 760px;
  }
  .intro {
    margin: 0 0 var(--space-3);
  }
  .note {
    font-size: var(--fs-sm);
    margin: var(--space-3) 0 0;
  }
  /* One line per passkey (#0188, docs/ui-redesign.md 3.8). */
  .creds {
    list-style: none;
    margin: 0;
    padding: 0;
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .cred {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-subtle);
  }
  .cred + .cred {
    border-top: var(--border-w) solid var(--border);
  }
  .cred-main {
    flex: 1;
    min-width: 0;
  }
  .cred-line {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: var(--space-1) var(--space-3);
  }
  .cred-name {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .cred-used {
    font-size: var(--fs-sm);
  }
  .tag-current {
    color: var(--accent);
    border-color: var(--accent);
  }
  .edit-form {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex-wrap: wrap;
  }
</style>
