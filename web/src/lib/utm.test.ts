// Unit tests for the UTM-parameter composition helpers (#0048).
// No DOM or network — only pure functions from lib/utm.ts.

import { describe, it, expect } from 'vitest';
import {
  UTM_KEYS,
  emptyUtmParams,
  isUtmEmpty,
  composeUtmUrl,
  type UtmParams,
} from './utm';

// Helper: build a UtmParams with explicit overrides on top of empty defaults.
function params(overrides: Partial<UtmParams> = {}): UtmParams {
  return { ...emptyUtmParams(), ...overrides };
}

// ─── emptyUtmParams ────────────────────────────────────────────────────────────

describe('emptyUtmParams', () => {
  it('returns all five keys with empty-string values', () => {
    const p = emptyUtmParams();
    expect(Object.keys(p)).toHaveLength(5);
    for (const k of UTM_KEYS) {
      expect(p[k]).toBe('');
    }
  });

  it('returns a fresh object on each call (no shared reference)', () => {
    const a = emptyUtmParams();
    const b = emptyUtmParams();
    a.utm_source = 'email';
    expect(b.utm_source).toBe('');
  });
});

// ─── isUtmEmpty ───────────────────────────────────────────────────────────────

describe('isUtmEmpty', () => {
  it('returns true when all fields are empty strings', () => {
    expect(isUtmEmpty(emptyUtmParams())).toBe(true);
  });

  it('returns true when all fields are whitespace-only', () => {
    expect(isUtmEmpty(params({ utm_source: '  ', utm_medium: '\t' }))).toBe(true);
  });

  it('returns false when any field has a non-blank value', () => {
    expect(isUtmEmpty(params({ utm_source: 'email' }))).toBe(false);
    expect(isUtmEmpty(params({ utm_content: 'hero-cta' }))).toBe(false);
  });
});

// ─── composeUtmUrl — no-op cases ──────────────────────────────────────────────

describe('composeUtmUrl — no-op cases', () => {
  it('returns empty string when base URL is empty', () => {
    expect(composeUtmUrl('', params({ utm_source: 'email' }))).toBe('');
  });

  it('returns empty string when base URL is whitespace-only', () => {
    expect(composeUtmUrl('   ', params({ utm_source: 'email' }))).toBe('');
  });

  it('returns base URL unchanged when all UTM params are empty', () => {
    const base = 'https://example.com/page';
    expect(composeUtmUrl(base, emptyUtmParams())).toBe(base);
  });

  it('returns base URL unchanged when all UTM params are whitespace-only', () => {
    const base = 'https://example.com/page?ref=foo';
    expect(composeUtmUrl(base, params({ utm_source: '   ' }))).toBe(base);
  });
});

// ─── composeUtmUrl — basic composition ────────────────────────────────────────

describe('composeUtmUrl — basic composition', () => {
  it('appends a single UTM param to a bare URL', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_source: 'email' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('email');
    expect(u.origin).toBe('https://example.com');
  });

  it('appends multiple non-empty UTM params', () => {
    const result = composeUtmUrl(
      'https://example.com/page',
      params({
        utm_source: 'email',
        utm_medium: 'newsletter',
        utm_campaign: 'launch',
      }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('email');
    expect(u.searchParams.get('utm_medium')).toBe('newsletter');
    expect(u.searchParams.get('utm_campaign')).toBe('launch');
    // Empty fields are absent.
    expect(u.searchParams.has('utm_term')).toBe(false);
    expect(u.searchParams.has('utm_content')).toBe(false);
  });

  it('drops empty UTM fields — no stray empty params', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_source: 'social', utm_medium: '' }),
    );
    const u = new URL(result);
    expect(u.searchParams.has('utm_medium')).toBe(false);
    expect(u.searchParams.has('utm_term')).toBe(false);
    expect(u.searchParams.has('utm_content')).toBe(false);
  });

  it('all five params are present when all filled', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({
        utm_source: 'src',
        utm_medium: 'med',
        utm_campaign: 'camp',
        utm_term: 'kw',
        utm_content: 'hero',
      }),
    );
    const u = new URL(result);
    for (const k of UTM_KEYS) {
      expect(u.searchParams.has(k)).toBe(true);
    }
  });
});

// ─── composeUtmUrl — merging with existing query string ───────────────────────

