import { describe, it, expect } from 'vitest';
import {
  componentToPreload,
  HOME_REDIRECT_DELAY_MS,
  shouldShowHome,
  redirectHost,
  registerOptionVisible,
  type HomeInfo,
} from './home';
import { loginPathWithRedirect } from './routing';

const closed: HomeInfo = { redirect_url: 'https://www.example.com/', registrations_enabled: false };

describe('shouldShowHome', () => {
  it('is true only for signed-out, root, closed enrollment, URL set', () => {
    expect(shouldShowHome(false, '/', closed)).toBe(true);
  });
  it('is false when signed in', () => {
    expect(shouldShowHome(true, '/', closed)).toBe(false);
  });
  it('is false off the root', () => {
    expect(shouldShowHome(false, '/login', closed)).toBe(false);
    expect(shouldShowHome(false, '/campaigns/x', closed)).toBe(false);
  });
  it('is false without info', () => {
    expect(shouldShowHome(false, '/', null)).toBe(false);
  });
  it('is false when no URL is set', () => {
    expect(shouldShowHome(false, '/', { ...closed, redirect_url: '' })).toBe(false);
  });
  it('is false when registration is open', () => {
    expect(shouldShowHome(false, '/', { ...closed, registrations_enabled: true })).toBe(false);
  });
});

describe('redirectHost', () => {
  it('returns the host', () => {
    expect(redirectHost('https://www.example.com/')).toBe('www.example.com');
  });
  it('keeps a port', () => {
    expect(redirectHost('https://example.org:8443/x')).toBe('example.org:8443');
  });
  it('falls back to the raw string', () => {
    expect(redirectHost('not a url')).toBe('not a url');
  });
});

describe('registerOptionVisible', () => {
  it('shows Register when enrollment is open', () => {
    expect(registerOptionVisible({ redirect_url: '', registrations_enabled: true })).toBe(true);
  });
  it('hides Register when enrollment is closed', () => {
    expect(registerOptionVisible(closed)).toBe(false);
    expect(registerOptionVisible({ redirect_url: '', registrations_enabled: false })).toBe(false);
  });
  it('hides Register when the fetch failed', () => {
    expect(registerOptionVisible(null)).toBe(false);
  });
});

describe('constants and routing', () => {
  it('delay is 3000 ms', () => {
    expect(HOME_REDIRECT_DELAY_MS).toBe(3000);
  });
  it('loginPathWithRedirect("/") is still /login', () => {
    expect(loginPathWithRedirect('/')).toBe('/login');
  });
});

describe('componentToPreload (#0186)', () => {
  const overview = { lazy: 'overview' };
  const route = { path: '/', component: overview };
  const showHome = { redirect_url: 'https://example.com', registrations_enabled: false } as HomeInfo;

  it('loads nothing when the public home will show, so signed-out visitors skip the Overview chunk', () => {
    expect(componentToPreload(false, route, showHome)).toBeNull();
  });
  it('loads the route component for a signed-in user, even if home info was fetched', () => {
    expect(componentToPreload(true, route, showHome)).toBe(overview);
  });
  it('loads the route component when there is no home info', () => {
    expect(componentToPreload(false, route, null)).toBe(overview);
  });
  it('loads the route component off the bare root', () => {
    const other = { path: '/links', component: { lazy: 'links' } };
    expect(componentToPreload(false, other, showHome)).toBe(other.component);
  });
});
