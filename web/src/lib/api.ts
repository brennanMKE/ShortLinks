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
  Campaign,
  CampaignWithCounts,
  CampaignDetail,
  CampaignRollup,
} from './types';
import type {
  ServerCredentialAssertion,
  AssertionFinishPayload,
  ServerCredentialCreation,
  AttestationFinishPayload,
} from './webauthn';

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

/**
 * Body accepted by POST /api/links. campaign_id/campaign_slug (#0099) are an
 * optional, mutually-alternative way to assign the link to one of the
 * caller's own campaigns at create time (campaign_id wins if both are sent).
 * The five utm_* fields and placement are the discrete columns; they are
 * expected to describe the SAME values already baked into destination_url
 * (see composeUtmUrl in lib/utm.ts) — the backend stores exactly what it is
 * given rather than re-deriving one from the other.
 */
export interface CreateLinkInput {
  destination_url: string;
  title?: string;
  key?: string;
  expires_at?: string | null;
  campaign_id?: number;
  campaign_slug?: string;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_term?: string;
  utm_content?: string;
  placement?: string;
}

/** POST /api/links — create (or dedup-reactivate) a short link. */
export function createLink(input: CreateLinkInput): Promise<Link> {
  return apiPost<Link>('/api/links', input);
}

/**
 * Fields PATCH /api/links/{key} can update. The five utm_* fields and
 * placement (#0099) let the edit form save a builder repopulated via
 * utmParamsFromLink (lib/utm.ts) back in lockstep with a changed
 * destination_url. Campaign membership is NOT patchable here — it only
 * changes via the dedicated assign/unassign campaign-links endpoints below.
 */
export interface UpdateLinkInput {
  title?: string;
  destination_url?: string;
  expires_at?: string | null;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_term?: string;
  utm_content?: string;
  placement?: string;
}

/** PATCH /api/links/{key} — update title, destination, expiry, or UTM/placement. */
export function updateLink(key: string, input: UpdateLinkInput): Promise<Link> {
  return apiPatch<Link>(`/api/links/${encodeURIComponent(key)}`, input);
}

/** DELETE /api/links/{key} — deactivate (soft delete) a link. */
export function deactivateLink(key: string): Promise<{ message: string }> {
  return apiDelete<{ message: string }>(`/api/links/${encodeURIComponent(key)}`);
}

// ── Campaigns (#0098, #0099, #0102, #0103) ──────────────────────────────────

/**
 * GET /api/campaigns — the caller's campaigns (archived included; #0103's
 * CampaignsList filters those client-side by default). Each item carries its
 * real, ALL-TIME link_count/total_clicks (#0102).
 */
export function listCampaigns(): Promise<{ campaigns: CampaignWithCounts[] }> {
  return apiGet('/api/campaigns');
}

/** Body accepted by POST /api/campaigns. Only name is required — see internal/handlers/campaigns.go createCampaignRequest. */
export interface CreateCampaignInput {
  name: string;
  description?: string;
  starts_at?: string | null;
  ends_at?: string | null;
  default_utm_source?: string;
  default_utm_medium?: string;
  default_utm_campaign?: string;
  default_utm_term?: string;
  default_utm_content?: string;
}

/** POST /api/campaigns — create a campaign; returns 201 with the full campaign object. */
export function createCampaign(input: CreateCampaignInput): Promise<Campaign> {
  return apiPost<Campaign>('/api/campaigns', input);
}

/**
 * Fields PATCH /api/campaigns/{slug} can update — every field optional so the
 * handler can distinguish "absent" from "present" (mirrors
 * patchCampaignRequest). The slug itself is never patchable — it is fixed at
 * creation (#0098 downstream constraint 4).
 */
export interface UpdateCampaignInput {
  name?: string;
  description?: string;
  starts_at?: string | null;
  ends_at?: string | null;
  archived?: boolean;
  default_utm_source?: string;
  default_utm_medium?: string;
  default_utm_campaign?: string;
  default_utm_term?: string;
  default_utm_content?: string;
}

/** PATCH /api/campaigns/{slug} — update name/description/dates/archived/UTM defaults. */
export function updateCampaign(slug: string, input: UpdateCampaignInput): Promise<Campaign> {
  return apiPatch<Campaign>(`/api/campaigns/${encodeURIComponent(slug)}`, input);
}

