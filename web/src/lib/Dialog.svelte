<!--
  Dialog (#0171) — a modal for short forms (add/edit a rule). Uses the native
  <dialog> so focus is trapped and the page behind is inert; Escape and a
  backdrop click close it. Focus returns to whatever opened it.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { restoreFocus } from './confirmDialog';

  interface Props {
    title: string;
    onclose: () => void;
    children: Snippet;
    /** A wider dialog for forms with several columns (#0164's batch create). */
    wide?: boolean;
  }
  let { title, onclose, children, wide = false }: Props = $props();

  let el: HTMLDialogElement | undefined = $state();
  const uid = Math.random().toString(36).slice(2, 8);

  $effect(() => {
    if (!el) return;
    const opener = document.activeElement as HTMLElement | null;
    if (!el.open) {
      if (typeof el.showModal === 'function') el.showModal();
      else el.setAttribute('open', ''); // jsdom has no showModal
    }
    return () => {
      restoreFocus(opener, document.body);
    };
  });
</script>

<dialog
  bind:this={el}
  class="dlg"
  class:wide
  aria-labelledby="dlg-{uid}"
  oncancel={(e) => {
    e.preventDefault();
    onclose();
  }}
  onclick={(e) => {
    if (e.target === el) onclose();
  }}
>
  <div class="body">
    <h2 id="dlg-{uid}" class="title">{title}</h2>
    {@render children()}
  </div>
</dialog>

<style>
  .dlg {
    width: min(480px, calc(100vw - 2 * var(--space-4)));
    padding: 0;
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    background: var(--bg-panel);
    color: var(--text);
  }
  .dlg.wide {
    width: min(760px, calc(100vw - 2 * var(--space-4)));
  }
  .dlg::backdrop {
    background: rgba(0, 0, 0, 0.4);
  }
  .body {
    padding: var(--space-5);
  }
  .title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0 0 var(--space-4);
  }
</style>
