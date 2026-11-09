// Pure logic for the signed-out home page (#0125). No DOM, no Svelte runtime,
// so it is unit-tested directly in home.test.ts.

/** How long the home page waits before sending the visitor on. Fixed, not a setting. */
export const HOME_REDIRECT_DELAY_MS = 3000;

/** GET /api/public/home body: exactly these two facts, no other settings. */
export interface HomeInfo {
  redirect_url: string;
  registrations_enabled: boolean;
}

/**
 * Show the redirect page only for a signed-out visitor at the bare root when
 * enrollment is closed and a primary URL is set. Any other combination keeps
 * today's behavior (the sign-in bounce).
 */
export function shouldShowHome(
  signedIn: boolean,
  path: string,
  info: HomeInfo | null,
): boolean {
  if (signedIn || path !== '/' || info === null) return false;
  return info.redirect_url !== '' && !info.registrations_enabled;
}

/** Host shown in "Taking you to <host>…"; falls back to the raw string if it does not parse. */
export function redirectHost(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

/**
 * Whether Login offers "Register". Only when the server said enrollment is
 * open: a closed answer or a failed fetch (null) hides it, because a sign-up
 * the server will refuse is worse than none. The server-side gate in
 * /auth/register/start is the enforcement; this is UX only.
 */
export function registerOptionVisible(info: HomeInfo | null): boolean {
  return info !== null && info.registrations_enabled;
}