/**
 * GET /api/campaigns/{slug} — campaign metadata, its links, and (when a
 * stats provider is wired) its windowed CampaignStats + clicks-over-time
 * series. See types.ts's CampaignDetail doc comment for the all-time-vs-
 * windowed numbers this response mixes.
 */
export function getCampaign(slug: string): Promise<CampaignDetail> {
  return apiGet<CampaignDetail>(`/api/campaigns/${encodeURIComponent(slug)}`);
}

/**
 * GET /api/campaigns/{slug}/stats — the full campaign rollup (CampaignStats,
 * timeseries, by_link, series_by_link), all read from one snapshot. `from`/
 * `to` are optional "YYYY-MM-DD" dates; omitting both applies the server's
 * default window (the campaign's own starts_at/ends_at when both are set,
 * otherwise the trailing 30 days). #0103 calls this with no window to get
 * `by_link` for the links table's "clicks in window"/"share" columns, on the
 * SAME default window GET /api/campaigns/{slug} already used for its stats.
 */
export function getCampaignStats(slug: string, from?: string, to?: string): Promise<CampaignRollup> {
  const params = new URLSearchParams();
  if (from) params.set('from', from);
  if (to) params.set('to', to);
  const qs = params.toString();
  return apiGet<CampaignRollup>(
    `/api/campaigns/${encodeURIComponent(slug)}/stats${qs ? `?${qs}` : ''}`,
  );
}

/**
 * POST /api/campaigns/{slug}/links — assign existing links (by key) to the
 * campaign. Capped at 50 keys per request and NOT atomic across keys (see
 * internal/handlers/campaigns.go AssignLinks' doc comment) — #0103's
 * CampaignDetail chunks larger requests via lib/campaigns.ts's chunkKeys and
 * surfaces a partial-success message rather than assuming all-or-nothing.
 */
export function assignLinksToCampaign(slug: string, keys: string[]): Promise<{ links: Link[] }> {
  return apiPost<{ links: Link[] }>(`/api/campaigns/${encodeURIComponent(slug)}/links`, { keys });
}

/** DELETE /api/campaigns/{slug}/links/{key} — unassign one link from the campaign. */
export function unassignLinkFromCampaign(slug: string, key: string): Promise<{ message: string }> {
  return apiDelete<{ message: string }>(
    `/api/campaigns/${encodeURIComponent(slug)}/links/${encodeURIComponent(key)}`,
  );
}

/**
 * One row of POST /api/campaigns/{slug}/links/batch's body (#0105). Mirrors
 * internal/handlers/campaigns.go's batchCreateLinkRow: destination_url is
 * the row's FULLY COMPOSED URL — the client bakes each row's UTM values into
 * it via lib/campaigns.ts's composeBatchRowDestinationUrl (the SAME
 * composeUtmUrl helper single-create uses), so the server does not
 * recompose it. Structurally identical to lib/campaigns.ts's
 * BatchCreateLinkRowPayload; kept as a separate declaration here rather than
 * imported, matching this file's existing convention of defining its own
 * *Input shapes independently of lib/ (e.g. CreateLinkInput above).
 */
export interface BatchCreateLinkRowInput {
  destination_url: string;
  title?: string;
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_term?: string;
  utm_content?: string;
  placement?: string;
}

/**
 * POST /api/campaigns/{slug}/links/batch — create one new short link per row
 * (already filtered to non-blank rows by the caller — see
 * lib/campaigns.ts's buildBatchCreateRows), all assigned to this campaign,
 * in ONE atomic server call (#0105: "prefer one server call — a partial
 * failure halfway through a client-side loop leaves the campaign in a state
 * the user did not ask for and cannot easily undo"). The whole request
 * either creates every row or (400/422/500) creates nothing — see
 * internal/handlers/campaigns.go's BatchCreateLinks doc comment.
 * skipped_blank_rows always comes back 0 here since the caller already
 * dropped blank rows before composing the request; the field exists on the
 * response type because the SERVER also defends against a blank row arriving
 * (e.g. a future caller that does not pre-filter).
 */
export function batchCreateLinksForCampaign(
  slug: string,
  rows: BatchCreateLinkRowInput[],
): Promise<{ links: Link[]; skipped_blank_rows: number }> {
  return apiPost<{ links: Link[]; skipped_blank_rows: number }>(
    `/api/campaigns/${encodeURIComponent(slug)}/links/batch`,
    { rows },
  );
}

