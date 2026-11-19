<!--
  Admin view (#0037). Admin-only — rendered only when currentUser.is_admin; a
  non-admin who somehow reaches it sees an access-denied notice rather than any
  admin data. It ties together four backend areas behind one view. Each section is its own route (#0154: #/admin/settings,
  /filters, /users, /audit; bare #/admin redirects to settings), with a side
  nav on desktop and a select on phones:

  - Settings   — GET/PATCH /admin/settings: a registrations_enabled toggle and
    the home_redirect_url primary URL (#0125).
  - URL filters — lib/AdminFilters.svelte (#0171): an inline "Test a URL" that
    highlights the matching rule, rules in a table, add/edit in a dialog.
  - Users      — GET /admin/users plus POST .../{id}/deactivate|reactivate:
    list with status, deactivate a non-admin (reason dropdown + note, note
    required for "other"), reactivate with an optional note.
  - Audit log  — lib/AdminAudit.svelte (#0171): GET /admin/audit filtered by
    user email, action and date range, with the filters held in the hash.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import {
    getSettings,
    updateSetting,
    listUsers,
    deactivateUser,
    reactivateUser,
    ApiError,
  } from '../lib/api';
  import {
    DEACTIVATION_REASONS,
    deactivationReasonLabel,
    validateDeactivation,
    canDeactivate,
    formatDateTime,
    registrationsEnabled,
    ADMIN_SECTIONS,
    DEFAULT_ADMIN_SECTION,
    adminRouteAction,
    adminSectionPath,
    type AdminSection,
  } from '../lib/admin';
  import { navigate, replace } from '../lib/hashRouter.svelte';
  import Link from '../lib/Link.svelte';
  import NotFound from './NotFound.svelte';
  import type { Setting, AdminUser } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import AdminFilters from '../lib/AdminFilters.svelte';
  import AdminAudit from '../lib/AdminAudit.svelte';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import type { Column } from '../lib/dataTable';

  // The section comes from the route (#0154): `#/admin/:section`, and bare
  // `#/admin` redirects to the default section.
  let { section: sectionParam }: { section?: string } = $props();
  const action = $derived(adminRouteAction(sectionParam));
  const section: AdminSection = $derived(
    action.kind === 'show' ? action.section : DEFAULT_ADMIN_SECTION,
  );

  // ── Settings ──────────────────────────────────────────────────────────────
  let settings = $state<Setting[]>([]);
  let settingsLoading = $state(true);
  let settingsError = $state<string | null>(null);
  let settingsNotice = $state<string | null>(null);
  let togglingRegistrations = $state(false);
  const regEnabled = $derived(
    registrationsEnabled(settings.find((s) => s.key === 'registrations_enabled')?.value),
  );

  async function loadSettings() {
    settingsLoading = true;
    settingsError = null;
    try {
      settings = (await getSettings()).settings;
    } catch (err) {
      if (handleAuthError(err)) return;
      settingsError = 'Could not load settings. Please try again.';
    } finally {
      settingsLoading = false;
    }
  }

  // Primary URL (#0125): the draft is initialized from the saved value after
  // each load/save and compared against it to enable Save.
  let homeRedirectDraft = $state('');
  let savingHomeRedirect = $state(false);
  let homeRedirectError = $state<string | null>(null);
  const homeRedirectSaved = $derived(
    settings.find((s) => s.key === 'home_redirect_url')?.value ?? '',
  );
  $effect(() => {
    homeRedirectDraft = homeRedirectSaved;
  });

  async function saveHomeRedirect(value: string) {
    savingHomeRedirect = true;
    settingsNotice = null;
    homeRedirectError = null;
    try {
      const res = (await updateSetting('home_redirect_url', value.trim())) as {
        settings: Setting[];
      };
      if (res && Array.isArray(res.settings)) settings = res.settings;
      else await loadSettings();
      settingsNotice = value.trim() === '' ? 'Primary URL cleared.' : 'Primary URL saved.';
    } catch (err) {
      if (handleAuthError(err)) return;
      homeRedirectError =
        err instanceof ApiError && err.status === 400
          ? err.message
          : 'Could not update the setting. Please try again.';
    } finally {
      savingHomeRedirect = false;
    }
  }

  async function toggleRegistrations() {
    togglingRegistrations = true;
    settingsNotice = null;
    settingsError = null;
    const next = regEnabled ? 'false' : 'true';
    try {
      const res = (await updateSetting('registrations_enabled', next)) as { settings: Setting[] };
      if (res && Array.isArray(res.settings)) settings = res.settings;
      else await loadSettings();
      settingsNotice = `Registrations ${next === 'true' ? 'enabled' : 'disabled'}.`;
    } catch (err) {
      if (handleAuthError(err)) return;
      settingsError = 'Could not update the setting. Please try again.';
    } finally {
      togglingRegistrations = false;
    }
  }

  // ── Users ─────────────────────────────────────────────────────────────────
  // Users list: a table at desktop width, cards below 900px (#0173).
  const columns: Column<AdminUser>[] = [
    { key: 'email', label: 'Email', mobile: 'title' },
    { key: 'status', label: 'Status', width: '7rem', mobile: 'metric' },
    { key: 'last', label: 'Last login', width: '11rem', mobile: 'meta' },
    { key: 'actions', label: 'Actions', width: '9rem', align: 'right', hideLabel: true, mobile: 'actions' },
  ];

  let users = $state<AdminUser[]>([]);
  let usersLoading = $state(true);
  let usersError = $state<string | null>(null);

  let deactivatingUser = $state<AdminUser | null>(null);
  let deactReason = $state<string>(DEACTIVATION_REASONS[0].value);
  let deactNote = $state('');
  let deactError = $state<string | null>(null);
  let deactSubmitting = $state(false);
  const deactNoteRequired = $derived(deactReason === 'other');

  let userRowError = $state<Record<number, string>>({});
  let reactivatingId = $state<number | null>(null);

  async function loadUsers() {
    usersLoading = true;
    usersError = null;
    try {
      users = (await listUsers()).users;
    } catch (err) {
      if (handleAuthError(err)) return;
      usersError = 'Could not load users. Please try again.';
    } finally {
      usersLoading = false;
    }
  }

  function openDeactivate(user: AdminUser) {
    deactivatingUser = user;
    deactReason = DEACTIVATION_REASONS[0].value;
    deactNote = '';
    deactError = null;
  }

  function closeDeactivate() {
    deactivatingUser = null;
  }

  async function submitDeactivate(e: SubmitEvent) {
    e.preventDefault();
    if (!deactivatingUser) return;
    const result = validateDeactivation(deactReason, deactNote);
    if ('error' in result) {
      deactError = result.error;
      return;
    }
    deactSubmitting = true;
    deactError = null;
    const target = deactivatingUser;
    try {
      const updated = await deactivateUser(target.id, deactReason, result.note);
      users = users.map((u) => (u.id === updated.id ? updated : u));
      deactivatingUser = null;
    } catch (err) {
      if (handleAuthError(err)) return;
      deactError =
        err instanceof ApiError && err.message
          ? err.message
          : 'Could not deactivate the user. Please try again.';
    } finally {
      deactSubmitting = false;
    }
  }

  async function handleReactivate(user: AdminUser) {
    const note = prompt('Optional note for reactivation (leave blank to skip):') ?? '';
    reactivatingId = user.id;
    clearUserRowError(user.id);
    try {
      const updated = await reactivateUser(user.id, note.trim());
      users = users.map((u) => (u.id === updated.id ? updated : u));
    } catch (err) {
      if (handleAuthError(err)) return;
      userRowError = {
        ...userRowError,
        [user.id]: 'Could not reactivate the user. Please try again.',
      };
    } finally {
      reactivatingId = null;
    }
  }

  function clearUserRowError(id: number) {
    if (id in userRowError) {
      const { [id]: _removed, ...rest } = userRowError;
      userRowError = rest;
    }
  }

  // ── Shared ──────────────────────────────────────────────────────────────────
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

  onMount(() => {
    if (action.kind === 'redirect') {
      replace(action.to);
      return;
    }
    if (action.kind === 'not-found') return;
    void loadSettings();
    void loadUsers();
  });
</script>

{#if action.kind === 'not-found' && $currentUser?.is_admin}
  <NotFound />
{:else if action.kind === 'redirect'}
  <div class="app-shell"><p class="text-muted" role="status">Loading…</p></div>
{:else}
<div class="app-shell">
  <h1 class="page-title">Admin</h1>
  {#if !$currentUser?.is_admin}
    <Panel title="Admin">
      <p class="text-error" role="alert">You do not have access to this area.</p>
    </Panel>
  {:else}
    <div class="admin-layout">
    <nav class="admin-nav" aria-label="Admin sections">
      {#each ADMIN_SECTIONS as s (s.id)}
        <Link to={adminSectionPath(s.id)} class="admin-nav-link">{s.label}</Link>
      {/each}
    </nav>
    <select
      class="admin-select"
      aria-label="Admin section"
      value={section}
      onchange={(e) => navigate(adminSectionPath(e.currentTarget.value as AdminSection))}
    >
      {#each ADMIN_SECTIONS as s (s.id)}
        <option value={s.id}>{s.label}</option>
      {/each}
    </select>
    <div class="admin-main">
    {#if section === 'settings'}
      <Panel title="Settings">
        {#if settingsLoading}
          <p class="text-muted" role="status">Loading settings…</p>
        {:else if settingsError}
          <p class="text-error" role="alert">{settingsError}</p>
          <Button variant="primary" onclick={loadSettings}>Retry</Button>
        {:else}
          <div class="setting-row">
            <div class="setting-info">
              <span class="setting-name">Registrations enabled</span>
              <p class="text-muted setting-desc">
                When on, the registration form accepts new users. When off, the server is locked to
                existing users.
              </p>
            </div>
            <button
              type="button"
              class="toggle"
              class:on={regEnabled}
              role="switch"
              aria-checked={regEnabled}
              disabled={togglingRegistrations}
              onclick={toggleRegistrations}
            >
              {togglingRegistrations ? 'Saving…' : regEnabled ? 'On' : 'Off'}
            </button>
          </div>
          <div class="setting-row home-redirect-row">
            <div class="setting-info">
              <label class="setting-name" for="home-redirect-url">Primary URL</label>
              <p class="text-muted setting-desc">
                Where a signed-out visitor to the home page is sent when registration is off. Leave
                empty to show the sign-in screen instead.
              </p>
              <div class="home-redirect-controls">
                <input
                  type="url"
                  id="home-redirect-url"
                  inputmode="url"
                  placeholder="https://www.example.com/"
                  bind:value={homeRedirectDraft}
                  disabled={savingHomeRedirect}
                />
                <Button
                  variant="primary"
                  disabled={savingHomeRedirect || homeRedirectDraft.trim() === homeRedirectSaved}
                  onclick={() => saveHomeRedirect(homeRedirectDraft)}
                >
                  {savingHomeRedirect ? 'Saving…' : 'Save'}
                </Button>
                {#if homeRedirectSaved !== ''}
                  <Button
                    variant="subtle"
                    disabled={savingHomeRedirect}
                    onclick={() => saveHomeRedirect('')}
                  >
                    Clear
                  </Button>
                {/if}
              </div>
              {#if homeRedirectError}
                <p class="text-error" role="alert">{homeRedirectError}</p>
              {/if}
              {#if regEnabled}
                <p class="text-muted setting-desc">
                  Registration is on, so the home page shows the sign-in screen regardless.
                </p>
              {/if}
            </div>
          </div>
          {#if settingsNotice}
            <p class="text-notice" role="status">{settingsNotice}</p>
          {/if}
        {/if}
      </Panel>
    {/if}

    {#if section === 'filters'}
      <AdminFilters onAuthError={handleAuthError} />
    {/if}

    {#if section === 'users'}
      <Panel title="Users" noPadding={users.length > 0 && !usersLoading && !usersError}>
        {#if usersLoading}
          <p class="text-muted" role="status">Loading users…</p>
        {:else if usersError}
          <p class="text-error" role="alert">{usersError}</p>
          <Button variant="primary" onclick={loadUsers}>Retry</Button>
        {:else if users.length === 0}
          <p class="text-muted">No users found.</p>
        {:else}
          <DataTable {columns} rows={users} rowKey={(u) => String(u.id)} caption="Users">
            {#snippet cell(user, col)}
              {#if col.key === 'email'}
                <TwoLineCell title={user.email} meta={user.is_admin ? 'Admin' : ''} dim={!user.active} />
                {#if userRowError[user.id]}
                  <p class="text-error row-err" role="alert">{userRowError[user.id]}</p>
                {/if}
              {:else if col.key === 'status'}
                <span class="badge" class:badge-success={user.active} class:badge-danger={!user.active}>
                  {user.active ? 'Active' : 'Inactive'}
                </span>
              {:else if col.key === 'last'}
                {user.last_login_at ? formatDateTime(user.last_login_at) : 'Never logged in'}
              {:else if col.key === 'actions'}
                {#if canDeactivate(user, $currentUser?.id ?? -1)}
                  <Button variant="danger" onclick={() => openDeactivate(user)}>Deactivate</Button>
                {:else if !user.active}
                  <Button disabled={reactivatingId === user.id} onclick={() => handleReactivate(user)}>
                    {reactivatingId === user.id ? 'Reactivating…' : 'Reactivate'}
                  </Button>
                {:else}
                  <span class="text-muted">—</span>
                {/if}
              {/if}
            {/snippet}
          </DataTable>
        {/if}
      </Panel>

      {#if deactivatingUser}
        <div
          class="modal-backdrop"
          role="presentation"
          onclick={closeDeactivate}
          onkeydown={(e) => {
            if (e.key === 'Escape') closeDeactivate();
          }}
        >
          <div
            class="modal"
            role="dialog"
            tabindex="-1"
            aria-modal="true"
            aria-label="Deactivate user"
            onclick={(e) => e.stopPropagation()}
            onkeydown={(e) => e.stopPropagation()}
          >
            <h2 class="modal-title">Deactivate {deactivatingUser.email}</h2>
            <form onsubmit={submitDeactivate}>
              <div class="field">
                <label for="deact-reason">Reason</label>
                <select
                  id="deact-reason"
                  bind:value={deactReason}
                  disabled={deactSubmitting}
                  onchange={() => (deactError = null)}
                >
                  {#each DEACTIVATION_REASONS as r (r.value)}
                    <option value={r.value}>{r.label}</option>
                  {/each}
                </select>
              </div>
              <div class="field">
                <label for="deact-note">
                  Note {deactNoteRequired ? '(required)' : '(optional)'}
                </label>
                <textarea
                  id="deact-note"
                  rows="3"
                  bind:value={deactNote}
                  disabled={deactSubmitting}
                  oninput={() => (deactError = null)}
                ></textarea>
              </div>
              {#if deactError}
                <p class="text-error" role="alert">{deactError}</p>
              {/if}
              <div class="row" style="margin-top: var(--space-3);">
                <Button type="submit" variant="danger" disabled={deactSubmitting}>
                  {deactSubmitting ? 'Deactivating…' : 'Deactivate'}
                </Button>
                <Button disabled={deactSubmitting} onclick={closeDeactivate}>Cancel</Button>
              </div>
            </form>
          </div>
        </div>
      {/if}
    {/if}

    {#if section === 'audit'}
      <AdminAudit {users} onAuthError={handleAuthError} />
    {/if}
    </div>
    </div>
  {/if}
</div>
{/if}

<style>
  /* Section nav (#0154): left rail on desktop, a select on phones. */
  .admin-layout {
    display: grid;
    grid-template-columns: 180px minmax(0, 1fr);
    gap: var(--space-5, 24px);
    align-items: start;
  }
  .admin-nav {
    display: flex;
    flex-direction: column;
    gap: var(--space-1, 4px);
    position: sticky;
    top: var(--space-4);
  }
  .admin-nav :global(.admin-nav-link) {
    display: block;
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius);
    border-left: 3px solid transparent;
    color: var(--text-muted);
    text-decoration: none;
    font-size: var(--fs-md);
  }
  .admin-nav :global(.admin-nav-link:hover) {
    background: var(--bg-subtle);
    color: var(--text);
  }
  .admin-nav :global(.admin-nav-link.active) {
    border-left-color: var(--accent);
    color: var(--accent);
    font-weight: 600;
    background: var(--bg-subtle);
  }
  .admin-select {
    display: none;
    width: 100%;
    min-height: 44px;
    font-size: var(--fs-md);
  }
  .admin-main {
    min-width: 0;
  }
  @media (max-width: 720px) {
    .admin-layout {
      grid-template-columns: minmax(0, 1fr);
      gap: var(--space-3);
    }
    .admin-nav {
      display: none;
    }
    .admin-select {
      display: block;
    }
  }

  @media (max-width: 480px) {
  }
  .setting-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-4);
  }
  .setting-info {
    flex: 1;
    min-width: 0;
  }
  .home-redirect-row {
    margin-top: var(--space-4);
  }
  .home-redirect-controls {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
  .home-redirect-controls input {
    flex: 1 1 14rem;
    min-width: 0;
  }
  .setting-name {
    font-weight: 600;
  }
  .setting-desc {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
  }
  @media (max-width: 899px) {
    .toggle {
      min-height: 40px;
    }
  }
  .row-err {
    margin: var(--space-1) 0 0;
    white-space: normal;
  }
  .toggle {
    flex-shrink: 0;
    min-width: 4rem;
    border: var(--border-w) solid var(--border-strong);
    border-radius: 10px;
    padding: var(--space-1) var(--space-3);
    background: var(--bg-subtle);
    cursor: pointer;
    font-size: var(--fs-sm);
    font-weight: 600;
    font-family: var(--font);
    color: var(--text-muted);
  }
  .toggle.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .toggle:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: var(--space-4);
    z-index: 10;
  }
  .modal {
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-5);
    width: 100%;
    max-width: 420px;
  }
  .modal-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0 0 var(--space-4);
    overflow-wrap: anywhere;
  }

</style>
