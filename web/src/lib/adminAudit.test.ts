// Tests for the audit-log filters held in the hash and the user picker
// matching (#0171). Pure logic; no DOM.

import { describe, it, expect } from 'vitest';
import type { AdminUser } from './types';
import {
  parseAuditQuery,
  auditQueryString,
  hasAuditFilters,
  auditTimeBounds,
  DEFAULT_AUDIT_FILTERS,
  matchUsers,
  presetSince,
  metadataDetail,
} from './admin';

function user(id: number, email: string): AdminUser {
  return { id, email, is_admin: false, active: true, created_at: '2026-01-01T00:00:00Z' };
}

describe('audit filters in the hash', () => {
  const q = (s: string) => new URLSearchParams(s);

  it('parses email, actions, range and page', () => {
    expect(parseAuditQuery(q('email=a%40b.com&action=link.created,account.login&range=7d&page=3'))).toEqual({
      email: 'a@b.com',
      actions: ['link.created', 'account.login'],
      range: '7d',
      since: '',
      from: '',
      to: '',
      page: 3,
    });
  });

  it('falls back to defaults for junk', () => {
    expect(parseAuditQuery(q('range=bogus&page=-2&from=2026-01-01'))).toEqual(DEFAULT_AUDIT_FILTERS);
    expect(parseAuditQuery(q('range=custom&from=yesterday&to=2026-02-03')).from).toBe('');
    expect(parseAuditQuery(q('range=custom&from=2026-01-02&to=2026-02-03'))).toMatchObject({
      from: '2026-01-02',
      to: '2026-02-03',
    });
  });

  it('round-trips and omits defaults', () => {
    expect(auditQueryString(DEFAULT_AUDIT_FILTERS)).toBe('');
    const f = {
      email: 'a@b.com',
      actions: ['link.created'],
      range: 'custom' as const,
      since: '',
      from: '2026-01-02',
      to: '',
      page: 2,
    };
    expect(parseAuditQuery(new URLSearchParams(auditQueryString(f).slice(1)))).toEqual(f);
    expect(auditQueryString({ ...DEFAULT_AUDIT_FILTERS, range: '30d' })).toBe('?range=30d');
  });

  it('knows when a filter is active (page alone is not)', () => {
    expect(hasAuditFilters({ ...DEFAULT_AUDIT_FILTERS, page: 4 })).toBe(false);
    expect(hasAuditFilters({ ...DEFAULT_AUDIT_FILTERS, email: 'a@b.com' })).toBe(true);
    expect(hasAuditFilters({ ...DEFAULT_AUDIT_FILTERS, range: '24h' })).toBe(true);
  });

  it('pins a preset window in the hash so paging does not drift', () => {
    const since = presetSince('7d', new Date('2026-05-25T12:00:00Z'));
    expect(since).toBe('2026-05-18T12:00:00.000Z');
    const f = { ...DEFAULT_AUDIT_FILTERS, range: '7d' as const, since };
    expect(parseAuditQuery(new URLSearchParams(auditQueryString(f).slice(1)))).toEqual(f);
    // Later "now" does not move the window once pinned.
    expect(auditTimeBounds(f, new Date('2026-06-30T00:00:00Z'))).toEqual({ from: since });
    expect(presetSince('all')).toBe('');
    expect(presetSince('custom')).toBe('');
  });

  it('turns presets into a from instant and custom days into local day bounds', () => {
    const now = new Date('2026-05-25T12:00:00Z');
    expect(auditTimeBounds({ ...DEFAULT_AUDIT_FILTERS, range: '7d' }, now)).toEqual({
      from: '2026-05-18T12:00:00.000Z',
    });
    expect(auditTimeBounds(DEFAULT_AUDIT_FILTERS, now)).toEqual({});
    const b = auditTimeBounds(
      { ...DEFAULT_AUDIT_FILTERS, range: 'custom', from: '2026-05-20', to: '2026-05-21' },
      now,
    );
    expect(b.from).toBe(new Date(2026, 4, 20).toISOString());
    expect(b.to).toBe(new Date(2026, 4, 22).toISOString());
    expect(
      auditTimeBounds({ ...DEFAULT_AUDIT_FILTERS, range: 'custom', from: '2026-05-20' }, now).to,
    ).toBeUndefined();
  });
});

describe('matchUsers', () => {
  const users = [
    user(1, 'sam@example.com'),
    user(2, 'riley@example.com'),
    user(3, 'admin@localhost'),
    user(4, 'samantha@x.org'),
  ];

  it('matches case-insensitively, prefix matches first', () => {
    expect(matchUsers(users, 'SAM').map((u) => u.email)).toEqual(['sam@example.com', 'samantha@x.org']);
    expect(matchUsers(users, 'example').map((u) => u.email)).toEqual(['riley@example.com', 'sam@example.com']);
  });

  it('returns everyone (capped) for an empty query and nobody for a miss', () => {
    expect(matchUsers(users, '')).toHaveLength(4);
    expect(matchUsers(users, '', 2)).toHaveLength(2);
    expect(matchUsers(users, 'zzz')).toEqual([]);
  });
});

describe('metadataDetail', () => {
  it('pretty-prints objects and is empty when there is nothing to show', () => {
    expect(metadataDetail({ a: 1 })).toBe('{\n  "a": 1\n}');
    expect(metadataDetail(null)).toBe('');
    expect(metadataDetail({})).toBe('');
    expect(metadataDetail('x')).toBe('x');
  });
});
