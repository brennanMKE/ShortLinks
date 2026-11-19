// Pure helpers behind ConfirmDialog.svelte (#0160). Kept free of Svelte and the
// DOM globals so Vitest (node environment) can cover focus return, Escape and
// the Tab trap with plain fake elements.

export interface FocusLike {
  focus(): void;
  isConnected?: boolean;
}

/** What the dialog should do with a keydown. */
export type DialogKeyAction = 'cancel' | 'trap' | null;

export function dialogKeyAction(key: string): DialogKeyAction {
  if (key === 'Escape') return 'cancel';
  if (key === 'Tab') return 'trap';
  return null;
}

/**
 * Where Tab / Shift+Tab should land so focus never leaves the dialog.
 * Returns the element to focus, or null when the browser's own movement is
 * already inside the dialog and should be left alone.
 */
export function trapTarget<T>(focusables: T[], active: T | null, shift: boolean): T | null {
  if (focusables.length === 0) return null;
  const first = focusables[0];
  const last = focusables[focusables.length - 1];
  const idx = active === null ? -1 : focusables.indexOf(active);
  if (idx === -1) return shift ? last : first;
  if (shift && active === first) return last;
  if (!shift && active === last) return first;
  return null;
}

/**
 * Return focus to whatever opened the dialog. A trigger that was removed from
 * the page (a revoked row, say) cannot take focus; `fallback` gets it instead.
 */
export function restoreFocus(trigger: FocusLike | null, fallback?: FocusLike | null): boolean {
  if (trigger && trigger.isConnected !== false) {
    trigger.focus();
    return true;
  }
  if (fallback) {
    fallback.focus();
    return true;
  }
  return false;
}
