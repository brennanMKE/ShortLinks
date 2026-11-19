import { describe, it, expect } from 'vitest';
import { applyChannel, carryOver, nextChannel, pastedUrl, CHANNELS } from './newLink';
import { emptyUtmParams } from './utm';

describe('applyChannel', () => {
  it('fills utm_source and utm_medium from the channel', () => {
    const p = applyChannel(emptyUtmParams(), 'mastodon');
    expect(p.utm_source).toBe('mastodon');
    expect(p.utm_medium).toBe('social');
  });
  it('uses print for a poster', () => {
    expect(applyChannel(emptyUtmParams(), 'poster').utm_medium).toBe('print');
  });
  it('leaves the other UTM fields alone', () => {
    const start = { ...emptyUtmParams(), utm_campaign: 'guides-2026-11', utm_content: 'post-84' };
    const p = applyChannel(start, 'bluesky');
    expect(p.utm_campaign).toBe('guides-2026-11');
    expect(p.utm_content).toBe('post-84');
  });
  it('switching channels replaces source and medium', () => {
    const a = applyChannel(emptyUtmParams(), 'mastodon');
    const b = applyChannel(a, 'newsletter');
    expect(b.utm_source).toBe('newsletter');
    expect(b.utm_medium).toBe('email');
  });
  it('no channel changes nothing', () => {
    const start = { ...emptyUtmParams(), utm_source: 'x', utm_medium: 'y' };
    expect(applyChannel(start, '', { default_utm_medium: 'z' })).toEqual(start);
  });
  it('does not mutate its input', () => {
    const start = emptyUtmParams();
    applyChannel(start, 'mastodon');
    expect(start.utm_source).toBe('');
  });
});

describe('applyChannel with a campaign medium', () => {
  it('lets the campaign default medium win over the channel medium', () => {
    const p = applyChannel(emptyUtmParams(), 'mastodon', { default_utm_medium: 'community' });
    expect(p.utm_source).toBe('mastodon');
    expect(p.utm_medium).toBe('community');
  });
  it('uses the channel medium when the campaign sets none', () => {
    expect(applyChannel(emptyUtmParams(), 'poster', { default_utm_medium: '' }).utm_medium).toBe('print');
  });
});

describe('nextChannel', () => {
  it('walks the list and wraps', () => {
    expect(nextChannel('mastodon')).toBe('bluesky');
    expect(nextChannel(CHANNELS[CHANNELS.length - 1].id)).toBe(CHANNELS[0].id);
  });
  it('starts at the first channel for an unknown one', () => {
    expect(nextChannel('')).toBe(CHANNELS[0].id);
  });
});

describe('carryOver (create another for this page)', () => {
  const form = {
    destinationUrl: 'https://example.com/guide',
    title: 'Measuring current',
    campaignId: 7 as number | '',
    channel: 'mastodon',
    utmParams: {
      utm_source: 'mastodon',
      utm_medium: 'social',
      utm_campaign: 'guides-2026-11',
      utm_term: 'amps',
      utm_content: 'post-84',
    },
  };
  it('keeps URL, title and campaign', () => {
    const c = carryOver(form);
    expect(c.destinationUrl).toBe(form.destinationUrl);
    expect(c.title).toBe(form.title);
    expect(c.campaignId).toBe(7);
    expect(c.utmParams.utm_campaign).toBe('guides-2026-11');
    expect(c.utmParams.utm_term).toBe('amps');
  });
  it('clears the channel-specific fields', () => {
    const c = carryOver(form);
    expect(c.utmParams.utm_source).toBe('');
    expect(c.utmParams.utm_medium).toBe('');
    expect(c.utmParams.utm_content).toBe('');
  });
  it('suggests the next channel, or takes the one picked', () => {
    expect(carryOver(form).channel).toBe('bluesky');
    expect(carryOver(form, 'poster').channel).toBe('poster');
  });
});

describe('pastedUrl', () => {
  it('accepts a bare http(s) URL, trimmed', () => {
    expect(pastedUrl('  https://example.com/a?b=1\n')).toBe('https://example.com/a?b=1');
  });
  it('rejects prose, other schemes and empties', () => {
    expect(pastedUrl('see https://example.com now')).toBeNull();
    expect(pastedUrl('ftp://example.com')).toBeNull();
    expect(pastedUrl('hello')).toBeNull();
    expect(pastedUrl('')).toBeNull();
    expect(pastedUrl(null)).toBeNull();
  });
});
