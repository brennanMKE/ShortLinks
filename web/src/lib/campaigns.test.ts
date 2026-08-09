// Unit tests for the campaigns list/detail data-shaping helpers (#0103).
// No DOM or Svelte — pure function tests only.

import { describe, it, expect, afterEach, vi } from 'vitest';
import type { CampaignStats, CampaignWithCounts, Link, LinkBucket } from './types';
import {
  visibleCampaigns,
  campaignDateRangeLabel,
  formatDateOnly,
  toDateInput,
  toIsoDate,
  windowDayCount,
  windowLabel,
  clicksPerDayAverage,
  campaignUtmDimensions,
  isEmptyCampaignChannelStats,
  buildLinkRows,
  unlistedClickCount,
  sortLinkRows,
  chunkKeys,
  parseKeysInput,
  copyAllShortUrlsText,
  campaignQrZipUrl,
  campaignExportCsvUrl,
  joinSentences,
  MAX_ASSIGN_KEYS_PER_REQUEST,
  type CampaignLinkRow,
  emptyBatchChannelRow,
  initialBatchRows,
  isBlankBatchRow,
  nonBlankBatchRows,
  duplicateBatchRowIndices,
  composeBatchRowDestinationUrl,
  buildBatchCreateRows,
  MAX_BATCH_CREATE_ROWS_PER_REQUEST,
  type BatchChannelRow,
} from './campaigns';

function campaign(overrides: Partial<CampaignWithCounts> = {}): CampaignWithCounts {
  return {
    id: 1,
    name: 'Summer fair',
    slug: 'summer-fair',
    description: '',
    starts_at: null,
    ends_at: null,
    archived: false,
    default_utm_source: '',
    default_utm_medium: '',
    default_utm_campaign: '',
    default_utm_term: '',
    default_utm_content: '',
    created_at: '2026-06-01T00:00:00Z',
    updated_at: '2026-06-01T00:00:00Z',
    link_count: 0,
    total_clicks: 0,
    ...overrides,
  };
}

function link(overrides: Partial<Link> = {}): Link {
  return {
    id: 1,
    key: 'abc123',
    destination_url: 'https://example.com',
    title: '',
    active: true,
    denied_reason: 0,
    created_at: '2026-06-01T00:00:00Z',
    expires_at: null,
    click_count: 0,
    campaign_id: 1,
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
    placement: '',
    ...overrides,
  };
}

function bucket(key: string, count: number): LinkBucket {
  return { link_id: 1, key, title: '', count };
}

function utmBucket(value: string, count: number) {
  return { value, count };
}

function campaignStats(overrides: Partial<CampaignStats> = {}): CampaignStats {
  return {
    click_count: 0,
    excluded_bot_count: 0,
    by_source: [],
    by_medium: [],
    by_content: [],
    by_referer: [],
    window_from: '2026-06-01',
    window_to: '2026-06-30',
    ...overrides,
  };
}

// ── visibleCampaigns ─────────────────────────────────────────────────────

describe('visibleCampaigns', () => {
  it('excludes archived campaigns by default', () => {
    const campaigns = [campaign({ id: 1, archived: false }), campaign({ id: 2, archived: true })];
    const result = visibleCampaigns(campaigns, false);
    expect(result.map((c) => c.id)).toEqual([1]);
  });

  it('includes archived campaigns when showArchived is true', () => {
    const campaigns = [campaign({ id: 1, archived: false }), campaign({ id: 2, archived: true })];
    const result = visibleCampaigns(campaigns, true);
    expect(result.map((c) => c.id)).toEqual([1, 2]);
  });

  it('does not mutate the input array', () => {
    const campaigns = [campaign({ id: 1, archived: true })];
    visibleCampaigns(campaigns, false);
    expect(campaigns).toHaveLength(1);
  });
});

// ── campaignDateRangeLabel ───────────────────────────────────────────────

describe('campaignDateRangeLabel', () => {
  it('reports "No dates set" when neither is present', () => {
    expect(campaignDateRangeLabel({ starts_at: null, ends_at: null })).toBe('No dates set');
  });

  it('formats both dates when present', () => {
    const label = campaignDateRangeLabel({
      starts_at: '2026-06-01T00:00:00Z',
      ends_at: '2026-06-30T00:00:00Z',
    });
    expect(label).toContain('2026');
    expect(label).toContain('–');
  });

  it('labels an open-ended campaign as "ongoing"', () => {
    const label = campaignDateRangeLabel({ starts_at: '2026-06-01T00:00:00Z', ends_at: null });
    expect(label).toContain('ongoing');
  });
});

// ── Date-only helpers (formatDateOnly / toDateInput / toIsoDate) ─────────
//
// #0103 fix 1: starts_at/ends_at are DATE-ONLY values that travel as full
// ISO timestamps with the SERVER's local offset — e.g.
// "2026-06-30T17:00:00-07:00" for what the database holds as a UTC-midnight
// 2026-07-01. toDateInput previously read this via iso.slice(0, 10), which
// read the raw text's day ("2026-06-30") — one day early — while toIsoDate
// wrote back UTC midnight, so three Edit → Save round trips with NO user
// edits walked the dates back by three days. Every test below runs with TZ
// stubbed (vi.stubEnv) to 'America/Los_Angeles' (UTC-7/8, the reviewer's
// machine and a negative offset — the exact condition that flips the day)
// to prove the fix holds regardless of the browser's timezone; vi.unstubAllEnvs()
// restores the original TZ so it can't leak into other test files.
afterEach(() => {
  vi.unstubAllEnvs();
});

