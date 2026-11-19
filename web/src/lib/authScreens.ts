// Pure helpers for the signed-out screens (#0172, docs/ui-redesign.md §3.10).

import { ApiError } from './api';

/** The next action an error offers inline, next to its message. */
export type NextAction = 'retry' | 'new-link' | 'sign-in' | 'none';

export interface Failure {
  message: string;
  action: NextAction;
  /** Button/link label for the action ('' when action is 'none'). */
  actionLabel: string;
}

export const ACTION_LABELS: Record<NextAction, string> = {
  retry: 'Try again',
  'new-link': 'Send a new link',
  'sign-in': 'Back to sign in',
  none: '',
};

export function failure(message: string, action: NextAction): Failure {
  return { message, action, actionLabel: ACTION_LABELS[action] };
}

/** Sign-in failure. Returns null for a silent dismissal (cancelled prompt). */
export function loginFailure(err: unknown): Failure | null {
  if (err instanceof ApiError) {
    if (err.status === 403) return failure('This account has been deactivated.', 'none');
    if (err.status === 429)
      return failure('Too many attempts. Wait a minute, then try again.', 'retry');
    if (err.status === 401)
      return failure('No matching passkey, or the request expired.', 'retry');
    return failure(err.message || 'Sign in failed.', 'retry');
  }
  if (typeof DOMException !== 'undefined' && err instanceof DOMException) {
    if (err.name === 'AbortError' || err.name === 'NotAllowedError') return null;
    return failure('The passkey prompt was cancelled or unavailable.', 'retry');
  }
  return failure('Could not reach the server. Check your connection.', 'retry');
}

export type VerifyKind = 'register' | 'recover';

/** Login query `mode=` values that open the Register / Recover form. */
export type LoginMode = 'signin' | 'register' | 'recover';

export function loginModeFromQuery(query: Record<string, string | undefined>): LoginMode {
  const v = query['mode'];
  return v === 'register' || v === 'recover' ? v : 'signin';
}

/** Login path for a mode, keeping the `redirect` target the user arrived with. */
export function loginModePath(mode: LoginMode, redirect: string | null): string {
  const params: string[] = [];
  if (mode !== 'signin') params.push(`mode=${mode}`);
  if (redirect) params.push(`redirect=${encodeURIComponent(redirect)}`);
  return params.length ? `/login?${params.join('&')}` : '/login';
}

/** Where the "Send a new link" action sends the user. */
export function newLinkPath(kind: VerifyKind): string {
  return `/login?mode=${kind}`;
}

// ── Verify-screen step indicator ──────────────────────────────────────────

export type StepState = 'done' | 'current' | 'todo' | 'failed';
export interface Step {
  label: string;
  state: StepState;
}

export const STEP_LABELS = ['Email confirmed', 'Create passkey', 'Done'] as const;

export type VerifyStatus = 'verifying' | 'creating' | 'finishing' | 'error';

/**
 * Steps for a verify screen. While the link is being checked step 1 is current;
 * the passkey prompt and the server round-trip are step 2; step 3 is only
 * reached once the session exists (the screen is then replaced). On error the
 * step that was running is marked failed; `failedAt` (0-based) says which.
 */
export function verifySteps(status: VerifyStatus, failedAt = 0): Step[] {
  const active = status === 'verifying' ? 0 : status === 'error' ? failedAt : 1;
  return STEP_LABELS.map((label, i) => {
    let state: StepState = 'todo';
    if (i < active) state = 'done';
    else if (i === active) state = status === 'error' ? 'failed' : 'current';
    // A link that could not be checked must not read "Email confirmed".
    const text: string = i === 0 && state === 'failed' ? 'Link not confirmed' : label;
    return { label: text, state };
  });
}

export const PASSKEY_PROMPT_HELP =
  'Your device will ask you to confirm with Face ID, Touch ID, a security key or your screen lock. That creates the passkey you sign in with next time. No password is involved.';
