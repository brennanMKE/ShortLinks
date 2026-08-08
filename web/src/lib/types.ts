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
  /**
   * Discrete campaign/UTM columns (#0099) — additive alongside
   * destination_url, which keeps carrying the composed (baked) URL.
   * campaign_id is null when the link is not assigned to a campaign; the
   * five UTM fields and placement are "" when never set (including every
   * link created before this migration).
   */
  campaign_id: number | null;
  utm_source: string;
  utm_medium: string;
  utm_campaign: string;
  utm_term: string;
  utm_content: string;
  placement: string;
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
 *
 * click_count and every by_* breakdown already exclude bot/crawler clicks
 * (#0101 — is_bot = TRUE). excluded_bot_count is that same exclusion made
 * visible rather than silent: the number of bot-flagged clicks left out of
 * click_count, so a UI can show it alongside the total instead of the total
 * just quietly being smaller than raw activity with no explanation. #0103
 * owns actually surfacing it in the UI; this type just tracks the payload
 * the backend already sends.
 */
export interface ClickStats {
  click_count: number;
  excluded_bot_count: number;
  by_source: UTMBucket[];
  by_medium: UTMBucket[];
  by_campaign: UTMBucket[];
}

/**
 * One day bucket from the clicks-over-time series (internal/clicks/stats.go
 * `DayBucket`). date is "YYYY-MM-DD" (UTC); count is clicks that day.
 */
export interface DayBucket {
  date: string;  // "YYYY-MM-DD"
  count: number;
}

/**
 * Clicks-over-time series for a link (#0049). Surfaced on the link-detail
 * response as `timeseries`. days only includes dates with at least one click;
 * the frontend fills gaps for continuous charts.
 */
export interface TimeseriesResult {
  days: DayBucket[];
}

/** GET /api/links/{key} — link detail with optional UTM breakdown and timeseries. */
export interface LinkDetail extends Link {
  utm_stats?: ClickStats;
  timeseries?: TimeseriesResult;
  /**
   * Present only when the link is currently assigned to a campaign (#0099).
   * "" otherwise, matching campaign_id being null.
   */
  campaign_name?: string;
  campaign_slug?: string;
}

/**
 * A campaign (GET /api/campaigns item, GET /api/campaigns/{slug}). Matches
 * internal/handlers/campaigns.go `campaignView` (#0098). The default_utm_*
 * fields are what selecting this campaign prefills onto the UTM builder —
 * every prefilled value stays editable, the campaign never locks a field
 * (#0099).
 */
export interface Campaign {
  id: number;
  name: string;
  slug: string;
  description: string;
  starts_at: string | null;
  ends_at: string | null;
  archived: boolean;
  default_utm_source: string;
  default_utm_medium: string;
  default_utm_campaign: string;
  default_utm_term: string;
  default_utm_content: string;
  created_at: string;
  updated_at: string;
}

/**
 * A campaign paired with its ALL-TIME link_count/total_clicks (#0102). GET
 * /api/campaigns item shape. Matches internal/handlers/campaigns.go
 * `campaignListItemView`.
 *
 * These two numbers carry no window — see CampaignDetail's doc comment for
 * how that differs from the WINDOWED numbers the detail view also shows.
 */
export interface CampaignWithCounts extends Campaign {
  link_count: number;
  total_clicks: number;
}

/**
 * Campaign-scoped click analytics (internal/clicks/stats.go `CampaignStats`).
 * click_count/excluded_bot_count and every by_* breakdown are bot-excluded
 * (#0101) and WINDOWED to the resolved [from, to) range — the campaign's own
 * starts_at/ends_at when both are set, otherwise the trailing 30 days
 * (through yesterday; today's clicks are never included). This is a
 * DIFFERENT number, on a different time scale, from
 * CampaignDetail.total_clicks (all-time) — see CampaignDetail's doc comment.
 * #0103 must label the two distinctly rather than reconciling them.
 *
 * window_from/window_to (#0103 fix 4) are the resolved [from, to) window the
 * queries above actually ran against — "YYYY-MM-DD", UTC, matching
 * DayBucket's date-string convention. For a dated campaign already in
 * flight, `to` is clamped to today by clicks.campaignWindow, so this can be
 * a materially SHORTER span than the campaign's own starts_at/ends_at (a
 * campaign running Jul 1 – Dec 31 queried today, Aug 8, resolves to roughly
 * Jul 1 – Aug 8, not the full ~6 months). lib/campaigns.ts's windowLabel/
 * windowDayCount read these fields rather than re-deriving the window from
 * the campaign's nominal dates client-side, which would drift from the
 * clamp and silently overstate the window (understating "clicks per day" by
 * the same factor).
 */
export interface CampaignStats {
  click_count: number;
  excluded_bot_count: number;
  by_source: UTMBucket[];
  by_medium: UTMBucket[];
  by_content: UTMBucket[];
  by_referer: UTMBucket[];
  window_from: string; // "YYYY-MM-DD"
  window_to: string; // "YYYY-MM-DD"
}

/**
 * GET /api/campaigns/{slug} — campaign metadata, its ALL-TIME
 * link_count/total_clicks, every link CURRENTLY assigned to it, and (when
 * the backend has a stats provider wired, which production always does) its
 * WINDOWED CampaignStats + clicks-over-time series for one resolved window.
 * Matches internal/handlers/campaigns.go `campaignDetailView`.
 *
 * FOUR NUMBERS, TWO TIME SCALES (#0102 constraint 2 — pinned by
 * TestCampaignsGet_TotalClicksIsAllTimeStatsClickCountIsWindowed on the Go
 * side; do NOT "fix" this by making them agree):
 *   - link_count         — ALL-TIME, current membership count.
 *   - total_clicks        — ALL-TIME, every click this campaign ever
 *                           recorded (including from links since reassigned
 *                           or unassigned).
 *   - stats.click_count   — WINDOWED + bot-excluded, the number the summary
 *                           and (indirectly, via GET .../stats) the links
 *                           table were computed from.
 *   - the links table's per-link counts (GET .../stats's `by_link`, a
 *     separate #0103 fetch) — WINDOWED, the same window as
 *     stats.click_count, so they sum to IT, not to total_clicks.
 * The UI must label each one so a user can tell which scale it is on.
 */
export interface CampaignDetail extends CampaignWithCounts {
  links: Link[];
  stats?: CampaignStats;
  timeseries?: TimeseriesResult;
}

/**
 * One row of the campaign's per-link click breakdown (GET
 * /api/campaigns/{slug}/stats `by_link`). Matches internal/clicks/stats.go
 * `LinkBucket`. WINDOWED and bot-excluded, the same window as the
 * CampaignStats it accompanies. link_id/key/title are null/empty only for
 * the (currently unreachable) case of a click whose link_id itself is NULL.
 */
export interface LinkBucket {
  link_id: number | null;
  key: string;
  title: string;
  count: number;
}

/**
 * One named per-link series in the campaign's per-link clicks-over-time
 * chart (GET /api/campaigns/{slug}/stats `series_by_link`) — #0104's chart
 * input, unused by #0103 but typed here since it ships in the same response
 * CampaignRollup below models.
 */
export interface LinkSeries {
  link_id: number | null;
  key: string;
  title: string;
  is_other: boolean;
  days: DayBucket[];
}

/**
 * GET /api/campaigns/{slug}/stats — the full campaign rollup: CampaignStats'
 * fields (embedded at the top level, matching the Go handler's
 * `campaignStatsResponse`, which embeds `clicks.CampaignStats`), the
 * clicks-over-time series, the uncapped per-link breakdown, and the
 * capped-plus-"Other" per-link series. #0103 uses `by_link` for the links
 * table's "clicks in window"/"share of total" columns; `timeseries` and
 * `series_by_link` are #0104's chart slot.
 */
export interface CampaignRollup extends CampaignStats {
  timeseries: TimeseriesResult;
  by_link: LinkBucket[];
  series_by_link: LinkSeries[];
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