describe('formatDateOnly', () => {
  it('reads the UTC calendar day, not the local one', () => {
    vi.stubEnv('TZ', 'America/Los_Angeles');
    // UTC midnight Jul 1 rendered by the server with a -07:00 offset — the
    // exact wire format #0103 reported. Must read as Jul 1, not Jun 30.
    expect(formatDateOnly('2026-06-30T17:00:00-07:00')).toContain('Jul 1');
  });

  it('returns "" for null/undefined/invalid input', () => {
    expect(formatDateOnly(null)).toBe('');
    expect(formatDateOnly(undefined)).toBe('');
    expect(formatDateOnly('not-a-date')).toBe('');
  });
});

describe('toDateInput', () => {
  it('reads the UTC calendar day via getUTC*, not iso.slice(0, 10)', () => {
    vi.stubEnv('TZ', 'America/Los_Angeles');
    // slice(0, 10) on this exact string would read "2026-06-30" — the bug.
    expect(toDateInput('2026-06-30T17:00:00-07:00')).toBe('2026-07-01');
  });

  it('is stable across timezones for a UTC-midnight instant', () => {
    vi.stubEnv('TZ', 'America/Los_Angeles');
    expect(toDateInput('2026-07-01T00:00:00Z')).toBe('2026-07-01');
    vi.stubEnv('TZ', 'Pacific/Kiritimati'); // UTC+14, the opposite extreme
    expect(toDateInput('2026-07-01T00:00:00Z')).toBe('2026-07-01');
  });

  it('returns "" for null/undefined/invalid input', () => {
    expect(toDateInput(null)).toBe('');
    expect(toDateInput(undefined)).toBe('');
    expect(toDateInput('not-a-date')).toBe('');
  });
});

describe('toIsoDate', () => {
  it('returns null for an empty value (clears the field)', () => {
    expect(toIsoDate('')).toBeNull();
  });

  it('returns undefined for a malformed value (omit from the patch)', () => {
    expect(toIsoDate('not-a-date')).toBeUndefined();
  });

  it('writes UTC midnight for a "YYYY-MM-DD" value', () => {
    expect(toIsoDate('2026-07-01')).toBe('2026-07-01T00:00:00.000Z');
  });
});

describe('toDateInput(toIsoDate(x)) round trip', () => {
  const dates = ['2026-01-01', '2026-06-30', '2026-07-01', '2026-12-31', '2026-02-28'];

  it('round-trips every date unchanged under America/Los_Angeles (UTC-7/8)', () => {
    vi.stubEnv('TZ', 'America/Los_Angeles');
    for (const x of dates) {
      const iso = toIsoDate(x);
      expect(typeof iso).toBe('string');
      expect(toDateInput(iso as string)).toBe(x);
    }
  });

  it('round-trips every date unchanged under UTC', () => {
    vi.stubEnv('TZ', 'UTC');
    for (const x of dates) {
      const iso = toIsoDate(x);
      expect(toDateInput(iso as string)).toBe(x);
    }
  });

  it('round-trips every date unchanged under Pacific/Kiritimati (UTC+14)', () => {
    vi.stubEnv('TZ', 'Pacific/Kiritimati');
    for (const x of dates) {
      const iso = toIsoDate(x);
      expect(toDateInput(iso as string)).toBe(x);
    }
  });

  it('is idempotent across three repeated round trips (the reported "walks backward on every save" scenario)', () => {
    vi.stubEnv('TZ', 'America/Los_Angeles');
    // Seeded from the WIRE shape the API actually returns — a full ISO
    // instant carrying the server's local offset, e.g.
    // "2026-06-30T17:00:00-07:00" for UTC-midnight 2026-07-01 — not a bare
    // "YYYY-MM-DD". A bare-date seed doesn't pin the bug this test exists
    // for: the OLD slice(0, 10) implementation passes a bare "2026-07-01"
    // through all three rounds unchanged (there's no offset for slice(0, 10)
    // to misread), so that seed alone can't catch the regression. Seeding
    // from the wire shape — matching what startEdit() actually receives via
    // detail.starts_at — makes this strictly stronger: reverting toDateInput/
    // toIsoDate to the pre-#0103 slice(0, 10)/local-Date implementation
    // makes this seed resolve one day early ('2026-06-30') and it never
    // recovers across the three rounds, so the final assertion fails.
    let value = toDateInput('2026-06-30T17:00:00-07:00');
    for (let round = 0; round < 3; round++) {
      const iso = toIsoDate(value);
      value = toDateInput(iso as string);
    }
    expect(value).toBe('2026-07-01');
  });
});

// ── windowDayCount ───────────────────────────────────────────────────────
//
// #0103 fix 4: windowDayCount/windowLabel read CampaignStats.window_from/
// window_to — the window the SERVER actually resolved and queried
// (clicks.campaignWindow, including its clamp of `to` at today) — rather
// than re-deriving an approximation from the campaign's own starts_at/
// ends_at, which would silently ignore that clamp for an in-flight dated
// campaign (#0103's "clicks/day understated 5x" defect).

