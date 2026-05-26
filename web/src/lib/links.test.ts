// Unit tests for the Dashboard pure logic (#0033): the short-URL builder,
// client-side URL validation, denial-reason labels, and the create-response →
// notice mapping (success/duplicate/422-denied/409/400). No DOM or network —
// only the data shaping the view delegates to lib/links.ts.

import { describe, it, expect } from 'vitest';
import { ApiError } from './api';
import type { Link } from './types';
import {
  SHORT_URL_BASE,
  shortUrl,
  isValidHttpUrl,
  deniedReasonLabel,
  noticeForCreated,
  noticeForError,
  linkStatus,
  destinationDomain,
  DUPLICATE_NOTICE,
} from './links';

function link(overrides: Partial<Link> = {}): Link {
  return {
    id: 1,
    key: '8d0d93',
    destination_url: 'https://www.example.com/page',
    title: '',
    active: true,
    denied_reason: 0,
    created_at: '2026-05-25T12:00:00Z',
    expires_at: null,
    click_count: 0,
    ...overrides,
  };
}

describe('shortUrl', () => {
  it('builds the branded /u/{key} URL from a key', () => {
    expect(shortUrl('8d0d93')).toBe(`${SHORT_URL_BASE}/u/8d0d93`);
  });

  it('uses the production base, not the dev origin', () => {
    expect(shortUrl('abc')).toBe('https://go.sstools.co/u/abc');
  });

  it('encodes an unusual key defensively', () => {
    expect(shortUrl('a b')).toBe('https://go.sstools.co/u/a%20b');
  });
});

describe('isValidHttpUrl', () => {
  it('accepts http and https absolute URLs', () => {
    expect(isValidHttpUrl('https://example.com')).toBe(true);
    expect(isValidHttpUrl('http://example.com/path?q=1')).toBe(true);
  });

  it('trims surrounding whitespace before validating', () => {
    expect(isValidHttpUrl('  https://example.com  ')).toBe(true);
  });

  it('rejects empty, non-absolute, and non-http(s) schemes', () => {
    expect(isValidHttpUrl('')).toBe(false);
    expect(isValidHttpUrl('   ')).toBe(false);
    expect(isValidHttpUrl('example.com')).toBe(false);
    expect(isValidHttpUrl('/relative/path')).toBe(false);
    expect(isValidHttpUrl('ftp://example.com')).toBe(false);
    expect(isValidHttpUrl('javascript:alert(1)')).toBe(false);
    expect(isValidHttpUrl('mailto:a@b.com')).toBe(false);
  });
});

describe('deniedReasonLabel', () => {
  it('maps each non-zero code to its PRD label', () => {
    expect(deniedReasonLabel(1)).toBe('Malware or ransomware');
    expect(deniedReasonLabel(2)).toBe('Phishing');
    expect(deniedReasonLabel(3)).toBe('Spam');
    expect(deniedReasonLabel(4)).toBe('Adult content');
    expect(deniedReasonLabel(5)).toBe('Policy violation');
    expect(deniedReasonLabel(6)).toBe('Other');
  });

  it('returns empty string for "not denied" (0) and unknown codes', () => {
    expect(deniedReasonLabel(0)).toBe('');
    expect(deniedReasonLabel(99)).toBe('');
  });
});

describe('noticeForCreated', () => {
  it('returns a "created" notice with the short URL for a fresh link', () => {
    const n = noticeForCreated(link({ key: 'xyz789', duplicate: false }));
    expect(n.kind).toBe('created');
    if (n.kind === 'created') {
      expect(n.shortUrl).toBe('https://go.sstools.co/u/xyz789');
      expect(n.link.key).toBe('xyz789');
    }
  });

  it('returns a "duplicate" notice with the AC copy when duplicate:true', () => {
    const n = noticeForCreated(link({ key: 'dup111', duplicate: true }));
    expect(n.kind).toBe('duplicate');
    if (n.kind === 'duplicate') {
      expect(n.message).toBe(DUPLICATE_NOTICE);
      expect(n.shortUrl).toBe('https://go.sstools.co/u/dup111');
      // The returned link is still surfaced.
      expect(n.link.key).toBe('dup111');
    }
  });

  it('treats a missing duplicate field as a fresh create', () => {
    const n = noticeForCreated(link({ duplicate: undefined }));
    expect(n.kind).toBe('created');
  });
});

describe('noticeForError', () => {
  it('maps 422 url_denied to a denied notice using the server label', () => {
    const err = new ApiError(422, 'url_denied', {
      error: 'url_denied',
      reason: 2,
      label: 'Phishing',
    });
    const n = noticeForError(err);
    expect(n.kind).toBe('denied');
    if (n.kind === 'denied') expect(n.message).toContain('Phishing');
  });

  it('falls back to the code→label table when 422 body has no label', () => {
    const err = new ApiError(422, 'url_denied', { error: 'url_denied', reason: 1 });
    const n = noticeForError(err);
    expect(n.kind).toBe('denied');
    if (n.kind === 'denied') expect(n.message).toContain('Malware or ransomware');
  });

  it('maps 409 to an inline error on the key field', () => {
    const n = noticeForError(new ApiError(409, 'key already taken', { error: 'key already taken' }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') expect(n.field).toBe('key');
  });

  it('maps 400 to an inline error on the destination_url field', () => {
    const n = noticeForError(new ApiError(400, 'bad url', { error: 'bad url' }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') expect(n.field).toBe('destination_url');
  });

  it('maps a non-ApiError to a generic connection error', () => {
    const n = noticeForError(new Error('network down'));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      expect(n.field).toBeNull();
      expect(n.message).toMatch(/connection/i);
    }
  });
});

describe('linkStatus', () => {
  it('reports denied when denied_reason > 0 regardless of active', () => {
    expect(linkStatus(link({ active: false, denied_reason: 3 }))).toBe('denied');
  });

  it('reports inactive for a deactivated, non-denied link', () => {
    expect(linkStatus(link({ active: false, denied_reason: 0 }))).toBe('inactive');
  });

  it('reports active for an active, non-denied link', () => {
    expect(linkStatus(link({ active: true, denied_reason: 0 }))).toBe('active');
  });
});

describe('destinationDomain', () => {
  it('extracts the hostname', () => {
    expect(destinationDomain('https://www.example.com/a/b?c=1')).toBe('www.example.com');
  });

  it('falls back to the raw string for an unparseable value', () => {
    expect(destinationDomain('not a url')).toBe('not a url');
  });
});
