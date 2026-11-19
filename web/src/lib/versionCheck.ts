// versionCheck — notice that a newer build was deployed while this tab sat
// open, and move onto it (#0185).
//
// The server stamps its build id into <meta name="sl-build"> and answers
// GET /api/version with the same id. When the page becomes visible (or is
// restored from the back/forward cache), at most once an hour, the two are
// compared; on a mismatch the tab reloads in place, or — when work is
// unsaved — the caller is asked to show a "Reload" notice instead.
//
// The newest build a server response reported is shared through localStorage
// (#0185), so a second stale tab reloads on focus without waiting out the hour.
//
// No service worker, no polling interval, no external request (PRD §7.6).

import { hasUnsavedInput } from './unsavedWork';

export const CHECK_INTERVAL_MS = 3_600_000;
export const RETRY_AFTER_FAILURE_MS = 300_000;
export const LAST_CHECK_KEY = 'sl.versionCheck.lastAt';
export const SEEN_BUILD_KEY = 'sl.versionCheck.seenBuild';
export const RELOAD_SCROLL_KEY = 'sl.reload.scroll';

export function readRunningBuild(doc: Pick<Document, 'querySelector'> = document): string | null {
  const meta = doc.querySelector('meta[name="sl-build"]');
  const content = meta?.getAttribute('content') ?? '';
  // An unsubstituted token (Vite dev) means "unknown": the feature is off.
  if (content === '' || content.startsWith('%%')) return null;
  return content;
}

export interface ThrottleStore {
  get(): number | null;
  set(ms: number): void;
}

export function createThrottleStore(
  getStorage: () => Storage | null = () => window.localStorage,
): ThrottleStore {
  let memory: number | null = null;
  return {
    get() {
      try {
        const raw = getStorage()?.getItem(LAST_CHECK_KEY);
        if (raw != null) {
          const n = Number(raw);
          if (Number.isFinite(n)) return n;
        }
      } catch {
        // storage blocked: fall through to memory
      }
      return memory;
    },
    set(ms: number) {
      memory = ms;
      try {
        getStorage()?.setItem(LAST_CHECK_KEY, String(ms));
      } catch {
        // storage blocked: memory still holds it
      }
    },
  };
}

export interface SeenBuild {
  build: string;
  at: number;
}

export interface SeenBuildStore {
  get(): SeenBuild | null;
  set(v: SeenBuild): void;
}

export function createSeenBuildStore(
  getStorage: () => Storage | null = () => window.localStorage,
): SeenBuildStore {
  let memory: SeenBuild | null = null;
  return {
    get() {
      try {
        const raw = getStorage()?.getItem(SEEN_BUILD_KEY);
        if (raw != null) {
          const v = JSON.parse(raw) as { build?: unknown; at?: unknown } | null;
          if (v && typeof v.build === 'string' && v.build !== '' && typeof v.at === 'number' && Number.isFinite(v.at)) {
            return { build: v.build, at: v.at };
          }
        }
      } catch {
        // storage blocked or malformed: fall through to memory
      }
      return memory;
    },
    set(v: SeenBuild) {
      memory = v;
      try {
        getStorage()?.setItem(SEEN_BUILD_KEY, JSON.stringify({ build: v.build, at: v.at }));
      } catch {
        // storage blocked: memory still holds it
      }
    },
  };
}

/** A build other than ours, reported by the server after this tab loaded. */
export function seenNewerBuild(running: string, seen: SeenBuild | null, loadedAt: number, now: number): boolean {
  return seen !== null && seen.build !== running && seen.at > loadedAt && seen.at <= now;
}

export function shouldCheck(now: number, last: number | null): boolean {
  if (last === null) return true;
  if (last > now) return true; // clock moved backward
  return now - last >= CHECK_INTERVAL_MS;
}