describe('windowDayCount', () => {
  it('defaults to 30 when stats are absent', () => {
    expect(windowDayCount(undefined)).toBe(30);
    expect(windowDayCount(null)).toBe(30);
  });

  it('defaults to 30 when window_from/window_to are missing', () => {
    expect(windowDayCount({ window_from: '', window_to: '' })).toBe(30);
  });

  it('computes the day count from window_from/window_to', () => {
    // Jun 1 through Jun 10 = 9 days (half-open, matching campaignWindow's
    // [from, to) convention — see the doc comment on windowDayCount).
    const days = windowDayCount({ window_from: '2026-06-01', window_to: '2026-06-10' });
    expect(days).toBe(9);
  });

  it('reflects a clamped-at-today window shorter than the campaign span', () => {
    // The exact #0103 scenario: a campaign nominally running Jul 1 – Dec 31,
    // but the resolved window (window_to clamped to today, Aug 8) is only
    // ~38 days — nothing here re-derives the full ~184-day nominal span.
    const days = windowDayCount({ window_from: '2026-07-01', window_to: '2026-08-08' });
    expect(days).toBe(38);
    expect(days).toBeLessThan(184);
  });

  it('never returns less than 1 for a same-day window', () => {
    const days = windowDayCount({ window_from: '2026-06-01', window_to: '2026-06-01' });
    expect(days).toBeGreaterThanOrEqual(1);
  });

  it('falls back to 30 for an inverted (malformed) window', () => {
    const days = windowDayCount({ window_from: '2026-06-10', window_to: '2026-06-01' });
    expect(days).toBe(30);
  });
});

// ── windowLabel ──────────────────────────────────────────────────────────

describe('windowLabel', () => {
  // #0104 re-review finding 1. window_from === window_to is a half-open range
  // spanning ZERO days, and the inclusive-end subtraction that fixed the
  // off-by-one caption turns it backwards: "Aug 1, 2026 - Jul 31, 2026". The
  // state is reachable, not theoretical -- starts_at == ends_at is how you
  // enter a one-day event, campaigns_check permits it (ends_at >= starts_at),
  // and the create form imposes no min/max. All three other windowLabel tests
  // use non-empty windows, so nothing else covers this.
  it('says the window covers no days rather than rendering a backwards range', () => {
    expect(windowLabel({ window_from: '2026-08-01', window_to: '2026-08-01' })).toBe(
      'Aug 1, 2026 (no days in window)',
    );
  });

  it('still renders a normal range for a one-DAY (not zero-day) window', () => {
    // The adjacent case that must not regress: [Aug 1, Aug 2) is one real day.
    expect(windowLabel({ window_from: '2026-08-01', window_to: '2026-08-02' })).toBe(
      'Aug 1, 2026 - Aug 1, 2026'.replace(/-/g, '\u2013'),
    );
  });

  it('describes a generic fallback when stats are absent', () => {
    expect(windowLabel(undefined)).toBe('recent activity');
    expect(windowLabel(null)).toBe('recent activity');
  });

  it('describes a generic fallback when window_from/window_to are missing', () => {
    expect(windowLabel({ window_from: '', window_to: '' })).toBe('recent activity');
  });

  it('labels the resolved window from window_from/window_to', () => {
    const label = windowLabel({ window_from: '2026-06-01', window_to: '2026-06-30' });
    expect(label).toContain('2026');
    expect(label).toContain('–');
  });

  it('labels the CLAMPED window, not the campaign nominal span', () => {
    // window_to here (Aug 8) is what campaignWindow actually clamped to —
    // the label must say Aug, never Dec (the campaign's real ends_at).
    const label = windowLabel({ window_from: '2026-07-01', window_to: '2026-08-08' });
    expect(label).toContain('Aug');
    expect(label).not.toContain('Dec');
  });

  // #0104 review finding 3: window_to is the EXCLUSIVE end of the half-open
  // [window_from, window_to) range — the same convention
  // fillDayGapsInWindow (charts.ts) already accounts for by subtracting a
  // day before it walks calendar days. Rendering window_to VERBATIM in the
  // caption disagreed with the chart axis it sits above by one day.
  it('labels the INCLUSIVE last day, one before the exclusive window_to (agreeing with the chart axis)', () => {
    // [2026-06-01, 2026-06-30) => the axis's last bucket is Jun 29, not Jun
    // 30. A verbatim-window_to caption would read "Jun 1 – Jun 30" here.
    const label = windowLabel({ window_from: '2026-06-01', window_to: '2026-06-30' });
    expect(label).toContain('Jun 29');
    expect(label).not.toContain('Jun 30');
  });

  it('labels a single-day window (window_to one day after window_from) as that ONE day, not a two-day range', () => {
    // [2026-08-01, 2026-08-02) => the axis has exactly one bucket, Aug 1.
    // The #0104 review's exact repro: verbatim window_to captioned this as
    // "Aug 1 – Aug 2" over an axis with only one point.
    const label = windowLabel({ window_from: '2026-08-01', window_to: '2026-08-02' });
    expect(label).toContain('Aug 1, 2026 – Aug 1, 2026');
  });

  it('agrees with the axis across a month boundary (Jul 1 – Aug 1 exclusive => ends Jul 31)', () => {
    const label = windowLabel({ window_from: '2026-07-01', window_to: '2026-08-01' });
    expect(label).toContain('Jul 31');
    expect(label).not.toContain('Aug 1');
  });
});

// ── campaignUtmDimensions / isEmptyCampaignChannelStats ─────────────────────

