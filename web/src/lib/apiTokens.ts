// Pure helpers backing the API Tokens panel on the Account view: the expiry
// choices and their conversion to the expires_at the server takes, name
// validation, status labels, and the sample request shown with a new token.
// Kept out of the .svelte file so they are unit-testable — see
// apiTokens.test.ts.

import type { ApiToken } from './types';
import { ApiError } from './api';
import { formatDate } from './account';

/** Mirrors auth.MaxAPITokenNameLen. */
export const MAX_TOKEN_NAME_LEN = 100;

export type ExpiryChoice = '30' | '90' | '365' | 'custom' | 'never';

export const EXPIRY_CHOICES: { value: ExpiryChoice; label: string }[] = [
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
  { value: 'custom', label: 'Custom date' },
  { value: 'never', label: 'Never expires' },
];

export const DEFAULT_EXPIRY: ExpiryChoice = '90';

/**
 * Resolve an expiry choice to the RFC 3339 `expires_at` the server takes, or
 * null for a token that never expires. A custom date (YYYY-MM-DD from a date
 * input) expires at the END of that local day, and must be in the future.
 */
export function expiresAtFor(
  choice: ExpiryChoice,
  customDate: string,
  now: Date = new Date(),
): { value: string | null } | { error: string } {
  if (choice === 'never') return { value: null };
  if (choice === 'custom') {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(customDate);
    if (!m) return { error: 'Choose an expiration date.' };
    const end = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 23, 59, 59);
    if (Number.isNaN(end.getTime()) || end <= now) {
      return { error: 'The expiration date must be in the future.' };
    }
    return { value: end.toISOString() };
  }
  const days = Number(choice);
  return { value: new Date(now.getTime() + days * 24 * 60 * 60 * 1000).toISOString() };
}

/** Validate a token name: non-empty after trimming, within the server cap. */
export function validateTokenName(raw: string): { value: string } | { error: string } {
  const trimmed = raw.trim();
  if (trimmed === '') return { error: 'Give the token a name, e.g. the script or agent that will use it.' };
  if ([...trimmed].length > MAX_TOKEN_NAME_LEN) {
    return { error: `Keep the name under ${MAX_TOKEN_NAME_LEN} characters.` };
  }
  return { value: trimmed };
}

/** Map a create failure to a message (409 = a live token already has that name). */
export function createErrorMessage(err: unknown): string {
  if (err instanceof ApiError && err.status === 409) {
    return 'You already have an active token with that name.';
  }
  if (err instanceof ApiError && err.status === 400) {
    return 'The server rejected this token. Check the name and expiration.';
  }
  return 'Could not create the token. Please try again.';
}

/** The "Expires" cell: "Never", a date, or "Expired <date>". */
export function expiryLabel(token: ApiToken): string {
  if (token.expires_at === null) return 'Never';
  const date = formatDate(token.expires_at);
  return token.status === 'expired' ? `Expired ${date}` : date;
}

/** Human status for the badge. */
export function statusLabel(token: ApiToken): string {
  return token.status === 'active' ? 'Active' : token.status === 'expired' ? 'Expired' : 'Revoked';
}

/**
 * Sort for display: active tokens first, then expired, then revoked; newest
 * first within each group (the server already returns newest first).
 */
export function sortTokens(tokens: ApiToken[]): ApiToken[] {
  const rank = { active: 0, expired: 1, revoked: 2 } as const;
  return [...tokens].sort((a, b) => rank[a.status] - rank[b.status]);
}

/** The sample request shown beside a freshly minted token. */
export function curlExample(origin: string, token: string): string {
  return `curl -H "Authorization: Bearer ${token}" ${origin}/api/me`;
}
