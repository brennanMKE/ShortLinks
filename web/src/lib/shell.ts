// Pure helpers for AppShell (#0153): the tab list and the `n` shortcut guard.
// Kept free of Svelte so shell.test.ts can cover them without a DOM.

export interface NavTab {
  to: string;
  label: string;
}

/** The primary nav tabs. Admin is only offered to admins; Account lives in the avatar menu. */
export function navTabs(isAdmin: boolean): NavTab[] {
  const tabs: NavTab[] = [
    { to: '/', label: 'Overview' },
    { to: '/links', label: 'Links' },
    { to: '/campaigns', label: 'Campaigns' },
  ];
  if (isAdmin) tabs.push({ to: '/admin', label: 'Admin' });
  return tabs;
}

/** The one-letter avatar label for an email ('?' when there is none). */
export function avatarInitial(email: string | undefined | null): string {
  const c = (email ?? '').trim().charAt(0);
  return c ? c.toUpperCase() : '?';
}

/** Minimal shape of the event target the shortcut guard inspects. */
export interface KeyTargetLike {
  tagName?: string;
  isContentEditable?: boolean;
}

export interface ShortcutEvent {
  key: string;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  target: EventTarget | KeyTargetLike | null;
}

/**
 * Whether a keydown should open New link: a bare `n` (no ctrl/meta/alt) with
 * focus outside any text-entry control. Select counts as one, since typing a
 * letter in a focused <select> jumps to an option.
 */
export function isNewLinkShortcut(e: ShortcutEvent): boolean {
  if (e.key !== 'n' || e.ctrlKey || e.metaKey || e.altKey) return false;
  const t = e.target as KeyTargetLike | null;
  if (t) {
    const tag = (t.tagName ?? '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return false;
    if (t.isContentEditable) return false;
  }
  return true;
}
