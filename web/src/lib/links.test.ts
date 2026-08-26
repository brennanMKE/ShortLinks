// Unit tests for the Dashboard pure logic (#0033): the short-URL builder,
// client-side URL validation, denial-reason labels, and the create-response →
// notice mapping (success/duplicate/422-denied/409/400). No DOM or network —
// only the data shaping the view delegates to lib/links.ts.

import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { ApiError } from './api';
import { currentUser } from './stores';
import type { Link } from './types';
import {
  shortUrlBase,
  shortUrl,
  qrSvgUrl,
  qrPngUrl,
  isValidHttpUrl,
  isValidKey,
  MAX_KEY_LENGTH,
  deniedReasonLabel,
  noticeForCreated,
  noticeForError,
  linkStatus,
  destinationDomain,
  DUPLICATE_NOTICE,
} from './links';

// #0117: the short-URL base is no longer a compiled-in constant — it comes
// from the profile GET /api/me returns. Signing in a user with a known
// base_url is therefore the setup every short-URL assertion below needs, and
// TEST_BASE deliberately is NOT the production domain, so a regression that
// reintroduced a hard-coded `go.sstools.co` would fail these tests rather than
// pass them by coincidence.
const TEST_BASE = 'https://example.test';

beforeEach(() => {
  currentUser.set({ id: 1, email: 'user@example.com', is_admin: false, base_url: TEST_BASE });
});

afterEach(() => {
  currentUser.set(null);
});

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
    campaign_id: null,
    utm_source: '',
    utm_medium: '',
    utm_campaign: '',
    utm_term: '',
    utm_content: '',
    placement: '',
    ...overrides,
  };
}

describe('shortUrl', () => {
  it('builds the branded /u/{key} URL from a key', () => {
    expect(shortUrl('8d0d93')).toBe(`${shortUrlBase()}/u/8d0d93`);
  });

  it("uses the signed-in profile's base_url, not a compiled-in domain", () => {
    expect(shortUrl('abc')).toBe('https://example.test/u/abc');
  });

  it('follows base_url when the deployment changes domain', () => {
    currentUser.set({ id: 1, email: 'u@e.test', is_admin: false, base_url: 'https://links.other.test' });
    expect(shortUrl('abc')).toBe('https://links.other.test/u/abc');
  });

  it('trims a trailing slash on base_url rather than doubling the separator', () => {
    currentUser.set({ id: 1, email: 'u@e.test', is_admin: false, base_url: 'https://example.test/' });
    expect(shortUrl('abc')).toBe('https://example.test/u/abc');
  });

  it('encodes an unusual key defensively', () => {
    expect(shortUrl('a b')).toBe('https://example.test/u/a%20b');
  });
});

// #0106: QR download URLs are same-origin API routes (the backend GENERATES
// the code), deliberately NOT built on the configured base like shortUrl() above —
// that would point at the production redirect domain, not this app's own
// API, and would 404 in dev.
describe('qrSvgUrl / qrPngUrl', () => {
  it('builds the same-origin QR SVG API route from a key', () => {
    expect(qrSvgUrl('abc123')).toBe('/api/links/abc123/qr.svg');
  });

  it('builds the same-origin QR PNG API route from a key', () => {
    expect(qrPngUrl('abc123')).toBe('/api/links/abc123/qr.png');
  });

  it('encodes an unusual key defensively', () => {
    expect(qrSvgUrl('a b')).toBe('/api/links/a%20b/qr.svg');
    expect(qrPngUrl('a b')).toBe('/api/links/a%20b/qr.png');
  });

  it('does not use the configured short-URL base', () => {
    expect(qrSvgUrl('abc123')).not.toContain(shortUrlBase());
    expect(qrPngUrl('abc123')).not.toContain(shortUrlBase());
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
      expect(n.shortUrl).toBe('https://example.test/u/xyz789');
      expect(n.link.key).toBe('xyz789');
    }
  });

  it('returns a "duplicate" notice with the AC copy when duplicate:true', () => {
    const n = noticeForCreated(link({ key: 'dup111', duplicate: true }));
    expect(n.kind).toBe('duplicate');
    if (n.kind === 'duplicate') {
      expect(n.message).toBe(DUPLICATE_NOTICE);
      expect(n.shortUrl).toBe('https://example.test/u/dup111');
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

  // #0118 — POST /api/links has four distinct 400s (internal/handlers/links.go
  // Create). Every one of them used to be reported as "Enter a valid absolute
  // http(s) URL." on the destination field, which sent users to edit the one
  // input that was correct. Each shape below is the server's literal message.

  it("maps the 400 about the URL to the destination_url field, using the server's words", () => {
    const msg = 'destination_url must be a valid absolute http(s) URL';
    const n = noticeForError(new ApiError(400, msg, { error: msg }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      expect(n.field).toBe('destination_url');
      expect(n.message).toBe(msg);
    }
  });

  it('maps the 400 about the custom alias to the key field, not the URL field', () => {
    const msg = 'custom key must be 1-12 url-safe characters';
    const n = noticeForError(new ApiError(400, msg, { error: msg }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      expect(n.field).toBe('key');
      expect(n.message).toBe(msg);
    }
  });

  it('maps an "invalid request body" 400 to a banner, blaming no field', () => {
    const msg = 'invalid request body';
    const n = noticeForError(new ApiError(400, msg, { error: msg }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      expect(n.field).toBeNull();
      expect(n.message).toBe(msg);
    }
  });

  it('maps a "campaigns are not available" 400 to a banner, blaming no field', () => {
    const msg = 'campaigns are not available';
    const n = noticeForError(new ApiError(400, msg, { error: msg }));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      expect(n.field).toBeNull();
      expect(n.message).toBe(msg);
    }
  });

  it('falls back to a banner naming both inputs when a 400 carries no message', () => {
    const n = noticeForError(new ApiError(400, 'HTTP 400', undefined));
    expect(n.kind).toBe('error');
    if (n.kind === 'error') {
      // Unattributable: naming one field would be the guess #0118 is about.
      expect(n.field).toBeNull();
      expect(n.message).toMatch(/alias/i);
    }
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

describe('isValidKey', () => {
  it('treats an empty alias as valid — it means "generate one"', () => {
    expect(isValidKey('')).toBe(true);
    expect(isValidKey('   ')).toBe(true);
  });

  it('accepts letters, digits, hyphen and underscore up to the cap', () => {
    expect(isValidKey('launch')).toBe(true);
    expect(isValidKey('a-b_C9')).toBe(true);
    expect(isValidKey('a'.repeat(MAX_KEY_LENGTH))).toBe(true);
  });

  // The reported case (#0118): both aliases the user tried were over the cap
  // by one and two characters, and the failure was reported on the URL field.
  it('rejects an alias past the 12-character cap', () => {
    expect(isValidKey('soldering0903')).toBe(false); // 13
    expect(isValidKey('soldering-0903')).toBe(false); // 14
    expect(isValidKey('a'.repeat(MAX_KEY_LENGTH + 1))).toBe(false);
  });

  it('rejects characters outside the server\'s url-safe alphabet', () => {
    expect(isValidKey('has space')).toBe(false);
    expect(isValidKey('dot.dot')).toBe(false);
    expect(isValidKey('sla/sh')).toBe(false);
  });
});
