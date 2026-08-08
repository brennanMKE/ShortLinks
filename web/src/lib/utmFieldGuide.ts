// Shared copy for the UTM builder's five fields (#0095). Pure data, no DOM —
// both Dashboard.svelte's create builder and LinkDetail.svelte's edit builder
// (added by #0099) render from this single source via UtmField.svelte, so a
// future wording change only has to happen once. See utm.test.ts's sibling
// utmFieldGuide.test.ts for the guard that keeps the two builders in sync.
//
// Copy choices trace back to #0095's research on typical values:
// - Source/medium is explicitly the pair analytics tools group on, stated in
//   the medium hint (the acceptance criterion for the source/medium
//   distinction).
// - Term is marked optional AND says why: it's a paid-search keyword, most
//   ad platforms auto-populate it, and it is normally left blank.
// - Content is marked optional AND says why: it's the one field expected to
//   differ between two links in the same campaign.

import type { UtmKey } from './utm';

export interface UtmFieldGuide {
  /** Field label shown above the input, e.g. "Source". */
  label: string;
  /** Single canonical, non-truncating example — no "e.g. " prefix (#0095). */
  placeholder: string;
  /**
   * Persistent one-line meaning plus a few typical values. This is the text
   * that survives the user typing — placeholders vanish, this does not.
   */
  hint: string;
  /**
   * Present only for the two optional fields (Term, Content). Rendered next
   * to "optional" in the label annotation so the annotation says WHY the
   * field is optional, not just that it is.
   */
  optionalReason?: string;
}

export const UTM_FIELD_GUIDES: Record<UtmKey, UtmFieldGuide> = {
  utm_source: {
    label: 'Source',
    placeholder: 'newsletter',
    hint: 'Where the traffic comes from — the specific named source. Typical: newsletter, google, twitter, linkedin.',
  },
  utm_medium: {
    label: 'Medium',
    placeholder: 'email',
    hint: 'How the traffic arrives, not where it’s from (that’s Source). Keep this to a small reusable set: email, social, cpc, referral. Source + medium is the pair most analytics tools group on.',
  },
  utm_campaign: {
    label: 'Campaign',
    placeholder: 'spring-launch-2026',
    hint: 'Which initiative this link belongs to. Include a date or version so a repeat next quarter stays distinguishable. Typical: spring-launch, black-friday-2026, q3-webinar.',
  },
  utm_term: {
    label: 'Term',
    placeholder: 'running+shoes',
    hint: 'Paid-search keyword — usually left blank. Only meaningful for paid search ads, and most ad platforms auto-populate it anyway. Example: running+shoes.',
    optionalReason: 'paid-search keyword, usually blank',
  },
  utm_content: {
    label: 'Content',
    placeholder: 'hero-cta',
    hint: 'Which specific creative or placement within the campaign. Typical: hero-cta, sidebar-banner, variant-a, variant-b. The one field expected to differ between two links in the same campaign.',
    optionalReason: 'the A/B or placement differentiator',
  },
};

/**
 * One line, stated once, covering the casing/hyphenation/PII conventions
 * that #0095's Description ties to the fragmentation problem
 * (`Newsletter` / `newsletter` / `news-letter` splitting into three bars).
 * Rendered above the five fields rather than repeated per field.
 */
export const UTM_CONVENTIONS_LINE =
  'Lowercase, with hyphens — not spaces or underscores (black-friday, not Black Friday). No PII: these values are visible in the address bar and in referrer headers.';
