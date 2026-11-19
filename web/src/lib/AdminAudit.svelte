<!--
  Admin → Audit log (#0171). Filters by user (typeahead over the users list, sent
  as `email`), action and date range, all held in the hash so a filtered view can
  be linked or reloaded: `#/admin/audit?email=…&action=…&range=7d&page=2`. Rows
  show relative time (absolute on hover), the actor's email, the action, the
  target, and the metadata collapsed to one line that expands in place.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { listAudit } from './api';
  import {
    AUDIT_ACTION_GROUPS,
    AUDIT_RANGES,
    DEFAULT_AUDIT_FILTERS,
    actorLabel,
    auditQueryString,
    auditTimeBounds,
    formatMetadata,
    hasAuditFilters,
    metadataDetail,
    pageInfo,
    presetSince,
    parseAuditQuery,
    targetLabel,
    type AuditFilters,
    type AuditRange,
  } from './admin';
  import { getRoute, replace } from './hashRouter.svelte';
  import type { AdminUser, AuditEntry } from './types';
  import Button from './Button.svelte';
  import Panel from './Panel.svelte';
  import DataTable from './DataTable.svelte';
  import RelativeDate from './RelativeDate.svelte';
  import TwoLineCell from './TwoLineCell.svelte';
  import UserPicker from './UserPicker.svelte';
  import type { Column } from './dataTable';

  interface Props {
    users: AdminUser[];
    /** Returns true when the error was a 401 and has been handled. */
    onAuthError: (err: unknown) => boolean;
  }
  let { users, onAuthError }: Props = $props();

  const PER_PAGE = 50;
  const BASE = '/admin/audit';

  const filters = $derived(parseAuditQuery(getRoute().query));
  const queryKey = $derived(auditQueryString(filters));
  const actionValue = $derived(filters.actions.join(','));
  const groupValues = $derived(AUDIT_ACTION_GROUPS.map((g) => g.actions.join(',')));
  const knownAction = $derived(
    actionValue === '' ||
      groupValues.includes(actionValue) ||
      AUDIT_ACTION_GROUPS.some((g) => g.actions.includes(actionValue)),
  );

  let entries = $state<AuditEntry[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let open = $state<Set<number>>(new Set());
  let seq = 0; // drops responses that a newer request has superseded
  const paging = $derived(pageInfo(total, filters.page, PER_PAGE));

  async function load() {
    const mine = ++seq;
    const f = filters;
    loading = true;
    error = null;
    try {
      const res = await listAudit(f.page, PER_PAGE, {
        email: f.email || undefined,
        actions: f.actions,
        ...auditTimeBounds(f),
      });
      if (mine !== seq) return;
      entries = res.audit_log;
      total = res.total;
      open = new Set();
    } catch (err) {
      if (mine !== seq) return;
      if (onAuthError(err)) return;
      error = 'Could not load the audit log. Please try again.';
    } finally {
      if (mine === seq) loading = false;
    }
  }

  // Reload whenever the filters in the hash change.
  $effect(() => {
    queryKey;
    untrack(load);
  });

  function setFilters(next: Partial<AuditFilters>) {
    // Any filter change returns to page 1; only a page move keeps its page.
    replace(BASE + auditQueryString({ ...filters, page: 1, ...next }));
  }

  function setAction(value: string) {
    setFilters({ actions: value === '' ? [] : value.split(',') });
  }

  function setRange(range: AuditRange) {
    setFilters({ range, since: presetSince(range), from: '', to: '' });
  }

  function toggle(id: number) {
    const next = new Set(open);
    if (!next.delete(id)) next.add(id);
    open = next;
  }

  function detailOf(e: AuditEntry): string {
    const meta = metadataDetail(e.metadata);
    return [meta, e.ip_address ? `IP ${e.ip_address}` : ''].filter(Boolean).join('\n');
  }

  function targetText(e: AuditEntry): string {
    return e.target_type === 'user' && e.user_email ? e.user_email : targetLabel(e);
  }

  const columns: Column<AuditEntry>[] = [
    { key: 'when', label: 'When', width: '5.5rem', mobile: 'metric' },
    { key: 'action', label: 'Action', width: '13rem', mobile: 'title' },
    { key: 'actor', label: 'Actor', width: '12.5rem', mobile: 'meta' },
    { key: 'details', label: 'Details', mobile: 'hide' },
    { key: 'toggle', label: 'Show details', width: '2.5rem', hideLabel: true, mobile: 'actions' },
  ];
</script>

<Panel title="Audit log">
  <div class="filters" role="search" aria-label="Audit log filters">
    <div class="f f-user">
      <span class="f-label" aria-hidden="true">User</span>
      <UserPicker
        {users}
        value={filters.email}
        onchange={(email) => setFilters({ email })}
        label="User"
        placeholder="Any user"
      />
    </div>
    <div class="f">
      <label class="f-label" for="audit-action">Action</label>
      <select id="audit-action" value={actionValue} onchange={(e) => setAction(e.currentTarget.value)}>
        <option value="">All actions</option>
        {#if !knownAction}<option value={actionValue}>{actionValue}</option>{/if}
        {#each AUDIT_ACTION_GROUPS as g, gi (g.label)}
          <optgroup label={g.label}>
            {#if g.actions.length > 1}<option value={groupValues[gi]}>All {g.label.toLowerCase()} events</option>{/if}
            {#each g.actions as a (a)}<option value={a}>{a}</option>{/each}
          </optgroup>
        {/each}
      </select>
    </div>
    <div class="f">
      <label class="f-label" for="audit-range">When</label>
      <select id="audit-range" value={filters.range} onchange={(e) => setRange(e.currentTarget.value as AuditRange)}>
        {#each AUDIT_RANGES as r (r.id)}<option value={r.id}>{r.label}</option>{/each}
      </select>
    </div>
    {#if filters.range === 'custom'}
      <div class="f">
        <label class="f-label" for="audit-from">From</label>
        <input id="audit-from" type="date" value={filters.from} max={filters.to || undefined} onchange={(e) => setFilters({ from: e.currentTarget.value })} />
      </div>
      <div class="f">
        <label class="f-label" for="audit-to">To</label>
        <input id="audit-to" type="date" value={filters.to} min={filters.from || undefined} onchange={(e) => setFilters({ to: e.currentTarget.value })} />
      </div>
    {/if}
    {#if hasAuditFilters(filters)}
      <Button onclick={() => replace(BASE + auditQueryString(DEFAULT_AUDIT_FILTERS))}>Clear filters</Button>
    {/if}
  </div>
</Panel>

<Panel title="Entries" noPadding={!error}>
  {#if error}
    <p class="text-error" role="alert">{error}</p>
    <Button variant="primary" onclick={load}>Retry</Button>
  {:else}
    <DataTable {columns} rows={entries} rowKey={(e) => String(e.id)} caption="Audit log entries" {loading}>
      {#snippet cell(e, col)}
        {#if col.key === 'when'}
          <RelativeDate date={e.created_at} />
        {:else if col.key === 'action'}
          <span class="mono" title={e.action}>{e.action}</span>
          {#if open.has(e.id) && detailOf(e)}<pre class="detail narrow-only">{detailOf(e)}</pre>{/if}
        {:else if col.key === 'actor'}
          <TwoLineCell
            title={actorLabel(e)}
            meta={targetText(e) === '—' ? '' : `→ ${targetText(e)}`}
            dim={e.actor_id === null}
          />
        {:else if col.key === 'details'}
          {#if open.has(e.id)}
            <pre class="detail">{detailOf(e) || '—'}</pre>
          {:else}
            <span class="mono summary" title={formatMetadata(e.metadata)}>{formatMetadata(e.metadata) || '—'}</span>
          {/if}
        {:else if detailOf(e)}
          <button
            type="button"
            class="chev"
            aria-expanded={open.has(e.id)}
            aria-label="{open.has(e.id) ? 'Hide' : 'Show'} details for {e.action}"
            onclick={() => toggle(e.id)}
          >
            <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" class:turned={open.has(e.id)}>
              <path d="M5 3l5 5-5 5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </button>
        {/if}
      {/snippet}
      {#snippet empty()}
        <span class="text-muted">
          {hasAuditFilters(filters) ? 'No entries match these filters.' : 'No audit entries.'}
        </span>
      {/snippet}
    </DataTable>

    {#if !loading && total > 0}
      <div class="pager">
        <Button disabled={!paging.hasPrev} onclick={() => setFilters({ page: paging.page - 1 })}>Previous</Button>
        <span class="text-muted">
          {paging.firstItem}–{paging.lastItem} of {paging.total} · page {paging.page} of {paging.totalPages}
        </span>
        <Button disabled={!paging.hasNext} onclick={() => setFilters({ page: paging.page + 1 })}>Next</Button>
      </div>
    {/if}
  {/if}
</Panel>

<style>
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: var(--space-3);
  }
  .f {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    min-width: 0;
  }
  .f-label {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    font-weight: 600;
  }
  .f select,
  .f input {
    width: auto;
    min-width: 11rem;
  }
  .f-user {
    flex: 1 1 16rem;
    max-width: 22rem;
  }
  .f-user :global(input) {
    width: 100%;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
  }
  .summary {
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    color: var(--text-muted);
  }
  .detail {
    margin: var(--space-1) 0;
    padding: var(--space-2);
    background: var(--bg-subtle);
    border-radius: var(--radius);
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 14rem;
    overflow-y: auto;
  }
  .narrow-only {
    display: none;
  }
  .chev {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    padding: 0;
    border: none;
    border-radius: var(--radius);
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .chev:hover {
    background: var(--bg-subtle);
    color: var(--text);
  }
  .chev:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }
  .chev svg {
    transition: transform 0.12s;
  }
  .chev svg.turned {
    transform: rotate(90deg);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3);
    border-top: var(--border-w) solid var(--border);
  }
  /* Phone cards: the one-line details column is hidden, so an expanded row
     shows its details under the action instead. */
  @media (max-width: 899px) {
    .chev {
      width: 40px;
      height: 40px;
    }
    .narrow-only {
      display: block;
    }
    .f select,
    .f input {
      min-width: 0;
      width: 100%;
    }
    .f {
      flex: 1 1 8rem;
    }
    .f-user {
      flex-basis: 100%;
      max-width: none;
    }
    .pager {
      flex-wrap: wrap;
      justify-content: center;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .chev svg {
      transition: none;
    }
  }
</style>
