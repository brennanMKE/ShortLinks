// Pure, framework-free helpers backing the Link Detail view (#0035). Keeping the
// UTM-bucket sorting/formatting, empty-stats detection, the human status label
// derivation, and date formatting here (rather than inline in the .svelte file)
// makes them unit-testable without a DOM — see linkDetail.test.ts.

import type { Link, ClickStats, UTMBucket } from './types';
import { linkStatus, deniedReasonLabel } from './links';

/**
 * The label the backend uses for clicks whose UTM dimension was NULL or empty
 * (internal/clicks/stats.go `NoneBucket`). The aggregation folds NULL and the
 * empty string into this single bucket, so the view treats it as the "no value"
 * row and may render it more quietly.
 */
export const NONE_BUCKET = '(none)';

/**
 * One UTM breakdown dimension prepared for display: a human label and its rows.
 * `dimension` is the raw key (`source`/`medium`/`campaign`) for keying/markup.
 */
export interface UTMDimension {
  dimension: 'source' | 'medium' | 'campaign';
  label: string;
  buckets: UTMBucket[];
}

/**
 * Whether a UTM bucket is the "no value" bucket (its value is the backend's
 * NoneBucket sentinel, or — defensively — empty/whitespace). The view renders
 * this row with a muted "(none)" label rather than a real UTM value.
 */
export function isNoneBucket(b: UTMBucket): boolean {
  return b.value === NONE_BUCKET || b.value.trim() === '';
}

/**
 * Sort one breakdown by count descending, with the value ascending as a stable
 * tiebreaker — mirroring the server's `ORDER BY count DESC, value ASC`. Returns
 * a new array (never mutates the input). Buckets that are missing/null collapse
 * to an empty array so a malformed/absent dimension renders as empty rather than
 * throwing.
 */
export function sortBuckets(buckets: UTMBucket[] | undefined | null): UTMBucket[] {
  if (!buckets) return [];
  return [...buckets].sort((a, b) => {
    if (b.count !== a.count) return b.count - a.count;
    return a.value.localeCompare(b.value);
  });
}

/**
 * Whether a UTM stats payload carries no information worth charting: it is
 * absent, reports zero total clicks, or every dimension is empty. The view uses
 * this to show a single "No click data yet" message instead of three empty
 * tables. (The AC: the UTM section is skipped when all counts are zero.)
 */
export function isEmptyStats(stats: ClickStats | undefined | null): boolean {
  if (!stats) return true;
  if (stats.click_count > 0) return false;
  const dims = [stats.by_source, stats.by_medium, stats.by_campaign];
  return dims.every((d) => !d || d.length === 0);
}

/**
 * The three UTM dimensions prepared for display: each labeled and sorted by
 * count descending. Dimensions with no rows are still returned (with an empty
 * `buckets` array) so the view can decide how to render an empty dimension; use
 * `isEmptyStats` to gate the whole section.
 */
export function utmDimensions(stats: ClickStats | undefined | null): UTMDimension[] {
  return [
    { dimension: 'source', label: 'Source', buckets: sortBuckets(stats?.by_source) },
    { dimension: 'medium', label: 'Medium', buckets: sortBuckets(stats?.by_medium) },
    { dimension: 'campaign', label: 'Campaign', buckets: sortBuckets(stats?.by_campaign) },
  ];
}

/**
 * A human, single-string status label for the detail header, derived from the
 * effective link state (PRD "Effective link states"). A denied link includes its
 * denial reason ("Denied: Phishing"); an active or inactive link is just
 * "Active"/"Inactive". The companion `linkStatus` (lib/links.ts) supplies the
 * machine state used for the badge CSS class and to gate the Deactivate action.
 */
export function statusLabel(link: Pick<Link, 'active' | 'denied_reason'>): string {
  const state = linkStatus(link as Link);
  if (state === 'denied') {
    const reason = deniedReasonLabel(link.denied_reason);
    return reason ? `Denied: ${reason}` : 'Denied';
  }
  if (state === 'inactive') return 'Inactive';
  return 'Active';
}

/**
 * Format an RFC 3339 timestamp as a human date for the detail view (e.g.
 * "May 25, 2026"). Falls back to the raw string when it does not parse, so a
 * malformed server value is shown rather than swallowed. A null/empty expiry
 * yields the "Never" sentinel so the view can label a non-expiring link.
 */
export function formatDate(iso: string | null | undefined, fallback = 'Never'): string {
  if (!iso) return fallback;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  });
}
