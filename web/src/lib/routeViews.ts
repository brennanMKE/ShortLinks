// Route-level code splitting (#0186). Every lazily loaded view goes through
// this module, so there is one cache, one recovery path and one place to test.
//
// Sign-in (Login), the public home and NotFound stay in the entry chunk: they
// are what a signed-out visitor sees first. Every other view is its own chunk,
// fetched when its route is first shown.
//
// Recovery from a missing chunk is a full reload of the same URL, not a retry:
// a browser caches a failed module fetch for the life of the document, so
// re-importing the same URL fails again. The usual cause is a tab left open
// across a deploy that asks for a chunk hash the new release no longer has (the
// server answers 404, internal/handlers/static.go). The reload is
// reloadToSameURL from #0185, the same one the version check uses, and a
// sessionStorage key per URL makes it happen at most once per URL, so a chunk
// that is genuinely broken cannot loop.
//
// The guard for a URL is cleared only once every load started under it has
// settled with no failure, so a view that loads while a second chunk of the
// same URL still fails cannot reload forever.
import type { Component } from 'svelte';
import { parseHash, matchRoute, type RouteDef } from './routing';
import { reloadToSameURL } from './versionCheck';

export type ViewModule = { default: Component<any> };
export type Loader = () => Promise<ViewModule>;

export const VIEW_LOADERS = {
  overview: () => import('../views/Overview.svelte'),
  links: () => import('../views/LinksList.svelte'),
  'link-detail': () => import('../views/LinkDetail.svelte'),
  'new-link': () => import('../views/NewLink.svelte'),
  campaigns: () => import('../views/CampaignsList.svelte'),
  'campaign-detail': () => import('../views/CampaignDetail.svelte'),
  account: () => import('../views/Account.svelte'),
  admin: () => import('../views/Admin.svelte'),
  'register-verify': () => import('../views/RegisterVerify.svelte'),
  'recover-verify': () => import('../views/RecoverVerify.svelte'),
} as const satisfies Record<string, Loader>;

export type ViewKey = keyof typeof VIEW_LOADERS;

/** A route-table entry that names a lazy view instead of holding a component. */
export interface LazyRef {
  lazy: ViewKey;
}

export const lazy = (key: ViewKey): LazyRef => ({ lazy: key });

export function isLazyRef(x: unknown): x is LazyRef {
  return typeof x === 'object' && x !== null && typeof (x as LazyRef).lazy === 'string' && (x as LazyRef).lazy in VIEW_LOADERS;
}

export const CHUNK_RELOAD_KEY_PREFIX = 'sl.chunkReload:';

export interface RecoveryDeps {
  href(): string;
  getSession(): Storage | null;
  reload(): void;
}

export const defaultDeps: RecoveryDeps = {
  href: () => location.href,
  getSession: () => sessionStorage,
  reload: () => reloadToSameURL(),
};

const cache = new Map<string, Component<any>>();
const inflight = new Map<string, Promise<ViewModule>>();
const pendingByHref = new Map<string, number>();
const failedHrefs = new Set<string>();
let reloadStarted = false;

/** True once a stale-chunk reload has been started: the page is about to be replaced, so callers should not mount into it. */
export function isRecoveryReloading(): boolean {
  return reloadStarted;
}

function importOnce(key: string, loader?: Loader): Promise<ViewModule> {
  const existing = inflight.get(key);
  if (existing) return existing;
  const load = loader ?? (VIEW_LOADERS as Record<string, Loader>)[key];
  if (!load) return Promise.reject(new Error(`no lazy loader for ${key}`));
  const p = load().catch((err) => {
    inflight.delete(key); // a later attempt may try again
    throw err;
  });
  inflight.set(key, p);
  return p;
}

export function cachedComponent(key: string): Component<any> | null {
  return cache.get(key) ?? null;
}

