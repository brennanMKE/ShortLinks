// Unit tests for the shared UTM field guidance data (#0095).
// No DOM — only pure data from lib/utmFieldGuide.ts, consumed by
// UtmField.svelte in both Dashboard.svelte's create builder and
// LinkDetail.svelte's edit builder.

import { describe, it, expect } from 'vitest';
import { UTM_KEYS } from './utm';
import { UTM_FIELD_GUIDES, UTM_CONVENTIONS_LINE } from './utmFieldGuide';

// A generous ceiling for "fits without truncation at 375px" — the old
// placeholders (`e.g. newsletter, google, twitter`) ran ~34 chars and did
// truncate; every replacement here is well under that.
const MAX_PLACEHOLDER_LENGTH = 24;

describe('UTM_FIELD_GUIDES', () => {
  it('has an entry for every one of the five UTM keys, in canonical order', () => {
    expect(Object.keys(UTM_FIELD_GUIDES)).toEqual([...UTM_KEYS]);
  });

  for (const key of UTM_KEYS) {
    describe(key, () => {
      it('has a non-empty label', () => {
        expect(UTM_FIELD_GUIDES[key].label.trim()).not.toBe('');
      });

      it('has a placeholder with no "e.g. " prefix', () => {
        expect(UTM_FIELD_GUIDES[key].placeholder.toLowerCase()).not.toContain('e.g.');
      });

      it('has a placeholder with no comma-separated list', () => {
        // A single canonical example should never need a separator — a
        // comma is the signature of the old truncating list-of-examples.
        expect(UTM_FIELD_GUIDES[key].placeholder).not.toContain(',');
      });

      it(`has a placeholder short enough to fit at 375px (<= ${MAX_PLACEHOLDER_LENGTH} chars)`, () => {
        expect(UTM_FIELD_GUIDES[key].placeholder.length).toBeLessThanOrEqual(MAX_PLACEHOLDER_LENGTH);
      });

      it('has non-empty persistent hint text', () => {
        expect(UTM_FIELD_GUIDES[key].hint.trim().length).toBeGreaterThan(0);
      });
    });
  }

  it('marks only Term and Content as optional (with a stated reason)', () => {
    expect(UTM_FIELD_GUIDES.utm_source.optionalReason).toBeUndefined();
    expect(UTM_FIELD_GUIDES.utm_medium.optionalReason).toBeUndefined();
    expect(UTM_FIELD_GUIDES.utm_campaign.optionalReason).toBeUndefined();
    expect(UTM_FIELD_GUIDES.utm_term.optionalReason).toBeTruthy();
    expect(UTM_FIELD_GUIDES.utm_content.optionalReason).toBeTruthy();
  });

  it('states Term is a paid-search keyword that is normally blank', () => {
    const hay = `${UTM_FIELD_GUIDES.utm_term.hint} ${UTM_FIELD_GUIDES.utm_term.optionalReason}`.toLowerCase();
    expect(hay).toContain('paid');
    expect(hay).toContain('blank');
  });

  it('explains the source/medium distinction explicitly in the medium hint', () => {
    const hint = UTM_FIELD_GUIDES.utm_medium.hint.toLowerCase();
    expect(hint).toContain('source');
    expect(hint).toMatch(/group/);
  });

  it('does not restate the source/medium pairing language in the source hint', () => {
    // Sanity check that the explanation lives in one place (medium), not
    // duplicated verbatim in source's hint too.
    expect(UTM_FIELD_GUIDES.utm_source.hint.toLowerCase()).not.toContain('group');
  });
});

describe('UTM_CONVENTIONS_LINE', () => {
  it('is a single non-empty line', () => {
    expect(UTM_CONVENTIONS_LINE.trim().length).toBeGreaterThan(0);
    expect(UTM_CONVENTIONS_LINE).not.toContain('\n');
  });

  it('states the lowercase/hyphen casing convention', () => {
    const line = UTM_CONVENTIONS_LINE.toLowerCase();
    expect(line).toContain('lowercase');
    expect(line).toContain('hyphen');
  });

  it('states the no-PII convention', () => {
    expect(UTM_CONVENTIONS_LINE.toUpperCase()).toContain('PII');
  });
});