/** POST /auth/logout — invalidate the current session. */
export function logout(): Promise<void> {
  return apiPost<void>('/auth/logout');
}

/**
 * POST /auth/logout/all — "sign out everywhere" (#0094): revokes every
 * session for the caller's account, including this one, on every device and
 * client (browser, iPhone app). It never touches enrolled passkeys — those
 * are what the next sign-in uses. Returns the number of sessions revoked.
 */
export function logoutAll(): Promise<{ message: string; revoked_count: number }> {
  return apiPost<{ message: string; revoked_count: number }>('/auth/logout/all');
}

// ── Auth ceremony (login + registration entry) ──────────────────────────────

/**
 * GET /auth/login/start — issue a WebAuthn assertion challenge. An optional
 * email narrows the prompt via `allowCredentials`; absent it the server issues a
 * discoverable (conditional-UI) challenge. The response is the
 * `CredentialAssertion` JSON (`{publicKey, mediation?}`) the browser glue
 * decodes; the shape is identical whether or not the email is registered, so it
 * never leaks account existence.
 */
export function loginStart(email?: string): Promise<ServerCredentialAssertion> {
  const trimmed = email?.trim();
  const path = trimmed
    ? `/auth/login/start?email=${encodeURIComponent(trimmed)}`
    : '/auth/login/start';
  return apiGet<ServerCredentialAssertion>(path);
}

/**
 * POST /auth/login/finish — submit the serialized assertion. On success the
 * server sets the session cookie and returns `{user_id}`; a deactivated account
 * yields ApiError(403) and any verification failure yields ApiError(401).
 */
export function loginFinish(
  assertion: AssertionFinishPayload,
): Promise<{ user_id: number }> {
  return apiPost<{ user_id: number }>('/auth/login/finish', assertion);
}

/**
 * POST /auth/register/start — submit an email to begin registration. The server
 * returns a generic `{message}` (200) whether or not the email is already
 * registered, or ApiError(403) when registration is closed.
 */
export function registerStart(email: string): Promise<{ message: string }> {
  return apiPost<{ message: string }>('/auth/register/start', { email });
}

/**
 * GET /auth/register/verify?token=… — exchange the magic-link token for
 * WebAuthn `PublicKeyCredentialCreationOptions`. Called by the register-verify
 * landing view after the SPA loads (#0041). Returns ApiError(400/401/410) for
 * invalid or expired tokens; ApiError(404) if the session challenge has expired.
 */
export function registerVerify(token: string): Promise<ServerCredentialCreation> {
  return apiGet<ServerCredentialCreation>(
    `/auth/register/verify?token=${encodeURIComponent(token)}`,
  );
}

/**
 * POST /auth/register/finish?token=…&device_name=… — submit the serialized
 * attestation to complete registration. The token and optional device_name are
 * query parameters (the handler reads them from the URL so the raw attestation
 * body is passed untouched to go-webauthn). On success the server sets the
 * session cookie and returns `{id, email, is_admin}`. Errors: 400 for
 * invalid/expired token or failed attestation.
 *
 * See: internal/handlers/auth.go (RegisterFinish).
 */
export function registerFinish(
  token: string,
  attestation: AttestationFinishPayload,
  deviceName?: string,
): Promise<{ id: number; email: string; is_admin: boolean }> {
  let path = `/auth/register/finish?token=${encodeURIComponent(token)}`;
  if (deviceName) {
    path += `&device_name=${encodeURIComponent(deviceName)}`;
  }
  return apiPost<{ id: number; email: string; is_admin: boolean }>(path, attestation);
}

/**
 * POST /auth/recover — submit an email to begin account recovery. The server
 * returns a generic `{message}` (200) whether or not the email is registered,
 * so this endpoint never leaks account existence. Called by the Login view's
 * recover sub-form.
 */
export function recoverStart(email: string): Promise<{ message: string }> {
  return apiPost<{ message: string }>('/auth/recover', { email });
}

/**
 * GET /auth/recover/verify?token=… — exchange the recovery magic-link token
 * for WebAuthn `PublicKeyCredentialCreationOptions`. Called by the
 * recover-verify landing view. The response shape is identical to
 * `registerVerify`; the helpers in `webauthn.ts` are reused verbatim.
 */
