<script lang="ts">
  // Styled replacement for the native browser confirm dialog (#0160), built on <dialog>.
  // showModal() makes the rest of the page inert; we still trap Tab ourselves,
  // cancel on Escape and backdrop click, and return focus to the trigger.
  import { dialogKeyAction, trapTarget, restoreFocus } from './confirmDialog';

  interface Props {
    open: boolean;
    title: string;
    message: string;
    /** Names the action, e.g. "Revoke passkey" — never "OK". */
    confirmLabel: string;
    /** Shown on the action button while onconfirm is pending. */
    busyLabel?: string;
    cancelLabel?: string;
    destructive?: boolean;
    /**
     * Runs the action. May return a Promise: both buttons are disabled with
     * busy text while it runs. If it throws/rejects the dialog stays open and
     * shows the error; if it resolves the dialog calls `oncancel` so the
     * parent clears its target.
     */
    onconfirm: () => void | Promise<void>;
    oncancel: () => void;
    /** Element to refocus on close (e.g. the ⋯ button whose menu item was used). */
    returnFocus?: HTMLElement | null;
  }

  let {
    open,
    title,
    message,
    confirmLabel,
    busyLabel = 'Working…',
    cancelLabel = 'Cancel',
    destructive = true,
    onconfirm,
    oncancel,
    returnFocus = null,
  }: Props = $props();

  let dialogEl = $state<HTMLDialogElement | null>(null);
  let cancelBtn = $state<HTMLButtonElement | null>(null);
  let busy = $state(false);
  let errorText = $state<string | null>(null);
  let trigger: HTMLElement | null = null;
  let lastClicked: HTMLElement | null = null;
  const uid = Math.random().toString(36).slice(2, 8);

  $effect(() => {
    const track = (e: MouseEvent) => {
      const t = e.target instanceof Element ? e.target.closest<HTMLElement>('button, a[href], [role="button"]') : null;
      lastClicked = t;
    };
    document.addEventListener('click', track, true);
    return () => document.removeEventListener('click', track, true);
  });

  // $effect.pre runs before the DOM updates for this change, so a menu item
  // that the same click removes is still in place when we look.
  $effect.pre(() => {
    if (open && dialogEl && !dialogEl.open) {
      const active = document.activeElement;
      const focused = active instanceof HTMLElement && active !== document.body ? active : null;
      // Safari doesn't focus a button on click, so fall back to the control
      // that was clicked (captured before its own handler ran).
      trigger = returnFocus ?? focused ?? (lastClicked?.isConnected ? lastClicked : null);
    }
  });

  $effect(() => {
    if (!dialogEl) return;
    if (open && !dialogEl.open) {
      busy = false;
      errorText = null;
      dialogEl.showModal();
      // Default to the safe choice.
      cancelBtn?.focus();
    } else if (!open && dialogEl.open) {
      dialogEl.close();
      restoreFocus(trigger, document.body);
      trigger = null;
    }
  });

  async function confirmClicked() {
    if (busy) return;
    busy = true;
    errorText = null;
    try {
      await onconfirm();
    } catch (err) {
      errorText = err instanceof Error && err.message ? err.message : 'Something went wrong. Please try again.';
      busy = false;
      return;
    }
    busy = false;
    oncancel();
  }

  function cancelRequested() {
    if (!busy) oncancel();
  }

  function focusables(): HTMLElement[] {
    if (!dialogEl) return [];
    return Array.from(dialogEl.querySelectorAll<HTMLElement>('button:not([disabled])'));
  }

  function onkeydown(e: KeyboardEvent) {
    const action = dialogKeyAction(e.key);
    if (action === 'cancel') {
      e.preventDefault();
      e.stopPropagation();
      cancelRequested();
    } else if (action === 'trap') {
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const target = trapTarget(focusables(), active, e.shiftKey);
      if (target) {
        e.preventDefault();
        target.focus();
      }
    }
  }

  // The browser's own Escape handling fires `cancel`; route it through oncancel
  // so the parent stays the single source of truth for `open`.
  function oncancelEvent(e: Event) {
    e.preventDefault();
    cancelRequested();
  }

  // Backdrop click = a click outside the dialog's rectangle. Clicks on its own
  // border/padding, or a drag that starts inside and ends outside, don't count.
  let downInside = false;
  function onpointerdown(e: PointerEvent) {
    downInside = insideRect(e);
  }
  function insideRect(e: MouseEvent): boolean {
    if (!dialogEl) return false;
    const r = dialogEl.getBoundingClientRect();
    return e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
  }
  function onbackdrop(e: MouseEvent) {
    if (e.target !== dialogEl) return;
    if (!downInside && !insideRect(e)) cancelRequested();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<dialog
  bind:this={dialogEl}
  class="confirm-dialog"
  class:destructive
  aria-labelledby="cd-title-{uid}"
  aria-describedby="cd-msg-{uid}"
  {onkeydown}
  {onpointerdown}
  oncancel={oncancelEvent}
  onclick={onbackdrop}
>
  <div class="body">
    <h2 id="cd-title-{uid}">{title}</h2>
    <p id="cd-msg-{uid}">{message}</p>
    {#if errorText}
      <p class="error" role="alert">{errorText}</p>
    {/if}
  </div>
  <div class="actions">
    <button type="button" class="btn" bind:this={cancelBtn} disabled={busy} onclick={cancelRequested}>{cancelLabel}</button>
    <button type="button" class="btn confirm" class:danger={destructive} disabled={busy} aria-busy={busy} onclick={confirmClicked}>
      {busy ? busyLabel : confirmLabel}
    </button>
  </div>
</dialog>

<style>
  .confirm-dialog {
    width: min(26rem, calc(100vw - 2 * var(--space-4, 16px)));
    padding: 0;
    border: var(--border-w, 1px) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-subtle);
    color: var(--text);
    box-shadow: 0 12px 40px rgba(0, 0, 0, 0.35);
  }
  .confirm-dialog::backdrop { background: rgba(0, 0, 0, 0.5); }
  .body { padding: var(--space-4, 16px); }
  h2 { margin: 0 0 var(--space-2, 8px); font-size: 1.1rem; }
  p { margin: 0; line-height: 1.45; color: var(--text); }
  .error { margin-top: var(--space-2, 8px); color: var(--danger); font-size: var(--fs-sm, 0.9rem); }
  .btn:disabled { opacity: 0.6; cursor: default; }
  .destructive h2 { color: var(--danger); }
  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2, 8px);
    padding: var(--space-3, 12px) var(--space-4, 16px);
    border-top: var(--border-w, 1px) solid var(--border);
  }
  .btn {
    font-family: var(--font);
    font-size: var(--fs-base);
    line-height: 1;
    min-height: 40px;
    padding: var(--space-2) var(--space-3);
    border: var(--border-w, 1px) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-subtle);
    color: var(--text);
    cursor: pointer;
  }
  .btn:hover { background: var(--bg-header); }
  .btn.confirm { background: var(--accent); border-color: var(--accent); color: var(--accent-text); }
  .btn.confirm.danger { background: var(--danger); border-color: var(--danger); color: #fff; }
  .btn:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  @media (max-width: 480px) {
    .actions { flex-direction: column-reverse; }
    .btn { width: 100%; }
  }
</style>