export async function fetchServerBuild(fetchFn: typeof fetch = fetch): Promise<string | null> {
  try {
    const res = await fetchFn('/api/version', {
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
    if (!res.ok) return null;
    const body = (await res.json()) as { build?: unknown };
    return typeof body?.build === 'string' && body.build !== '' ? body.build : null;
  } catch {
    return null;
  }
}

export type Decision = 'skip' | 'same' | 'reload' | 'notify' | 'failed';

export interface CheckDeps {
  now(): number;
  store: ThrottleStore;
  runningBuild: string | null;
  fetchBuild(): Promise<string | null>;
  hasUnsavedWork(): boolean;
  seen: SeenBuildStore;
  /** ms timestamp at which this page instance started. */
  loadedAt: number;
}

export async function checkForNewVersion(deps: CheckDeps): Promise<Decision> {
  if (deps.runningBuild === null) return 'skip';
  const now = deps.now();
  if (seenNewerBuild(deps.runningBuild, deps.seen.get(), deps.loadedAt, now)) {
    return deps.hasUnsavedWork() ? 'notify' : 'reload'; // no fetch, no throttle write
  }
  if (!shouldCheck(now, deps.store.get())) return 'skip';
  deps.store.set(now); // before the fetch, so two near-simultaneous events cannot both fetch
  const server = await deps.fetchBuild();
  if (server === null) {
    // Allow the next try in five minutes rather than a full hour.
    deps.store.set(now - CHECK_INTERVAL_MS + RETRY_AFTER_FAILURE_MS);
    return 'failed';
  }
  deps.seen.set({ build: server, at: now }); // only ever a server's answer
  if (server === deps.runningBuild) return 'same';
  return deps.hasUnsavedWork() ? 'notify' : 'reload';
}

/**
 * Reload this tab onto the newest build, on the same page.
 *
 * Calls location.reload() — never assign/replace/href — so path, query and
 * hash (admin `#/…` routes included) are kept; the scroll offset is saved for
 * restoreScrollAfterReload. This is the reload #0186 reuses for stale-chunk
 * recovery.
 */
export function reloadToSameURL(
  loc: Pick<Location, 'href' | 'reload'> = window.location,
  getSession: () => Storage | null = () => window.sessionStorage,
  scrollY: number = window.scrollY,
): void {
  try {
    getSession()?.setItem(RELOAD_SCROLL_KEY, JSON.stringify({ href: loc.href, y: scrollY }));
  } catch {
    // best effort
  }
  loc.reload();
}

export function restoreScrollAfterReload(
  loc: Pick<Location, 'href'> = window.location,
  getSession: () => Storage | null = () => window.sessionStorage,
  scrollTo: (y: number) => void = (y) => window.scrollTo(0, y),
  raf: (cb: () => void) => unknown = (cb) => requestAnimationFrame(cb),
): void {
  let saved: { href?: unknown; y?: unknown } | null = null;
  try {
    const s = getSession();
    const raw = s?.getItem(RELOAD_SCROLL_KEY);
    if (raw == null) return;
    s?.removeItem(RELOAD_SCROLL_KEY);
    saved = JSON.parse(raw);
  } catch {
    return;
  }
  const y = saved?.y;
  if (saved?.href === loc.href && typeof y === 'number' && y > 0) raf(() => scrollTo(y));
}

export interface WatchOptions {
  onNotify(): void;
  win?: EventTarget;
  doc?: EventTarget & { visibilityState: string };
  deps?: Partial<CheckDeps>;
  reload?: () => void;
}

/** Wire the check to visibility/pageshow; returns a cleanup function. */
export function startVersionWatch(opts: WatchOptions): () => void {
  const win = opts.win ?? window;
  const doc = opts.doc ?? document;
  const reload = opts.reload ?? (() => reloadToSameURL());
  const deps: CheckDeps = {
    now: () => Date.now(),
    store: opts.deps?.store ?? createThrottleStore(),
    runningBuild: opts.deps?.runningBuild !== undefined ? opts.deps.runningBuild : readRunningBuild(),
    fetchBuild: () => fetchServerBuild(),
    hasUnsavedWork: () => hasUnsavedInput(),
    seen: opts.deps?.seen ?? createSeenBuildStore(),
    loadedAt: Date.now(),
    ...opts.deps,
  };
  let inFlight = false;
  // After a "Reload" notice, the next navigation reloads if the work that held
  // the reload back is gone by then (#0185).
  let awaitingNavigation = false;
  const run = async () => {
    if (inFlight) return;
    inFlight = true;
    try {
      const d = await checkForNewVersion(deps);
      if (d === 'reload') reload();
      else if (d === 'notify') {
        opts.onNotify();
        awaitingNavigation = true;
      }
    } finally {
      inFlight = false;
    }
  };
  const onVisibility = () => {
    if (doc.visibilityState === 'visible') void run();
  };
  const onPageShow = (e: Event) => {
    if ((e as PageTransitionEvent).persisted) void run();
  };
  const onNavigate = () => {
    if (!awaitingNavigation) return;
    // Let the router swap the view first: the form that held the reload back is
    // still in the DOM while the hashchange event itself is being dispatched.
    setTimeout(() => {
      if (awaitingNavigation && !deps.hasUnsavedWork()) {
        awaitingNavigation = false;
        reload();
      }
    }, 0);
  };
  doc.addEventListener('visibilitychange', onVisibility);
  win.addEventListener('pageshow', onPageShow);
  win.addEventListener('hashchange', onNavigate);
  return () => {
    win.removeEventListener('hashchange', onNavigate);
    doc.removeEventListener('visibilitychange', onVisibility);
    win.removeEventListener('pageshow', onPageShow);
  };
}