export function recoverVerify(token: string): Promise<ServerCredentialCreation> {
  return apiGet<ServerCredentialCreation>(
    `/auth/recover/verify?token=${encodeURIComponent(token)}`,
  );
}

/**
 * POST /auth/recover/finish?token=…&device_name=… — submit the serialized
 * attestation to complete account recovery. The token and optional device_name
 * are query parameters (the handler reads them from the URL so the raw
 * attestation body is passed untouched to go-webauthn). On success the server
 * adds the new credential to the EXISTING account, sets the session cookie,
 * and returns `{user_id}` (differs from `registerFinish` which returns
 * `{id, email, is_admin}`). Errors: 400 for invalid/expired token or failed
 * attestation.
 *
 * See: internal/handlers/auth.go (RecoverFinish), internal/auth/recovery.go
 * (FinishRecovery).
 */
export function recoverFinish(
  token: string,
  attestation: AttestationFinishPayload,
  deviceName?: string,
): Promise<{ user_id: number }> {
  let path = `/auth/recover/finish?token=${encodeURIComponent(token)}`;
  if (deviceName) {
    path += `&device_name=${encodeURIComponent(deviceName)}`;
  }
  return apiPost<{ user_id: number }>(path, attestation);
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

/**
 * POST /admin/users/{id}/deactivate — deactivate a non-admin user (admin only).
 * `reason` is one of the six deactivation reason values; `note` is required by
 * the server when reason is `other`. Returns the updated user.
 */
export function deactivateUser(id: number, reason: string, note: string): Promise<AdminUser> {
  return apiPost<AdminUser>(`/admin/users/${id}/deactivate`, { reason, note });
}

/**
 * POST /admin/users/{id}/reactivate — reactivate a user (admin only). `note` is
 * optional and recorded in the account.reactivated audit metadata. Returns the
 * updated user.
 */
export function reactivateUser(id: number, note: string): Promise<AdminUser> {
  return apiPost<AdminUser>(`/admin/users/${id}/reactivate`, { note });
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

/** Body accepted by POST /admin/url-filters. */
export interface CreateFilterRuleInput {
  pattern: string;
  reason_code: number;
  description: string;
}

/** POST /admin/url-filters — create a URL filter rule (admin only); returns 201 with the new rule. */
export function createFilterRule(input: CreateFilterRuleInput): Promise<FilterRule> {
  return apiPost<FilterRule>('/admin/url-filters', input);
}

/** Fields PATCH /admin/url-filters/{id} can update (all optional). */
export interface UpdateFilterRuleInput {
  pattern?: string;
  reason_code?: number;
  description?: string;
  active?: boolean;
}

/** PATCH /admin/url-filters/{id} — partial update of a rule (admin only). */
export function updateFilterRule(id: number, input: UpdateFilterRuleInput): Promise<FilterRule> {
  return apiPatch<FilterRule>(`/admin/url-filters/${id}`, input);
}

/** DELETE /admin/url-filters/{id} — remove a rule (admin only). */
export function deleteFilterRule(id: number): Promise<{ message: string }> {
  return apiDelete<{ message: string }>(`/admin/url-filters/${id}`);
}

/** Result of POST /admin/url-filters/test. */
export interface FilterTestResult {
  matched: boolean;
  reason_code?: number;
  rule_id?: number;
}

/**
 * POST /admin/url-filters/test — evaluate a URL against the active rules (admin
 * only). A dry run; never inserts a link. Returns whether it matched and, when
 * it did, the matched rule id and reason code.
 */
export function testFilterRule(url: string): Promise<FilterTestResult> {
  return apiPost<FilterTestResult>('/admin/url-filters/test', { url });
}

/** Envelope returned by GET /admin/audit. */
export interface AuditPage {
  audit_log: AuditEntry[];
  total: number;
  page: number;
  per_page: number;
}

/**
 * GET /admin/audit — paginated audit log (admin only), newest-first. An optional
 * `userId` narrows to a single actor/target via `?user_id=`.
 */
export function listAudit(page = 1, perPage = 50, userId?: number): Promise<AuditPage> {
  let path = `/admin/audit?page=${page}&per_page=${perPage}`;
  if (userId !== undefined) {
    path += `&user_id=${userId}`;
  }
  return apiGet<AuditPage>(path);
}
