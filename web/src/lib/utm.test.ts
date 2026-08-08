// Unit tests for the UTM-parameter composition helpers (#0048).
// No DOM or network — only pure functions from lib/utm.ts.

import { describe, it, expect } from 'vitest';
import {
  UTM_KEYS,
  emptyUtmParams,
  isUtmEmpty,
  composeUtmUrl,
  utmParamsFromLink,
  utmParamsFromCampaignDefaults,
  fillBlankUtmParams,
  type UtmParams,
  type CampaignUtmDefaultsSource,
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

// ─── utmParamsFromLink (#0099) ─────────────────────────────────────────────────

describe('utmParamsFromLink', () => {
  it('carries all five fields over field-by-field', () => {
    const got = utmParamsFromLink({
      utm_source: 'email',
      utm_medium: 'newsletter',
      utm_campaign: 'summer-fair',
      utm_term: 'shoes',
      utm_content: 'banner1',
    });
    expect(got).toEqual({
      utm_source: 'email',
      utm_medium: 'newsletter',
      utm_campaign: 'summer-fair',
      utm_term: 'shoes',
      utm_content: 'banner1',
    });
  });

  it('maps a pre-migration link (fields absent/"") to emptyUtmParams, not an error', () => {
    expect(utmParamsFromLink({})).toEqual(emptyUtmParams());
    expect(
      utmParamsFromLink({
        utm_source: '',
        utm_medium: '',
        utm_campaign: '',
        utm_term: '',
        utm_content: '',
      }),
    ).toEqual(emptyUtmParams());
  });

  it('treats a partially-populated link field-by-field, not all-or-nothing', () => {
    const got = utmParamsFromLink({ utm_source: 'email' });
    expect(got.utm_source).toBe('email');
    expect(got.utm_medium).toBe('');
    expect(got.utm_campaign).toBe('');
  });

  it('returns a fresh object each call (no shared reference)', () => {
    const source = { utm_source: 'email' };
    const a = utmParamsFromLink(source);
    const b = utmParamsFromLink(source);
    a.utm_source = 'mutated';
    expect(b.utm_source).toBe('email');
  });
});

// ─── utmParamsFromCampaignDefaults (#0099) ─────────────────────────────────────

describe('utmParamsFromCampaignDefaults', () => {
  it('maps default_utm_* onto the plain utm_* keys the builder binds to', () => {
    const got = utmParamsFromCampaignDefaults({
      default_utm_source: 'flyer',
      default_utm_medium: 'print',
      default_utm_campaign: 'summer-fair',
      default_utm_term: '',
      default_utm_content: 'poster-a',
    });
    expect(got).toEqual({
      utm_source: 'flyer',
      utm_medium: 'print',
      utm_campaign: 'summer-fair',
      utm_term: '',
      utm_content: 'poster-a',
    });
  });

  it('maps an all-empty/absent campaign to emptyUtmParams', () => {
    expect(utmParamsFromCampaignDefaults({})).toEqual(emptyUtmParams());
  });

  it('a NULL default_utm_campaign (serialized as "") prefills empty — it does NOT fall back to the campaign slug', () => {
    // #0098's downstream constraint 5 / #0099 review item 10: the decision
    // is "stays empty", not "falls back to slug". This function does not
    // even accept a slug parameter, so passing one alongside an empty
    // default_utm_campaign proves there is no reach-for-the-slug fallback
    // hiding anywhere — the slug field here is simply ignored input.
    const campaignWithClearedDefault = {
      slug: 'summer-fair',
      default_utm_source: 'flyer',
      default_utm_campaign: '',
    } as CampaignUtmDefaultsSource & { slug: string };
    const got = utmParamsFromCampaignDefaults(campaignWithClearedDefault);
    expect(got.utm_campaign).toBe('');
    expect(got.utm_campaign).not.toBe(campaignWithClearedDefault.slug);
  });

  it('the result is a plain, independent UtmParams the caller can mutate freely — prefilling never locks the field', () => {
    const campaign = { default_utm_campaign: 'summer-fair' };
    const prefilled = utmParamsFromCampaignDefaults(campaign);
    prefilled.utm_campaign = 'overridden-by-author';
    // Re-deriving from the same campaign source still returns the campaign's
    // own value — proving the mutation above touched only the caller's copy,
    // not any state this function holds.
    expect(utmParamsFromCampaignDefaults(campaign).utm_campaign).toBe('summer-fair');
    expect(prefilled.utm_campaign).toBe('overridden-by-author');
  });
});

// ─── fillBlankUtmParams (#0099 review item 6) ──────────────────────────────────

describe('fillBlankUtmParams', () => {
  const campaign = {
    default_utm_source: 'flyer',
    default_utm_medium: 'print',
    default_utm_campaign: 'summer-fair',
    default_utm_term: '',
    default_utm_content: 'poster-a',
  };

  it('fills every field from the campaign when current is entirely blank', () => {
    const got = fillBlankUtmParams(emptyUtmParams(), campaign);
    expect(got).toEqual(utmParamsFromCampaignDefaults(campaign));
  });

  it('does NOT overwrite a field the author already typed something into', () => {
    const current = params({ utm_source: 'hand-typed-value' });
    const got = fillBlankUtmParams(current, campaign);
    expect(got.utm_source).toBe('hand-typed-value');
    // The still-blank fields are still filled from the campaign.
    expect(got.utm_medium).toBe('print');
    expect(got.utm_campaign).toBe('summer-fair');
  });

  it('leaves a field blank when the campaign has no default for it either', () => {
    const got = fillBlankUtmParams(emptyUtmParams(), campaign);
    expect(got.utm_term).toBe('');
  });

  it('a whitespace-only field counts as blank and gets filled', () => {
    const current = params({ utm_source: '   ' });
    const got = fillBlankUtmParams(current, campaign);
    expect(got.utm_source).toBe('flyer');
  });

  it('does not mutate the current object passed in', () => {
    const current = params({ utm_source: 'typed' });
    const before = { ...current };
    fillBlankUtmParams(current, campaign);
    expect(current).toEqual(before);
  });

  it('selecting a second campaign fills only what is still blank after the first selection', () => {
    // Simulates: author picks campaign A (fills all blanks), hand-edits
    // utm_source, then picks campaign B — B must not clobber the hand edit,
    // but SHOULD fill fields still blank (including ones A left blank).
    const afterA = fillBlankUtmParams(emptyUtmParams(), campaign);
    const handEdited = { ...afterA, utm_source: 'author-override' };
    const campaignB = { ...campaign, default_utm_term: 'shoes' };
    const afterB = fillBlankUtmParams(handEdited, campaignB);
    expect(afterB.utm_source).toBe('author-override'); // untouched
    expect(afterB.utm_term).toBe('shoes'); // was blank, now filled by B
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
// `previous` is emptyUtmParams() throughout this section (matching the
// CREATE path's constant) except where a test is specifically about the
// previously-owned-key deletion rule (see the dedicated describe block below).

describe('composeUtmUrl — no-op cases', () => {
  it('returns empty string when base URL is empty', () => {
    expect(composeUtmUrl('', params({ utm_source: 'email' }), emptyUtmParams())).toBe('');
  });

  it('returns empty string when base URL is whitespace-only', () => {
    expect(composeUtmUrl('   ', params({ utm_source: 'email' }), emptyUtmParams())).toBe('');
  });

  it('returns base URL unchanged when all UTM params are empty', () => {
    const base = 'https://example.com/page';
    expect(composeUtmUrl(base, emptyUtmParams(), emptyUtmParams())).toBe(base);
  });

  it('returns base URL unchanged when all UTM params are whitespace-only', () => {
    const base = 'https://example.com/page?ref=foo';
    expect(composeUtmUrl(base, params({ utm_source: '   ' }), emptyUtmParams())).toBe(base);
  });
});

// ─── composeUtmUrl — basic composition ────────────────────────────────────────

describe('composeUtmUrl — basic composition', () => {
  it('appends a single UTM param to a bare URL', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_source: 'email' }),
      emptyUtmParams(),
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
      emptyUtmParams(),
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
      emptyUtmParams(),
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
      emptyUtmParams(),
    );
    const u = new URL(result);
    for (const k of UTM_KEYS) {
      expect(u.searchParams.has(k)).toBe(true);
    }
  });
});

// ─── composeUtmUrl — the create-path guard (#0099 review: overshoot fix) ──────

describe('composeUtmUrl — create path never deletes a key the builder never owned', () => {
  it('a URL pasted with its own baked UTM params survives when the builder is never touched', () => {
    // Case (a) from the review: user pastes a URL that already carries
    // ?utm_source=partner-newsletter, never opens the builder. previous is
    // emptyUtmParams() (the builder never owned anything, matching
    // Dashboard.svelte's actual call), so the pasted param must survive.
    const base = 'https://example.com/promo?utm_source=partner-newsletter&ref=homepage';
    const result = composeUtmUrl(base, emptyUtmParams(), emptyUtmParams());
    expect(result).toBe(base);
  });

  it('a URL pasted with baked UTM params survives even if the builder fills an UNRELATED field', () => {
    const base = 'https://example.com/promo?utm_source=partner-newsletter';
    const result = composeUtmUrl(base, params({ utm_medium: 'newsletter' }), emptyUtmParams());
    const u = new URL(result);
    // The pasted, builder-untouched key survives...
    expect(u.searchParams.get('utm_source')).toBe('partner-newsletter');
    // ...and the field the builder DID fill in is added.
    expect(u.searchParams.get('utm_medium')).toBe('newsletter');
  });
});

// ─── composeUtmUrl — merging with existing query string ───────────────────────

describe('composeUtmUrl — merge with existing query string', () => {
  it('preserves existing non-UTM query params', () => {
    const result = composeUtmUrl(
      'https://example.com/page?ref=homepage&foo=bar',
      params({ utm_source: 'email' }),
      emptyUtmParams(),
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
      emptyUtmParams(),
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
      emptyUtmParams(),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('new-src');
    expect(u.searchParams.get('utm_medium')).toBe('new-med');
    expect(u.searchParams.get('utm_campaign')).toBe('new-camp');
    expect(u.searchParams.get('utm_term')).toBe('new-term');
    expect(u.searchParams.get('utm_content')).toBe('new-content');
  });

  it('a blank builder field REMOVES the corresponding key when the builder previously owned it (edit path)', () => {
    // utm_medium is already in the base URL, AND `previous` shows the
    // builder started this edit session already holding 'cpc' for it — the
    // author has now cleared the field. composeUtmUrl must delete it, not
    // preserve it, otherwise the recomposed URL still ships utm_medium=cpc
    // while the discrete column the same save writes goes to NULL, and the
    // two permanently disagree (the exact defect this issue exists to close).
    const result = composeUtmUrl(
      'https://example.com?utm_medium=cpc',
      params({ utm_source: 'email' }),
      params({ utm_medium: 'cpc' }),
    );
    const u = new URL(result);
    expect(u.searchParams.has('utm_medium')).toBe(false);
    expect(u.searchParams.get('utm_source')).toBe('email');
  });
});

// ─── composeUtmUrl — clearing a previously-baked key (#0099 edit path) ────────

describe('composeUtmUrl — clearing a previously-baked key', () => {
  const baked =
    'https://example.com/landing?utm_source=email&utm_medium=newsletter&utm_campaign=summer-fair&utm_term=shoes&utm_content=banner1';
  const allFilled = params({
    utm_source: 'email',
    utm_medium: 'newsletter',
    utm_campaign: 'summer-fair',
    utm_term: 'shoes',
    utm_content: 'banner1',
  });

  it.each(UTM_KEYS)('clearing only %s removes just that key and leaves the other four', (clearedKey) => {
    const edited = { ...allFilled, [clearedKey]: '' };

    // `previous` = allFilled: the builder started this edit session with all
    // five values (as utmParamsFromLink would return for a link whose
    // columns hold them), so clearing one is a deliberate deletion.
    const result = composeUtmUrl(baked, edited, allFilled);
    const u = new URL(result);

    expect(u.searchParams.has(clearedKey)).toBe(false);
    for (const k of UTM_KEYS) {
      if (k === clearedKey) continue;
      expect(u.searchParams.get(k)).toBe(allFilled[k]);
    }
  });

  it('clearing all five keys strips every utm_* param from an already-baked URL', () => {
    const result = composeUtmUrl(baked, emptyUtmParams(), allFilled);
    const u = new URL(result);
    for (const k of UTM_KEYS) {
      expect(u.searchParams.has(k)).toBe(false);
    }
    // Non-UTM structure (path) survives.
    expect(u.pathname).toBe('/landing');
  });

  it('clearing all five keys from a URL with a non-UTM param preserves that param', () => {
    const previous = params({ utm_source: 'email', utm_medium: 'newsletter' });
    const result = composeUtmUrl(
      'https://example.com?ref=homepage&utm_source=email&utm_medium=newsletter',
      emptyUtmParams(),
      previous,
    );
    const u = new URL(result);
    expect(u.searchParams.get('ref')).toBe('homepage');
    expect(u.searchParams.has('utm_source')).toBe(false);
    expect(u.searchParams.has('utm_medium')).toBe(false);
  });

  it('clearing a key under the fallback path (relative/invalid URL) also deletes it when previously owned', () => {
    // A relative URL makes new URL() throw, engaging reconcileQueryFallback —
    // the same delete-if-previously-owned rule must hold there too.
    const result = composeUtmUrl(
      '/page?utm_source=old&ref=foo',
      params({ utm_source: '' }),
      params({ utm_source: 'old' }),
    );
    expect(result).not.toContain('utm_source');
    expect(result).toContain('ref=foo');
  });

  it('re-clearing an already-blank key on a URL with no UTM params is a true no-op (returns base unchanged, no reformatting)', () => {
    const base = 'https://example.com/page?ref=foo';
    expect(composeUtmUrl(base, emptyUtmParams(), emptyUtmParams())).toBe(base);
  });
});

// ─── composeUtmUrl — edit-path regressions (#0099 review: overshoot fix) ──────

describe('composeUtmUrl — edit path only deletes what the builder previously owned', () => {
  it('title-only save of a pre-migration link (all-NULL columns) leaves destination_url byte-identical', () => {
    // The exact scenario from review: a link whose UTM columns are all NULL
    // (pre-#0099, or one that legitimately never had UTM params) but whose
    // destination_url already carries UTM params from an external source.
    // utmParamsFromLink({}) -> emptyUtmParams(), so `previous` here is
    // emptyUtmParams() — matching what LinkDetail.svelte's startEdit()
    // actually captures for such a link. A title-only edit never touches
    // editUtmParams, so `params` here is the SAME emptyUtmParams() the
    // builder started with (nothing was cleared, nothing was owned).
    const base = 'https://example.com/old?utm_source=external&utm_medium=partner&ref=homepage';
    const previous = emptyUtmParams();
    const untouchedParams = previous;
    expect(composeUtmUrl(base, untouchedParams, previous)).toBe(base);
  });

  it('title-only save of a link that DOES have UTM columns also leaves destination_url unchanged', () => {
    // Same shape, but the link legitimately has UTM columns set — the
    // builder starts non-empty AND stays non-empty (title-only edit), so
    // params === previous again and nothing should change.
    const base = 'https://example.com/old?utm_source=email&utm_medium=newsletter&ref=homepage';
    const previous = params({ utm_source: 'email', utm_medium: 'newsletter' });
    const untouchedParams = previous;
    expect(composeUtmUrl(base, untouchedParams, previous)).toBe(base);
  });
});

// ─── composeUtmUrl — URL encoding ─────────────────────────────────────────────

describe('composeUtmUrl — URL encoding', () => {
  it('encodes spaces in values', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_campaign: 'summer sale 2026' }),
      emptyUtmParams(),
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
      emptyUtmParams(),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_content')).toBe('a=1&b=2#anchor');
  });

  it('encodes non-ASCII characters', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_campaign: 'été 2026' }),
      emptyUtmParams(),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_campaign')).toBe('été 2026');
    expect(u.search).not.toContain('é');
  });

  it('trims leading/trailing whitespace from values before encoding', () => {
    const result = composeUtmUrl(
      'https://example.com',
      params({ utm_source: '  email  ' }),
      emptyUtmParams(),
    );
    const u = new URL(result);
    expect(u.searchParams.get('utm_source')).toBe('email');
  });
});

// ─── composeUtmUrl — invalid / relative URLs ──────────────────────────────────

describe('composeUtmUrl — invalid / relative URLs', () => {
  it('falls back gracefully for a relative URL path', () => {
    // Relative URL: new URL() throws, fallback appends params as string.
    const result = composeUtmUrl('/page', params({ utm_source: 'email' }), emptyUtmParams());
    expect(result).toContain('utm_source=email');
    expect(result).toContain('/page');
  });

  it('falls back gracefully for a partially-typed URL (no scheme yet)', () => {
    const result = composeUtmUrl(
      'example.com/page',
      params({ utm_source: 'test' }),
      emptyUtmParams(),
    );
    expect(result).toContain('utm_source=test');
    expect(result).toContain('example.com/page');
  });

  it('handles a URL that already has a query string under the fallback path', () => {
    const result = composeUtmUrl(
      '/page?ref=foo',
      params({ utm_source: 'email' }),
      emptyUtmParams(),
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
      emptyUtmParams(),
    );
    expect(result).toContain('utm_source=new');
    expect(result).not.toContain('utm_source=old');
    // Still only one `?`.
    const questionMarks = (result.match(/\?/g) || []).length;
    expect(questionMarks).toBe(1);
  });
});