describe('campaignUtmDimensions', () => {
  it('returns source, medium, and content — not referer', () => {
    const dims = campaignUtmDimensions(campaignStats());
    expect(dims.map((d) => d.dimension)).toEqual(['source', 'medium', 'content']);
  });

  it('labels each dimension for display', () => {
    const dims = campaignUtmDimensions(campaignStats());
    expect(dims.map((d) => d.label)).toEqual(['Source', 'Medium', 'Content']);
  });

  it('sorts each dimension by count descending', () => {
    const dims = campaignUtmDimensions(
      campaignStats({
        by_source: [utmBucket('email', 3), utmBucket('social', 9)],
      }),
    );
    expect(dims[0].buckets.map((b) => b.value)).toEqual(['social', 'email']);
  });

  it('returns empty buckets (not undefined) for a missing dimension, and [] for absent stats', () => {
    expect(campaignUtmDimensions(undefined).every((d) => d.buckets.length === 0)).toBe(true);
    expect(campaignUtmDimensions(null).every((d) => d.buckets.length === 0)).toBe(true);
  });
});

describe('isEmptyCampaignChannelStats', () => {
  it('is empty when stats are absent', () => {
    expect(isEmptyCampaignChannelStats(undefined)).toBe(true);
    expect(isEmptyCampaignChannelStats(null)).toBe(true);
  });

  it('is empty when click_count is 0 and every dimension is empty', () => {
    expect(isEmptyCampaignChannelStats(campaignStats())).toBe(true);
  });

  it('is NOT empty when click_count is positive, even with empty dimension arrays', () => {
    expect(isEmptyCampaignChannelStats(campaignStats({ click_count: 5 }))).toBe(false);
  });

  it('is NOT empty when any dimension has rows, even if click_count reads 0', () => {
    expect(
      isEmptyCampaignChannelStats(campaignStats({ by_medium: [utmBucket('email', 1)] })),
    ).toBe(false);
  });
});

// ── clicksPerDayAverage ──────────────────────────────────────────────────

describe('clicksPerDayAverage', () => {
  it('divides clicks by days', () => {
    expect(clicksPerDayAverage(60, 30)).toBe(2);
  });

  it('is safe against a zero day count', () => {
    expect(clicksPerDayAverage(60, 0)).toBe(0);
  });

  it('is safe against a negative day count', () => {
    expect(clicksPerDayAverage(60, -5)).toBe(0);
  });

  it('rounds to one decimal place for low-volume campaigns', () => {
    expect(clicksPerDayAverage(1, 3)).toBeCloseTo(0.3, 5);
  });
});

// ── buildLinkRows ────────────────────────────────────────────────────────

describe('buildLinkRows', () => {
  it('matches each link to its windowed count by key', () => {
    const links = [link({ key: 'a' }), link({ key: 'b' })];
    const byLink = [bucket('a', 30), bucket('b', 10)];
    const rows = buildLinkRows(links, byLink);
    expect(rows.find((r) => r.key === 'a')?.clicksInWindow).toBe(30);
    expect(rows.find((r) => r.key === 'b')?.clicksInWindow).toBe(10);
  });

  it('defaults unmatched links to 0 clicks', () => {
    const links = [link({ key: 'a' })];
    const rows = buildLinkRows(links, []);
    expect(rows[0].clicksInWindow).toBe(0);
    expect(rows[0].shareOfTotal).toBe(0);
  });

  it('is safe against a null/undefined byLink (dev mode, no stats provider)', () => {
    const links = [link({ key: 'a' })];
    expect(buildLinkRows(links, undefined)).toHaveLength(1);
    expect(buildLinkRows(links, null)).toHaveLength(1);
  });

  it('does not divide by zero when total clicks is zero', () => {
    const links = [link({ key: 'a' }), link({ key: 'b' })];
    const rows = buildLinkRows(links, [bucket('a', 0), bucket('b', 0)]);
    expect(rows.every((r) => r.shareOfTotal === 0)).toBe(true);
    expect(rows.every((r) => Number.isFinite(r.shareOfTotal))).toBe(true);
  });

  it('shares sum to ~100% across the displayed rows', () => {
    const links = [link({ key: 'a' }), link({ key: 'b' }), link({ key: 'c' })];
    const byLink = [bucket('a', 50), bucket('b', 30), bucket('c', 20)];
    const rows = buildLinkRows(links, byLink);
    const sum = rows.reduce((s, r) => s + r.shareOfTotal, 0);
    expect(sum).toBeGreaterThanOrEqual(99);
    expect(sum).toBeLessThanOrEqual(101);
  });

  it('ignores by_link entries for links no longer assigned (not in the links array)', () => {
    const links = [link({ key: 'a' })];
    // 'phantom' historically belonged to this campaign but was reassigned —
    // it must not appear as a row, and must not affect 'a's share.
    const byLink = [bucket('a', 10), bucket('phantom', 90)];
    const rows = buildLinkRows(links, byLink);
    expect(rows).toHaveLength(1);
    expect(rows[0].shareOfTotal).toBe(100);
  });

  it('ignores a by_link entry with an empty key rather than crediting it to an unkeyed row', () => {
    const links = [link({ key: 'a' })];
    // A bucket with an empty key must not be indexed (it would collide with
    // nothing, but must also not inflate the total that shares are computed
    // against).
    const byLink = [bucket('a', 10), bucket('', 90)];
    const rows = buildLinkRows(links, byLink);
    expect(rows).toHaveLength(1);
    expect(rows[0].shareOfTotal).toBe(100);
  });
});

// ── unlistedClickCount ───────────────────────────────────────────────────
//
// #0103 fix 3: "Share of listed links" is denominated against the displayed
// rows only (buildLinkRows), which can silently look like 100% coverage
// even when by_link carries clicks for since-unassigned links. This helper
// is what drives the "N clicks... not listed above" note under the table.

