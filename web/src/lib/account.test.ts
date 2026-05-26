// Unit tests for the Account pure logic (#0036): the revoke-failure → friendly
// message mapping (notably the backend's 409 last-credential refusal), the
// last-credential guard, rename validation, and date / last-used formatting. No
// DOM or network — only the data shaping the Account view delegates to
// lib/account.ts.

import { describe, it, expect } from 'vitest';
import { ApiError } from './api';
import type { Credential } from './types';
import {
  LAST_CREDENTIAL_MESSAGE,
  LAST_CREDENTIAL_CODE,
  REVOKE_FAILED_MESSAGE,
  revokeErrorMessage,
  isLastCredentialError,
  validateDeviceName,
  canRevoke,
  formatDate,
  lastUsedLabel,
} from './account';

function credential(overrides: Partial<Credential> = {}): Credential {
  return {
    id: 1,
    device_name: 'iCloud Keychain',
    aaguid: 'fbfc3007-154e-4ecc-8c0b-6e020557d7bd',
    device_hint: 'iCloud Keychain',
    sign_count: 0,
    created_at: '2026-05-25T12:00:00Z',
    last_used_at: null,
    ...overrides,
  };
}

describe('revokeErrorMessage', () => {
  it('maps a 409 to the friendly last-credential message', () => {
    const err = new ApiError(409, LAST_CREDENTIAL_CODE, { error: LAST_CREDENTIAL_CODE });
    expect(revokeErrorMessage(err)).toBe(LAST_CREDENTIAL_MESSAGE);
  });

  it('maps the last-credential error CODE even on a non-409 status', () => {
    // Defends against a future status change while the code stays stable.
    const err = new ApiError(400, LAST_CREDENTIAL_CODE, { error: LAST_CREDENTIAL_CODE });
    expect(revokeErrorMessage(err)).toBe(LAST_CREDENTIAL_MESSAGE);
  });

  it('falls back to the generic message for other API errors', () => {
    const err = new ApiError(500, 'internal server error', { error: 'internal server error' });
    expect(revokeErrorMessage(err)).toBe(REVOKE_FAILED_MESSAGE);
  });

  it('falls back to the generic message for a non-ApiError throwable', () => {
    expect(revokeErrorMessage(new Error('network down'))).toBe(REVOKE_FAILED_MESSAGE);
    expect(revokeErrorMessage('boom')).toBe(REVOKE_FAILED_MESSAGE);
  });
});

describe('isLastCredentialError', () => {
  it('is true for a 409', () => {
    expect(isLastCredentialError(new ApiError(409, LAST_CREDENTIAL_CODE, {}))).toBe(true);
  });

  it('is true when the error code matches even off a different status', () => {
    expect(
      isLastCredentialError(new ApiError(403, 'x', { error: LAST_CREDENTIAL_CODE })),
    ).toBe(true);
  });

  it('is false for unrelated failures', () => {
    expect(isLastCredentialError(new ApiError(404, 'credential not found', {}))).toBe(false);
    expect(isLastCredentialError(new Error('nope'))).toBe(false);
  });
});

describe('validateDeviceName', () => {
  it('accepts and trims a non-empty name', () => {
    expect(validateDeviceName('  YubiKey 5C  ')).toEqual({ value: 'YubiKey 5C' });
  });

  it('rejects an empty / whitespace-only name', () => {
    expect(validateDeviceName('')).toHaveProperty('error');
    expect(validateDeviceName('   ')).toHaveProperty('error');
  });
});

describe('canRevoke', () => {
  it('is false when there is only one credential (the last one)', () => {
    expect(canRevoke([credential()])).toBe(false);
  });

  it('is false when there are no credentials', () => {
    expect(canRevoke([])).toBe(false);
  });

  it('is true when more than one credential exists', () => {
    expect(canRevoke([credential({ id: 1 }), credential({ id: 2 })])).toBe(true);
  });
});

describe('formatDate', () => {
  it('formats an RFC 3339 timestamp as a human date', () => {
    expect(formatDate('2026-05-25T12:00:00Z')).toMatch(/2026/);
  });

  it('returns the default sentinel for a null/empty value', () => {
    expect(formatDate(null)).toBe('Never');
    expect(formatDate(undefined)).toBe('Never');
  });

  it('falls back to the raw string for an unparseable value', () => {
    expect(formatDate('not-a-date')).toBe('not-a-date');
  });
});

describe('lastUsedLabel', () => {
  it('shows "Never used" for a passkey with no last_used_at', () => {
    expect(lastUsedLabel(null)).toBe('Never used');
  });

  it('formats a recorded last-used timestamp', () => {
    expect(lastUsedLabel('2026-05-20T09:00:00Z')).toMatch(/2026/);
  });
});
