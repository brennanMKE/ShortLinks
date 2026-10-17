import { describe, it, expect } from 'vitest';
import {
  expiresAtFor,
  validateTokenName,
  createErrorMessage,
  expiryLabel,
  sortTokens,
  curlExample,
  MAX_TOKEN_NAME_LEN,
} from './apiTokens';
import { ApiError } from './api';
import type { ApiToken } from './types';

const now = new Date('2026-10-16T12:00:00Z');

function token(over: Partial<ApiToken>): ApiToken {
  return {
    id: 1,
    name: 't',
    prefix: 'slk_abcdefgh',
    read_only: false,
    status: 'active',
    created_at: '2026-10-01T00:00:00Z',
    expires_at: null,
    last_used_at: null,
    revoked_at: null,
    ...over,
  };
}

describe('expiresAtFor', () => {
  it('returns null for never', () => {
    expect(expiresAtFor('never', '', now)).toEqual({ value: null });
  });
  it('adds whole days for the presets', () => {
    expect(expiresAtFor('30', '', now)).toEqual({ value: '2026-11-15T12:00:00.000Z' });
    expect(expiresAtFor('365', '', now)).toEqual({ value: '2027-10-16T12:00:00.000Z' });
  });
  it('requires a future custom date', () => {
    expect(expiresAtFor('custom', '', now)).toHaveProperty('error');
    expect(expiresAtFor('custom', '2020-01-01', now)).toHaveProperty('error');
    const ok = expiresAtFor('custom', '2027-01-31', now);
    expect(ok).toHaveProperty('value');
    expect(new Date((ok as { value: string }).value).getDate()).toBe(31);
  });
});

describe('validateTokenName', () => {
  it('trims and rejects empty or long names', () => {
    expect(validateTokenName('  agent ')).toEqual({ value: 'agent' });
    expect(validateTokenName('   ')).toHaveProperty('error');
    expect(validateTokenName('x'.repeat(MAX_TOKEN_NAME_LEN + 1))).toHaveProperty('error');
  });
});

describe('createErrorMessage', () => {
  it('explains a duplicate name', () => {
    expect(createErrorMessage(new ApiError(409, 'api_token_name_taken', null))).toMatch(/already/);
    expect(createErrorMessage(new Error('x'))).toMatch(/try again/);
  });
});

describe('display helpers', () => {
  it('labels expiry', () => {
    expect(expiryLabel(token({}))).toBe('Never');
    expect(expiryLabel(token({ status: 'expired', expires_at: '2026-01-02T00:00:00Z' }))).toMatch(/^Expired /);
  });
  it('sorts active first', () => {
    const sorted = sortTokens([
      token({ id: 1, status: 'revoked' }),
      token({ id: 2, status: 'expired' }),
      token({ id: 3, status: 'active' }),
    ]);
    expect(sorted.map((t) => t.id)).toEqual([3, 2, 1]);
  });
  it('builds a curl example against /api/me', () => {
    expect(curlExample('https://go.example', 'slk_x')).toBe(
      'curl -H "Authorization: Bearer slk_x" https://go.example/api/me',
    );
  });
});
