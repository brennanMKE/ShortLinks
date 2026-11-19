<!--
  FacetFilter (#0163) — one UTM filter dropdown: a button that opens a panel of
  checkboxes, one per value the report's facet lists, each with its click
  count. Several values may be ticked (OR within the dimension). The panel has
  a search box above SEARCH_ABOVE values; "(none)" is the report's `~none`
  bucket. The owner decides what a change does (the Overview navigates, so
  Back undoes it); this component only reports the new selection. The search
  text is local, not part of the URL.
-->
<script lang="ts" module>
  let panelSeq = 0;
</script>

<script lang="ts">
  import { tick } from 'svelte';
  import { NONE, sameValue, valueLabel, type FacetValue } from './viewQuery';

  interface Props {
    label: string;
    values: FacetValue[];
    selected: string[];
    onchange: (next: string[]) => void;
    /** Phone filter sheet (#0173): no trigger or popover, just the heading and the checkbox list as a section. */
    inline?: boolean;
  }

  let { label, values, selected, onchange, inline = false }: Props = $props();

  /** The search box appears above this many values. */
  const SEARCH_ABOVE = 8;

  const panelId = 'facet-panel-' + ++panelSeq;
  let open = $state(false);
  let query = $state('');
  let wrap = $state<HTMLElement | null>(null);
  let btn = $state<HTMLButtonElement | null>(null);
  let search = $state<HTMLInputElement | null>(null);

  // A selected value the facet no longer lists (no clicks under the other
  // filters) stays visible so it can be unticked.
  const all = $derived<FacetValue[]>([
    ...values,
    ...selected.filter((s) => !values.some((v) => sameValue(v.value, s))).map((value) => ({ value, clicks: 0 })),
  ]);
  const searchable = $derived(all.length > SEARCH_ABOVE);
  const shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    if (!searchable || q === '') return all;
    return all.filter((v) => valueLabel(v.value).toLowerCase().includes(q));
  });

  function toggle(value: string, on: boolean) {
    const next = on ? [...selected, value] : selected.filter((s) => !sameValue(s, value));
    onchange(next);
  }

  async function openPanel() {
    open = true;
    query = '';
    await tick();
    (search ?? wrap?.querySelector<HTMLElement>('input[type="checkbox"]'))?.focus();
  }
  function close(refocus: boolean) {
    open = false;
    if (refocus) btn?.focus();
  }
  function onWindowClick(e: MouseEvent) {
    if (open && wrap && !wrap.contains(e.target as Node)) open = false;
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      close(true);
    }
  }
  function onFocusOut(e: FocusEvent) {
    const next = e.relatedTarget as Node | null;
    if (open && next && wrap && !wrap.contains(next)) open = false;
  }
</script>

<svelte:window onclick={onWindowClick} />

