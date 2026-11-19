import { describe, expect, it } from 'vitest';
import { chartGeoForWidth, DEFAULT_CHART_GEO, PHONE_CHART_GEO, xLabelIndices } from './charts';

describe('phone chart geometry (#0162)', () => {
  it('picks the phone geometry only for narrow containers', () => {
    expect(chartGeoForWidth(0)).toBe(DEFAULT_CHART_GEO);
    expect(chartGeoForWidth(340)).toBe(PHONE_CHART_GEO);
    expect(chartGeoForWidth(900)).toBe(DEFAULT_CHART_GEO);
    expect(PHONE_CHART_GEO.height).toBeGreaterThan(PHONE_CHART_GEO.width * 0.6);
  });
  it('spaces x labels within the budget, from the first point to the last', () => {
    for (const [n, max] of [[7, 4], [30, 4], [30, 5], [90, 5], [2, 5]] as const) {
      const idx = xLabelIndices(n, max);
      expect(idx.length).toBeLessThanOrEqual(Math.max(2, max));
      expect(idx[0]).toBe(0);
      expect(idx[idx.length - 1]).toBe(n - 1);
    }
    expect(xLabelIndices(7, 4)).toEqual([0, 2, 4, 6]);
    // Never two labels closer than 3/4 of the regular step.
    for (const [n, max] of [[30, 5], [30, 4], [29, 5], [90, 5], [14, 5]] as const) {
      const idx = xLabelIndices(n, max);
      const step = Math.ceil((n - 1) / (Math.min(max, n) - 1));
      for (let i = 1; i < idx.length; i++) expect(idx[i] - idx[i - 1]).toBeGreaterThanOrEqual(step * 0.75);
    }
    expect(xLabelIndices(1, 4)).toEqual([0]);
    expect(xLabelIndices(0, 4)).toEqual([]);
  });
});