describe('unlistedClickCount', () => {
  it('is 0 when every by_link entry matches a currently-assigned link', () => {
    const links = [link({ key: 'a' }), link({ key: 'b' })];
    const byLink = [bucket('a', 40), bucket('b', 8)];
    expect(unlistedClickCount(links, byLink)).toBe(0);
  });

  it('sums clicks for keys not present in links', () => {
    // The exact #0103 repro: a since-unassigned link holding 100 clicks
    // alongside 168 clicks (120/40/8) across three still-listed links.
    const links = [link({ key: 'a' }), link({ key: 'b' }), link({ key: 'c' })];
    const byLink = [bucket('a', 120), bucket('b', 40), bucket('c', 8), bucket('phantom', 100)];
    expect(unlistedClickCount(links, byLink)).toBe(100);
  });

  it('is 0 for empty/absent byLink', () => {
    const links = [link({ key: 'a' })];
    expect(unlistedClickCount(links, [])).toBe(0);
    expect(unlistedClickCount(links, undefined)).toBe(0);
    expect(unlistedClickCount(links, null)).toBe(0);
  });

  it('ignores a by_link entry with an empty key', () => {
    const links = [link({ key: 'a' })];
    const byLink = [bucket('a', 10), bucket('', 90)];
    expect(unlistedClickCount(links, byLink)).toBe(0);
  });
});

// ── sortLinkRows ─────────────────────────────────────────────────────────

describe('sortLinkRows', () => {
  function row(key: string, clicks: number): CampaignLinkRow {
    return { ...link({ key }), clicksInWindow: clicks, shareOfTotal: 0 };
  }

  it('sorts descending by default', () => {
    const rows = [row('a', 5), row('b', 20), row('c', 10)];
    expect(sortLinkRows(rows).map((r) => r.key)).toEqual(['b', 'c', 'a']);
  });

  it('sorts ascending when requested', () => {
    const rows = [row('a', 5), row('b', 20), row('c', 10)];
    expect(sortLinkRows(rows, 'asc').map((r) => r.key)).toEqual(['a', 'c', 'b']);
  });

  it('is stable for equal counts: preserves original relative order', () => {
    const rows = [row('first', 10), row('second', 10), row('third', 10)];
    expect(sortLinkRows(rows).map((r) => r.key)).toEqual(['first', 'second', 'third']);
  });

  it('is stable for equal counts across repeated calls (no arbitrary reordering)', () => {
    const rows = [row('x', 5), row('y', 5), row('z', 5)];
    const once = sortLinkRows(rows).map((r) => r.key);
    const twice = sortLinkRows(rows).map((r) => r.key);
    expect(once).toEqual(twice);
  });

  it('does not mutate the input array', () => {
    const rows = [row('a', 5), row('b', 20)];
    sortLinkRows(rows);
    expect(rows.map((r) => r.key)).toEqual(['a', 'b']);
  });

  it('is stable for equal counts at a scale above TimSort\'s merge threshold (30 rows)', () => {
    const rows = Array.from({ length: 30 }, (_, i) => row(`k${i}`, 5));
    const expectedOrder = rows.map((r) => r.key);
    expect(sortLinkRows(rows).map((r) => r.key)).toEqual(expectedOrder);
  });
});

// ── chunkKeys ────────────────────────────────────────────────────────────

describe('chunkKeys', () => {
  it('returns [] for empty input', () => {
    expect(chunkKeys([])).toEqual([]);
  });

  it('returns a single chunk when under the cap', () => {
    const keys = ['a', 'b', 'c'];
    expect(chunkKeys(keys)).toEqual([['a', 'b', 'c']]);
  });

  it('splits into multiple chunks at the cap boundary', () => {
    const keys = Array.from({ length: 120 }, (_, i) => `k${i}`);
    const chunks = chunkKeys(keys);
    expect(chunks).toHaveLength(3);
    expect(chunks[0]).toHaveLength(MAX_ASSIGN_KEYS_PER_REQUEST);
    expect(chunks[1]).toHaveLength(MAX_ASSIGN_KEYS_PER_REQUEST);
    expect(chunks[2]).toHaveLength(20);
  });

  it('every key appears exactly once across chunks, in order', () => {
    const keys = Array.from({ length: 55 }, (_, i) => `k${i}`);
    const chunks = chunkKeys(keys);
    expect(chunks.flat()).toEqual(keys);
  });

  it('respects a custom chunk size', () => {
    expect(chunkKeys(['a', 'b', 'c', 'd', 'e'], 2)).toEqual([['a', 'b'], ['c', 'd'], ['e']]);
  });

  it('keeps exactly-at-the-cap input in a single chunk (n=50)', () => {
    const keys = Array.from({ length: 50 }, (_, i) => `k${i}`);
    const chunks = chunkKeys(keys);
    expect(chunks).toHaveLength(1);
    expect(chunks[0]).toHaveLength(50);
  });

  it('splits one-over-the-cap input into a full chunk plus a singleton (n=51)', () => {
    const keys = Array.from({ length: 51 }, (_, i) => `k${i}`);
    const chunks = chunkKeys(keys);
    expect(chunks.map((c) => c.length)).toEqual([50, 1]);
  });
});

// ── parseKeysInput ───────────────────────────────────────────────────────