{#snippet values_list()}
      {#if searchable}
        <input
          type="search"
          class="q"
          placeholder="Search {label.toLowerCase()}"
          aria-label="Search {label.toLowerCase()} values"
          bind:this={search}
          bind:value={query}
        />
      {/if}
      {#if all.length === 0}
        <p class="none text-muted">No values in this view.</p>
      {:else if shown.length === 0}
        <p class="none text-muted">No match.</p>
      {:else}
        <ul>
          {#each shown as v (v.value)}
            <li>
              <label class:muted={v.value === NONE}>
                <input
                  type="checkbox"
                  checked={selected.some((s) => sameValue(s, v.value))}
                  onchange={(e) => toggle(v.value, e.currentTarget.checked)}
                />
                <span class="val" title={valueLabel(v.value)}>{valueLabel(v.value)}</span>
                <span class="count">{v.clicks.toLocaleString()}</span>
              </label>
            </li>
          {/each}
        </ul>
      {/if}
{/snippet}

{#if inline}
  <section class="facet-inline" aria-label="{label} values">
    <div class="inline-h">
      <h3>
        {label}
        {#if selected.length > 0}<span class="n" aria-label="{selected.length} selected">{selected.length}</span>{/if}
      </h3>
      {#if selected.length > 0}
        <button type="button" class="btn-link inline-clear" onclick={() => onchange([])}>Clear</button>
      {/if}
    </div>
    <div class="inline-body">{@render values_list()}</div>
  </section>
{:else}
<div class="facet" bind:this={wrap} onkeydown={onKey} onfocusout={onFocusOut} role="presentation">
  <button
    type="button"
    class="trigger"
    class:active={selected.length > 0}
    bind:this={btn}
    aria-controls={panelId}
    aria-expanded={open}
    onclick={() => (open ? close(false) : openPanel())}
  >
    <span>{label}</span>
    {#if selected.length > 0}<span class="n" aria-label="{selected.length} selected">{selected.length}</span>{/if}
    <span class="caret" aria-hidden="true">▾</span>
  </button>
  {#if open}
    <div class="panel" id={panelId} role="group" aria-label="{label} values">
      {@render values_list()}
      <div class="foot">
        <button type="button" class="btn-link" disabled={selected.length === 0} onclick={() => onchange([])}>Clear</button>
        <button type="button" class="btn-link" onclick={() => close(true)}>Done</button>
      </div>
    </div>
  {/if}
</div>

{/if}

<style>
  .facet {
    position: relative;
    flex: 0 0 auto;
  }
  .trigger {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 34px;
    padding: 0 var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-panel);
    color: var(--text);
    font: inherit;
    cursor: pointer;
    white-space: nowrap;
  }
  .trigger:hover {
    background: var(--bg-subtle);
  }
  .trigger.active {
    border-color: var(--accent);
    background: var(--accent-subtle);
  }
  .n {
    min-width: 18px;
    padding: 0 5px;
    border-radius: 9px;
    background: var(--accent);
    color: var(--accent-text);
    font-size: var(--fs-sm);
    text-align: center;
    line-height: 18px;
  }
  .caret {
    font-size: 10px;
    color: var(--text-muted);
  }
  .panel {
    position: absolute;
    z-index: 30;
    top: calc(100% + 4px);
    left: 0;
    width: 280px;
    max-width: calc(100vw - 2 * var(--space-4));
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 6px 18px rgba(0, 0, 0, 0.18);
  }
  .q {
    display: block;
    width: calc(100% - 2 * var(--space-2));
    margin: var(--space-2);
  }
  ul {
    margin: 0;
    padding: var(--space-1) 0;
    list-style: none;
    max-height: 264px;
    overflow-y: auto;
  }
  label {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 36px;
    margin: 0;
    padding: 0 var(--space-3);
    font-weight: 400;
    cursor: pointer;
  }
  label:hover {
    background: var(--bg-subtle);
  }
  label input {
    width: auto;
    margin: 0;
    flex: 0 0 auto;
  }
  label.muted .val {
    font-style: italic;
    color: var(--text-muted);
  }
  .val {
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .count {
    flex: 0 0 auto;
    font-size: var(--fs-sm);
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }
  .none {
    margin: 0;
    padding: var(--space-3);
  }
  .foot {
    display: flex;
    justify-content: space-between;
    padding: var(--space-2) var(--space-3);
    border-top: var(--border-w) solid var(--border);
  }
  .foot .btn-link:disabled {
    opacity: 0.5;
    cursor: default;
  }
  .facet-inline {
    padding: var(--space-3) 0;
    border-bottom: var(--border-w) solid var(--border);
  }
  .inline-h {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin: 0 0 var(--space-1);
  }
  .inline-h h3 {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    margin: 0;
    font-size: var(--fs-base);
    font-weight: 600;
  }
  .inline-clear {
    margin-left: auto;
    min-height: 40px;
    font-weight: 400;
  }
  .inline-body ul {
    max-height: 200px;
  }
  .inline-body label {
    min-height: 40px;
    padding: 0;
  }
  .inline-body .q {
    width: 100%;
    margin: var(--space-1) 0;
  }
</style>
