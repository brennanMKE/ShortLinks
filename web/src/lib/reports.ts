// Types and client for GET /api/reports/{name} (#0139, #0140). Only the
// fields the Overview reads are typed. Mirrors internal/reports.

import { apiGet } from './api';

export interface ReportEnvelope<T> {
  report: string;
  window: { from: string; to: string; tz: string; partial_last_day: boolean };
  previous_window: { from: string; to: string } | null;
  filters: Record<string, unknown>;
  generated_at: string;
  highlights: string[];
  data: T;
}

export interface ReportDay {
  date: string;
  clicks: number;
}

export interface OverviewCampaign {
  slug: string | null;
  name: string;
  clicks: number;
}

export interface OverviewData {
  totals: {
    clicks: number;
    visitor_days: number;
    excluded_bots: number;
    links_clicked: number;
    links_total: number;
  };
  previous: { clicks: number; visitor_days: number | null } | null;
  timeseries: ReportDay[];
  previous_timeseries: ReportDay[];
  best_day: ReportDay | null;
  campaigns: OverviewCampaign[];
}

export interface SourceCount {
  value: string;
  clicks: number;
}

export interface GroupLink {
  key: string;
  label: string;
  utm_source: string;
  utm_medium: string;
  campaign_slug: string | null;
  clicks: number;
}

export interface ContentGroup {
  content_key: string;
  title: string;
  path: string;
  clicks: number;
  previous_clicks: number | null;
  change_pct: number | null;
  by_source: SourceCount[];
  links: GroupLink[];
}

export interface TopContentData {
  groups: ContentGroup[];
  total_groups: number;
  truncated?: boolean;
}

export interface TopLink {
  key: string;
  short_url: string;
  title: string;
  label: string;
  content_title: string;
  destination: string;
  campaign_slug: string | null;
  utm_source: string;
  utm_medium: string;
  clicks: number;
  previous_clicks: number | null;
  change_pct: number | null;
}

export interface TopLinksData {
  links: TopLink[];
  total_links: number;
}

export interface BreakdownData {
  dimension: string;
  total: number;
  previous_total: number | null;
  rows: { value: string; clicks: number; share: number; previous_clicks: number | null }[];
  other: { clicks: number; values: number } | null;
}

/** GET /api/reports/{name}?{query}. Cookie session; throws ApiError on non-2xx. */
export function getReport<T>(name: string, query: string): Promise<ReportEnvelope<T>> {
  return apiGet<ReportEnvelope<T>>(`/api/reports/${name}?${query}`);
}
