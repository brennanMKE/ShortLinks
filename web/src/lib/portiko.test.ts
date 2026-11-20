import { describe, it, expect } from 'vitest';
import {
  AUTO_REDIRECT_WINDOW_MS,
  isPortiko,
  mayAutoRedirect,
  portikoLoginURL,
  portikoSignUpVisible,
  returnToFromHash,
} from './portiko';
import { registerOptionVisible, type HomeInfo } from './home';

const info = (over: Partial<HomeInfo> = {}): HomeInfo => ({
  redirect_url: '',
  registrations_enabled: false,
  ...over,
});

describe('returnToFromHash', () => {
  it('keeps the hash route and its query', () => {
    expect(returnToFromHash('#/campaigns/x')).toBe('/#/campaigns/x');
    expect(returnToFromHash('#/links?range=7&utm_source=a%2Cb')).toBe(
      '/#/links?range=7&utm_source=a%2Cb',
    );
  });
  it('sends the bare root to /', () => {
    expect(returnToFromHash('')).toBe('/');
    expect(returnToFromHash('#/')).toBe('/');
  });
  it('on /login returns to its redirect target', () => {
    expect(returnToFromHash('#/login?redirect=%2Fcampaigns%2Fx')).toBe('/#/campaigns/x');
    expect(returnToFromHash('#/login')).toBe('/');
    expect(returnToFromHash('#/login?mode=register')).toBe('/');
  });
  it('never lets a login redirect leave the site', () => {
    expect(returnToFromHash('#/login?redirect=%2F%2Fevil.example')).toBe('/');
  });
});

describe('portikoLoginURL', () => {
  it('percent-encodes the fragment so it reaches the server intact', () => {
    const url = portikoLoginURL('#/campaigns/x?range=7');
    expect(url).toBe('/auth/portiko/login?return_to=%2F%23%2Fcampaigns%2Fx%3Frange%3D7');
    const back = new URL(url, 'http://h').searchParams.get('return_to');
    expect(back).toBe('/#/campaigns/x?range=7');
  });
  it('asks Portiko to open on account creation', () => {
    expect(portikoLoginURL('#/', { create: true })).toBe(
      '/auth/portiko/login?return_to=%2F&prompt=create',
    );
  });
});

describe('sign-up visibility', () => {
  it('shows only with Portiko on and enrollment open', () => {
    expect(portikoSignUpVisible(info({ portiko: true, registrations_enabled: true }))).toBe(true);
    expect(portikoSignUpVisible(info({ portiko: true, registrations_enabled: false }))).toBe(false);
    expect(portikoSignUpVisible(info({ portiko: false, registrations_enabled: true }))).toBe(false);
    expect(portikoSignUpVisible(null)).toBe(false);
  });
  it('follows the same rule as the passkey screens', () => {
    for (const reg of [true, false]) {
      const i = info({ portiko: true, registrations_enabled: reg });
      expect(portikoSignUpVisible(i)).toBe(registerOptionVisible(i));
    }
  });
});

describe('isPortiko', () => {
  it('reads absent and null as off', () => {
    expect(isPortiko(info())).toBe(false);
    expect(isPortiko(null)).toBe(false);
    expect(isPortiko(info({ portiko: true }))).toBe(true);
  });
});

describe('mayAutoRedirect (loop guard)', () => {
  it('allows the first redirect and one after the window', () => {
    expect(mayAutoRedirect(null, 1000)).toBe(true);
    expect(mayAutoRedirect(1000, 1000 + AUTO_REDIRECT_WINDOW_MS)).toBe(true);
  });
  it('refuses a second redirect right after the first', () => {
    expect(mayAutoRedirect(1000, 2000)).toBe(false);
  });
});
