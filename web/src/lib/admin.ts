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
 * the actor's email, falling back to the numeric id if the server could not
 * resolve one.
 */
export function actorLabel(entry: AuditEntry): string {
  if (entry.actor_id === null) return 'system';
  return entry.actor_email ?? `user #${entry.actor_id}`;
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

/** Whether the `registrations_enabled` setting value string is the truthy "true". */
export function registrationsEnabled(value: string | undefined): boolean {
  return value === 'true';
}

// ── Section routes (#0154) ──────────────────────────────────────────────────
// Each admin section has its own hash route, `#/admin/<section>`.

export type AdminSection = 'settings' | 'filters' | 'users' | 'audit';

export const ADMIN_SECTIONS: readonly { id: AdminSection; label: string }[] = [
  { id: 'settings', label: 'Settings' },
  { id: 'filters', label: 'URL filters' },
  { id: 'users', label: 'Users' },
  { id: 'audit', label: 'Audit log' },
];

/** The section `#/admin` redirects to. */
export const DEFAULT_ADMIN_SECTION: AdminSection = 'settings';

/** The hash path for a section. */
export function adminSectionPath(section: AdminSection): string {
  return `/admin/${section}`;
}

/** Narrows a route param to a known section, or null (unknown or missing). */
export function parseAdminSection(param: string | undefined | null): AdminSection | null {
  return ADMIN_SECTIONS.find((s) => s.id === param)?.id ?? null;
}

/** What the Admin view should do for a given `:section` route param. */
export type AdminRouteAction =
  | { kind: 'redirect'; to: string }
  | { kind: 'show'; section: AdminSection }
  | { kind: 'not-found' };

export function adminRouteAction(param: string | undefined | null): AdminRouteAction {
  if (param === undefined || param === null || param === '') {
    return { kind: 'redirect', to: adminSectionPath(DEFAULT_ADMIN_SECTION) };
  }
  const section = parseAdminSection(param);
  return section ? { kind: 'show', section } : { kind: 'not-found' };
}

// ── Audit filters (#0171) ───────────────────────────────────────────────────
// Filters live in the hash: `#/admin/audit?email=…&action=a,b&range=7d&page=2`.

/** Action names the audit log records, grouped for the Action select. */
export const AUDIT_ACTION_GROUPS: readonly { label: string; actions: readonly string[] }[] = [
  {
    label: 'Account',
    actions: [
      'account.registration_started',
      'account.registered',
      'account.login',
      'account.logout',
      'account.recovery_started',
      'account.recovered',
      'account.deactivated',
      'account.reactivated',
    ],
  },
  { label: 'Credential', actions: ['credential.added', 'credential.revoked', 'session.revoked_all'] },
  { label: 'API token', actions: ['api_token.created', 'api_token.revoked'] },
  { label: 'Link', actions: ['link.created', 'link.deactivated', 'link.reactivated', 'link.denied'] },
  {
    label: 'Campaign',
    actions: [
      'campaign.created',
      'campaign.updated',
      'campaign.deleted',
      'campaign.link_assigned',
      'campaign.link_unassigned',
    ],
  },
  { label: 'URL filter', actions: ['url_filter.created', 'url_filter.updated', 'url_filter.deleted'] },
  { label: 'Settings', actions: ['settings.updated'] },
];

export type AuditRange = 'all' | '24h' | '7d' | '30d' | 'custom';

export const AUDIT_RANGES: readonly { id: AuditRange; label: string }[] = [
  { id: 'all', label: 'All time' },
  { id: '24h', label: 'Last 24 hours' },
  { id: '7d', label: 'Last 7 days' },
  { id: '30d', label: 'Last 30 days' },
  { id: 'custom', label: 'Custom range' },
];

export interface AuditFilters {
  /** Exact account email, or '' for everyone. */
  email: string;
  /** Selected actions; empty means all. */
  actions: string[];
  range: AuditRange;
  /**
   * For a preset range: the instant the window started, fixed when the preset
   * was chosen so paging does not drift ('' until chosen; falls back to now).
   */
  since: string;
  /** YYYY-MM-DD, used when range is 'custom' (either may be ''). */
  from: string;
  to: string;
  page: number;
}

export const DEFAULT_AUDIT_FILTERS: AuditFilters = {
  email: '',
  actions: [],
  range: 'all',
  since: '',
  from: '',
  to: '',
  page: 1,
};

const DATE_RE = /^\d{4}-\d{2}-\d{2}$/;

/** Reads the filters out of a route query; unknown or malformed values fall back to defaults. */
export function parseAuditQuery(q: URLSearchParams): AuditFilters {
  const rangeRaw = q.get('range');
  const range = AUDIT_RANGES.find((r) => r.id === rangeRaw)?.id ?? 'all';
  const date = (v: string | null) => (v && DATE_RE.test(v) ? v : '');
  const instant = (v: string | null) => (v && !Number.isNaN(Date.parse(v)) ? v : '');
  const page = Number(q.get('page'));
  return {
    email: (q.get('email') ?? '').trim(),
    actions: (q.get('action') ?? '')
      .split(',')
      .map((a) => a.trim())
      .filter(Boolean),
    range,
    since: range !== 'all' && range !== 'custom' ? instant(q.get('since')) : '',
    from: range === 'custom' ? date(q.get('from')) : '',
    to: range === 'custom' ? date(q.get('to')) : '',
    page: Number.isInteger(page) && page > 0 ? page : 1,
  };
}

/** The `?…` query string for a filter state, omitting defaults ('' when none). */
export function auditQueryString(f: AuditFilters): string {
  const p = new URLSearchParams();
  if (f.email) p.set('email', f.email);
  if (f.actions.length > 0) p.set('action', f.actions.join(','));
  if (f.range !== 'all') p.set('range', f.range);
  if (f.since && f.range !== 'all' && f.range !== 'custom') p.set('since', f.since);
  if (f.range === 'custom') {
    if (f.from) p.set('from', f.from);
    if (f.to) p.set('to', f.to);
  }
  if (f.page > 1) p.set('page', String(f.page));
  const s = p.toString();
  return s ? `?${s}` : '';
}

/** Whether any filter narrows the log (page does not count). */
export function hasAuditFilters(f: AuditFilters): boolean {
  return f.email !== '' || f.actions.length > 0 || f.range !== 'all';
}

const RANGE_MS: Record<string, number> = {
  '24h': 24 * 3600_000,
  '7d': 7 * 24 * 3600_000,
  '30d': 30 * 24 * 3600_000,
};

/** A local-time YYYY-MM-DD as an instant at local midnight (+ `addDays`). */
function localDay(date: string, addDays = 0): string {
  const [y, m, d] = date.split('-').map(Number);
  return new Date(y, m - 1, d + addDays).toISOString();
}

/**
 * The server `from`/`to` instants for a filter state. Presets are relative to
 * `now`; a custom range covers whole local days, `to` inclusive.
 */
export function auditTimeBounds(f: AuditFilters, now: Date = new Date()): { from?: string; to?: string } {
  if (f.range === 'custom') {
    return {
      from: f.from ? localDay(f.from) : undefined,
      to: f.to ? localDay(f.to, 1) : undefined,
    };
  }
  const ms = RANGE_MS[f.range];
  if (!ms) return {};
  return { from: f.since || new Date(now.getTime() - ms).toISOString() };
}

/** The window start to pin in the hash when a preset is chosen ('' for all/custom). */
export function presetSince(range: AuditRange, now: Date = new Date()): string {
  const ms = RANGE_MS[range];
  return ms ? new Date(now.getTime() - ms).toISOString() : '';
}

/** Users whose email contains `q` (case-insensitive), best prefix matches first, at most `limit`. */
export function matchUsers(users: readonly AdminUser[], q: string, limit = 8): AdminUser[] {
  const needle = q.trim().toLowerCase();
  const hits = users.filter((u) => u.email.toLowerCase().includes(needle));
  hits.sort((a, b) => {
    const pa = a.email.toLowerCase().startsWith(needle) ? 0 : 1;
    const pb = b.email.toLowerCase().startsWith(needle) ? 0 : 1;
    return pa - pb || a.email.localeCompare(b.email);
  });
  return hits.slice(0, limit);
}

/** The pretty-printed metadata shown when a row is expanded ('' when there is none). */
export function metadataDetail(metadata: unknown): string {
  if (metadata === null || metadata === undefined) return '';
  if (typeof metadata !== 'object') return String(metadata);
  return Object.keys(metadata as object).length === 0 ? '' : JSON.stringify(metadata, null, 2);
}
