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

export interface FacetCount {
  /** The value, or '~none' for clicks without one. */
  value: string;
  clicks: number;
}

/** Values per UTM dimension, each counted with every other filter applied. */
export interface OverviewFacets {
  source: FacetCount[];
  medium: FacetCount[];
  utm_campaign: FacetCount[];
  content: FacetCount[];
  placement: FacetCount[];
  term: FacetCount[];
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
  facets: OverviewFacets;
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

// ── campaign-performance and campaign-comparison (#0143, #0164) ──────────

export interface MatrixCell {
  clicks: number;
  /** Set when exactly one link has clicks in the cell. */
  key?: string;
  /** Set when several do, most clicks first. */
  keys?: string[];
}

export interface MatrixRow {
  content_key: string;
  title: string;
  /** A pairing nobody clicked is absent, not zero. */
  cells: Record<string, MatrixCell>;
  total: number;
}

export interface CampaignPerformanceData {
  campaign: { slug: string; name: string; status: string; starts_on: string | null; ends_on: string | null };
  totals: {
    clicks: number;
    previous_clicks: number | null;
    per_active_day: number | null;
    links: number;
    links_clicked: number;
  };
  matrix: {
    by: string;
    columns: string[];
    rows: MatrixRow[];
    column_totals: Record<string, number>;
    total_rows: number;
  };
  removed_links_clicks: number;
  truncated?: boolean;
}

export interface CampaignComparisonRow {
  slug: string;
  name: string;
  status: string;
  clicks: number;
  previous_clicks: number | null;
  change_pct: number | null;
  clicks_per_active_day: number | null;
  top_content: { title: string; clicks: number } | null;
  top_source: SourceCount | null;
}

export interface CampaignComparisonData {
  campaigns: CampaignComparisonRow[];
}

/** GET /api/reports/{name}?{query}. Cookie session; throws ApiError on non-2xx. */
export function getReport<T>(name: string, query: string): Promise<ReportEnvelope<T>> {
  return apiGet<ReportEnvelope<T>>(`/api/reports/${name}?${query}`);
}
