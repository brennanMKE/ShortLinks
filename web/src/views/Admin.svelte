<!--
  Admin view (#0037). Admin-only — rendered only when currentUser.is_admin; a
  non-admin who somehow reaches it sees an access-denied notice rather than any
  admin data. It ties together four backend areas behind a single view with a
  tab bar:

  - Settings   — GET/PATCH /admin/settings: a registrations_enabled toggle.
  - URL filters — GET/POST/PATCH/DELETE /admin/url-filters plus
    POST /admin/url-filters/test: list, create, edit, delete (with confirm), and
    a dry-run test tool.
  - Users      — GET /admin/users plus POST .../{id}/deactivate|reactivate:
    list with status, deactivate a non-admin (reason dropdown + note, note
    required for "other"), reactivate with an optional note.
  - Audit log  — GET /admin/audit?page=&per_page=&user_id=: a paginated,
    newest-first table with a per-user filter.

  All non-trivial pure logic (reason-code/value ↔ label maps, the
  other-requires-note validation, the test-result mapping, audit
  actor/target/metadata rendering, pagination math, the user-id filter parser)
  lives in lib/admin.ts and is unit-tested there. We match the Svelte 5 runes +
  error-handling style and the topbar/tabs of the other views for consistency.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, links } from '../lib/stores';
  import {
    getSettings,
    updateSetting,
    listFilterRules,
    createFilterRule,
    updateFilterRule,
    deleteFilterRule,
    testFilterRule,
    listUsers,
    deactivateUser,
    reactivateUser,
    listAudit,
    logout,
    ApiError,
  } from '../lib/api';
  import {
    REASON_OPTIONS,
    reasonLabel,
    DEACTIVATION_REASONS,
    deactivationReasonLabel,
    validateDeactivation,
    canDeactivate,
    filterTestNotice,
    type FilterTestNotice,
    actorLabel,
    targetLabel,
    formatMetadata,
    formatDateTime,
    pageInfo,
    parseUserIdFilter,
    registrationsEnabled,
  } from '../lib/admin';
  import type { Setting, FilterRule, AdminUser, AuditEntry } from '../lib/types';

  type Section = 'settings' | 'filters' | 'users' | 'audit';
  let section = $state<Section>('settings');

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

  async function toggleRegistrations() {
    togglingRegistrations = true;
    settingsNotice = null;
    settingsError = null;
    const next = regEnabled ? 'false' : 'true';
    try {
      const res = (await updateSetting('registrations_enabled', next)) as { settings: Setting[] };
      // The PATCH returns the authoritative full settings list.
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

  // ── URL filters ─────────────────────────────────────────────────────────────
  let rules = $state<FilterRule[]>([]);
  let rulesLoading = $state(true);
  let rulesError = $state<string | null>(null);

  // Create-rule form.
  let newPattern = $state('');
  let newReason = $state<number>(REASON_OPTIONS[0].code);
  let newDescription = $state('');
  let creating = $state(false);
  let createError = $state<string | null>(null);

  // Inline edit of one rule.
  let editingRuleId = $state<number | null>(null);
  let editPattern = $state('');
  let editReason = $state<number>(REASON_OPTIONS[0].code);
  let editDescription = $state('');
  let editActive = $state(true);
  let savingRule = $state(false);
  let ruleRowError = $state<Record<number, string>>({});

  // Test tool.
  let testUrl = $state('');
  let testing = $state(false);
  let testNotice = $state<FilterTestNotice | null>(null);
  let testError = $state<string | null>(null);

  async function loadRules() {
    rulesLoading = true;
    rulesError = null;
    try {
      rules = (await listFilterRules()).rules;
    } catch (err) {
      if (handleAuthError(err)) return;
      rulesError = 'Could not load filter rules. Please try again.';
    } finally {
      rulesLoading = false;
    }
  }

  async function handleCreateRule(e: SubmitEvent) {
    e.preventDefault();
    createError = null;
    if (newPattern.trim() === '') {
      createError = 'A pattern is required.';
      return;
    }
    creating = true;
    try {
      await createFilterRule({
        pattern: newPattern.trim(),
        reason_code: newReason,
        description: newDescription.trim(),
      });
      newPattern = '';
      newDescription = '';
      newReason = REASON_OPTIONS[0].code;
      await loadRules();
    } catch (err) {
      if (handleAuthError(err)) return;
      if (err instanceof ApiError && err.status === 400) {
        createError = err.message || 'The pattern must be a valid regular expression.';
        return;
      }
      createError = 'Could not create the rule. Please try again.';
    } finally {
      creating = false;
    }
  }

  function startEditRule(rule: FilterRule) {
    editingRuleId = rule.id;
    editPattern = rule.pattern;
    editReason = rule.reason_code;
    editDescription = rule.description;
    editActive = rule.active;
    clearRuleRowError(rule.id);
  }

  function cancelEditRule() {
    editingRuleId = null;
  }

  async function saveEditRule(rule: FilterRule) {
    if (editPattern.trim() === '') {
      ruleRowError = { ...ruleRowError, [rule.id]: 'A pattern is required.' };
      return;
    }
    savingRule = true;
    clearRuleRowError(rule.id);
    try {
      await updateFilterRule(rule.id, {
        pattern: editPattern.trim(),
        reason_code: editReason,
        description: editDescription.trim(),
        active: editActive,
      });
      editingRuleId = null;
      await loadRules();
    } catch (err) {
      if (handleAuthError(err)) return;
      const msg =
        err instanceof ApiError && err.status === 400
          ? err.message || 'The pattern must be a valid regular expression.'
          : 'Could not update the rule. Please try again.';
      ruleRowError = { ...ruleRowError, [rule.id]: msg };
    } finally {
      savingRule = false;
    }
  }

  async function handleDeleteRule(rule: FilterRule) {
    if (!confirm(`Delete the rule "${rule.pattern}"? This cannot be undone.`)) return;
    clearRuleRowError(rule.id);
    try {
      await deleteFilterRule(rule.id);
      await loadRules();
    } catch (err) {
      if (handleAuthError(err)) return;
      ruleRowError = {
        ...ruleRowError,
        [rule.id]: 'Could not delete the rule. Please try again.',
      };
    }
  }

  function clearRuleRowError(id: number) {
    if (id in ruleRowError) {
      const { [id]: _removed, ...rest } = ruleRowError;
      ruleRowError = rest;
    }
  }

  async function handleTest(e: SubmitEvent) {
    e.preventDefault();
    testError = null;
    testNotice = null;
    if (testUrl.trim() === '') {
      testError = 'Enter a URL to test.';
      return;
    }
    testing = true;
    try {
      const result = await testFilterRule(testUrl.trim());
      testNotice = filterTestNotice(result);
    } catch (err) {
      if (handleAuthError(err)) return;
      testError = 'Could not run the test. Please try again.';
    } finally {
      testing = false;
    }
  }

  // ── Users ─────────────────────────────────────────────────────────────────
  let users = $state<AdminUser[]>([]);
  let usersLoading = $state(true);
  let usersError = $state<string | null>(null);

  // Deactivation modal state.
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

  // ── Audit log ───────────────────────────────────────────────────────────────
  const AUDIT_PER_PAGE = 50;
  let auditEntries = $state<AuditEntry[]>([]);
  let auditTotal = $state(0);
  let auditPage = $state(1);
  let auditLoading = $state(true);
  let auditError = $state<string | null>(null);
  let auditUserIdRaw = $state('');
  let auditUserIdFilter = $state<number | null>(null);
  let auditFilterError = $state<string | null>(null);
  const auditPaging = $derived(pageInfo(auditTotal, auditPage, AUDIT_PER_PAGE));

  async function loadAudit() {
    auditLoading = true;
    auditError = null;
    try {
      const res = await listAudit(auditPage, AUDIT_PER_PAGE, auditUserIdFilter ?? undefined);
      auditEntries = res.audit_log;
      auditTotal = res.total;
      auditPage = res.page;
    } catch (err) {
      if (handleAuthError(err)) return;
      auditError = 'Could not load the audit log. Please try again.';
    } finally {
      auditLoading = false;
    }
  }

  function applyAuditFilter(e: SubmitEvent) {
    e.preventDefault();
    const parsed = parseUserIdFilter(auditUserIdRaw);
    if ('error' in parsed) {
      auditFilterError = parsed.error;
      return;
    }
    auditFilterError = null;
    auditUserIdFilter = parsed.userId;
    auditPage = 1;
    void loadAudit();
  }

  function clearAuditFilter() {
    auditUserIdRaw = '';
    auditUserIdFilter = null;
    auditFilterError = null;
    auditPage = 1;
    void loadAudit();
  }

  function auditGoTo(page: number) {
    auditPage = page;
    void loadAudit();
  }

  // ── Shared ──────────────────────────────────────────────────────────────────
  function handleAuthError(err: unknown): boolean {
    if (err instanceof ApiError && err.status === 401) {
      currentUser.set(null);
      currentView.set('login');
      return true;
    }
    return false;
  }

  function go(view: 'dashboard' | 'account') {
    currentView.set(view);
  }

  async function handleSignOut() {
    try {
      await logout();
    } catch {
      // Drop local state regardless of the server result.
    }
    currentUser.set(null);
    links.set([]);
    currentView.set('login');
  }

  onMount(() => {
    void loadSettings();
    void loadRules();
    void loadUsers();
    void loadAudit();
  });
</script>

<div class="admin">
  <header class="topbar">
    <h1 class="wordmark">go.sstools.co</h1>
    <nav class="tabs" aria-label="Primary">
      <button type="button" class="tab" onclick={() => go('dashboard')}>Dashboard</button>
      <button type="button" class="tab" onclick={() => go('account')}>Account</button>
      <button type="button" class="tab active" aria-current="page">Admin</button>
    </nav>
    <button type="button" class="signout" onclick={handleSignOut}>Sign out</button>
  </header>

  {#if !$currentUser?.is_admin}
    <section class="card">
      <h2>Admin</h2>
      <p class="error" role="alert">You do not have access to this area.</p>
    </section>
  {:else}
    <nav class="subtabs" aria-label="Admin sections">
      <button
        type="button"
        class="subtab"
        class:active={section === 'settings'}
        aria-current={section === 'settings' ? 'page' : undefined}
        onclick={() => (section = 'settings')}>Settings</button
      >
      <button
        type="button"
        class="subtab"
        class:active={section === 'filters'}
        aria-current={section === 'filters' ? 'page' : undefined}
        onclick={() => (section = 'filters')}>URL filters</button
      >
      <button
        type="button"
        class="subtab"
        class:active={section === 'users'}
        aria-current={section === 'users' ? 'page' : undefined}
        onclick={() => (section = 'users')}>Users</button
      >
      <button
        type="button"
        class="subtab"
        class:active={section === 'audit'}
        aria-current={section === 'audit' ? 'page' : undefined}
        onclick={() => (section = 'audit')}>Audit log</button
      >
    </nav>

    {#if section === 'settings'}
      <section class="card">
        <h2>Settings</h2>
        {#if settingsLoading}
          <p class="muted" role="status">Loading settings…</p>
        {:else if settingsError}
          <p class="error" role="alert">{settingsError}</p>
          <button type="button" class="primary" onclick={loadSettings}>Retry</button>
        {:else}
          <div class="setting-row">
            <div>
              <span class="setting-name">Registrations enabled</span>
              <p class="muted small">
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
          {#if settingsNotice}
            <p class="notice" role="status">{settingsNotice}</p>
          {/if}
        {/if}
      </section>
    {/if}

    {#if section === 'filters'}
      <section class="card">
        <h2>Test a URL</h2>
        <p class="muted small intro">
          Check whether a destination URL would be blocked by the active rules. This is a dry run —
          nothing is created.
        </p>
        <form class="test-form" onsubmit={handleTest}>
          <label class="sr-only" for="test-url">URL to test</label>
          <input
            id="test-url"
            type="text"
            placeholder="https://example.com/path"
            bind:value={testUrl}
            disabled={testing}
          />
          <button type="submit" class="primary" disabled={testing}>
            {testing ? 'Testing…' : 'Test'}
          </button>
        </form>
        {#if testError}
          <p class="error small" role="alert">{testError}</p>
        {:else if testNotice}
          <p class={testNotice.kind === 'match' ? 'error small' : 'notice'} role="status">
            {testNotice.message}
          </p>
        {/if}
      </section>

      <section class="card">
        <h2>Add a rule</h2>
        <form class="rule-form" onsubmit={handleCreateRule}>
          <div class="field">
            <label for="new-pattern">Pattern (Go regular expression)</label>
            <input
              id="new-pattern"
              type="text"
              placeholder="(?i)malware\\.example\\.com"
              bind:value={newPattern}
              disabled={creating}
            />
          </div>
          <div class="field">
            <label for="new-reason">Reason</label>
            <select id="new-reason" bind:value={newReason} disabled={creating}>
              {#each REASON_OPTIONS as opt (opt.code)}
                <option value={opt.code}>{opt.label}</option>
              {/each}
            </select>
          </div>
          <div class="field">
            <label for="new-description">Description</label>
            <input
              id="new-description"
              type="text"
              placeholder="Known malware host"
              bind:value={newDescription}
              disabled={creating}
            />
          </div>
          <button type="submit" class="primary" disabled={creating}>
            {creating ? 'Adding…' : 'Add rule'}
          </button>
        </form>
        {#if createError}
          <p class="error small" role="alert">{createError}</p>
        {/if}
      </section>

      <section class="card">
        <h2>Filter rules</h2>
        {#if rulesLoading}
          <p class="muted" role="status">Loading rules…</p>
        {:else if rulesError}
          <p class="error" role="alert">{rulesError}</p>
          <button type="button" class="primary" onclick={loadRules}>Retry</button>
        {:else if rules.length === 0}
          <p class="muted">No filter rules defined.</p>
        {:else}
          <table class="data-table">
            <thead>
              <tr>
                <th>Pattern</th>
                <th>Reason</th>
                <th>Description</th>
                <th>Active</th>
                <th class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {#each rules as rule (rule.id)}
                <tr>
                  {#if editingRuleId === rule.id}
                    <td><input type="text" bind:value={editPattern} disabled={savingRule} /></td>
                    <td>
                      <select bind:value={editReason} disabled={savingRule}>
                        {#each REASON_OPTIONS as opt (opt.code)}
                          <option value={opt.code}>{opt.label}</option>
                        {/each}
                      </select>
                    </td>
                    <td><input type="text" bind:value={editDescription} disabled={savingRule} /></td>
                    <td>
                      <label class="inline-check">
                        <input type="checkbox" bind:checked={editActive} disabled={savingRule} />
                        Active
                      </label>
                    </td>
                    <td class="actions-col">
                      <button
                        type="button"
                        class="primary small"
                        disabled={savingRule}
                        onclick={() => saveEditRule(rule)}>{savingRule ? 'Saving…' : 'Save'}</button
                      >
                      <button
                        type="button"
                        class="ghost small"
                        disabled={savingRule}
                        onclick={cancelEditRule}>Cancel</button
                      >
                    </td>
                  {:else}
                    <td class="mono">{rule.pattern}</td>
                    <td>{rule.reason_label || reasonLabel(rule.reason_code)}</td>
                    <td>{rule.description || '—'}</td>
                    <td>{rule.active ? 'Yes' : 'No'}</td>
                    <td class="actions-col">
                      <button type="button" class="ghost small" onclick={() => startEditRule(rule)}
                        >Edit</button
                      >
                      <button type="button" class="danger small" onclick={() => handleDeleteRule(rule)}
                        >Delete</button
                      >
                    </td>
                  {/if}
                </tr>
                {#if ruleRowError[rule.id]}
                  <tr>
                    <td colspan="5"><p class="error small" role="alert">{ruleRowError[rule.id]}</p></td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        {/if}
      </section>
    {/if}

    {#if section === 'users'}
      <section class="card">
        <h2>Users</h2>
        {#if usersLoading}
          <p class="muted" role="status">Loading users…</p>
        {:else if usersError}
          <p class="error" role="alert">{usersError}</p>
          <button type="button" class="primary" onclick={loadUsers}>Retry</button>
        {:else if users.length === 0}
          <p class="muted">No users found.</p>
        {:else}
          <table class="data-table">
            <thead>
              <tr>
                <th>Email</th>
                <th>Admin</th>
                <th>Status</th>
                <th>Last login</th>
                <th class="actions-col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {#each users as user (user.id)}
                <tr>
                  <td>{user.email}</td>
                  <td>{user.is_admin ? 'Admin' : '—'}</td>
                  <td>
                    <span class="status" class:inactive={!user.active}>
                      {user.active ? 'Active' : 'Inactive'}
                    </span>
                  </td>
                  <td>{user.last_login_at ? formatDateTime(user.last_login_at) : 'Never'}</td>
                  <td class="actions-col">
                    {#if canDeactivate(user, $currentUser?.id ?? -1)}
                      <button type="button" class="danger small" onclick={() => openDeactivate(user)}
                        >Deactivate</button
                      >
                    {:else if !user.active}
                      <button
                        type="button"
                        class="ghost small"
                        disabled={reactivatingId === user.id}
                        onclick={() => handleReactivate(user)}
                        >{reactivatingId === user.id ? 'Reactivating…' : 'Reactivate'}</button
                      >
                    {:else}
                      <span class="muted small">—</span>
                    {/if}
                  </td>
                </tr>
                {#if userRowError[user.id]}
                  <tr>
                    <td colspan="5"><p class="error small" role="alert">{userRowError[user.id]}</p></td>
                  </tr>
                {/if}
              {/each}
            </tbody>
          </table>
        {/if}
      </section>

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
            <h2>Deactivate {deactivatingUser.email}</h2>
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
                <p class="error small" role="alert">{deactError}</p>
              {/if}
              <div class="modal-actions">
                <button type="submit" class="danger" disabled={deactSubmitting}>
                  {deactSubmitting ? 'Deactivating…' : 'Deactivate'}
                </button>
                <button type="button" class="ghost" disabled={deactSubmitting} onclick={closeDeactivate}
                  >Cancel</button
                >
              </div>
            </form>
          </div>
        </div>
      {/if}
    {/if}

    {#if section === 'audit'}
      <section class="card">
        <h2>Audit log</h2>
        <form class="filter-form" onsubmit={applyAuditFilter}>
          <label for="audit-user">Filter by user id</label>
          <input
            id="audit-user"
            type="text"
            inputmode="numeric"
            placeholder="e.g. 5"
            bind:value={auditUserIdRaw}
          />
          <button type="submit" class="primary small">Apply</button>
          {#if auditUserIdFilter !== null}
            <button type="button" class="ghost small" onclick={clearAuditFilter}>Clear</button>
          {/if}
        </form>
        {#if auditFilterError}
          <p class="error small" role="alert">{auditFilterError}</p>
        {/if}

        {#if auditLoading}
          <p class="muted" role="status">Loading audit log…</p>
        {:else if auditError}
          <p class="error" role="alert">{auditError}</p>
          <button type="button" class="primary" onclick={loadAudit}>Retry</button>
        {:else if auditEntries.length === 0}
          <p class="muted">No audit entries.</p>
        {:else}
          <table class="data-table">
            <thead>
              <tr>
                <th>When</th>
                <th>Action</th>
                <th>Actor</th>
                <th>Target</th>
                <th>Metadata</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              {#each auditEntries as entry (entry.id)}
                <tr>
                  <td>{formatDateTime(entry.created_at)}</td>
                  <td class="mono">{entry.action}</td>
                  <td>{actorLabel(entry)}</td>
                  <td>{targetLabel(entry)}</td>
                  <td class="meta-cell">{formatMetadata(entry.metadata)}</td>
                  <td>{entry.ip_address ?? '—'}</td>
                </tr>
              {/each}
            </tbody>
          </table>

          <div class="pager">
            <button
              type="button"
              class="ghost small"
              disabled={!auditPaging.hasPrev}
              onclick={() => auditGoTo(auditPaging.page - 1)}>Previous</button
            >
            <span class="muted small">
              {auditPaging.firstItem}–{auditPaging.lastItem} of {auditPaging.total} · page
              {auditPaging.page} of {auditPaging.totalPages}
            </span>
            <button
              type="button"
              class="ghost small"
              disabled={!auditPaging.hasNext}
              onclick={() => auditGoTo(auditPaging.page + 1)}>Next</button
            >
          </div>
        {/if}
      </section>
    {/if}
  {/if}
</div>

<style>
  .admin {
    max-width: 60rem;
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
  .subtabs {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1.5rem;
    flex-wrap: wrap;
  }
  .subtab {
    background: #fff;
    border: 1px solid #ccc;
    border-bottom-width: 2px;
    border-radius: 0.375rem;
    padding: 0.5rem 1rem;
    cursor: pointer;
    font-size: 0.9375rem;
  }
  .subtab.active {
    border-bottom-color: #1f6feb;
    color: #1f6feb;
    font-weight: 600;
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
  .intro {
    margin: 0 0 1rem;
  }
  .setting-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
  }
  .setting-name {
    font-weight: 600;
  }
  .toggle {
    flex-shrink: 0;
    min-width: 4rem;
    border: 1px solid #ccc;
    border-radius: 1rem;
    padding: 0.375rem 0.875rem;
    background: #eee;
    cursor: pointer;
    font-size: 0.875rem;
    font-weight: 600;
  }
  .toggle.on {
    background: #1f6feb;
    border-color: #1f6feb;
    color: #fff;
  }
  .toggle:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    margin-bottom: 0.875rem;
  }
  .field label {
    font-size: 0.8125rem;
    font-weight: 600;
    color: #444;
  }
  .field input,
  .field select,
  .field textarea {
    padding: 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 0.9375rem;
    font-family: inherit;
  }
  .test-form,
  .filter-form {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    flex-wrap: wrap;
  }
  .test-form input {
    flex: 1;
    min-width: 12rem;
    padding: 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 0.9375rem;
  }
  .filter-form label {
    font-size: 0.875rem;
    font-weight: 600;
  }
  .filter-form input {
    padding: 0.375rem 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 0.875rem;
    width: 8rem;
  }
  .data-table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.875rem;
  }
  .data-table th,
  .data-table td {
    text-align: left;
    padding: 0.5rem;
    border-bottom: 1px solid #eee;
    vertical-align: top;
  }
  .data-table th {
    color: #666;
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .data-table input,
  .data-table select {
    width: 100%;
    padding: 0.3125rem 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 0.8125rem;
  }
  .mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.8125rem;
    overflow-wrap: anywhere;
  }
  .meta-cell {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.75rem;
    overflow-wrap: anywhere;
    max-width: 18rem;
  }
  .actions-col {
    white-space: nowrap;
  }
  .inline-check {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.8125rem;
  }
  .inline-check input {
    width: auto;
  }
  .status {
    display: inline-block;
    padding: 0.125rem 0.5rem;
    border-radius: 0.75rem;
    background: #e6f4ea;
    color: #1a7f37;
    font-size: 0.75rem;
    font-weight: 600;
  }
  .status.inactive {
    background: #fbe9e7;
    color: #c0362c;
  }
  .pager {
    display: flex;
    align-items: center;
    gap: 1rem;
    margin-top: 1rem;
  }
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.4);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1rem;
    z-index: 10;
  }
  .modal {
    background: #fff;
    border-radius: 0.5rem;
    padding: 1.5rem;
    width: 100%;
    max-width: 28rem;
  }
  .modal h2 {
    margin: 0 0 1rem;
    font-size: 1.125rem;
    overflow-wrap: anywhere;
  }
  .modal textarea {
    resize: vertical;
  }
  .modal-actions {
    display: flex;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }
  .muted {
    color: #888;
  }
  .small {
    font-size: 0.8125rem;
  }
  .notice {
    color: #1a7f37;
    font-size: 0.875rem;
    margin: 0.75rem 0 0;
  }
  .primary {
    padding: 0.5rem 0.875rem;
    border: none;
    border-radius: 0.375rem;
    background: #1f6feb;
    color: #fff;
    font-size: 0.9375rem;
    cursor: pointer;
  }
  .primary:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .ghost {
    background: #fff;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    padding: 0.5rem 0.875rem;
    cursor: pointer;
    font-size: 0.9375rem;
  }
  .ghost:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .danger {
    border: 1px solid #c0362c;
    border-radius: 0.375rem;
    background: #fff;
    color: #c0362c;
    padding: 0.5rem 0.875rem;
    cursor: pointer;
    font-size: 0.9375rem;
  }
  .danger:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .primary.small,
  .ghost.small,
  .danger.small {
    padding: 0.3125rem 0.625rem;
    font-size: 0.8125rem;
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
