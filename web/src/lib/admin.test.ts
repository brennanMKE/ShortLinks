// Unit tests for the Admin pure logic (#0037): the reason-code/value ↔ label
// maps, the deactivation `other`-requires-note validation, the URL-filter
// test-result mapping, audit actor/target/metadata rendering, the user-id filter
// parser, and the pagination math. No DOM or network — only the data shaping the
// Admin view delegates to lib/admin.ts.

import { describe, it, expect } from 'vitest';
import type { AdminUser, AuditEntry } from './types';
import {
  REASON_OPTIONS,
  reasonLabel,
  DEACTIVATION_REASONS,
  deactivationReasonLabel,
  isValidDeactivationReason,
  validateDeactivation,
  canDeactivate,
  filterTestNotice,
  actorLabel,
  targetLabel,
  formatMetadata,
  formatDateTime,
  pageInfo,
  parseUserIdFilter,
  registrationsEnabled,
} from './admin';

function adminUser(overrides: Partial<AdminUser> = {}): AdminUser {
  return {
    id: 2,
    email: 'user@example.com',
    is_admin: false,
    active: true,
    created_at: '2026-05-25T12:00:00Z',
    ...overrides,
  };
}

function auditEntry(overrides: Partial<AuditEntry> = {}): AuditEntry {
  return {
    id: 1,
    actor_id: 1,
    user_id: null,
    action: 'settings.updated',
    target_type: 'settings',
    target_id: null,
    metadata: null,
    ip_address: '127.0.0.1',
    created_at: '2026-05-25T12:00:00Z',
    ...overrides,
  };
}

