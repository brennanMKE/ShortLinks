// @vitest-environment jsdom
// Dev mode (STORAGE=json) has no /api/reports, so GET /api/reports/link-performance
// falls through to the SPA catch-all and "succeeds" with the HTML shell as a string.
// LinkDetail's #0169 fallback must treat that as no report and still render the
// link with its own 30-day series (it used to throw reading 'partial_last_day').
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';

const detail = {
  id: 2, key: 'gh', short_url: 'http://localhost:8216/u/gh', destination_url: 'https://github.com',
  title: 'GitHub', active: true, denied_reason: 0, created_at: '2026-11-18T15:43:12-08:00', expires_at: null,
  click_count: 0, campaign_id: null, utm_source: '', utm_medium: '', utm_campaign: '', utm_term: '',
  utm_content: '', placement: '',
  utm_stats: { click_count: 0, excluded_bot_count: 0, by_source: [], by_medium: [], by_campaign: [] },
  timeseries: { days: [] },
};
const getLinkPerformance = vi.hoisted(() => vi.fn());

vi.mock('../lib/api', async (orig) => ({
  ...(await orig<typeof import('../lib/api')>()),
  getLink: vi.fn(async () => detail),
  getLinkPerformance,
}));

import LinkDetail from './LinkDetail.svelte';

beforeAll(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} });
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

afterAll(() => vi.unstubAllGlobals());

describe('LinkDetail without a report (dev mode)', () => {
  for (const [label, body] of [
    ['an HTML shell string', '<!doctype html><title>Short Links</title>'],
    ['an empty object', {}],
    ['undefined', undefined],
  ] as const) {
    it(`renders the link when the report request returns ${label}`, async () => {
      getLinkPerformance.mockResolvedValue(body);
      const errors: unknown[] = [];
      const onError = (e: ErrorEvent) => errors.push(e.error);
      const onRejection = (e: PromiseRejectionEvent) => errors.push(e.reason);
      window.addEventListener('error', onError);
      window.addEventListener('unhandledrejection', onRejection);
      render(LinkDetail, { key: 'gh' });
      expect(await screen.findByRole('heading', { level: 1 })).toBeTruthy();
      await new Promise((r) => setTimeout(r, 50));
      window.removeEventListener('error', onError);
      window.removeEventListener('unhandledrejection', onRejection);
      expect(errors).toEqual([]);
      expect(screen.queryByText(/Could not load this range/)).toBeNull();
      expect(document.body.textContent).toContain('GitHub');
    });
  }
});
