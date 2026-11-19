<!--
  StatTile (#0162) — one headline number on the Overview: a label, the value,
  a muted line under it and the change against the previous period. Change
  carries an arrow glyph as well as colour, and a hover title with the prior
  number. `help` adds a small (i) button that toggles an explanation line; it
  is a button rather than a hover tooltip so it works on touch.
-->
<script lang="ts">
  import type { Change } from './overview';

  interface Props {
    label: string;
    value: string;
    sub?: string;
    change?: Change | null;
    /** Shown after the change, e.g. "vs previous 30 days". */
    changeNote?: string;
    /** Shown instead of the change when there is none to show (e.g. no prior data). */
    unavailable?: string;
    help?: string;
    /** Full text for the value's tooltip when it is truncated. */
    valueTitle?: string;
    /** Long text value (a title): smaller, up to two lines. */
    wrap?: boolean;
  }

  let { label, value, sub = '', change = null, changeNote = '', unavailable = '', help = '', valueTitle = '', wrap = false }: Props = $props();

  let helpOpen = $state(false);
  const helpId = `tile-help-${Math.random().toString(36).slice(2, 8)}`;
</script>

<div class="tile">
  <div class="tile-label">
    <span>{label}</span>
    {#if help}
      <button
        type="button"
        class="help"
        aria-label="About {label}"
        aria-expanded={helpOpen}
        aria-controls={helpId}
        title={help}
        onclick={() => (helpOpen = !helpOpen)}>i</button
      >
    {/if}
  </div>
  <div class="tile-value" class:wrap title={valueTitle || undefined}>{value}</div>
  {#if sub}<div class="tile-sub" title={sub}>{sub}</div>{/if}
  {#if change && change.kind !== 'none'}
    <div class="tile-change {change.kind}" title={change.title}>
      {#if change.arrow}<span aria-hidden="true">{change.arrow}</span>{/if}
      <span class="chg-text">{change.kind === 'new' ? 'new' : change.text}</span>
      {#if changeNote}<span class="chg-note">{changeNote}</span>{/if}
      <span class="sr-only">{change.kind === 'up' ? 'up' : change.kind === 'down' ? 'down' : ''}</span>
    </div>
  {:else if unavailable}
    <div class="tile-change none"><span class="chg-note">{unavailable}</span></div>
  {/if}
  {#if help}
    <p id={helpId} class="help-text" hidden={!helpOpen}>{help}</p>
  {/if}
</div>

<style>
  .tile {
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    padding: var(--space-3) var(--space-4);
    min-width: 0;
  }
  .tile-label {
    display: flex;
    align-items: center;
    gap: var(--space-1);
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
  }
  .help {
    font: inherit;
    font-size: 11px;
    font-style: italic;
    line-height: 1;
    width: 16px;
    height: 16px;
    padding: 0;
    border-radius: 50%;
    border: var(--border-w) solid var(--border-strong);
    background: none;
    color: var(--text-muted);
    cursor: pointer;
  }
  .help:hover {
    color: var(--text);
    border-color: var(--text-muted);
  }
  .tile-value {
    font-size: var(--fs-xl);
    font-weight: 600;
    line-height: 1.25;
    margin-top: 2px;
    font-variant-numeric: tabular-nums;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tile-value.wrap {
    font-size: var(--fs-md);
    line-height: 1.3;
    white-space: normal;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    min-height: 2.6em;
  }
  .tile-sub {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tile-change {
    margin-top: var(--space-1);
    font-size: var(--fs-sm);
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .tile-change.up {
    color: var(--success);
  }
  .tile-change.down {
    color: var(--danger);
  }
  .tile-change.new {
    color: var(--accent);
  }
  .tile-change.flat,
  .tile-change.none {
    color: var(--text-muted);
  }
  .chg-note {
    font-weight: 400;
    color: var(--text-muted);
    margin-left: var(--space-1);
  }
  .help-text {
    margin: var(--space-2) 0 0;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    white-space: normal;
  }
  .help-text[hidden] {
    display: none;
  }
</style>
