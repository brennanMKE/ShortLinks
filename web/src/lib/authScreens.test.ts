import { describe, it, expect } from 'vitest';
import { ApiError } from './api';
import {
  loginFailure,
  loginModeFromQuery,
  newLinkPath,
  loginModePath,
  verifySteps,
} from './authScreens';

describe('loginFailure', () => {
  it('offers Try again on a failed assertion', () => {
    const f = loginFailure(new ApiError(401, 'x', null));
    expect(f?.action).toBe('retry');
    expect(f?.actionLabel).toBe('Try again');
  });
  it('offers no action for a deactivated account', () => {
    expect(loginFailure(new ApiError(403, 'x', null))?.action).toBe('none');
  });
  it('is silent when the user dismisses the prompt', () => {
    expect(loginFailure(new DOMException('x', 'NotAllowedError'))).toBeNull();
    expect(loginFailure(new DOMException('x', 'AbortError'))).toBeNull();
  });
  it('says the prompt was unavailable for other DOM errors', () => {
    expect(loginFailure(new DOMException('x', 'SecurityError'))?.action).toBe('retry');
  });
  it('treats a network failure as retryable', () => {
    expect(loginFailure(new TypeError('fetch'))?.action).toBe('retry');
  });
});

describe('login paths', () => {
  it('the new-link action lands on the matching login form', () => {
    expect(newLinkPath('register')).toBe('/login?mode=register');
    expect(newLinkPath('recover')).toBe('/login?mode=recover');
  });
  it('keeps the redirect target on mode links', () => {
    expect(loginModePath('register', '/campaigns/x')).toBe(
      '/login?mode=register&redirect=%2Fcampaigns%2Fx',
    );
    expect(loginModePath('signin', '/links')).toBe('/login?redirect=%2Flinks');
    expect(loginModePath('recover', null)).toBe('/login?mode=recover');
    expect(loginModePath('signin', null)).toBe('/login');
  });
});

describe('loginModeFromQuery', () => {
  it('reads register and recover, defaulting to sign in', () => {
    expect(loginModeFromQuery({ mode: 'register' })).toBe('register');
    expect(loginModeFromQuery({ mode: 'recover' })).toBe('recover');
    expect(loginModeFromQuery({ mode: 'bogus' })).toBe('signin');
    expect(loginModeFromQuery({})).toBe('signin');
  });
});

describe('verifySteps', () => {
  const states = (s: ReturnType<typeof verifySteps>) => s.map((x) => x.state);
  it('checking the link is step 1', () => {
    expect(states(verifySteps('verifying'))).toEqual(['current', 'todo', 'todo']);
  });
  it('the passkey prompt is step 2', () => {
    expect(states(verifySteps('creating'))).toEqual(['done', 'current', 'todo']);
    expect(states(verifySteps('finishing'))).toEqual(['done', 'current', 'todo']);
  });
  it('marks the failed step', () => {
    expect(states(verifySteps('error', 0))).toEqual(['failed', 'todo', 'todo']);
    expect(states(verifySteps('error', 1))).toEqual(['done', 'failed', 'todo']);
  });
  it('a failed step 1 does not read "Email confirmed"', () => {
    expect(verifySteps('error', 0)[0].label).toBe('Link not confirmed');
    expect(verifySteps('error', 1)[0].label).toBe('Email confirmed');
  });
  it('labels follow the design', () => {
    expect(verifySteps('verifying').map((s) => s.label)).toEqual([
      'Email confirmed',
      'Create passkey',
      'Done',
    ]);
  });
});
