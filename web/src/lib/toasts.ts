// Toast state for Toast.svelte (#0165): a small store of up to three toasts
// that each expire after 8 seconds, pause while hovered or focused, and may
// carry one action (Undo). Kept free of Svelte components so Vitest can drive
// it with fake timers.

import { writable, type Readable } from 'svelte/store';

export const TOAST_MS = 8000;
export const MAX_TOASTS = 3;

export interface ToastInput {
  message: string;
  /** Label of the action button, e.g. "Undo". Omit for a plain notice. */
  actionLabel?: string;
  /** Runs when the action is used. A rejection is shown on the toast. */
  onaction?: () => void | Promise<void>;
  /**
   * Where keyboard focus goes after the action succeeds (the toast, and the
   * button that had focus in it, is gone). Resolved lazily because the element
   * may be re-rendered. Falls back to the page heading, then <main>.
   */
  returnFocus?: () => HTMLElement | null | undefined;
  /** Override the lifetime (ms). */
  duration?: number;
}

export interface Toast extends ToastInput {
  id: number;
  /** Set while the action runs. */
  busy: boolean;
  /** Set when the action failed. */
  error: string | null;
}

interface Timer {
  handle: ReturnType<typeof setTimeout> | null;
  remaining: number;
  startedAt: number;
}

const { subscribe, update, set } = writable<Toast[]>([]);
const timers = new Map<number, Timer>();
let nextId = 1;
let current: Toast[] = [];
subscribe((v) => (current = v));

export const toasts: Readable<Toast[]> = { subscribe };

function startTimer(id: number) {
  const t = timers.get(id);
  if (!t || t.handle !== null) return;
  t.startedAt = Date.now();
  t.handle = setTimeout(() => dismissToast(id), t.remaining);
}

function clearTimer(id: number) {
  const t = timers.get(id);
  if (t?.handle != null) clearTimeout(t.handle);
  timers.delete(id);
}

/** Show a toast; the oldest is dropped when more than three are up. */
export function pushToast(input: ToastInput): number {
  const id = nextId++;
  const toast: Toast = { ...input, id, busy: false, error: null };
  const dropped: number[] = [];
  update((cur) => {
    const next = [...cur, toast];
    while (next.length > MAX_TOASTS) dropped.push(next.shift()!.id);
    return next;
  });
  for (const d of dropped) clearTimer(d);
  timers.set(id, { handle: null, remaining: input.duration ?? TOAST_MS, startedAt: 0 });
  startTimer(id);
  return id;
}

export function dismissToast(id: number): void {
  clearTimer(id);
  update((cur) => cur.filter((t) => t.id !== id));
}

/** Hover or focus: stop the clock, remembering what is left. */
export function pauseToast(id: number): void {
  const t = timers.get(id);
  if (!t || t.handle === null) return;
  clearTimeout(t.handle);
  t.handle = null;
  t.remaining = Math.max(0, t.remaining - (Date.now() - t.startedAt));
}

export function resumeToast(id: number): void {
  // Don't restart the clock while the action is running; the toast closes or
  // shows its error when it finishes.
  if (current.find((t) => t.id === id)?.busy) return;
  startTimer(id);
}

/** Focus the preferred target, else the page's h1, else <main>. */
export function focusAfterUndo(preferred?: HTMLElement | null): void {
  if (typeof document === 'undefined') return;
  const ok = (el: HTMLElement | null | undefined): el is HTMLElement => !!el && el.isConnected;
  const target = ok(preferred)
    ? preferred
    : (document.querySelector<HTMLElement>('main h1') ?? document.querySelector<HTMLElement>('main'));
  if (!target) return;
  if (!target.matches('a[href], button, input, select, textarea, [tabindex]')) target.setAttribute('tabindex', '-1');
  target.focus();
}

function patch(id: number, p: Partial<Toast>) {
  update((cur) => cur.map((t) => (t.id === id ? { ...t, ...p } : t)));
}

/** Run a toast's action; it closes on success and shows the error otherwise. */
export async function runToastAction(id: number): Promise<void> {
  const target = current.find((t) => t.id === id);
  if (!target || target.busy || !target.onaction) return;
  pauseToast(id);
  patch(id, { busy: true, error: null });
  try {
    await target.onaction();
    dismissToast(id);
    focusAfterUndo(target.returnFocus?.());
  } catch (err) {
    patch(id, {
      busy: false,
      error: err instanceof Error && err.message ? err.message : 'Could not undo. Please try again.',
    });
  }
}

/** Test helper: drop everything. */
export function resetToasts(): void {
  for (const id of [...timers.keys()]) clearTimer(id);
  set([]);
}
