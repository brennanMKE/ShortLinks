// @vitest-environment jsdom
// Route-level code splitting (#0186): the route-to-view mapping, the
// once-per-URL stale-chunk recovery guard, and waiting for the first chunk.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Component } from 'svelte';
import { matchRoute } from './routing';
import { routes } from './routeTable';
import {
  CHUNK_RELOAD_KEY_PREFIX,
  VIEW_LOADERS,
  __resetForTest,
  cachedComponent,
  installChunkErrorRecovery,
  isRecoveryReloading,
  isLazyRef,
  lazy,
  loadComponent,
  preloadComponent,
  preloadForLocation,
  recoverFromChunkError,
  type Loader,
  type RecoveryDeps,
} from './routeViews';

function fakeStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

function depsFor(href: string, store: Storage | null = fakeStorage()) {
  const reload = vi.fn();
  const deps: RecoveryDeps = { href: () => href, getSession: () => store, reload };
  return { deps, reload, store };
}

const View = (() => {}) as unknown as Component<any>;
const ok: Loader = async () => ({ default: View });
const boom: Loader = () => Promise.reject(new TypeError('Failed to fetch dynamically imported module'));

beforeEach(() => __resetForTest());
afterEach(() => vi.restoreAllMocks());

describe('route to view mapping', () => {
  const lazyOf = (path: string) => {
    const c = matchRoute(path, routes)?.component;
    return isLazyRef(c) ? c.lazy : null;
  };

  it('maps every non-auth route to its own lazy view', () => {
    expect(lazyOf('/')).toBe('overview');
    expect(lazyOf('/links')).toBe('links');
    expect(lazyOf('/links/abc')).toBe('link-detail');
    expect(lazyOf('/new')).toBe('new-link');
    expect(lazyOf('/campaigns')).toBe('campaigns');
    expect(lazyOf('/campaigns/spring')).toBe('campaign-detail');
    expect(lazyOf('/account')).toBe('account');
    expect(lazyOf('/admin')).toBe('admin');
    expect(lazyOf('/admin/settings')).toBe('admin');
  });

  it('keeps sign-in and the not-found page eager', () => {
    expect(lazyOf('/login')).toBeNull();
    expect(matchRoute('/login', routes)?.component).toBeTruthy();
    expect(lazyOf('/no/such/page/here')).toBeNull();
    expect(matchRoute('/no/such/page/here', routes)?.component).toBeTruthy();
  });

  it('names a loader for every lazy key the route table uses', () => {
    for (const r of routes) {
      if (isLazyRef(r.component)) expect(VIEW_LOADERS[r.component.lazy]).toBeTypeOf('function');
    }
    expect(isLazyRef(lazy('admin'))).toBe(true);
    expect(isLazyRef({ lazy: 'nope' })).toBe(false);
    expect(isLazyRef(null)).toBe(false);
  });
});

