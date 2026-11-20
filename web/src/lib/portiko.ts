// Portiko sign-in (#0133). When the server runs with PORTIKO_ENABLED it signs
// people in through a full-page redirect to /auth/portiko/login and the SPA runs
// no passkey ceremony itself. The server says which mode it is in with the
// `portiko` field of GET /api/public/home, so one SPA build serves both until
// #0136 removes the passkey screens.
//
// Pure helpers, except the two functions that navigate. The return-target
// encoding and the redirect-loop guard are unit-tested in portiko.test.ts.

import { parseHash, redirectTargetFrom } from './routing';
import type { HomeInfo } from './home';

export const PORTIKO_LOGIN_PATH = '/auth/portiko/login';
export const PORTIKO_LOGOUT_PATH = '/auth/logout';
/** Portiko's passkey management; it redirects to the account's page, or to sign-in first (#0137). */
export const PORTIKO_PASSKEYS_PATH = '/id/passkeys';

/** A second automatic redirect inside this window means the first one did not sign anyone in. */
export const AUTO_REDIRECT_WINDOW_MS = 15_000;
export const AUTO_REDIRECT_KEY = 'sl.portikoRedirectAt';

/** True when the server signs in through Portiko. Unknown (a failed fetch) reads as off. */
export function isPortiko(info: HomeInfo | null | undefined): boolean {
  return info?.portiko === true;
}

/**
 * The return_to for a hash. rp's `localPath` only refuses a path that could
 * leave the site, so a `#…` fragment survives and the whole hash route, query
 * included, comes back as `/#/route?query`. On the login route itself the
 * target is its own `redirect` param, so landing on /login?redirect=/campaigns/x
 * still returns to /campaigns/x.
 */
export function returnToFromHash(hash: string): string {
  const { path, query, rawQuery } = parseHash(hash);
  if (path === '/login') {
    const target = redirectTargetFrom(query);
    return target === '/' ? '/' : `/#${target}`;
  }
  if (path === '/') return '/';
  return `/#${path}${rawQuery ? `?${rawQuery}` : ''}`;
}

/** URL that starts a sign-in (or, with `create`, opens Portiko on account creation) and comes back to `hash`. */
export function portikoLoginURL(hash: string, opts: { create?: boolean } = {}): string {
  const params = new URLSearchParams();
  params.set('return_to', returnToFromHash(hash));
  if (opts.create) params.set('prompt', 'create');
  return `${PORTIKO_LOGIN_PATH}?${params.toString()}`;
}

/** Whether Login offers "Create an account": Portiko on and enrollment open (#0125). */
export function portikoSignUpVisible(info: HomeInfo | null): boolean {
  return isPortiko(info) && info !== null && info.registrations_enabled;
}

/**
 * Loop guard for the automatic redirect a signed-out deep link takes. If the
 * last one was moments ago it did not produce a session (cookies blocked, a
 * provider error), so show the sign-in card instead of bouncing forever.
 */
export function mayAutoRedirect(lastAt: number | null, now: number): boolean {
  if (lastAt === null) return true;
  const age = now - lastAt;
  return !(age >= 0 && age < AUTO_REDIRECT_WINDOW_MS);
}

function sessionStore(): Storage | null {
  try {
    return sessionStorage;
  } catch {
    return null;
  }
}

/** Forgets the loop guard; called once a sign-in has worked, so a later 401 can redirect again at once. */
export function clearAutoRedirectGuard(): void {
  try {
    sessionStore()?.removeItem(AUTO_REDIRECT_KEY);
  } catch {
    // ignore
  }
}

/** Sends the browser to Portiko's sign-in. Returns false, doing nothing, when the loop guard says not to. */
export function autoRedirectToPortiko(hash: string, now: number = Date.now()): boolean {
  const store = sessionStore();
  let last: number | null = null;
  try {
    const raw = store?.getItem(AUTO_REDIRECT_KEY);
    last = raw ? Number(raw) : null;
    if (last !== null && Number.isNaN(last)) last = null;
  } catch {
    last = null;
  }
  if (!mayAutoRedirect(last, now)) return false;
  try {
    store?.setItem(AUTO_REDIRECT_KEY, String(now));
  } catch {
    // Without storage the guard cannot work; redirect anyway.
  }
  window.location.replace(portikoLoginURL(hash));
  return true;
}

/**
 * Signs out by a real form POST, so the browser follows the server's 302 (to
 * `/`, possibly through the provider's end-session page). fetch() cannot do
 * the provider hop, and without it the provider's own session would stay.
 */
export function portikoSignOut(): void {
  try {
    sessionStorage.removeItem(AUTO_REDIRECT_KEY);
  } catch {
    // ignore
  }
  const form = document.createElement('form');
  form.method = 'POST';
  form.action = PORTIKO_LOGOUT_PATH;
  form.style.display = 'none';
  document.body.appendChild(form);
  form.submit();
}
