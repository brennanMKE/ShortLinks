// unsavedWork — a tiny registry of in-progress admin edits (#0185).
//
// The version check (versionCheck.ts) asks hasUnsavedWork() before reloading
// a tab onto a new build; a form that registers here gets a "Reload" notice
// instead of losing its typing. isEditingField is the safety net for forms
// that do not register: a focused text field also holds the reload back.

const unsaved = new Set<string>();

export function setUnsaved(key: string, value: boolean): void {
  if (value) unsaved.add(key);
  else unsaved.delete(key);
}

export function hasUnsavedWork(): boolean {
  return unsaved.size > 0;
}

export function resetUnsavedForTests(): void {
  unsaved.clear();
}

const NON_TEXT_INPUT_TYPES = new Set([
  'button', 'submit', 'reset', 'checkbox', 'radio', 'file', 'hidden', 'range', 'color', 'image',
]);

export interface FieldLike {
  tagName: string;
  type?: string;
  isContentEditable?: boolean;
}

export function isEditingField(el: FieldLike | null | undefined): boolean {
  if (!el) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName.toUpperCase();
  if (tag === 'TEXTAREA') return true;
  if (tag === 'INPUT') return !NON_TEXT_INPUT_TYPES.has((el.type ?? 'text').toLowerCase());
  return false;
}

// --- DOM-level detection (#0185) -------------------------------------------
// ShortLinks has many forms (New link dialog, batch grid, filter rules, admin
// settings); rather than make each register, scan the page for the signs of
// unsaved input. Over-detecting only costs a "Reload" notice instead of an
// automatic reload; under-detecting would discard typing, so lean cautious.

/** True while a modal (New link dialog, batch grid, rule editor, confirm) is open. */
export function hasOpenDialog(root: ParentNode = document): boolean {
  // Same selector AppShell's shortcut guard uses, so ARIA modals that are not a
  // native <dialog> (Admin's deactivate-user modal) block the reload too.
  return root.querySelector('dialog[open], [aria-modal="true"]') !== null;
}

/**
 * True when any text-like field holds a value that differs from its default.
 * Svelte's bind:value writes the .value property and leaves the value
 * attribute (defaultValue) alone, so typed or programmatically bound text
 * reads as dirty here. A field bound to a non-empty initial value counts as
 * dirty too: that only costs a notice instead of a reload, never lost input.
 */
export function hasDirtyField(root: ParentNode = document): boolean {
  const fields = root.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>('textarea, input');
  for (const el of Array.from(fields)) {
    if (el.tagName.toUpperCase() === 'INPUT' && NON_TEXT_INPUT_TYPES.has((el.type ?? 'text').toLowerCase())) continue;
    if (el.value !== el.defaultValue) return true;
  }
  return false;
}

/** Any reason not to reload this tab out from under the user right now. */
export function hasUnsavedInput(doc: Document = document): boolean {
  return (
    hasUnsavedWork() ||
    hasOpenDialog(doc) ||
    hasDirtyField(doc) ||
    isEditingField((doc.activeElement as FieldLike | null) ?? null)
  );
}
