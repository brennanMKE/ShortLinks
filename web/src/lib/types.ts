// TypeScript interfaces mirroring the Go backend's JSON shapes. Field names are
// snake_case to match the API exactly (see internal/handlers/*.go). These are the
// shared contract the views in #0032–#0037 build on.

/** GET /api/me — the current user profile used to gate the admin view. */
export interface User {
  id: number;
  email: string;
  is_admin: boolean;
}

/**
 * A short link as returned by GET /api/links (each item), GET /api/links/{key},
 * POST /api/links (with `duplicate`), and PATCH /api/links/{key}. Matches
 * internal/handlers/links.go `linkView`.
 */
export interface Link {
  id: number;
  key: string;
  destination_url: string;
  title: string;
  active: boolean;
  denied_reason: number;
  created_at: string;
  expires_at: string | null;
  click_count: number;
  /** Only present on the POST /api/links create response. */
  duplicate?: boolean;
}

/** GET /api/links — paginated list envelope. */
export interface LinkList {
  links: Link[];
  page: number;
  per_page: number;
  total: number;
}

/** One value/count row of a UTM breakdown dimension. */
export interface UTMBucket {
  value: string;
  count: number;
}

/**
 * Per-link click analytics (internal/clicks/stats.go `UTMStats`). Surfaced on
 * the link-detail response as `utm_stats`.
 */
export interface ClickStats {
  click_count: number;
  by_source: UTMBucket[];
  by_medium: UTMBucket[];
  by_campaign: UTMBucket[];
}

/** GET /api/links/{key} — link detail with optional UTM breakdown. */
export interface LinkDetail extends Link {
  utm_stats?: ClickStats;
}

/**
 * A registered passkey (GET /account/credentials item). Matches
 * internal/handlers/credentials.go `credentialView`.
 */
export interface Credential {
  id: number;
  device_name: string;
  aaguid: string;
  device_hint: string;
  sign_count: number;
  created_at: string;
  last_used_at: string | null;
}

/**
 * One audit-log row (GET /admin/audit item). Matches
 * internal/handlers/audit.go `auditRecordView`.
 */
export interface AuditEntry {
  id: number;
  actor_id: number | null;
  user_id: number | null;
  action: string;
  target_type: string | null;
  target_id: number | null;
  metadata: unknown;
  ip_address: string | null;
  created_at: string;
}

/**
 * A URL filter rule (GET /admin/url-filters item). Matches
 * internal/handlers/url_filters.go `ruleView`.
 */
export interface FilterRule {
  id: number;
  pattern: string;
  reason_code: number;
  reason_label: string;
  description: string;
  active: boolean;
  created_by: number | null;
  created_at: string;
}

/** An admin-visible user account (GET /admin/users item). */
export interface AdminUser {
  id: number;
  email: string;
  is_admin: boolean;
  active: boolean;
  created_at: string;
  last_login_at?: string;
}

/** A runtime server setting (GET /admin/settings item). */
export interface Setting {
  key: string;
  value: string;
  updated_at?: string;
}
