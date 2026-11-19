import { describe, it, expect } from 'vitest';
import {
  getTheme, setTheme, applyTheme, getTimeZoneChoice, setTimeZoneChoice, getChartTimeZone,
  isValidTimeZone, browserTimeZone, THEME_KEY, TZ_KEY, TZ_BROWSER,
} from './preferences';

function mem(): Storage {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}
function root() {
  const attrs: Record<string, string> = {};
  const el = {
    attrs,
    setAttribute: (k: string, v: string) => void (attrs[k] = v),
    removeAttribute: (k: string) => void delete attrs[k],
  };
  return el as unknown as HTMLElement & { attrs: Record<string, string> };
}

describe('theme', () => {
  it('defaults to system and ignores junk', () => {
    const s = mem();
    expect(getTheme(s)).toBe('system');
    s.setItem(THEME_KEY, 'neon');
    expect(getTheme(s)).toBe('system');
  });
  it('persists and applies data-theme; system clears both', () => {
    const s = mem();
    const r = root();
    setTheme('dark', s, r);
    expect(getTheme(s)).toBe('dark');
    expect(r.attrs['data-theme']).toBe('dark');
    setTheme('system', s, r);
    expect(s.getItem(THEME_KEY)).toBeNull();
    expect(r.attrs['data-theme']).toBeUndefined();
    applyTheme('light', r);
    expect(r.attrs['data-theme']).toBe('light');
  });
});

describe('time zone', () => {
  it('defaults to the browser zone', () => {
    const s = mem();
    expect(getTimeZoneChoice(s)).toBe(TZ_BROWSER);
    expect(getChartTimeZone(s)).toBe(browserTimeZone());
  });
  it('stores a valid zone and rejects an invalid one', () => {
    const s = mem();
    setTimeZoneChoice('America/Los_Angeles', s);
    expect(getChartTimeZone(s)).toBe('America/Los_Angeles');
    setTimeZoneChoice('Not/AZone', s);
    expect(s.getItem(TZ_KEY)).toBeNull();
    s.setItem(TZ_KEY, 'Bogus/Zone');
    expect(getTimeZoneChoice(s)).toBe(TZ_BROWSER);
    setTimeZoneChoice(TZ_BROWSER, s);
    expect(s.getItem(TZ_KEY)).toBeNull();
  });
  it('validates zones', () => {
    expect(isValidTimeZone('UTC')).toBe(true);
    expect(isValidTimeZone('nope')).toBe(false);
  });
});
