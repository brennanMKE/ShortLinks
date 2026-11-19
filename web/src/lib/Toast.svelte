<!--
  Toast (#0165) — the single toast region, mounted once in App.svelte. Polite
  live region; each toast can carry an action (Undo). Toasts last 8 seconds,
  pause while hovered or focused, and stack up to three (lib/toasts.ts).
-->
<script lang="ts">
  import { tick } from 'svelte';
  import { toasts, dismissToast, pauseToast, resumeToast, runToastAction } from './toasts';

  let region = $state<HTMLElement | null>(null);

  // Alt+Z moves focus to the newest toast's action (or its dismiss button) so
  // Undo is reachable without hunting for it with Tab.
  async function onKey(e: KeyboardEvent) {
    if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || e.code !== 'KeyZ') return;
    const btns = region?.querySelectorAll<HTMLElement>('.toast:last-of-type .act, .toast:last-of-type .x');
    const first = btns?.[0];
    if (!first) return;
    e.preventDefault();
    await tick();
    first.focus();
  }
</script>

<svelte:window onkeydown={onKey} />

<div class="toast-region" bind:this={region} role="region" aria-label="Notifications. Press Alt+Z to focus the latest action." aria-live="polite">
  {#each $toasts as t (t.id)}
    <div
      class="toast"
      role="group"
      onmouseenter={() => pauseToast(t.id)}
      onmouseleave={() => resumeToast(t.id)}
      onfocusin={() => pauseToast(t.id)}
      onfocusout={() => resumeToast(t.id)}
    >
      <span class="msg">
        {t.message}
        {#if t.error}<span class="err"> {t.error}</span>{/if}
      </span>
      {#if t.actionLabel && t.onaction}
        <button type="button" class="act" disabled={t.busy} onclick={() => runToastAction(t.id)}>
          {t.busy ? 'Working…' : t.actionLabel}
        </button>
      {/if}
      <button type="button" class="x" aria-label="Dismiss" onclick={() => dismissToast(t.id)}>×</button>
    </div>
  {/each}
</div>

<style>
  .toast-region {
    position: fixed;
    left: var(--space-4, 16px);
    bottom: var(--space-4, 16px);
    z-index: 1000;
    display: flex;
    flex-direction: column;
    gap: var(--space-2, 8px);
    max-width: min(28rem, calc(100vw - 2 * var(--space-4, 16px)));
    pointer-events: none;
  }
  .toast {
    pointer-events: auto;
    display: flex;
    align-items: center;
    gap: var(--space-3, 12px);
    padding: var(--space-2, 8px) var(--space-3, 12px);
    background: var(--text);
    color: var(--bg-panel);
    border-radius: var(--radius);
    box-shadow: 0 6px 24px rgba(0, 0, 0, 0.3);
    font-size: var(--fs-base);
  }
  .msg { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .err { font-weight: 600; }
  .act, .x {
    font-family: var(--font);
    font-size: var(--fs-base);
    min-height: 36px;
    background: transparent;
    border: 0;
    color: inherit;
    cursor: pointer;
    border-radius: var(--radius);
  }
  .act { font-weight: 700; text-decoration: underline; padding: 0 var(--space-2, 8px); }
  .x { font-size: 1.25rem; line-height: 1; min-width: 36px; opacity: 0.8; }
  .act:disabled { opacity: 0.6; cursor: default; }
  .act:focus-visible, .x:focus-visible { outline: 2px solid var(--bg-panel); outline-offset: 1px; }
  @media (max-width: 480px) {
    .toast-region { right: var(--space-4, 16px); max-width: none; }
  }
</style>