describe('stale-chunk recovery guard', () => {
  it('reloads the same URL exactly once per URL', async () => {
    const { deps, reload } = depsFor('http://x/#/links');
    expect(await loadComponent('links', deps, boom)).toBeNull();
    expect(reload).toHaveBeenCalledTimes(1);
    __resetForTest(); // a fresh document after the reload; sessionStorage survives it
    expect(await loadComponent('links', deps, boom)).toBeNull();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('allows one reload for a different URL', async () => {
    const store = fakeStorage();
    const a = depsFor('http://x/#/links', store);
    const b = depsFor('http://x/#/account', store);
    await loadComponent('links', a.deps, boom);
    await loadComponent('account', b.deps, boom);
    expect(a.reload).toHaveBeenCalledTimes(1);
    expect(b.reload).toHaveBeenCalledTimes(1);
  });

  it('clears the guard after a successful load under the URL', async () => {
    const { deps, store } = depsFor('http://x/#/links');
    await loadComponent('links', deps, boom);
    expect(store!.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).not.toBeNull();
    __resetForTest();
    expect(await loadComponent('links', deps, ok)).toBe(View);
    expect(store!.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).toBeNull();
    expect(cachedComponent('links')).toBe(View);
  });

  it('does not clear the guard while another load under the URL is still in flight', async () => {
    const { deps, store } = depsFor('http://x/#/links');
    store!.setItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links', '1');
    let release: () => void = () => {};
    const slow: Loader = () => new Promise((r) => (release = () => r({ default: View })));
    const p1 = loadComponent('links', deps, slow);
    const p2 = loadComponent('account', deps, ok);
    await p2;
    expect(store!.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).not.toBeNull();
    release();
    await p1;
    expect(store!.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).toBeNull();
  });

  it('does not clear the guard when one load under the URL failed', async () => {
    const { deps, store, reload } = depsFor('http://x/#/links');
    await Promise.all([loadComponent('links', deps, ok), loadComponent('account', deps, boom)]);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(store!.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).not.toBeNull();
  });

  it('does not reload when session storage is unavailable', () => {
    const { deps, reload } = depsFor('http://x/#/links', null);
    expect(recoverFromChunkError(deps)).toBe(false);
    expect(reload).not.toHaveBeenCalled();
  });

  it('a failed import is not cached, so a later attempt can try again', async () => {
    const { deps } = depsFor('http://x/#/links');
    expect(await loadComponent('links', deps, boom)).toBeNull();
    expect(cachedComponent('links')).toBeNull();
    expect(await loadComponent('links', deps, ok)).toBe(View);
  });

  it('records and recovers against the URL the load started under', async () => {
    const store = fakeStorage();
    let href = 'http://x/#/links';
    const reload = vi.fn();
    const deps: RecoveryDeps = { href: () => href, getSession: () => store, reload };
    let fail: (e: Error) => void = () => {};
    const pending: Loader = () => new Promise((_, rej) => (fail = rej));
    const p = loadComponent('links', deps, pending);
    fail(new Error('x'));
    await p;
    expect(reload).toHaveBeenCalledTimes(1);
    expect(store.getItem(CHUNK_RELOAD_KEY_PREFIX + 'http://x/#/links')).not.toBeNull();
    expect(store.length).toBe(1);
    expect(isRecoveryReloading()).toBe(true);
  });

  it('a failure for a URL the user has already left does not reload the new URL', async () => {
    const store = fakeStorage();
    let href = 'http://x/#/links';
    const reload = vi.fn();
    const deps: RecoveryDeps = { href: () => href, getSession: () => store, reload };
    let fail: (e: Error) => void = () => {};
    const p = loadComponent('links', deps, () => new Promise((_, rej) => (fail = rej)));
    href = 'http://x/#/account'; // navigated away while the chunk was loading
    fail(new Error('x'));
    expect(await p).toBeNull();
    expect(reload).not.toHaveBeenCalled();
    expect(store.length).toBe(0);
    expect(isRecoveryReloading()).toBe(false);
  });

  it('vite:preloadError reloads once, and not twice when the import also rejects', async () => {
    const { deps, reload } = depsFor('http://x/#/campaigns');
    const off = installChunkErrorRecovery(window, deps);
    window.dispatchEvent(new Event('vite:preloadError'));
    expect(reload).toHaveBeenCalledTimes(1);
    await loadComponent('campaigns', deps, boom);
    window.dispatchEvent(new Event('vite:preloadError'));
    expect(reload).toHaveBeenCalledTimes(1);
    off();
    __resetForTest();
    const fresh = depsFor('http://x/#/other');
    window.dispatchEvent(new Event('vite:preloadError'));
    expect(fresh.reload).not.toHaveBeenCalled();
  });
});

describe('waiting for the first route', () => {
  it('preloadForLocation resolves only once the matched lazy view has loaded', async () => {
    let release: () => void = () => {};
    const spy = vi.spyOn(VIEW_LOADERS, 'campaigns').mockImplementation(
      () => new Promise((r) => (release = () => r({ default: View } as never))),
    );
    let done = false;
    const p = preloadForLocation(routes, { pathname: '/', hash: '#/campaigns' }).then(() => (done = true));
    await Promise.resolve();
    await Promise.resolve();
    expect(spy).toHaveBeenCalledTimes(1);
    expect(done).toBe(false);
    release();
    await p;
    expect(done).toBe(true);
    expect(cachedComponent('campaigns')).toBe(View);
  });

  it('loads nothing for an eager route, the bare root or an unknown page', async () => {
    const spies = Object.keys(VIEW_LOADERS).map((k) => vi.spyOn(VIEW_LOADERS, k as keyof typeof VIEW_LOADERS));
    await preloadForLocation(routes, { pathname: '/', hash: '#/login' });
    await preloadForLocation(routes, { pathname: '/', hash: '' });
    await preloadForLocation(routes, { pathname: '/', hash: '#/' });
    await preloadForLocation(routes, { pathname: '/', hash: '#/no/such/page/here' });
    expect(spies.every((s) => s.mock.calls.length === 0)).toBe(true);
  });

  it('loads the magic-link views by pathname', async () => {
    const spy = vi.spyOn(VIEW_LOADERS, 'register-verify').mockResolvedValue({ default: View } as never);
    await preloadForLocation(routes, { pathname: '/register/verify', hash: '' });
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it('preloadComponent ignores an eager component and never rejects on a failed chunk', async () => {
    await expect(preloadComponent(function Eager() {})).resolves.toBeUndefined();
    vi.spyOn(VIEW_LOADERS, 'account').mockImplementation(boom as never);
    const { deps } = depsFor('http://x/#/account');
    expect(await loadComponent('account', deps)).toBeNull();
    await expect(preloadComponent(lazy('account'))).resolves.toBeUndefined();
  });
});