describe('composeUtmUrl — merge with existing query string', () => {
  it('preserves existing non-UTM query params', () => {
    const result = composeUtmUrl(
      'https://example.com/page?ref=homepage&foo=bar',
      params({ utm_source: 'email' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('ref')).toBe('homepage');
    expect(u.searchParams.get('foo')).toBe('bar');
    expect(u.searchParams.get('utm_source')).toBe('email');
  });

  it('replaces an existing utm_source value (no duplicates)', () => {
    const result = composeUtmUrl(
      'https://example.com?utm_source=old-value&utm_medium=cpc',
      params({ utm_source: 'email', utm_medium: 'newsletter' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('email');
    expect(u.searchParams.get('utm_medium')).toBe('newsletter');
    // Confirm there is only ONE value for utm_source (not the old one duplicated).
    expect(u.searchParams.getAll('utm_source')).toHaveLength(1);
    expect(u.searchParams.getAll('utm_medium')).toHaveLength(1);
  });

  it('replaces all five UTM keys when base has them and overrides are provided', () => {
    const base =
      'https://example.com?utm_source=a&utm_medium=b&utm_campaign=c&utm_term=d&utm_content=e';
    const result = composeUtmUrl(
      base,
      params({
        utm_source: 'new-src',
        utm_medium: 'new-med',
        utm_campaign: 'new-camp',
        utm_term: 'new-term',
        utm_content: 'new-content',
      }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('new-src');
    expect(u.searchParams.get('utm_medium')).toBe('new-med');
    expect(u.searchParams.get('utm_campaign')).toBe('new-camp');
    expect(u.searchParams.get('utm_term')).toBe('new-term');
    expect(u.searchParams.get('utm_content')).toBe('new-content');
  });

  it('keeps a base UTM param if the corresponding builder field is empty', () => {
    // utm_medium is already in the base URL; the builder leaves it blank — the
    // base value should be preserved (builder does NOT erase what is already there
    // unless the user provides a new value).
    const result = composeUtmUrl(
      'https://example.com?utm_medium=cpc',
      params({ utm_source: 'email' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_medium')).toBe('cpc');
    expect(u.searchParams.get('utm_source')).toBe('email');
  });
});

// ─── composeUtmUrl — URL encoding ─────────────────────────────────────────────

describe('composeUtmUrl — URL encoding', () => {
  it('encodes spaces in values', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_campaign: 'summer sale 2026' }),
    );
    const u = new URL(result);
    // URLSearchParams.get decodes; raw should not contain a literal space.
    expect(u.searchParams.get('utm_campaign')).toBe('summer sale 2026');
    // The raw query string should not contain a space.
    expect(u.search).not.toContain(' ');
  });

  it('encodes special characters (ampersands, equals, hash)', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_content: 'a=1&b=2#anchor' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_content')).toBe('a=1&b=2#anchor');
  });

  it('encodes non-ASCII characters', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_campaign: 'été 2026' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_campaign')).toBe('été 2026');
    expect(u.search).not.toContain('é');
  });

  it('trims leading/trailing whitespace from values before encoding', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_source: '  email  ' }),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('email');
  });
});

// ─── composeUtmUrl — invalid / relative URLs ──────────────────────────────────

describe('composeUtmUrl — invalid / relative URLs', () => {
  it('falls back gracefully for a relative URL path', () => {
    // Relative URL: new URL() throws, fallback appends params as string.
    const result = composeUtmUrl('/page', params({ utm_source: 'email' }));
    expect(result).toContain('utm_source=email');
    expect(result).toContain('/page');
  });

  it('falls back gracefully for a partially-typed URL (no scheme yet)', () => {
    const result = composeUtmUrl(
      'example.com/page',
      params({ utm_source: 'test' }),
    );
    expect(result).toContain('utm_source=test');
    expect(result).toContain('example.com/page');
  });

  it('handles a URL that already has a query string under the fallback path', () => {
    const result = composeUtmUrl(
      '/page?ref=foo',
      params({ utm_source: 'email' }),
    );
    expect(result).toContain('ref=foo');
    expect(result).toContain('utm_source=email');
    // Should not produce a double `?`.
    const questionMarks = (result.match(/\?/g) || []).length;
    expect(questionMarks).toBe(1);
  });

  it('replaces a duplicate key under the fallback path', () => {
    const result = composeUtmUrl(
      '/page?utm_source=old',
      params({ utm_source: 'new' }),
    );
    expect(result).toContain('utm_source=new');
    expect(result).not.toContain('utm_source=old');
    // Still only one `?`.
    const questionMarks = (result.match(/\?/g) || []).length;
    expect(questionMarks).toBe(1);
  });
});
