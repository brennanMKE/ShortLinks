// Row metadata for the links table (#0156).
import { describe, it, expect } from 'vitest';
import { destinationPath, linkMeta } from './links';
import type { Link } from './types';

const base: Link = {
  id: 1,
  key: 'k3Fz9a',
  destination_url: 'https://www.opencircuitsf.com/guides/measuring?x=1&utm_source=mastodon',
  title: 't',
  active: true,
  denied_reason: 0,
  created_at: '',
  expires_at: null,
  click_count: 0,
  campaign_id: null,
  utm_source: 'mastodon',
  utm_medium: 'social',
  utm_campaign: '',
  utm_term: '',
  utm_content: '',
  placement: '',
};

describe('destinationPath', () => {
  it('returns path and query', () => {
    expect(destinationPath(base.destination_url)).toBe('/guides/measuring?x=1');
  });
  it('drops utm_* params but keeps others', () => {
    expect(destinationPath('https://e.com/a?utm_source=x&id=2&utm_medium=y')).toBe('/a?id=2');
    expect(destinationPath('https://e.com/a?utm_source=x')).toBe('/a');
  });
  it('falls back to the host for a bare origin and the raw string for junk', () => {
    expect(destinationPath('https://example.com/')).toBe('example.com');
    expect(destinationPath('nonsense')).toBe('nonsense');
  });
});

describe('linkMeta', () => {
  it('joins key, source, channel and path', () => {
    expect(linkMeta(base)).toBe('/u/k3Fz9a · mastodon · social · /guides/measuring?x=1');
  });
  it('skips empty parts', () => {
    expect(linkMeta({ ...base, utm_source: '', utm_medium: '' })).toBe('/u/k3Fz9a · /guides/measuring?x=1');
  });
});
