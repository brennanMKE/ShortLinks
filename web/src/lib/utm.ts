// Pure, DOM-free UTM-parameter composition helpers (#0048). Keeping all logic
// here (rather than inline in Dashboard.svelte) makes it unit-testable without
// a DOM — see utm.test.ts.
//
// STORAGE DECISION: UTM values are BAKED into the destination_url field before
// it is sent to the API. The current /api/links create path accepts a single
// `destination_url` string; the backend and DB schema are not changed for this
// issue. The composed URL (base destination + UTM params) is what is stored.
//
// EDIT RE-POPULATION IMPLICATION: Because only the composed URL is stored, if
// a user later edits a link the UTM builder fields cannot be individually
// pre-populated from the stored data. The edit form would show the raw
// destination URL with the UTM params already appended. If discrete re-population
// is needed in future, it would require either (a) a DB schema change to store
// UTM params as separate columns, or (b) parsing the stored URL's query string
// back into the five fields on edit-form load (lossy if params overlap with
// non-UTM query params).

/** The five standard UTM parameter keys, in their canonical order. */
export const UTM_KEYS = [
  'utm_source',
  'utm_medium',
  'utm_campaign',
  'utm_term',
  'utm_content',
] as const;

export type UtmKey = (typeof UTM_KEYS)[number];

/** A bag of (potentially empty) UTM field values keyed by the five UTM keys. */
export type UtmParams = Record<UtmKey, string>;

/** Returns a fresh UtmParams object with all fields empty. */
export function emptyUtmParams(): UtmParams {
  return {
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
  };
}

/**
 * Whether every UTM field is blank (or whitespace-only). The create form uses
 * this to skip composition entirely — no stray `?` appended to a clean URL.
 */
export function isUtmEmpty(params: UtmParams): boolean {
  return UTM_KEYS.every((k) => params[k].trim() === '');
}

/**
 * Compose a destination URL by merging UTM parameters onto any existing query
 * string. Rules:
 *
 * - Empty/whitespace UTM values are dropped — no stray empty params.
 * - URL-encodes all values (via URLSearchParams which handles spaces, &, = etc).
 * - If a param already exists in the base URL's query string under the same key,
 *   it is replaced (the UTM builder's value wins).
 * - If `base` is empty or blank, returns an empty string (nothing to compose).
 * - If `base` is an invalid or relative URL, falls back to appending query
 *   params as a plain string (best-effort, so the user sees a preview even
 *   before a valid URL is typed).
 * - When all UTM values are empty, returns `base` unchanged.
 */
export function composeUtmUrl(base: string, params: UtmParams): string {
  const trimmed = base.trim();
  if (trimmed === '') return '';
  if (isUtmEmpty(params)) return trimmed;

  // Build the set of non-empty UTM entries to merge.
  const entries: [string, string][] = UTM_KEYS.filter(
    (k) => params[k].trim() !== '',
  ).map((k) => [k, params[k].trim()]);

  try {
    const url = new URL(trimmed);
    for (const [key, value] of entries) {
      // URLSearchParams.set replaces an existing key, avoiding duplicates.
      url.searchParams.set(key, value);
    }
    return url.toString();
  } catch {
    // `trimmed` is not a valid absolute URL (could be relative or still being
    // typed). Append params as a plain query string so the live preview is still
    // useful while the user is mid-entry.
    return appendQueryFallback(trimmed, entries);
  }
}

/**
 * Fallback for invalid/relative base URLs: merges UTM params onto whatever
 * query string is present using simple string manipulation. Used only when
 * `new URL(base)` throws — i.e., the URL is not yet fully valid.
 */
function appendQueryFallback(base: string, entries: [string, string][]): string {
  if (entries.length === 0) return base;

  // Split at `?` to isolate any existing query string.
  const qIdx = base.indexOf('?');
  const pathPart = qIdx === -1 ? base : base.slice(0, qIdx);
  const existingQuery = qIdx === -1 ? '' : base.slice(qIdx + 1);

  // Parse the existing query into a simple map so we can replace duplicate keys.
  const existing = new Map<string, string>();
  if (existingQuery) {
    for (const pair of existingQuery.split('&')) {
      const eqIdx = pair.indexOf('=');
      if (eqIdx === -1) {
        existing.set(decodeURIComponent(pair), '');
      } else {
        existing.set(
          decodeURIComponent(pair.slice(0, eqIdx)),
          decodeURIComponent(pair.slice(eqIdx + 1)),
        );
      }
    }
  }

  // Merge UTM entries, replacing any existing same-named params.
  for (const [key, value] of entries) {
    existing.set(key, value);
  }

  // Reassemble using URLSearchParams for correct encoding.
  const sp = new URLSearchParams();
  for (const [key, value] of existing) {
    sp.set(key, value);
  }
  return `${pathPart}?${sp.toString()}`;
}
