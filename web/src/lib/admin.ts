// Pure, framework-free helpers backing the Admin view (#0037): the four admin
// sub-sections (settings, URL filters, users, audit log). Keeping the
// reason-code/value ↔ label maps, the deactivation `other`-requires-note
// validation, the URL-filter test-result message, the audit metadata/actor
// rendering, and the pagination math here (rather than inline in the .svelte
// file) makes them unit-testable without a DOM — see admin.test.ts.

import type { AdminUser, AuditEntry } from './types';
import type { FilterTestResult } from './api';

// ── Denial reason codes (URL filter rules) ──────────────────────────────────
// The six denial reason codes (1..6) from the PRD "Denial Reason Codes" table.
// Code 0 ("not denied") is never assigned to a rule, so it is not offered in the
// create/edit dropdown.

/** One option in the URL-filter reason-code dropdown: numeric code + label. */
export interface ReasonOption {
  code: number;
  label: string;
}

/** The denial reason codes 1..6 with their labels, in dropdown order. */
export const REASON_OPTIONS: readonly ReasonOption[] = [
  { code: 1, label: 'Malware or ransomware' },
  { code: 2, label: 'Phishing' },
  { code: 3, label: 'Spam' },
  { code: 4, label: 'Adult content' },
  { code: 5, label: 'Policy violation' },
  { code: 6, label: 'Other' },
] as const;

/**
 * Human-readable label for a denial `reason_code`. Mirrors the backend's
 * `filters.ReasonLabel`: 0 is "Not denied", 1..6 map to their labels, and any
 * unknown code falls back to "Other" so a label is always non-empty.
 */
export function reasonLabel(code: number): string {
  if (code === 0) return 'Not denied';
  const opt = REASON_OPTIONS.find((o) => o.code === code);
  return opt ? opt.label : 'Other';
}

// ── Deactivation reasons (user management) ───────────────────────────────────
// The six account.deactivated reason values from the PRD "Deactivation reasons"
// table. `note` is REQUIRED when the reason is `other`.

/** One option in the deactivation reason dropdown: stored value + label. */
export interface DeactivationReason {
  value: string;
  label: string;
}

/** The deactivation reason values with their labels, in dropdown order. */
export const DEACTIVATION_REASONS: readonly DeactivationReason[] = [
  { value: 'malware_distribution', label: 'Malware or ransomware distribution' },
  { value: 'phishing', label: 'Phishing' },
  { value: 'spam', label: 'Spam' },
  { value: 'harassment', label: 'Harassment' },
  { value: 'terms_violation', label: 'Terms of service violation' },
  { value: 'other', label: 'Other' },
] as const;

/**
 * Human-readable label for a stored deactivation reason value. An unknown value
 * is returned as-is so a server-stored value the UI doesn't know is still shown
 * rather than swallowed.
 */
export function deactivationReasonLabel(value: string): string {
  const r = DEACTIVATION_REASONS.find((d) => d.value === value);
  return r ? r.label : value;
}

/** Whether a string is one of the six known deactivation reason values. */
export function isValidDeactivationReason(value: string): boolean {
  return DEACTIVATION_REASONS.some((d) => d.value === value);
}

/**
 * Validate a deactivation form (reason + note) before submitting, mirroring the
 * server (internal/handlers/users.go): the reason must be one of the six values,
 * and the note is REQUIRED (non-empty after trimming) when reason is `other`.
 * Returns the trimmed note on success or a message to show. This is a
 * convenience gate so an invalid submit never round-trips; the server remains
 * the source of truth.
 */
export function validateDeactivation(
  reason: string,
  note: string,
): { note: string } | { error: string } {
  if (!isValidDeactivationReason(reason)) {
    return { error: 'Select a deactivation reason.' };
  }
  const trimmed = note.trim();
  if (reason === 'other' && trimmed === '') {
    return { error: 'A note is required when the reason is "Other".' };
  }
  return { note: trimmed };
}

/**
 * Whether the acting admin should even be offered the "Deactivate" control for a
 * given user: never for an admin account, and never for oneself (the backend
 * refuses both). Already-inactive users are handled separately (Reactivate).
 */
export function canDeactivate(user: AdminUser, currentUserId: number): boolean {
  return user.active && !user.is_admin && user.id !== currentUserId;
}

// ── URL-filter test tool ─────────────────────────────────────────────────────

/** The display state of a /admin/url-filters/test result. */
export type FilterTestNotice =
  | { kind: 'match'; ruleId: number; reasonCode: number; label: string; message: string }
  | { kind: 'no-match'; message: string };

/**
 * Map a POST /admin/url-filters/test response to the notice to display. A match
 * carries the matched rule id, reason code, and its label; no match yields the
 * "allowed" copy.
 */
