// Relative-date formatting for tables (#0155, docs/ui-redesign.md principle 11):
// "3d ago" for dates under 7 days old, an absolute date otherwise.

export const RELATIVE_DATE_DAYS = 7;

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

/** Absolute display date: "Nov 10" within the current year, "Nov 10, 2025" otherwise. */
export function formatAbsoluteDate(d: Date, now: Date = new Date()): string {
  const opts: Intl.DateTimeFormatOptions =
    d.getFullYear() === now.getFullYear()
      ? { month: 'short', day: 'numeric' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
  return d.toLocaleDateString('en-US', opts);
}

/** Full timestamp for the `title` tooltip. */
export function formatTimestamp(d: Date): string {
  return d.toLocaleString('en-US', {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });
}

/**
 * "just now" / "5m ago" / "3h ago" / "3d ago" below 7 days; the absolute date
 * from 7 days on. Future dates (clock skew) also fall back to absolute.
 */
export function formatRelativeDate(date: Date | string | number, now: Date = new Date()): string {
  const d = date instanceof Date ? date : new Date(date);
  if (Number.isNaN(d.getTime())) return '';
  const diff = now.getTime() - d.getTime();
  if (diff < 0) return formatAbsoluteDate(d, now);
  if (diff >= RELATIVE_DATE_DAYS * DAY) return formatAbsoluteDate(d, now);
  if (diff < MIN) return 'just now';
  if (diff < HOUR) return `${Math.floor(diff / MIN)}m ago`;
  if (diff < DAY) return `${Math.floor(diff / HOUR)}h ago`;
  return `${Math.floor(diff / DAY)}d ago`;
}
