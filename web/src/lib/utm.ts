// Pure, DOM-free UTM-parameter composition helpers (#0048). Keeping all logic
// here (rather than inline in Dashboard.svelte/LinkDetail.svelte) makes it
// unit-testable without a DOM — see utm.test.ts.
//
// STORAGE DECISION (updated by #0099 — this paragraph used to describe a
// limitation that no longer exists): UTM values are still BAKED into
// destination_url before it is sent to the API — the destination site's own
// analytics depends on those params arriving, so the composed URL is still
// what gets stored and served. As of #0099, the backend ALSO stores the five
// UTM values (plus a `placement` label) as discrete columns on the link,
// alongside destination_url. The two are expected to agree: whatever this
// module bakes into the composed URL is exactly what gets sent as the
// discrete fields (see buildInput() in Dashboard.svelte).
//
// EDIT RE-POPULATION: because the discrete columns now exist, editing a link
// CAN pre-populate the UTM builder from the API response's utm_source /
// utm_medium / utm_campaign / utm_term / utm_content fields — see
// utmParamsFromLink below. This closes the limitation this comment used to
// document (#0048). A link created before #0099's migration has all five
// columns NULL (serialized as "" by the API); utmParamsFromLink treats that
// the same as "no UTM params set", so the edit form simply opens with empty
// fields rather than erroring.

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
 * A source of the five discrete UTM columns, shaped like the subset of
 * `Link` (or `LinkDetail`) that carries them — accepted structurally so this
 * module does not need to import `./types` for a single shape. `undefined`
 * fields (a caller that only has some of the five) are treated the same as
 * `""`.
 */
export interface UtmColumnSource {
  utm_source?: string;
  utm_medium?: string;
  utm_campaign?: string;
  utm_term?: string;
  utm_content?: string;
}

/**
 * Repopulates the UTM builder's fields from a link's discrete UTM columns
 * (#0099) — the edit-form counterpart to `composeUtmUrl`. A link created
 * before #0099's migration has every column NULL, which the API serializes
 * as `""`; that collapses to `emptyUtmParams()` here exactly like a link
 * that legitimately never had UTM params, so the edit form opens with empty
 * fields rather than erroring either way.
 */
export function utmParamsFromLink(link: UtmColumnSource): UtmParams {
  return {
    utm_source: link.utm_source ?? '',
    utm_medium: link.utm_medium ?? '',
    utm_campaign: link.utm_campaign ?? '',
    utm_term: link.utm_term ?? '',
    utm_content: link.utm_content ?? '',
  };
}

/**
 * A source of a campaign's five `default_utm_*` columns, shaped like the
 * subset of `Campaign` that carries them.
 */
export interface CampaignUtmDefaultsSource {
  default_utm_source?: string;
  default_utm_medium?: string;
  default_utm_campaign?: string;
  default_utm_term?: string;
  default_utm_content?: string;
}

/**
 * Prefills the UTM builder's fields from a campaign's `default_utm_*`
 * values when the author selects a campaign on the create form (#0099).
 * These are a STARTING POINT, never a lock: the caller assigns the returned
 * UtmParams into the same `$state` the builder already binds to, so every
 * field stays as editable as if the author had typed it themselves — this
 * function does nothing to enforce the campaign's values afterward.
 *
 * DECISION (#0098's downstream constraint 5, closed here): a NULL
 * `default_utm_campaign` (the column can be cleared via `PATCH
 * /api/campaigns/{slug}` and is never re-derived afterward — see
 * campaigns.CampaignUpdate's doc comment) prefills to an EMPTY field, not
 * the campaign's slug. This function does not even accept a slug as input,
 * so there is structurally no fallback to reach for. Reasoning: the slug is
 * fixed at creation and `default_utm_campaign` defaults to it only at that
 * moment (campaigns.Store.CreateCampaign); once a user has deliberately
 * cleared the field, silently re-deriving it from the slug would resurrect
 * a value they removed, and the slug can already drift from the campaign's
 * current name (it never renames). See
 * "clearing default_utm_campaign does not fall back to the slug" in
 * utm.test.ts and docs/utm.md's "Campaign membership" section.
 */
export function utmParamsFromCampaignDefaults(campaign: CampaignUtmDefaultsSource): UtmParams {
  return {
    utm_source: campaign.default_utm_source ?? '',
    utm_medium: campaign.default_utm_medium ?? '',
    utm_campaign: campaign.default_utm_campaign ?? '',
    utm_term: campaign.default_utm_term ?? '',
    utm_content: campaign.default_utm_content ?? '',
  };
}

/**
 * Merges a campaign's `default_utm_*` values onto `current` field-by-field,
 * filling in ONLY the fields that are currently blank — a field the author
 * has already typed something into is left exactly as they wrote it. Used
 * by the create form's campaign-select handler (#0099 review item 6): the
 * naive `utmParams = utmParamsFromCampaignDefaults(campaign)` replaces the
 * whole object, silently wiping anything already typed (including values
 * from a PREVIOUSLY selected campaign the author then hand-edited). This is
 * the non-destructive counterpart — "prefill", not "overwrite" — matching
 * the same starting-point-not-a-lock rule `utmParamsFromCampaignDefaults`
 * documents.
 */
export function fillBlankUtmParams(current: UtmParams, campaign: CampaignUtmDefaultsSource): UtmParams {
  const defaults = utmParamsFromCampaignDefaults(campaign);
  const out = { ...current };
  for (const k of UTM_KEYS) {
    if (out[k].trim() === '') out[k] = defaults[k];
  }
  return out;
}

/**
 * Whether every UTM field is blank (or whitespace-only). The create form uses
 * this to skip composition entirely — no stray `?` appended to a clean URL.
 */