/** Loads a view; on failure starts the once-per-URL reload and resolves null. */
export function loadComponent(
  key: string,
  deps: RecoveryDeps = defaultDeps,
  loader?: Loader,
): Promise<Component<any> | null> {
  const hit = cache.get(key);
  if (hit) return Promise.resolve(hit);
  const href = deps.href();
  pendingByHref.set(href, (pendingByHref.get(href) ?? 0) + 1);
  const settle = (): boolean => {
    const left = (pendingByHref.get(href) ?? 1) - 1;
    if (left > 0) pendingByHref.set(href, left);
    else pendingByHref.delete(href);
    return left <= 0;
  };
  return importOnce(key, loader).then(
    (mod) => {
      cache.set(key, mod.default);
      if (settle() && !failedHrefs.has(href)) clearChunkRecovery(deps, href);
      return mod.default;
    },
    () => {
      settle();
      failedHrefs.add(href);
      // The guard key and the reload both belong to the URL this load started
      // under. If the user has since moved on, that view is gone and reloading
      // the new URL would be wrong, so the failure is only recorded.
      if (deps.href() === href) recoverFromChunkError(deps, href);
      return null;
    },
  );
}

/** Reloads the same URL once; false when already tried or storage is unavailable. */
export function recoverFromChunkError(deps: RecoveryDeps = defaultDeps, href: string = deps.href()): boolean {
  try {
    const store = deps.getSession();
    if (!store) return false;
    const k = CHUNK_RELOAD_KEY_PREFIX + href;
    if (store.getItem(k) !== null) return false;
    store.setItem(k, String(Date.now()));
  } catch {
    return false;
  }
  reloadStarted = true;
  deps.reload();
  return true;
}

export function clearChunkRecovery(deps: RecoveryDeps = defaultDeps, href = deps.href()): void {
  try {
    deps.getSession()?.removeItem(CHUNK_RELOAD_KEY_PREFIX + href);
  } catch {
    // storage unavailable: nothing to clear
  }
}

/**
 * Listens for Vite's own signal that a dynamic import's preload failed
 * (`vite:preloadError`), so a stale-chunk failure that does not pass through
 * loadComponent still reloads. The once-per-URL guard makes it harmless when
 * both paths fire for one failure. It deliberately does not preventDefault:
 * the import must still reject, so loadComponent's own handler runs.
 */
export function installChunkErrorRecovery(
  win: Pick<Window, 'addEventListener' | 'removeEventListener'> = window,
  deps: RecoveryDeps = defaultDeps,
): () => void {
  const onError = () => {
    recoverFromChunkError(deps);
  };
  win.addEventListener('vite:preloadError', onError);
  return () => win.removeEventListener('vite:preloadError', onError);
}

/** The lazy view a route-table component names, or null for an eager one. */
export function lazyKeyOf(component: unknown): ViewKey | null {
  return isLazyRef(component) ? component.lazy : null;
}

/** Loads the view for a matched route entry; never rejects. */
export function preloadComponent(component: unknown): Promise<void> {
  const key = lazyKeyOf(component);
  return key ? loadComponent(key).then(() => undefined) : Promise.resolve();
}

/**
 * The lazy view the document's own location will show first, for main.ts to
 * wait for before mounting. A magic-link landing path is matched by pathname;
 * everything else by hash route.
 *
 * The bare root is skipped on purpose: it is Overview for a signed-in user but
 * the public home or sign-in for a signed-out one, which is not known until
 * /api/me answers. Loading Overview for every anonymous visitor would undo the
 * split for them; App waits for the real target once auth is known (enterApp).
 */
export function preloadForLocation<T>(
  routes: RouteDef<T>[],
  loc: Pick<Location, 'pathname' | 'hash'> = location,
): Promise<void> {
  if (loc.pathname === '/register/verify') return preloadComponent(lazy('register-verify'));
  if (loc.pathname === '/recover/verify') return preloadComponent(lazy('recover-verify'));
  const { path } = parseHash(loc.hash);
  if (path === '/') return Promise.resolve();
  return preloadComponent(matchRoute(path, routes)?.component);
}

/** Test-only: forget every cached and in-flight import. */
export function __resetForTest(): void {
  cache.clear();
  inflight.clear();
  pendingByHref.clear();
  failedHrefs.clear();
  reloadStarted = false;
}
