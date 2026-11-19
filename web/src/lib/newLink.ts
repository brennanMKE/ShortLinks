// Pure helpers for the create-link flow (#0158): the Channel shortcut that
// fills utm_source / utm_medium, the "Create another for this page" carry-over,
// and the paste-a-URL detector. DOM-free so newLink.test.ts covers them
// without a browser.

import { emptyUtmParams, type UtmParams, type CampaignUtmDefaultsSource } from './utm';
import { isValidHttpUrl } from './links';

export interface Channel {
  /** Value of the Channel select; also what utm_source is set to. */
  id: string;
  label: string;
  /** The utm_medium this channel implies. */
  medium: string;
}

/** The channels offered by the Channel select, in the order "next channel" walks them. */
export const CHANNELS: readonly Channel[] = [
  { id: 'mastodon', label: 'Mastodon', medium: 'social' },
  { id: 'bluesky', label: 'Bluesky', medium: 'social' },
  { id: 'linkedin', label: 'LinkedIn', medium: 'social' },
  { id: 'x', label: 'X', medium: 'social' },
  { id: 'facebook', label: 'Facebook', medium: 'social' },
  { id: 'instagram', label: 'Instagram', medium: 'social' },
  { id: 'reddit', label: 'Reddit', medium: 'social' },
  { id: 'newsletter', label: 'Newsletter', medium: 'email' },
  { id: 'poster', label: 'Poster', medium: 'print' },
  { id: 'flyer', label: 'Flyer', medium: 'print' },
];

export function channelById(id: string): Channel | undefined {
  return CHANNELS.find((c) => c.id === id);
}

/**
 * Applies a channel to the UTM fields: utm_source becomes the channel. utm_medium
 * is the campaign's default medium when it sets one (campaign conventions win),
 * else the channel's medium, else whatever is already there.
 * Choosing no channel leaves the fields alone. The other three fields are
 * never touched, so the campaign's utm_campaign survives a channel switch.
 */
export function applyChannel(
  params: UtmParams,
  channelId: string,
  campaign?: CampaignUtmDefaultsSource | null,
): UtmParams {
  const ch = channelById(channelId);
  if (!ch) return params;
  return {
    ...params,
    utm_source: ch.id,
    utm_medium: campaign?.default_utm_medium || ch.medium || params.utm_medium,
  };
}

/** The channel to suggest after `current`: the next in CHANNELS, wrapping around. */
export function nextChannel(current: string): string {
  const i = CHANNELS.findIndex((c) => c.id === current);
  return CHANNELS[(i + 1) % CHANNELS.length].id;
}

/** The form fields "Create another for this page" reads and keeps. */
export interface CarryOver {
  destinationUrl: string;
  title: string;
  campaignId: number | '';
  /** On the way out only utm_campaign and utm_term are kept; source, medium and content belong to the channel. */
  utmParams: UtmParams;
  channel: string;
}

/**
 * What "Create another for this page" keeps from the link just created: the
 * destination (UTM-free, as typed), title and campaign, plus the page-level UTM
 * fields. The alias, expiry, placement, source, medium and content are cleared
 * because each belongs to one channel's link. `channel` is the next one to pick.
 */
export function carryOver(form: CarryOver, channel: string = nextChannel(form.channel)): CarryOver {
  const kept = emptyUtmParams();
  kept.utm_campaign = form.utmParams.utm_campaign;
  kept.utm_term = form.utmParams.utm_term;
  return {
    destinationUrl: form.destinationUrl,
    title: form.title,
    campaignId: form.campaignId,
    utmParams: kept,
    channel,
  };
}

/**
 * The URL in pasted text, or null. Only a single bare http(s) URL counts (no
 * surrounding text, no whitespace inside), so pasting prose never opens the
 * dialog.
 */
export function pastedUrl(text: string | null | undefined): string | null {
  const t = (text ?? '').trim();
  if (t === '' || /\s/.test(t)) return null;
  return isValidHttpUrl(t) ? t : null;
}

/** Phone widths get the full page at #/new; wider screens get the dialog. */
export const PHONE_MAX_WIDTH = 600;