describe('reason codes', () => {
  it('offers exactly the six 1..6 codes in order', () => {
    expect(REASON_OPTIONS.map((o) => o.code)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  it('maps each code to its PRD label', () => {
    expect(reasonLabel(1)).toBe('Malware or ransomware');
    expect(reasonLabel(2)).toBe('Phishing');
    expect(reasonLabel(3)).toBe('Spam');
    expect(reasonLabel(4)).toBe('Adult content');
    expect(reasonLabel(5)).toBe('Policy violation');
    expect(reasonLabel(6)).toBe('Other');
  });

  it('maps 0 to "Not denied" and an unknown code to "Other"', () => {
    expect(reasonLabel(0)).toBe('Not denied');
    expect(reasonLabel(99)).toBe('Other');
  });
});

describe('deactivation reasons', () => {
  it('offers exactly the six PRD reason values in order', () => {
    expect(DEACTIVATION_REASONS.map((d) => d.value)).toEqual([
      'malware_distribution',
      'phishing',
      'spam',
      'harassment',
      'terms_violation',
      'other',
    ]);
  });

  it('labels a known value and passes through an unknown one', () => {
    expect(deactivationReasonLabel('phishing')).toBe('Phishing');
    expect(deactivationReasonLabel('terms_violation')).toBe('Terms of service violation');
    expect(deactivationReasonLabel('mystery')).toBe('mystery');
  });

  it('validates membership', () => {
    expect(isValidDeactivationReason('spam')).toBe(true);
    expect(isValidDeactivationReason('nope')).toBe(false);
    expect(isValidDeactivationReason('')).toBe(false);
  });
});

describe('validateDeactivation', () => {
  it('rejects an unknown reason', () => {
    expect(validateDeactivation('', 'x')).toEqual({ error: 'Select a deactivation reason.' });
    expect(validateDeactivation('bogus', 'x')).toEqual({ error: 'Select a deactivation reason.' });
  });

  it('requires a note when reason is other', () => {
    expect(validateDeactivation('other', '')).toEqual({
      error: 'A note is required when the reason is "Other".',
    });
    expect(validateDeactivation('other', '   ')).toEqual({
      error: 'A note is required when the reason is "Other".',
    });
  });

  it('accepts other with a non-empty note (trimmed)', () => {
    expect(validateDeactivation('other', '  see ticket  ')).toEqual({ note: 'see ticket' });
  });

  it('allows an empty note for non-other reasons', () => {
    expect(validateDeactivation('spam', '')).toEqual({ note: '' });
    expect(validateDeactivation('phishing', '  noted  ')).toEqual({ note: 'noted' });
  });
});

describe('canDeactivate', () => {
  it('offers deactivate for an active non-admin who is not the current user', () => {
    expect(canDeactivate(adminUser({ id: 2 }), 1)).toBe(true);
  });

  it('refuses for an admin, for self, and for an already-inactive user', () => {
    expect(canDeactivate(adminUser({ id: 2, is_admin: true }), 1)).toBe(false);
    expect(canDeactivate(adminUser({ id: 1 }), 1)).toBe(false);
    expect(canDeactivate(adminUser({ id: 2, active: false }), 1)).toBe(false);
  });
});

describe('filterTestNotice', () => {
  it('reports a match with rule id, code, and label', () => {
    const notice = filterTestNotice({ matched: true, reason_code: 2, rule_id: 5 });
    expect(notice).toEqual({
      kind: 'match',
      ruleId: 5,
      reasonCode: 2,
      label: 'Phishing',
      message: 'Blocked by rule #5 — Phishing',
    });
  });

  it('reports no match', () => {
    const notice = filterTestNotice({ matched: false });
    expect(notice.kind).toBe('no-match');
    expect(notice.message).toContain('allowed');
  });

  it('treats a malformed match (missing fields) as no-match', () => {
    expect(filterTestNotice({ matched: true }).kind).toBe('no-match');
  });
});

describe('audit rendering', () => {
  it('labels a NULL actor as system and a numeric actor as user #N', () => {
    expect(actorLabel(auditEntry({ actor_id: null }))).toBe('system');
    expect(actorLabel(auditEntry({ actor_id: 7 }))).toBe('user #7');
  });

  it('labels the target with type and optional id', () => {
    expect(targetLabel(auditEntry({ target_type: null }))).toBe('—');
    expect(targetLabel(auditEntry({ target_type: 'settings', target_id: null }))).toBe('settings');
    expect(targetLabel(auditEntry({ target_type: 'user', target_id: 5 }))).toBe('user #5');
  });

  it('renders metadata as a sorted compact key=value string', () => {
    expect(formatMetadata({ new_value: 'true', key: 'registrations_enabled' })).toBe(
      'key=registrations_enabled, new_value=true',
    );
  });

  it('handles null, empty, primitive, and nested metadata', () => {
    expect(formatMetadata(null)).toBe('');
    expect(formatMetadata({})).toBe('');
    expect(formatMetadata('boom')).toBe('boom');
    expect(formatMetadata({ note: null, reason: 'spam' })).toBe('note=, reason=spam');
    expect(formatMetadata({ list: [1, 2] })).toBe('list=[1,2]');
  });

  it('formats a timestamp and falls back on a bad one', () => {
    expect(formatDateTime('not-a-date')).toBe('not-a-date');
    expect(formatDateTime('2026-05-25T12:00:00Z')).not.toBe('2026-05-25T12:00:00Z');
  });
});

describe('parseUserIdFilter', () => {
  it('treats empty as no filter', () => {
    expect(parseUserIdFilter('')).toEqual({ userId: null });
    expect(parseUserIdFilter('   ')).toEqual({ userId: null });
  });

  it('parses a positive integer', () => {
    expect(parseUserIdFilter(' 42 ')).toEqual({ userId: 42 });
  });

  it('rejects non-numeric or non-positive input', () => {
    expect(parseUserIdFilter('abc')).toEqual({ error: 'Enter a numeric user id.' });
    expect(parseUserIdFilter('0')).toEqual({ error: 'Enter a numeric user id.' });
    expect(parseUserIdFilter('-3')).toEqual({ error: 'Enter a numeric user id.' });
    expect(parseUserIdFilter('1.5')).toEqual({ error: 'Enter a numeric user id.' });
  });
});

describe('pageInfo', () => {
  it('computes pages, range, and nav flags for a middle page', () => {
    const info = pageInfo(125, 2, 50);
    expect(info.totalPages).toBe(3);
    expect(info.firstItem).toBe(51);
    expect(info.lastItem).toBe(100);
    expect(info.hasPrev).toBe(true);
    expect(info.hasNext).toBe(true);
  });

  it('treats an empty list as page 1 of 1 with a 0 range', () => {
    const info = pageInfo(0, 1, 50);
    expect(info.totalPages).toBe(1);
    expect(info.firstItem).toBe(0);
    expect(info.lastItem).toBe(0);
    expect(info.hasPrev).toBe(false);
    expect(info.hasNext).toBe(false);
  });

  it('caps the last item at total on the final partial page', () => {
    const info = pageInfo(125, 3, 50);
    expect(info.firstItem).toBe(101);
    expect(info.lastItem).toBe(125);
    expect(info.hasNext).toBe(false);
    expect(info.hasPrev).toBe(true);
  });

  it('clamps an out-of-range page and bad per_page defensively', () => {
    expect(pageInfo(10, 99, 50).page).toBe(1);
    expect(pageInfo(10, 0, 0).perPage).toBe(1);
  });
});

describe('registrationsEnabled', () => {
  it('is true only for the literal "true"', () => {
    expect(registrationsEnabled('true')).toBe(true);
    expect(registrationsEnabled('false')).toBe(false);
    expect(registrationsEnabled(undefined)).toBe(false);
    expect(registrationsEnabled('TRUE')).toBe(false);
  });
});
