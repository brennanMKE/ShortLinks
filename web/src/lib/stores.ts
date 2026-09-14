// Shared application state that is NOT part of the URL. Which screen is
// showing now lives entirely in the hash router (lib/hashRouter.svelte.ts,
// lib/routing.ts — #0120); this file holds everything else: the
// authenticated user, the dashboard's link list, and the magic-link token
// captured before the hash router is even wired up.

import { writable } from 'svelte/store';
import type { User, Link } from './types';

/** The authenticated user (from GET /api/me), or null when signed out. */
export const currentUser = writable<User | null>(null);

/**
 * The dashboard link list. Populated by GET /api/links and prepended to by the
 * SSE `link.created` stream (#0033). Empty until loaded.
 */
export const links = writable<Link[]>([]);

/**
 * The magic-link token parsed from the landing URL (/register/verify or
 * /recover/verify). App.svelte sets this on load when it detects one of those
 * paths — before the hash router is initialized, since these stay real paths
 * rather than becoming hash routes (#0120 Notes: they are landing targets for
 * emailed links) — and the register-verify/recover-verify views read it on
 * mount (#0041). Cleared after the ceremony completes or fails.
 */
export const pendingVerifyToken = writable<string | null>(null);
