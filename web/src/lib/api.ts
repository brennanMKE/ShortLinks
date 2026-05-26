// Typed fetch wrapper for the ShortLinks API. All requests are same-origin (the
// Vite dev server proxies /api, /auth, /u to the Go service; in production the Go
// binary serves the SPA itself), send the session cookie via
// `credentials: 'include'`, and throw a typed ApiError on a non-2xx response.

import type {
  User,
  Link,
  LinkList,
  LinkDetail,
  Credential,
  AuditEntry,
  FilterRule,
  AdminUser,
  Setting,
} from './types';

/**
 * Thrown by every helper on a non-2xx response. Carries the HTTP status and, when
 * the body was JSON, its parsed shape (the API returns `{error: "..."}` on
 * failures) so callers can branch on `status` (e.g. 401 → show login) or read a
 * machine-readable `error` code.
 */
export class ApiError extends Error {
  readonly status: number;
  readonly body: unknown;

  constructor(status: number, message: string, body: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
  }
}

/** Shape of the JSON error body the API returns on failures. */
interface ErrorBody {
  error?: string;
  message?: string;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const init: RequestInit = {
    method,
    credentials: 'include',
    headers: { Accept: 'application/json' },
  };
  if (body !== undefined) {
    init.headers = { ...init.headers, 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }

  const res = await fetch(path, init);

  // 204 No Content (and any empty body) parses to undefined.
  const text = await res.text();
  let parsed: unknown = undefined;
  if (text.length > 0) {
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
  }

  if (!res.ok) {
    const err = parsed as ErrorBody | undefined;
    const message = err?.error ?? err?.message ?? `HTTP ${res.status}`;
    throw new ApiError(res.status, message, parsed);
  }

  return parsed as T;
}

/** GET a JSON resource. */
export function apiGet<T>(path: string): Promise<T> {
  return request<T>('GET', path);
}

/** POST a JSON body and parse the JSON response. */
export function apiPost<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('POST', path, body);
}

/** PATCH a JSON body and parse the JSON response. */
export function apiPatch<T>(path: string, body?: unknown): Promise<T> {
  return request<T>('PATCH', path, body);
}

/** DELETE a resource and parse any JSON response. */
export function apiDelete<T>(path: string): Promise<T> {
  return request<T>('DELETE', path);
}

// ── Endpoint helpers ────────────────────────────────────────────────────────
// Thin, typed wrappers over the routes the views call. Views in #0032–#0037 use
// these rather than building paths by hand.

/** GET /api/me — current user profile; throws ApiError(401) when unauthenticated. */
export function getMe(): Promise<User> {
  return apiGet<User>('/api/me');
}

/** GET /api/links — the caller's links (paginated, most-recent-first). */
export function listLinks(page = 1, perPage = 20): Promise<LinkList> {
  return apiGet<LinkList>(`/api/links?page=${page}&per_page=${perPage}`);
}

/** GET /api/links/{key} — link detail plus UTM click stats. */
export function getLink(key: string): Promise<LinkDetail> {
  return apiGet<LinkDetail>(`/api/links/${encodeURIComponent(key)}`);
}

/** Body accepted by POST /api/links. */
export interface CreateLinkInput {
  destination_url: string;
  title?: string;
  key?: string;
  expires_at?: string | null;
}

/** POST /api/links — create (or dedup-reactivate) a short link. */
export function createLink(input: CreateLinkInput): Promise<Link> {
  return apiPost<Link>('/api/links', input);
}

/** Fields PATCH /api/links/{key} can update. */
export interface UpdateLinkInput {
  title?: string;
  destination_url?: string;
  expires_at?: string | null;
}

/** PATCH /api/links/{key} — update title, destination, or expiry. */
export function updateLink(key: string, input: UpdateLinkInput): Promise<Link> {
  return apiPatch<Link>(`/api/links/${encodeURIComponent(key)}`, input);
}

/** DELETE /api/links/{key} — deactivate (soft delete) a link. */
export function deactivateLink(key: string): Promise<{ message: string }> {
  return apiDelete<{ message: string }>(`/api/links/${encodeURIComponent(key)}`);
}

/** POST /auth/logout — invalidate the current session. */
export function logout(): Promise<void> {
  return apiPost<void>('/auth/logout');
}

// ── Account (passkeys) ──────────────────────────────────────────────────────

/** GET /account/credentials — the caller's registered passkeys. */
export function listCredentials(): Promise<Credential[]> {
  return apiGet<Credential[]>('/account/credentials');
}

/** PATCH /account/credentials/{id} — rename a passkey. */
export function renameCredential(id: number, deviceName: string): Promise<Credential> {
  return apiPatch<Credential>(`/account/credentials/${id}`, { device_name: deviceName });
}

/** DELETE /account/credentials/{id} — revoke a passkey. */
export function revokeCredential(id: number): Promise<{ message: string }> {
  return apiDelete<{ message: string }>(`/account/credentials/${id}`);
}

// ── Admin ───────────────────────────────────────────────────────────────────

/** GET /admin/users — all accounts (admin only). */
export function listUsers(): Promise<{ users: AdminUser[] }> {
  return apiGet<{ users: AdminUser[] }>('/admin/users');
}

/** GET /admin/settings — runtime settings (admin only). */
export function getSettings(): Promise<{ settings: Setting[] }> {
  return apiGet<{ settings: Setting[] }>('/admin/settings');
}

/** PATCH /admin/settings — update a runtime setting (admin only). */
export function updateSetting(key: string, value: string): Promise<unknown> {
  return apiPatch<unknown>('/admin/settings', { key, value });
}

/** GET /admin/url-filters — all URL filter rules (admin only). */
export function listFilterRules(): Promise<{ rules: FilterRule[] }> {
  return apiGet<{ rules: FilterRule[] }>('/admin/url-filters');
}

/** GET /admin/audit — paginated audit log (admin only). */
export function listAudit(page = 1, perPage = 20): Promise<{
  audit_log: AuditEntry[];
  total: number;
  page: number;
  per_page: number;
}> {
  return apiGet(`/admin/audit?page=${page}&per_page=${perPage}`);
}
