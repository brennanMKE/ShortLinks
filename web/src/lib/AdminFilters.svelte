<!--
  Admin → URL filters (#0171). "Test a URL" sits inline above the rule table and
  a hit highlights (and scrolls to) the rule that blocked it. Rules are added and
  edited in a dialog; Active is a switch in the row, Edit/Delete live in the ⋯
  menu. The server evaluates the rules (POST /admin/url-filters/test); this view
  only displays which one matched.
-->
<script lang="ts">
  import { tick, onMount } from 'svelte';
  import {
    listFilterRules,
    createFilterRule,
    updateFilterRule,
    deleteFilterRule,
    testFilterRule,
    ApiError,
  } from './api';
  import { REASON_OPTIONS, reasonLabel, filterTestNotice, type FilterTestNotice } from './admin';
  import type { FilterRule } from './types';
  import Button from './Button.svelte';
  import Panel from './Panel.svelte';
  import Dialog from './Dialog.svelte';
  import ConfirmDialog from './ConfirmDialog.svelte';
  import DataTable from './DataTable.svelte';
  import TwoLineCell from './TwoLineCell.svelte';
  import RowMenu from './RowMenu.svelte';
  import type { Column } from './dataTable';

  interface Props {
    /** Returns true when the error was a 401 and has been handled. */
    onAuthError: (err: unknown) => boolean;
  }
  let { onAuthError }: Props = $props();

  let rules = $state<FilterRule[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let rowError = $state<string | null>(null);
  let togglingId = $state<number | null>(null);

  // ── Test a URL ──
  let testUrl = $state('');
  let testing = $state(false);
  let testNotice = $state<FilterTestNotice | null>(null);
  let testError = $state<string | null>(null);
  const hitId = $derived(testNotice?.kind === 'match' ? testNotice.ruleId : null);
  const hitRule = $derived(hitId === null ? undefined : rules.find((r) => r.id === hitId));

  // ── Add / edit dialog ──
  let dialog = $state<{ mode: 'add' } | { mode: 'edit'; rule: FilterRule } | null>(null);
  let fPattern = $state('');
  let fReason = $state<number>(REASON_OPTIONS[0].code);
  let fDescription = $state('');
  let fActive = $state(true);
  let saving = $state(false);
  let formError = $state<string | null>(null);

  const columns: Column<FilterRule>[] = [
    { key: 'pattern', label: 'Pattern', sortable: true, value: (r) => r.pattern, mobile: 'title' },
    {
      key: 'reason',
      label: 'Reason',
      width: '12rem',
      sortable: true,
      value: (r) => reasonOf(r),
      mobile: 'meta',
    },
    { key: 'active', label: 'Active', width: '5.5rem', mobile: 'metric' },
    { key: 'actions', label: 'Actions', width: '3.5rem', hideLabel: true, mobile: 'actions' },
  ];

  function reasonOf(r: FilterRule): string {
    return r.reason_label || reasonLabel(r.reason_code);
  }

  async function load() {
    loading = true;
    loadError = null;
    try {
      rules = (await listFilterRules()).rules;
    } catch (err) {
      if (onAuthError(err)) return;
      loadError = 'Could not load filter rules. Please try again.';
    } finally {
      loading = false;
    }
  }

  onMount(load);

  async function runTest(e: SubmitEvent) {
    e.preventDefault();
    testError = null;
    testNotice = null;
    const url = testUrl.trim();
    if (url === '') {
      testError = 'Enter a URL to test.';
      return;
    }
    testing = true;
    try {
      testNotice = filterTestNotice(await testFilterRule(url));
      await tick();
      document.querySelector('tr.rule-hit')?.scrollIntoView({ block: 'nearest' });
    } catch (err) {
      if (onAuthError(err)) return;
      testError = 'Could not run the test. Please try again.';
    } finally {
      testing = false;
    }
  }

  function openAdd() {
    fPattern = '';
    fReason = REASON_OPTIONS[0].code;
    fDescription = '';
    fActive = true;
    formError = null;
    dialog = { mode: 'add' };
  }

  function openEdit(rule: FilterRule) {
    fPattern = rule.pattern;
    fReason = rule.reason_code;
    fDescription = rule.description;
    fActive = rule.active;
    formError = null;
    dialog = { mode: 'edit', rule };
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!dialog) return;
    const pattern = fPattern.trim();
    if (pattern === '') {
      formError = 'A pattern is required.';
      return;
    }
    saving = true;
    formError = null;
    try {
      if (dialog.mode === 'add') {
        await createFilterRule({ pattern, reason_code: fReason, description: fDescription.trim() });
      } else {
        await updateFilterRule(dialog.rule.id, {
          pattern,
          reason_code: fReason,
          description: fDescription.trim(),
          active: fActive,
        });
      }
      dialog = null;
      testNotice = null; // rules changed; the old hit may no longer apply
      await load();
    } catch (err) {
      if (onAuthError(err)) return;
      formError =
        err instanceof ApiError && err.status === 400
          ? err.message || 'The pattern must be a valid regular expression.'
          : 'Could not save the rule. Please try again.';
    } finally {
      saving = false;
    }
  }

  async function toggleActive(rule: FilterRule) {
    togglingId = rule.id;
    rowError = null;
    try {
      const updated = await updateFilterRule(rule.id, { active: !rule.active });
      rules = rules.map((r) => (r.id === rule.id ? { ...r, ...updated } : r));
    } catch (err) {
      if (onAuthError(err)) return;
      rowError = `Could not update "${rule.pattern}". Please try again.`;
    } finally {
      togglingId = null;
    }
  }

  // Delete asks first via ConfirmDialog (#0160); confirming runs performDelete.
  let deleteTarget = $state<FilterRule | null>(null);

  async function performDelete(rule: FilterRule) {
    rowError = null;
    try {
      await deleteFilterRule(rule.id);
      await load();
    } catch (err) {
      if (onAuthError(err)) return;
      throw new Error(`Could not delete "${rule.pattern}". Please try again.`);
    }
  }
</script>

<Panel title="URL filters">
  <form class="tester" onsubmit={runTest}>
    <label class="sr-only" for="test-url">URL to test</label>
    <input
      id="test-url"
      type="text"
      placeholder="Test a URL against the active rules, e.g. https://example.com/path"
      bind:value={testUrl}
      disabled={testing}
    />
    <Button type="submit" variant="primary" disabled={testing}>{testing ? 'Testing…' : 'Test a URL'}</Button>
    <Button type="button" onclick={openAdd}>Add rule</Button>
  </form>
  <div class="verdict" aria-live="polite">
    {#if testError}
      <p class="text-error" role="alert">{testError}</p>
    {:else if testNotice}
      <p class={testNotice.kind === 'match' ? 'text-error' : 'text-notice'}>
        {testNotice.message}{#if hitRule}: <span class="mono">{hitRule.pattern}</span>{/if}
      </p>
    {/if}
  </div>
</Panel>

<Panel title="Rules" noPadding={!loadError}>
  {#if loadError}
    <p class="text-error" role="alert">{loadError}</p>
    <Button variant="primary" onclick={load}>Retry</Button>
  {:else}
    {#if rowError}<p class="text-error row-error" role="alert">{rowError}</p>{/if}
    <DataTable
      {columns}
      rows={rules}
      rowKey={(r) => String(r.id)}
      caption="URL filter rules"
      {loading}
      rowClass={(r) => (r.id === hitId ? 'rule-hit' : '')}
    >
      {#snippet cell(rule, col)}
        {#if col.key === 'pattern'}
          <TwoLineCell
            title={rule.pattern}
            meta={rule.description}
            tag={rule.active ? '' : 'Off'}
            dim={!rule.active}
          />
        {:else if col.key === 'reason'}
          {reasonOf(rule)}
        {:else if col.key === 'active'}
          <button
            type="button"
            class="switch"
            class:on={rule.active}
            role="switch"
            aria-checked={rule.active}
            aria-label="Active: {rule.pattern}"
            disabled={togglingId === rule.id}
            onclick={() => toggleActive(rule)}
          >
            <span class="knob"></span>
          </button>
        {:else}
          <RowMenu
            label="Actions for {rule.pattern}"
            items={[
              { label: 'Edit', onselect: () => openEdit(rule) },
              { label: 'Delete', danger: true, onselect: () => (deleteTarget = rule) },
            ]}
          />
        {/if}
      {/snippet}
      {#snippet empty()}
        <span class="text-muted">No filter rules defined.</span>
      {/snippet}
    </DataTable>
  {/if}
</Panel>

<ConfirmDialog
  open={deleteTarget !== null}
  title="Delete filter rule?"
  message={`The rule "${deleteTarget?.pattern ?? ''}" will stop blocking URLs. This cannot be undone.`}
  confirmLabel="Delete rule"
  busyLabel="Deleting…"
  oncancel={() => (deleteTarget = null)}
  onconfirm={() => (deleteTarget ? performDelete(deleteTarget) : undefined)}
/>

{#if dialog}
  <Dialog title={dialog.mode === 'add' ? 'Add a rule' : 'Edit rule'} onclose={() => (dialog = null)}>
    <form onsubmit={submit}>
      <div class="field">
        <label for="rule-pattern">Pattern (Go regular expression)</label>
        <input
          id="rule-pattern"
          type="text"
          class="mono-input"
          placeholder="(?i)malware\.example\.com"
          bind:value={fPattern}
          disabled={saving}
        />
      </div>
      <div class="field">
        <label for="rule-reason">Reason</label>
        <select id="rule-reason" bind:value={fReason} disabled={saving}>
          {#each REASON_OPTIONS as opt (opt.code)}
            <option value={opt.code}>{opt.label}</option>
          {/each}
        </select>
      </div>
      <div class="field">
        <label for="rule-description">Description</label>
        <input
          id="rule-description"
          type="text"
          placeholder="Known malware host"
          bind:value={fDescription}
          disabled={saving}
        />
      </div>
      {#if dialog.mode === 'edit'}
        <label class="check">
          <input type="checkbox" bind:checked={fActive} disabled={saving} />
          Active
        </label>
      {/if}
      {#if formError}<p class="text-error" role="alert">{formError}</p>{/if}
      <div class="row actions">
        <Button type="submit" variant="primary" disabled={saving}>
          {saving ? 'Saving…' : dialog.mode === 'add' ? 'Add rule' : 'Save'}
        </Button>
        <Button type="button" disabled={saving} onclick={() => (dialog = null)}>Cancel</Button>
      </div>
    </form>
  </Dialog>
{/if}

<style>
  .tester {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-2);
    align-items: center;
  }
  .tester input {
    flex: 1 1 18rem;
    width: auto;
    min-width: 0;
  }
  .verdict {
    min-height: 1.5em;
    margin-top: var(--space-2);
  }
  .verdict p {
    margin: 0;
    font-size: var(--fs-base);
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    overflow-wrap: anywhere;
  }
  .row-error {
    margin: var(--space-3);
  }
  .check {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    margin-bottom: var(--space-3);
    cursor: pointer;
  }
  .check input {
    width: auto;
    margin: 0;
  }
  .actions {
    margin-top: var(--space-3);
  }
  .mono-input {
    font-family: var(--font-mono);
  }

  /* The hit row: tinted, with an accent bar on its left edge. */
  :global(tr.rule-hit),
  :global(tr.rule-hit:hover) {
    background: var(--danger-subtle);
    box-shadow: inset 3px 0 0 var(--danger);
  }

  /* Active switch */
  .switch {
    position: relative;
    width: 38px;
    height: 22px;
    padding: 0;
    border: var(--border-w) solid var(--border-strong);
    border-radius: 11px;
    background: var(--bg-header);
    cursor: pointer;
    vertical-align: middle;
  }
  .switch .knob {
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--bg-panel);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.35);
    transition: transform 0.12s;
  }
  .switch.on {
    background: var(--accent);
    border-color: var(--accent);
  }
  .switch.on .knob {
    transform: translateX(16px);
  }
  .switch:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .switch:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 2px;
  }
  @media (prefers-reduced-motion: reduce) {
    .switch .knob {
      transition: none;
    }
  }
</style>
