// Pure, framework-free helpers backing the Dashboard view (#0033). Keeping the
// short-URL builder, client-side URL validation, denial-reason labels, and the
// create-response → notice mapping here (rather than inline in the .svelte file)
// makes them unit-testable without a DOM — see links.test.ts.

import type { Link } from './types';
import { ApiError } from './api';

/**
 * The public base under which every short link resolves. The redirect namespace
 * is the fixed `/u/` prefix (see PRD "URL Format"): `https://go.sstools.co/u/{key}`.
 *
 * This is intentionally the PRODUCTION display base, not the dev origin. The
 * dashboard shows users the URL they will share, which is always the branded
 * domain; the backend's BASE_URL differs in dev (localhost) but a copied
 * localhost link is useless to share. The AC fixes the displayed value to
 * `https://go.sstools.co/u/{key}`.
 */
export const SHORT_URL_BASE = 'https://go.sstools.co';

/**
 * Build the shareable short URL for a link from its key, e.g.
 * `https://go.sstools.co/u/8d0d93`. The key is path-segment encoded defensively;
 * generated keys are base-62 and custom aliases are validated to a url-safe
 * alphabet server-side, so encoding is normally a no-op.
 */
export function shortUrl(key: string): string {
  return `${SHORT_URL_BASE}/u/${encodeURIComponent(key)}`;
}

/**
 * Client-side pre-validation of a destination URL, mirroring the server's
 * `validDestinationURL` (internal/handlers/links.go): a syntactically valid
 * absolute URL with an http or https scheme and a non-empty host. This is a
 * convenience gate so the form can flag an obviously bad URL before a round
 * trip; the server remains the source of truth (and still returns 400 on a bad
 * URL it rejects).
 */
export function isValidHttpUrl(raw: string): boolean {
  const trimmed = raw.trim();
  if (trimmed === '') return false;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return false;
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return false;
  return parsed.hostname !== '';
}

/**
 * Human-readable label for a `denied_reason` code (PRD "Denial Reason Codes").
 * Used to describe a denied link in the list when the create response did not
 * carry a server `label`. Code 0 ("not denied") returns an empty string.
 */
export function deniedReasonLabel(code: number): string {
  switch (code) {
    case 1:
      return 'Malware or ransomware';
    case 2:
      return 'Phishing';
    case 3:
      return 'Spam';
    case 4:
      return 'Adult content';
    case 5:
      return 'Policy violation';
    case 6:
      return 'Other';
    default:
      return '';
  }
}

/** The contextual banner shown after a create attempt, mapped from the response. */
export type CreateNotice =
  | { kind: 'created'; link: Link; shortUrl: string }
  | { kind: 'duplicate'; link: Link; shortUrl: string; message: string }
  | { kind: 'denied'; message: string }
  | { kind: 'error'; field: 'key' | 'destination_url' | null; message: string };

/** The "already shortened" copy the AC mandates for a duplicate response. */
export const DUPLICATE_NOTICE =
  "This URL was already shortened — here's your existing link";

/**
 * Map a successful POST /api/links response to the notice to display. A
 * `duplicate: true` link yields the "already shortened" banner (while still
 * surfacing the returned link + its short URL); otherwise a freshly created
 * link. Both branches carry the built short URL so the view can render it with a
 * copy button.
 */
export function noticeForCreated(link: Link): CreateNotice {
  const url = shortUrl(link.key);
  if (link.duplicate === true) {
    return { kind: 'duplicate', link, shortUrl: url, message: DUPLICATE_NOTICE };
  }
  return { kind: 'created', link, shortUrl: url };
}

/** Shape of the 422 url_denied body (internal/handlers/links.go Create). */
interface UrlDeniedBody {
  error?: string;
  reason?: number;
  label?: string;
}

/**
 * Map an ApiError thrown by `createLink` to the notice to display:
 *  - 422 url_denied → the denial reason label (from the body's `label`, falling
 *    back to the code → label table, then a generic message).
 *  - 409 → an inline error on the custom alias field (key already taken).
 *  - 400 → an inline error on the destination URL field (bad URL).
 *  - anything else → a generic error banner.
 */
export function noticeForError(err: unknown): CreateNotice {
  if (err instanceof ApiError) {
    if (err.status === 422) {
      const body = (err.body ?? {}) as UrlDeniedBody;
      const label =
        body.label ||
        (typeof body.reason === 'number' ? deniedReasonLabel(body.reason) : '') ||
        'This URL is not allowed';
      return {
        kind: 'denied',
        message: `This URL was blocked: ${label}`,
      };
    }
    if (err.status === 409) {
      return {
        kind: 'error',
        field: 'key',
        message: 'That custom alias is already taken — choose another.',
      };
    }
    if (err.status === 400) {
      return {
        kind: 'error',
        field: 'destination_url',
        message: 'Enter a valid absolute http(s) URL.',
      };
    }
    if (err.status === 401) {
      return { kind: 'error', field: null, message: 'Your session expired. Please sign in again.' };
    }
    return {
      kind: 'error',
      field: null,
      message: err.message || 'Could not create the link. Please try again.',
    };
  }
  return {
    kind: 'error',
    field: null,
    message: 'Could not reach the server. Check your connection and try again.',
  };
}

/**
 * The display state of a link row, derived from (active, denied_reason) per the
 * PRD "Effective link states" table. Surfaced as a status badge in the list.
 */
export function linkStatus(link: Link): 'active' | 'denied' | 'inactive' {
  if (link.denied_reason > 0) return 'denied';
  if (!link.active) return 'inactive';
  return 'active';
}

/**
 * The bare host of a destination URL for the list's "destination domain" column
 * (AC). Falls back to the raw string if it does not parse as a URL.
 */
export function destinationDomain(destinationUrl: string): string {
  try {
    return new URL(destinationUrl).hostname;
  } catch {
    return destinationUrl;
  }
}