export function filterTestNotice(result: FilterTestResult): FilterTestNotice {
  if (result.matched && result.reason_code !== undefined && result.rule_id !== undefined) {
    const label = reasonLabel(result.reason_code);
    return {
      kind: 'match',
      ruleId: result.rule_id,
      reasonCode: result.reason_code,
      label,
      message: `Blocked by rule #${result.rule_id} — ${label}`,
    };
  }
  return { kind: 'no-match', message: 'No matching rule — this URL would be allowed.' };
}

// ── Audit log rendering ──────────────────────────────────────────────────────

/**
 * Render an audit entry's actor for display. A NULL actor_id means a
 * system/anonymous action (e.g. account.registration_started); otherwise show
 * the numeric user id.
 */
export function actorLabel(entry: AuditEntry): string {
  return entry.actor_id === null ? 'system' : `user #${entry.actor_id}`;
}

/**
 * Render an audit entry's target for display, e.g. "user #5", "settings", or
 * "—" when there is no target. target_id is appended only when present.
 */
export function targetLabel(entry: AuditEntry): string {
  if (entry.target_type === null) return '—';
  if (entry.target_id === null) return entry.target_type;
  return `${entry.target_type} #${entry.target_id}`;
}

/**
 * Render an audit entry's `metadata` (arbitrary JSON) as a compact, stable,
 * one-line string for the table cell, e.g. `key=registrations_enabled,
 * new_value=true`. A null/empty metadata yields an empty string; a non-object
 * (string/number) is stringified directly. Keys are sorted so the output is
 * deterministic (and test-stable).
 */
export function formatMetadata(metadata: unknown): string {
  if (metadata === null || metadata === undefined) return '';
  if (typeof metadata !== 'object') return String(metadata);
  const obj = metadata as Record<string, unknown>;
  const keys = Object.keys(obj).sort();
  if (keys.length === 0) return '';
  return keys
    .map((k) => {
      const v = obj[k];
      const rendered =
        v === null || v === undefined
          ? ''
          : typeof v === 'object'
            ? JSON.stringify(v)
            : String(v);
      return `${k}=${rendered}`;
    })
    .join(', ');
}

/**
 * Format an RFC 3339 timestamp as a human date-time for the audit table (e.g.
 * "May 25, 2026, 12:00 PM"). Falls back to the raw string when it does not parse
 * so a malformed server value is shown rather than swallowed.
 */
export function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });
}

// ── Pagination math ──────────────────────────────────────────────────────────

/** Computed pagination state for a list page, derived from total + page + per_page. */
export interface PageInfo {
  page: number;
  perPage: number;
  total: number;
  totalPages: number;
  hasPrev: boolean;
  hasNext: boolean;
  /** 1-based index of the first item on this page (0 when empty). */
  firstItem: number;
  /** 1-based index of the last item on this page (0 when empty). */
  lastItem: number;
}

/**
 * Compute pagination state from the server envelope. totalPages is at least 1
 * (an empty list is "page 1 of 1"). hasNext/hasPrev gate the controls; firstItem
 * /lastItem give the "showing N–M of T" range. page/perPage are clamped to sane
 * minimums defensively.
 */
export function pageInfo(total: number, page: number, perPage: number): PageInfo {
  const safePerPage = Math.max(1, perPage);
  const safeTotal = Math.max(0, total);
  const totalPages = Math.max(1, Math.ceil(safeTotal / safePerPage));
  const safePage = Math.min(Math.max(1, page), totalPages);
  const firstItem = safeTotal === 0 ? 0 : (safePage - 1) * safePerPage + 1;
  const lastItem = safeTotal === 0 ? 0 : Math.min(safePage * safePerPage, safeTotal);
  return {
    page: safePage,
    perPage: safePerPage,
    total: safeTotal,
    totalPages,
    hasPrev: safePage > 1,
    hasNext: safePage < totalPages,
    firstItem,
    lastItem,
  };
}

/**
 * Parse a user-entered `user_id` filter string into a positive integer, or null
 * when it is empty (no filter) or not a positive integer. The view treats a
 * non-empty-but-invalid input as "invalid", distinct from "no filter".
 */
export function parseUserIdFilter(raw: string): { userId: number | null } | { error: string } {
  const trimmed = raw.trim();
  if (trimmed === '') return { userId: null };
  if (!/^\d+$/.test(trimmed)) {
    return { error: 'Enter a numeric user id.' };
  }
  const n = Number(trimmed);
  if (!Number.isInteger(n) || n <= 0) {
    return { error: 'Enter a numeric user id.' };
  }
  return { userId: n };
}

/** Whether the `registrations_enabled` setting value string is the truthy "true". */
export function registrationsEnabled(value: string | undefined): boolean {
  return value === 'true';
}