describe('parseKeysInput', () => {
  it('splits on commas', () => {
    expect(parseKeysInput('abc,def,ghi')).toEqual(['abc', 'def', 'ghi']);
  });

  it('splits on whitespace and newlines', () => {
    expect(parseKeysInput('abc def\nghi')).toEqual(['abc', 'def', 'ghi']);
  });

  it('extracts the key from a full short URL', () => {
    expect(parseKeysInput('https://go.sstools.co/u/abc123')).toEqual(['abc123']);
  });

  it('handles a mix of bare keys and short URLs', () => {
    expect(parseKeysInput('abc123, https://go.sstools.co/u/def456')).toEqual(['abc123', 'def456']);
  });

  it('de-duplicates while preserving first-seen order', () => {
    expect(parseKeysInput('abc, def, abc')).toEqual(['abc', 'def']);
  });

  it('drops blank entries', () => {
    expect(parseKeysInput('abc,   , def,,')).toEqual(['abc', 'def']);
  });

  it('returns [] for empty or whitespace-only input', () => {
    expect(parseKeysInput('')).toEqual([]);
    expect(parseKeysInput('   \n  ')).toEqual([]);
  });

  it('extracts the key from a short URL carrying a query string', () => {
    // A pasted short URL that still has its UTM params attached — the
    // ordinary case for this whole feature — must still resolve to the key,
    // not fall through to treating the entire URL as the key.
    expect(parseKeysInput('https://go.sstools.co/u/abc123?utm_source=newsletter')).toEqual([
      'abc123',
    ]);
  });

  it('extracts the key from a short URL carrying a fragment', () => {
    expect(parseKeysInput('https://go.sstools.co/u/abc123#section')).toEqual(['abc123']);
  });

  it('extracts the key from a short URL with a trailing slash and a query string', () => {
    expect(parseKeysInput('https://go.sstools.co/u/abc123/?utm_source=newsletter')).toEqual([
      'abc123',
    ]);
  });

  it('leaves a bare key with no /u/ segment unchanged (fall-through path)', () => {
    expect(parseKeysInput('abc123')).toEqual(['abc123']);
  });
});

// ── copyAllShortUrlsText ─────────────────────────────────────────────────

describe('copyAllShortUrlsText', () => {
  it('builds one short URL per line', () => {
    const text = copyAllShortUrlsText([{ key: 'abc' }, { key: 'def' }]);
    expect(text.split('\n')).toEqual([
      'https://go.sstools.co/u/abc',
      'https://go.sstools.co/u/def',
    ]);
  });

  it('returns an empty string for no links', () => {
    expect(copyAllShortUrlsText([])).toBe('');
  });
});

// ── campaignQrZipUrl (#0106) ─────────────────────────────────────────────

describe('campaignQrZipUrl', () => {
  it('builds the same-origin bulk QR zip API route from a slug', () => {
    expect(campaignQrZipUrl('summer-fair')).toBe('/api/campaigns/summer-fair/qr.zip');
  });

  it('encodes an unusual slug defensively', () => {
    expect(campaignQrZipUrl('a b')).toBe('/api/campaigns/a%20b/qr.zip');
  });
});

// ── campaignExportCsvUrl (#0107) ──────────────────────────────────────────

describe('campaignExportCsvUrl', () => {
  it('builds the same-origin CSV export API route from a slug', () => {
    expect(campaignExportCsvUrl('summer-fair')).toBe('/api/campaigns/summer-fair/export.csv');
  });

  it('encodes an unusual slug defensively', () => {
    expect(campaignExportCsvUrl('a b')).toBe('/api/campaigns/a%20b/export.csv');
  });

  it('carries no query params, so the export reuses the same default window the on-screen table shows', () => {
    expect(campaignExportCsvUrl('summer-fair')).not.toContain('?');
  });
});

// ── joinSentences ────────────────────────────────────────────────────────
//
// #0103 fix 4: the partial-assign-failure message concatenated the server's
// ApiError message with a client-authored follow-up sentence using a bare
// template-literal space, e.g. "link not found: nosuchkey Some links in
// this batch may have been assigned..." — no separator at all, because the
// server message has no trailing period.

describe('joinSentences', () => {
  it('adds a period when the first fragment has no terminal punctuation', () => {
    expect(joinSentences('link not found: nosuchkey', 'Some links were assigned.')).toBe(
      'link not found: nosuchkey. Some links were assigned.',
    );
  });

  it('does not double up a period the first fragment already ends with', () => {
    expect(joinSentences('Something went wrong.', 'Try again.')).toBe(
      'Something went wrong. Try again.',
    );
  });

  it('accepts ! and ? as terminal punctuation needing no added period', () => {
    expect(joinSentences('Whoa!', 'Try again.')).toBe('Whoa! Try again.');
    expect(joinSentences('Really?', 'Try again.')).toBe('Really? Try again.');
  });

  it('ignores trailing whitespace when checking for terminal punctuation', () => {
    expect(joinSentences('link not found  ', 'Some links were assigned.')).toBe(
      'link not found. Some links were assigned.',
    );
  });

  it('returns the second fragment alone when the first is empty or blank', () => {
    expect(joinSentences('', 'Some links were assigned.')).toBe('Some links were assigned.');
    expect(joinSentences('   ', 'Some links were assigned.')).toBe('Some links were assigned.');
  });
});

// ── Batch create (#0105) ────────────────────────────────────────────────

function batchRow(overrides: Partial<BatchChannelRow> = {}): BatchChannelRow {
  return { ...emptyBatchChannelRow(), ...overrides };
}

