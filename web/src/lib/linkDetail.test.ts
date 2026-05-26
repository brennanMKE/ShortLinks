// Unit tests for the Link Detail pure logic (#0035): UTM bucket sorting,
// none-bucket detection, empty-stats gating, the single-string status label
// derivation (including denial reasons), and date formatting. No DOM or
// network — only the data shaping the view delegates to lib/linkDetail.ts.

import { describe, it, expect } from 'vitest';
import type { Link, ClickStats, UTMBucket } from './types';
import {
  NONE_BUCKET,
  isNoneBucket,
  sortBuckets,
  isEmptyStats,
  utmDimensions,
  statusLabel,
  formatDate,
} from './linkDetail';

function bucket(value: string, count: number): UTMBucket {
  return { value, count };
}

function stats(overrides: Partial<ClickStats> = {}): ClickStats {
  return {
    click_count: 0,
    by_source: [],
    by_medium: [],
    by_campaign: [],
    ...overrides,
  };
}

function link(overrides: Partial<Link> = {}): Link {
  return {
    id: 1,
    key: '8d0d93',
    destination_url: 'https://www.example.com/page',
    title: '',
    active: true,
    denied_reason: 0,
    created_at: '2026-05-25T12:00:00Z',
    expires_at: null,
    click_count: 0,
    ...overrides,
  };
}

describe('isNoneBucket', () => {
  it('detects the backend NoneBucket sentinel', () => {
    expect(isNoneBucket(bucket(NONE_BUCKET, 3))).toBe(true);
    expect(isNoneBucket(bucket('(none)', 3))).toBe(true);
  });

  it('treats empty / whitespace values as the none bucket defensively', () => {
    expect(isNoneBucket(bucket('', 1))).toBe(true);
    expect(isNoneBucket(bucket('   ', 1))).toBe(true);
  });

  it('treats a real value as not-none', () => {
    expect(isNoneBucket(bucket('email', 1))).toBe(false);
  });
});

describe('sortBuckets', () => {
  it('orders by count descending', () => {
    const sorted = sortBuckets([bucket('a', 1), bucket('b', 5), bucket('c', 3)]);
    expect(sorted.map((b) => b.value)).toEqual(['b', 'c', 'a']);
  });

  it('breaks count ties by value ascending (matches the server order)', () => {
    const sorted = sortBuckets([bucket('zeta', 2), bucket('alpha', 2)]);
    expect(sorted.map((b) => b.value)).toEqual(['alpha', 'zeta']);
  });

  it('does not mutate the input array', () => {
    const input = [bucket('a', 1), bucket('b', 5)];
    sortBuckets(input);
    expect(input.map((b) => b.value)).toEqual(['a', 'b']);
  });

  it('returns an empty array for null/undefined input', () => {
    expect(sortBuckets(undefined)).toEqual([]);
    expect(sortBuckets(null)).toEqual([]);
  });
});

describe('isEmptyStats', () => {
  it('is empty when stats are absent', () => {
    expect(isEmptyStats(undefined)).toBe(true);
    expect(isEmptyStats(null)).toBe(true);
  });

  it('is empty when click_count is 0 and every dimension is empty', () => {
    expect(isEmptyStats(stats())).toBe(true);
  });

  it('is not empty when there are clicks', () => {
    expect(isEmptyStats(stats({ click_count: 4 }))).toBe(false);
  });

  it('is not empty when a dimension has rows even if count reads 0', () => {
    expect(isEmptyStats(stats({ by_source: [bucket('email', 2)] }))).toBe(false);
  });
});

describe('utmDimensions', () => {
  it('returns the three labeled, sorted dimensions', () => {
    const dims = utmDimensions(
      stats({
        click_count: 6,
        by_source: [bucket('email', 2), bucket('twitter', 4)],
        by_medium: [bucket('social', 6)],
        by_campaign: [],
      }),
    );
    expect(dims.map((d) => d.label)).toEqual(['Source', 'Medium', 'Campaign']);
    expect(dims[0].dimension).toBe('source');
    // Sorted by count desc: twitter(4) before email(2).
    expect(dims[0].buckets.map((b) => b.value)).toEqual(['twitter', 'email']);
    expect(dims[2].buckets).toEqual([]);
  });

  it('yields empty buckets for absent stats without throwing', () => {
    const dims = utmDimensions(undefined);
    expect(dims).toHaveLength(3);
    expect(dims.every((d) => d.buckets.length === 0)).toBe(true);
  });
});

describe('statusLabel', () => {
  it('labels an active link "Active"', () => {
    expect(statusLabel(link({ active: true, denied_reason: 0 }))).toBe('Active');
  });

  it('labels a user-deactivated link "Inactive"', () => {
    expect(statusLabel(link({ active: false, denied_reason: 0 }))).toBe('Inactive');
  });

  it('labels a denied link with its reason regardless of active', () => {
    expect(statusLabel(link({ active: false, denied_reason: 2 }))).toBe('Denied: Phishing');
    expect(statusLabel(link({ active: false, denied_reason: 1 }))).toBe(
      'Denied: Malware or ransomware',
    );
  });

  it('labels a denied link with an unknown reason code just "Denied"', () => {
    expect(statusLabel(link({ active: false, denied_reason: 99 }))).toBe('Denied');
  });
});

describe('formatDate', () => {
  it('formats an RFC 3339 timestamp as a human date', () => {
    expect(formatDate('2026-05-25T12:00:00Z')).toMatch(/2026/);
  });

  it('returns the default sentinel for a null/empty value', () => {
    expect(formatDate(null)).toBe('Never');
    expect(formatDate(undefined)).toBe('Never');
    expect(formatDate('', 'No expiry')).toBe('No expiry');
  });

  it('falls back to the raw string for an unparseable value', () => {
    expect(formatDate('not-a-date')).toBe('not-a-date');
  });
});
