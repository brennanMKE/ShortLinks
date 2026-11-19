<script lang="ts">
  import { tick } from 'svelte';
  import { nextMenuIndex, type MenuItem } from './rowMenu';

  interface Props {
    items: MenuItem[];
    /** Accessible name for the trigger, e.g. "Actions for My guide". */
    label?: string;
    /** When set, a copy icon (visible on row hover/focus) copies this text. */
    copyText?: string;
    oncopy?: (text: string) => void;
    /** Renders a labelled "Text ▾" button trigger instead of the ⋯ icon (toolbar menus). */
    text?: string;
  }
  let { items, label = 'Row actions', copyText, oncopy, text }: Props = $props();

  let open = $state(false);
  let triggerEl: HTMLButtonElement | undefined = $state();
  let menuEl: HTMLDivElement | undefined = $state();
  let pos = $state({ top: 0, left: 0 });
  let copied = $state(false);
  const uid = Math.random().toString(36).slice(2, 8);

  function enabledItems(): HTMLElement[] {
    return menuEl ? Array.from(menuEl.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')) : [];
  }

  async function openMenu(focus: 'first' | 'last' = 'first') {
    if (!triggerEl) return;
    const r = triggerEl.getBoundingClientRect();
    // Right-align under the trigger; position: fixed escapes the table's
    // overflow clipping. Flip above when near the bottom of the viewport.
    const menuW = 200;
    const left = Math.max(8, Math.min(r.right - menuW, window.innerWidth - menuW - 8));
    const below = r.bottom + 4;
    const estH = items.length * 36 + 8;
    const top = below + estH > window.innerHeight ? Math.max(8, r.top - estH - 4) : below;
    pos = { top, left };
    open = true;
    await tick();
    const els = enabledItems();
    (focus === 'last' ? els[els.length - 1] : els[0])?.focus();
  }

  function close(refocus = true) {
    open = false;
    if (refocus) triggerEl?.focus();
  }

  function onTriggerKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openMenu('first');
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      openMenu('last');
    }
  }

  function onMenuKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key === 'Tab') {
      close(false);
      return;
    }
    const els = enabledItems();
    const cur = els.indexOf(document.activeElement as HTMLElement);
    const next = nextMenuIndex(e.key, cur, els.length);
    if (next !== null) {
      e.preventDefault();
      els[next]?.focus();
    }
  }

  function choose(item: MenuItem) {
    if (item.disabled) return;
    close();
    item.onselect?.();
  }

  function onWindowPointer(e: PointerEvent) {
    if (!open) return;
    const t = e.target as Node;
    if (menuEl?.contains(t) || triggerEl?.contains(t)) return;
    close(false);
  }

  // Fixed-position menu would drift from its row: close on any scroll.
  $effect(() => {
    if (!open) return;
    const h = () => (open = false);
    window.addEventListener('scroll', h, true);
    return () => window.removeEventListener('scroll', h, true);
  });

  async function copy() {
    if (copyText === undefined) return;
    try {
      await navigator.clipboard?.writeText(copyText);
    } catch {
      /* clipboard may be unavailable (insecure context); caller can still react */
    }
    oncopy?.(copyText);
    copied = true;
    setTimeout(() => (copied = false), 1500);
  }
</script>

<svelte:window onpointerdown={onWindowPointer} onresize={() => (open = false)} />

<span class="row-menu">
  {#if copyText !== undefined}
    <button type="button" class="icon-btn copy" class:copied aria-label={copied ? 'Copied' : 'Copy short URL'} title={copied ? 'Copied' : 'Copy short URL'} onclick={copy}>
      {#if copied}
        <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d="M3 8.5l3.2 3.2L13 4.8" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
      {:else}
        <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><rect x="5.5" y="5.5" width="8" height="8" rx="1.5" fill="none" stroke="currentColor" stroke-width="1.4" /><path d="M10.5 3.5v-.5A1.5 1.5 0 0 0 9 1.5H3.5A1.5 1.5 0 0 0 2 3V8.5A1.5 1.5 0 0 0 3.5 10H4" fill="none" stroke="currentColor" stroke-width="1.4" /></svg>
      {/if}
    </button>
  {/if}
  <button
    bind:this={triggerEl}
    type="button"
    class={text ? 'text-btn trigger' : 'icon-btn trigger'}
    aria-label={label}
    aria-haspopup="menu"
    aria-expanded={open}
    aria-controls={open ? `rm-${uid}` : undefined}
    onclick={() => (open ? close(false) : openMenu())}
    onkeydown={onTriggerKey}
  >
    {#if text}
      {text}
      <svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><path d="M3.5 6l4.5 4.5L12.5 6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
    {:else}
      <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><circle cx="3" cy="8" r="1.4" fill="currentColor" /><circle cx="8" cy="8" r="1.4" fill="currentColor" /><circle cx="13" cy="8" r="1.4" fill="currentColor" /></svg>
    {/if}
  </button>

  {#if open}
    <!-- svelte-ignore a11y_interactive_supports_focus -->
    <div bind:this={menuEl} id="rm-{uid}" class="menu" role="menu" aria-label={label} style:top="{pos.top}px" style:left="{pos.left}px" onkeydown={onMenuKey}>
      {#each items as item (item.label)}
        {#if item.href && !item.disabled}
          <a role="menuitem" class="item" class:danger={item.danger} href={item.href} target="_blank" rel="noopener noreferrer" tabindex="-1" onclick={() => close(false)}>{item.label}</a>
        {:else}
          <button type="button" role="menuitem" class="item" class:danger={item.danger} aria-disabled={item.disabled ? 'true' : undefined} tabindex="-1" onclick={() => choose(item)}>{item.label}</button>
        {/if}
      {/each}
    </div>
  {/if}
</span>

<style>
  .row-menu {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    justify-content: flex-end;
  }
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 32px;
    height: 32px;
    padding: 0;
    border: none;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .icon-btn:hover,
  .icon-btn[aria-expanded='true'] { background: var(--bg-subtle); color: var(--text); }
  .icon-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 0; }
  .text-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 6px var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-panel);
    color: var(--text);
    font: inherit;
    font-size: var(--fs-base);
    cursor: pointer;
  }
  .text-btn:hover,
  .text-btn[aria-expanded='true'] { background: var(--bg-subtle); }
  .text-btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 0; }
  @media (max-width: 899px) {
    .icon-btn { width: 40px; height: 40px; }
    .text-btn { min-height: 40px; }
  }
  .copy { opacity: 0; transition: opacity 0.1s; }
  .copy.copied { color: var(--success); }
  /* Frequent action: appear on row hover / keyboard focus; always on touch. */
  :global(tr:hover) .copy,
  :global(tr:focus-within) .copy,
  .copy:focus-visible,
  .copy.copied { opacity: 1; }
  @media (hover: none) { .copy { opacity: 1; } }

  .menu {
    position: fixed;
    z-index: 50;
    min-width: 200px;
    padding: var(--space-1) 0;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.18);
    text-align: left;
  }
  .item {
    display: block;
    width: 100%;
    box-sizing: border-box;
    padding: 8px var(--space-3);
    border: none;
    background: none;
    font: inherit;
    font-size: var(--fs-base);
    text-align: left;
    color: var(--text);
    text-decoration: none;
    cursor: pointer;
  }
  .item:hover,
  .item:focus { background: var(--bg-subtle); outline: none; }
  .item:focus-visible { box-shadow: inset 2px 0 0 var(--accent); }
  .item.danger { color: var(--danger); }
  .item[aria-disabled='true'] { opacity: 0.5; cursor: default; }
</style>