describe('emptyBatchChannelRow', () => {
  it('returns every field blank', () => {
    expect(emptyBatchChannelRow()).toEqual({
      utm_source: '',
      utm_medium: '',
      utm_content: '',
      placement: '',
      title: '',
    });
  });

  it('returns a fresh object each call (mutating one does not affect another)', () => {
    const a = emptyBatchChannelRow();
    const b = emptyBatchChannelRow();
    a.utm_source = 'newsletter';
    expect(b.utm_source).toBe('');
  });
});

// initialBatchRows (review finding 2): the campaign's default_utm_source/
// _medium/_content prefill ROW 1 ONLY, not every row — pinning the fix and
// the "row 2 stays blank" decision this issue's review specifically asked
// for a test on.
describe('initialBatchRows', () => {
  it('prefills a single row from the campaign default_utm_source/_medium/_content', () => {
    const c = campaign({ default_utm_source: 'flyer', default_utm_medium: 'print', default_utm_content: 'hero-cta' });
    const rows = initialBatchRows(c);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toEqual({
      utm_source: 'flyer',
      utm_medium: 'print',
      utm_content: 'hero-cta',
      placement: '',
      title: '',
    });
  });

  it('does not prefill placement or title — the campaign has no default for either', () => {
    const c = campaign({ default_utm_source: 'flyer', default_utm_medium: 'print', default_utm_content: 'hero-cta' });
    const rows = initialBatchRows(c);
    expect(rows[0].placement).toBe('');
    expect(rows[0].title).toBe('');
  });

  it('row 1 is prefilled (non-blank) and a row 2 added afterward stays entirely blank', () => {
    const c = campaign({ default_utm_source: 'flyer', default_utm_medium: 'print', default_utm_content: 'hero-cta' });
    const rows = [...initialBatchRows(c), emptyBatchChannelRow()];
    expect(isBlankBatchRow(rows[0])).toBe(false);
    expect(isBlankBatchRow(rows[1])).toBe(true);
  });

  it('two rows built this way are NOT flagged as duplicates of each other', () => {
    // Guards against the exact regression a naive "prefill every row" fix
    // would introduce: row 2 is genuinely blank, not a copy of row 1, so it
    // must never collide with row 1 under duplicateBatchRowIndices.
    const c = campaign({ default_utm_source: 'flyer', default_utm_medium: 'print', default_utm_content: 'hero-cta' });
    const rows = [...initialBatchRows(c), emptyBatchChannelRow()];
    expect(duplicateBatchRowIndices(rows).size).toBe(0);
  });

  it('a campaign with no defaults set produces a blank row 1, same as before this fix', () => {
    const c = campaign({ default_utm_source: '', default_utm_medium: '', default_utm_content: '' });
    const rows = initialBatchRows(c);
    expect(isBlankBatchRow(rows[0])).toBe(true);
  });
});

describe('MAX_BATCH_CREATE_ROWS_PER_REQUEST', () => {
  it('matches the server-enforced cap (maxBatchCreateRows, internal/handlers/campaigns.go)', () => {
    expect(MAX_BATCH_CREATE_ROWS_PER_REQUEST).toBe(50);
  });
});

describe('isBlankBatchRow', () => {
  it('is true for an untouched row', () => {
    expect(isBlankBatchRow(emptyBatchChannelRow())).toBe(true);
  });

  it('is true for a row containing only whitespace', () => {
    expect(isBlankBatchRow(batchRow({ utm_source: '   ', title: '\t' }))).toBe(true);
  });

  it.each(['utm_source', 'utm_medium', 'utm_content', 'placement', 'title'] as const)(
    'is false once %s is filled in',
    (field) => {
      expect(isBlankBatchRow(batchRow({ [field]: 'x' }))).toBe(false);
    },
  );
});

describe('nonBlankBatchRows', () => {
  it('drops blank rows and keeps the rest in order', () => {
    const rows = [
      batchRow({ utm_source: 'newsletter' }),
      emptyBatchChannelRow(),
      batchRow({ utm_source: 'twitter' }),
    ];
    expect(nonBlankBatchRows(rows).map((r) => r.utm_source)).toEqual(['newsletter', 'twitter']);
  });

  it('does not mutate the input array', () => {
    const rows = [emptyBatchChannelRow(), batchRow({ utm_source: 'x' })];
    const before = [...rows];
    nonBlankBatchRows(rows);
    expect(rows).toEqual(before);
  });
});

