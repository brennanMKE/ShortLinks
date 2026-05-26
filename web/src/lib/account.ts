// Pure, framework-free helpers backing the Account view (#0036): passkey
// management. Keeping the date formatting, the rename validation, the
// last-credential guard, and — most importantly — the mapping of a revoke
// failure (notably the backend's 409 `cannot_revoke_last_credential`) to a
// human message here (rather than inline in the .svelte file) makes them
// unit-testable without a DOM — see account.test.ts.

import type { Credential } from './types';
import { ApiError } from './api';

/**
 * The friendly message shown when the user tries to revoke their only remaining
 * passkey. The backend refuses with 409 + {"error":"cannot_revoke_last_credential"}
 * (internal/handlers/credentials.go); per the PRD a user "cannot revoke the last
 * one without adding a replacement first". The copy matches the issue AC.
 */
export const LAST_CREDENTIAL_MESSAGE =
  'You cannot revoke your only passkey. Add another passkey first.';

/** The backend error code returned in the 409 last-credential body. */
export const LAST_CREDENTIAL_CODE = 'cannot_revoke_last_credential';

/** Generic fallback shown when a revoke fails for an unexpected reason. */
export const REVOKE_FAILED_MESSAGE = 'Could not revoke this passkey. Please try again.';

/** Generic fallback shown when a rename fails for an unexpected reason. */
export const RENAME_FAILED_MESSAGE = 'Could not rename this passkey. Please try again.';

/**
 * Map a failure thrown while revoking a credential to the message to display.
 * The 409 last-credential case (matched by status AND/OR the machine-readable
 * error code, so a future status change still resolves correctly) gets the
 * clear, actionable copy; everything else gets the generic retry message. A 401
 * is handled by the caller (session expiry → login) before this is consulted.
 */
export function revokeErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409 || extractErrorCode(err.body) === LAST_CREDENTIAL_CODE) {
      return LAST_CREDENTIAL_MESSAGE;
    }
  }
  return REVOKE_FAILED_MESSAGE;
}

/** Whether a thrown error is the last-credential refusal (for the caller's branching). */
export function isLastCredentialError(err: unknown): boolean {
  return (
    err instanceof ApiError &&
    (err.status === 409 || extractErrorCode(err.body) === LAST_CREDENTIAL_CODE)
  );
}

/**
 * Best-effort read of the `error` code from a parsed JSON error body. The API
 * returns `{error: "..."}`; anything else (string body, null) yields undefined.
 */
function extractErrorCode(body: unknown): string | undefined {
  if (body && typeof body === 'object' && 'error' in body) {
    const code = (body as { error?: unknown }).error;
    return typeof code === 'string' ? code : undefined;
  }
  return undefined;
}

/**
 * Validate a proposed device name for rename: non-empty after trimming. Returns
 * the trimmed value on success (the backend trims too, so we send the canonical
 * form) or an error message to show. The server caps/validates further; this is
 * a convenience gate so an empty save never round-trips.
 */
export function validateDeviceName(raw: string): { value: string } | { error: string } {
  const trimmed = raw.trim();
  if (trimmed === '') {
    return { error: 'A passkey name cannot be empty.' };
  }
  return { value: trimmed };
}

/**
 * Whether revoking the given credential should even be offered. The backend is
 * the source of truth (it returns 409), but disabling the control when this is
 * the only credential gives immediate feedback and matches the PRD rule. A list
 * of length <= 1 means the candidate is (or would be) the last one.
 */
export function canRevoke(credentials: Credential[]): boolean {
  return credentials.length > 1;
}

/**
 * Format an RFC 3339 timestamp as a human date for the account view (e.g.
 * "May 25, 2026"). Falls back to the raw string when it does not parse so a
 * malformed server value is shown rather than swallowed. A null/empty value
 * (e.g. a passkey that has never been used) yields the `fallback` sentinel.
 */
export function formatDate(iso: string | null | undefined, fallback = 'Never'): string {
  if (!iso) return fallback;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
}

/**
 * The "last used" display string for a credential. A synced iCloud Keychain
 * passkey may never have a `last_used_at` recorded distinct from creation; in
 * that case we show "Never used" so the empty value reads intentionally.
 */
export function lastUsedLabel(iso: string | null | undefined): string {
  return formatDate(iso, 'Never used');
}
