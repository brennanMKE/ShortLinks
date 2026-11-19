<!--
  FilterSheet (#0173) — the phone filter sheet: a native <dialog> pinned to the
  bottom of the screen that holds a view's whole filter set, opened by its
  "Filters (N)" button. Escape, the backdrop and Done close it; Clear all
  resets every filter. Focus returns to the button that opened it.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { restoreFocus } from './confirmDialog';

  interface Props {
    title?: string;
    /** How many filters are set; Clear all is disabled at 0. */
    count: number;
    onclear: () => void;
    onclose: () => void;
    children: Snippet;
  }
  let { title = 'Filters', count, onclear, onclose, children }: Props = $props();

  let el: HTMLDialogElement | undefined = $state();
  let heading: HTMLElement | undefined = $state();

  $effect(() => {
    if (!el) return;
    const opener = document.activeElement as HTMLElement | null;
    // The page behind the sheet must not scroll.
    const root = document.documentElement;
    const prevOverflow = root.style.overflow;
    root.style.overflow = 'hidden';
    if (!el.open) {
      if (typeof el.showModal === 'function') el.showModal();
      else el.setAttribute('open', ''); // jsdom has no showModal
      // Start on the heading so the keyboard does not pop up on a search box.
      heading?.focus({ preventScroll: true });
    }
    return () => {
      root.style.overflow = prevOverflow;
      restoreFocus(opener, document.body);
    };
  });
</script>

<dialog
  bind:this={el}
  class="sheet"
  aria-label={title}
  oncancel={(e) => {
    e.preventDefault();
    onclose();
  }}
  onclick={(e) => {
    if (e.target === el) onclose();
  }}
>
  <header>
    <h2 tabindex="-1" bind:this={heading}>{count > 0 ? `${title} (${count})` : title}</h2>
    <button type="button" class="btn-link" disabled={count === 0} onclick={onclear}>Clear all</button>
  </header>
  <div class="scroll">{@render children()}</div>
  <footer><button type="button" class="done" onclick={onclose}>Done</button></footer>
</dialog>

<style>
  .sheet {
    position: fixed;
    inset: auto 0 0 0;
    width: 100%;
    max-width: none;
    max-height: 85vh;
    margin: 0;
    padding: 0;
    border: none;
    border-top: var(--border-w) solid var(--border-strong);
    border-radius: 14px 14px 0 0;
    background: var(--bg-panel);
    color: var(--text);
    box-sizing: border-box;
  }
  .sheet[open] {
    display: flex;
    flex-direction: column;
  }
  .sheet::backdrop {
    background: rgba(0, 0, 0, 0.4);
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: var(--space-2) var(--space-4);
    border-bottom: var(--border-w) solid var(--border);
  }
  h2 {
    outline: none;
    margin: 0;
    font-size: var(--fs-lg);
    font-weight: 600;
  }
  header .btn-link {
    min-height: 40px;
  }
  header .btn-link:disabled {
    opacity: 0.5;
  }
  .scroll {
    flex: 1 1 auto;
    min-height: 0;
    overflow-y: auto;
    padding: 0 var(--space-4);
    overscroll-behavior: contain;
  }
  footer {
    padding: var(--space-3) var(--space-4) calc(var(--space-3) + env(safe-area-inset-bottom));
    border-top: var(--border-w) solid var(--border);
  }
  .done {
    width: 100%;
    min-height: 44px;
    border: none;
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--accent-text);
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
</style>
