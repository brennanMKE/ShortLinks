// Shared application state. The SPA has no client-side router: navigation is a
// write to the `currentView` store, and App.svelte renders the matching view.

import { writable } from 'svelte/store';
import type { User, Link } from './types';

/** The set of top-level views App.svelte can render. */
export type View =
  | 'login'
  | 'dashboard'
  | 'link-detail'
  | 'account'
  | 'admin'
  | 'register-verify'  // magic-link registration landing (#0041)
  | 'recover-verify';  // magic-link recovery landing (#0041)

/**
 * The active view. Default is "login"; App.svelte flips it to "dashboard" once
 * GET /api/me confirms a session. Navigation between sections is a store write.
 */
export const currentView = writable<View>('login');

/** The authenticated user (from GET /api/me), or null when signed out. */
export const currentUser = writable<User | null>(null);

/**
 * The dashboard link list. Populated by GET /api/links and prepended to by the
 * SSE `link.created` stream (#0033). Empty until loaded.
 */
export const links = writable<Link[]>([]);

/**
 * The key of the link currently shown in the link-detail view, or null. Because
 * there is no URL routing, the detail view reads which link to load from here.
 */
export const selectedLinkKey = writable<string | null>(null);

/**
 * The magic-link token parsed from the landing URL (/register/verify or
 * /recover/verify). App.svelte sets this on load when it detects one of those
 * paths; the register-verify and recover-verify views read it on mount (#0041).
 * Cleared after the ceremony completes or fails.
 */
export const pendingVerifyToken = writable<string | null>(null);