describe('duplicateBatchRowIndices', () => {
  // THE CENTRAL CASE (#0105's Relation note): two rows differing ONLY in
  // placement must NOT be flagged as duplicates — that is the trap this
  // issue exists to fix, and a duplicate-row guard that caught this case
  // would silently re-introduce it on the client side even though the
  // server-side dedup bypass is correct.
  it('does NOT flag two rows differing only in placement', () => {
    const rows = [
      batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: '18th & Texas board' }),
      batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: 'Congress & 6th board' }),
    ];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set());
  });

  it('flags a row identical to an earlier one, including placement', () => {
    const rows = [
      batchRow({ utm_source: 'newsletter', utm_medium: 'email', utm_content: 'hero-cta' }),
      batchRow({ utm_source: 'newsletter', utm_medium: 'email', utm_content: 'hero-cta' }),
    ];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set([1]));
  });

  it('flags only the later occurrences of a repeated tuple, never the first', () => {
    const rows = [
      batchRow({ utm_source: 'a', utm_medium: 'b' }),
      batchRow({ utm_source: 'a', utm_medium: 'b' }),
      batchRow({ utm_source: 'a', utm_medium: 'b' }),
    ];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set([1, 2]));
  });

  it('is case-sensitive — differing casing is not treated as a duplicate', () => {
    const rows = [batchRow({ utm_source: 'Newsletter' }), batchRow({ utm_source: 'newsletter' })];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set());
  });

  it('trims before comparing, so whitespace-only differences ARE flagged', () => {
    const rows = [batchRow({ utm_source: 'newsletter' }), batchRow({ utm_source: '  newsletter  ' })];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set([1]));
  });

  // Caught rendering the real form in a browser, not by a unit test: eight
  // rows with only a few filled in (the issue's own "eight rows at 375px"
  // verification state) left several blank rows behind, and a first version
  // of this function flagged every blank row after the first as a
  // "duplicate" of it — disabling the submit button even though every blank
  // row is always dropped before it would ever reach the server, and none of
  // them carry any real content that could collide. Blank rows must be
  // excluded from the scan entirely, not merely tolerated when there happen
  // to be only two of them.
  it('does not flag ANY blank row, no matter how many — not even a third one', () => {
    const rows = [emptyBatchChannelRow(), emptyBatchChannelRow(), emptyBatchChannelRow()];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set());
  });

  it('a blank row sitting between two real duplicate rows does not break the pairing', () => {
    const rows = [
      batchRow({ utm_source: 'a', utm_medium: 'b' }),
      emptyBatchChannelRow(),
      batchRow({ utm_source: 'a', utm_medium: 'b' }),
    ];
    expect(duplicateBatchRowIndices(rows)).toEqual(new Set([2]));
  });
});

describe('composeBatchRowDestinationUrl', () => {
  it('bakes the row source/medium/content plus the shared campaign/term onto the base URL', () => {
    const row = batchRow({ utm_source: 'newsletter', utm_medium: 'email', utm_content: 'hero-cta' });
    const url = composeBatchRowDestinationUrl('https://example.com/promo', row, 'summer-fair', '');
    const parsed = new URL(url);
    expect(parsed.searchParams.get('utm_source')).toBe('newsletter');
    expect(parsed.searchParams.get('utm_medium')).toBe('email');
    expect(parsed.searchParams.get('utm_content')).toBe('hero-cta');
    expect(parsed.searchParams.get('utm_campaign')).toBe('summer-fair');
    expect(parsed.searchParams.has('utm_term')).toBe(false);
  });

  // Placement is NEVER part of the composed URL — the whole reason the dedup
  // trap exists. Two rows differing only in placement must compose to the
  // SAME destination_url; the fix lives in bypassing dedup server-side, not
  // in making the URLs differ.
  it('does not bake placement into the URL — two rows differing only in placement compose identically', () => {
    const rowA = batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: '18th & Texas board' });
    const rowB = batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: 'Congress & 6th board' });
    const urlA = composeBatchRowDestinationUrl('https://example.com/promo', rowA, 'summer-fair', '');
    const urlB = composeBatchRowDestinationUrl('https://example.com/promo', rowB, 'summer-fair', '');
    expect(urlA).toBe(urlB);
  });

  it('never deletes a param the base URL already carried (previous is always empty)', () => {
    const row = emptyBatchChannelRow();
    const url = composeBatchRowDestinationUrl(
      'https://example.com/promo?utm_source=partner-newsletter&ref=homepage',
      row,
      '',
      '',
    );
    expect(url).toContain('utm_source=partner-newsletter');
    expect(url).toContain('ref=homepage');
  });
});

describe('buildBatchCreateRows', () => {
  it('drops blank rows and composes destination_url for the rest', () => {
    const rows = [
      batchRow({ utm_source: 'newsletter', utm_medium: 'email' }),
      emptyBatchChannelRow(),
      batchRow({ utm_source: 'twitter', utm_medium: 'social' }),
    ];
    const payload = buildBatchCreateRows('https://example.com/promo', rows, 'summer-fair', '');
    expect(payload).toHaveLength(2);
    expect(payload[0].utm_source).toBe('newsletter');
    expect(payload[1].utm_source).toBe('twitter');
    expect(payload.every((r) => r.utm_campaign === 'summer-fair')).toBe(true);
  });

  it('omits blank optional fields rather than sending empty strings', () => {
    const rows = [batchRow({ utm_source: 'newsletter', utm_medium: 'email' })];
    const payload = buildBatchCreateRows('https://example.com/promo', rows, '', '');
    expect(payload[0].placement).toBeUndefined();
    expect(payload[0].title).toBeUndefined();
    expect(payload[0].utm_campaign).toBeUndefined();
    expect(payload[0].utm_term).toBeUndefined();
  });

  // THE CENTRAL CASE at the request-building layer: two rows differing only
  // in placement must both survive into the request payload as two entries
  // sharing an identical destination_url — it is the SERVER's job (bypassing
  // dedup) to keep them as two links, not this function's job to make them
  // look different.
  it('two rows differing only in placement both appear in the payload with identical destination_url', () => {
    const rows = [
      batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: '18th & Texas board' }),
      batchRow({ utm_source: 'flyer', utm_medium: 'print', placement: 'Congress & 6th board' }),
    ];
    const payload = buildBatchCreateRows('https://example.com/promo', rows, 'summer-fair', '');
    expect(payload).toHaveLength(2);
    expect(payload[0].destination_url).toBe(payload[1].destination_url);
    expect(payload[0].placement).toBe('18th & Texas board');
    expect(payload[1].placement).toBe('Congress & 6th board');
  });

  it('returns an empty array when every row is blank', () => {
    expect(buildBatchCreateRows('https://example.com/promo', [emptyBatchChannelRow()], '', '')).toEqual([]);
  });
});