export function isUtmEmpty(params: UtmParams): boolean {
  return UTM_KEYS.every((k) => params[k].trim() === '');
}

/**
 * Compose a destination URL by RECONCILING its query string with `params` for
 * exactly the five UTM keys, relative to `previous` — the UTM values the
 * BUILDER held the last time it was authoritative for `base` (see the
 * `previous` parameter doc below). Rules:
 *
 * - A non-empty UTM value is set (replacing any existing same-named param —
 *   the builder's value wins). This never depends on `previous`.
 * - A blank/whitespace-only UTM value is REMOVED from the query string, but
 *   ONLY if `previous` shows the builder previously held a non-blank value
 *   for that same key — i.e. the author cleared something the builder
 *   itself owned. A key that is blank in both `params` and `previous` is
 *   left completely alone, even if `base`'s query string happens to already
 *   contain it.
 * - Non-UTM query params (`ref=homepage`) are always left untouched.
 * - URL-encodes all values (via URLSearchParams which handles spaces, &, = etc).
 * - If `base` is empty or blank, returns an empty string (nothing to compose).
 * - If `base` is an invalid or relative URL, falls back to reconciling the
 *   query string as a plain string (best-effort, so the user sees a preview
 *   even before a valid URL is typed).
 * - When nothing needs to change, `base` is returned unchanged (no `?` is
 *   appended to a clean URL, and no needless round-trip through
 *   URL/URLSearchParams reformatting).
 *
 * ## Why `previous` exists (do not remove the ownership gate)
 *
 * An EARLIER version of this function deleted any blank UTM key found in
 * `base`, unconditionally. That overshot in two ways review caught:
 *
 * 1. **Create.** A user pastes
 *    `https://example.com/promo?utm_source=partner-newsletter&ref=homepage`
 *    and never opens the builder. The builder's fields are all blank, but it
 *    never "owned" that `utm_source` — it came from the pasted URL, not from
 *    anything the builder composed. Deleting it destroys tracking the user
 *    never asked to remove.
 * 2. **Edit of a pre-migration link — worse.** A link with all-NULL UTM
 *    columns opens with `utmParamsFromLink` returning `emptyUtmParams()`.
 *    Saving a TITLE-ONLY change (the UTM builder never touched) would, under
 *    the unconditional-delete version, strip any `?utm_source=...` the
 *    link's `destination_url` already carried from an external source — the
 *    exact "wrong guess is worse than a known gap" the issue's migration
 *    notes warn against, now happening in the edit path instead of a
 *    migration backfill.
 *
 * `previous` closes both: pass `emptyUtmParams()` on the CREATE path (the
 * builder starts with nothing, so it can never have "owned" anything already
 * in a pasted URL — deletion never fires) and `utmParamsFromLink(detail)` on
 * the EDIT path, captured ONCE when the edit session starts (so it reflects
 * exactly what the builder was seeded with, and clearing a field the builder
 * DID seed still deletes it — the invariant #0099 exists to hold).
 */
export function composeUtmUrl(base: string, params: UtmParams, previous: UtmParams): string {
  const trimmed = base.trim();
  if (trimmed === '') return '';

  try {
    const url = new URL(trimmed);
    let changed = false;
    for (const key of UTM_KEYS) {
      const value = params[key].trim();
      if (value === '') {
        if (previous[key].trim() !== '' && url.searchParams.has(key)) {
          url.searchParams.delete(key);
          changed = true;
        }
      } else if (url.searchParams.get(key) !== value) {
        // URLSearchParams.set replaces an existing key, avoiding duplicates.
        url.searchParams.set(key, value);
        changed = true;
      }
    }
    // Nothing to reconcile: return the original string verbatim rather than
    // a URL-object round-trip, which can reformat (trailing slash, param
    // order) even when semantically identical.
    return changed ? url.toString() : trimmed;
  } catch {
    // `trimmed` is not a valid absolute URL (could be relative or still being
    // typed). Reconcile params as a plain query string so the live preview is
    // still useful while the user is mid-entry.
    return reconcileQueryFallback(trimmed, params, previous);
  }
}

/**
 * Fallback for invalid/relative base URLs: reconciles UTM params (set
 * non-blank values, delete blank ones the builder previously owned per
 * `previous`) against whatever query string is present, using simple string
 * manipulation. Used only when `new URL(base)` throws — i.e., the URL is not
 * yet fully valid. Mirrors composeUtmUrl's set-or-delete-if-previously-owned
 * rule so the edit path's "clear a previously-baked key" behavior (and the
 * create path's "never delete what the builder didn't own" guard) both hold
 * even for a not-yet-valid preview.
 */
function reconcileQueryFallback(base: string, params: UtmParams, previous: UtmParams): string {
  // Split at `?` to isolate any existing query string.
  const qIdx = base.indexOf('?');
  const pathPart = qIdx === -1 ? base : base.slice(0, qIdx);
  const existingQuery = qIdx === -1 ? '' : base.slice(qIdx + 1);

  // Parse the existing query into a simple map so we can replace/delete
  // duplicate keys while preserving insertion order for everything else.
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

  // Reconcile: set non-blank UTM values; delete blank ones only if the
  // builder previously owned that key (see composeUtmUrl's doc comment).
  let changed = false;
  for (const key of UTM_KEYS) {
    const value = params[key].trim();
    if (value === '') {
      if (previous[key].trim() !== '' && existing.delete(key)) changed = true;
    } else {
      if (existing.get(key) !== value) changed = true;
      existing.set(key, value);
    }
  }
  if (!changed) return base;

  if (existing.size === 0) return pathPart;

  // Reassemble using URLSearchParams for correct encoding.
  const sp = new URLSearchParams();
  for (const [key, value] of existing) {
    sp.set(key, value);
  }
  return `${pathPart}?${sp.toString()}`;
}
