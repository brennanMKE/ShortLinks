import { describe, expect, it, vi } from 'vitest';
import {
  CHECK_INTERVAL_MS,
  LAST_CHECK_KEY,
  RELOAD_SCROLL_KEY,
  RETRY_AFTER_FAILURE_MS,
  SEEN_BUILD_KEY,
  checkForNewVersion,
  createSeenBuildStore,
  createThrottleStore,
  fetchServerBuild,
  readRunningBuild,
  reloadToSameURL,
  restoreScrollAfterReload,
  seenNewerBuild,
  shouldCheck,
  startVersionWatch,
  type CheckDeps,
  type SeenBuild,
  type SeenBuildStore,
  type ThrottleStore,
} from './versionCheck';

function fakeStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; },
    clear: () => m.clear(),
    getItem: (k) => (m.has(k) ? m.get(k)! : null),
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}
const throwingStorage = (): Storage => { throw new Error('blocked'); };
const docWithMeta = (content: string | null) => ({
  querySelector: () => (content === null ? null : { getAttribute: () => content }),
}) as unknown as Pick<Document, 'querySelector'>;
const memStore = (v: number | null = null): ThrottleStore => {
  let x = v;
  return { get: () => x, set: (n) => void (x = n) };
};
const memSeen = (v: SeenBuild | null = null): SeenBuildStore => {
  let x = v;
  return { get: () => x, set: (n) => void (x = n) };
};
const deps = (over: Partial<CheckDeps> = {}): CheckDeps => ({
  now: () => 10 * CHECK_INTERVAL_MS,
  store: memStore(),
  runningBuild: 'a+1',
  fetchBuild: async () => 'a+1',
  hasUnsavedWork: () => false,
  seen: memSeen(),
  loadedAt: 0,
  ...over,
});
const resp = (ok: boolean, json: () => Promise<unknown>) => ({ ok, json }) as unknown as Response;

describe('versionCheck', () => {
  it('readRunningBuild returns the meta content', () => {
    expect(readRunningBuild(docWithMeta('0.16.2+abc'))).toBe('0.16.2+abc');
  });

  it('readRunningBuild returns null for a missing, empty, or unsubstituted %%SL_ value', () => {
    expect(readRunningBuild(docWithMeta(null))).toBeNull();
    expect(readRunningBuild(docWithMeta(''))).toBeNull();
    expect(readRunningBuild(docWithMeta('%%SL_BUILD%%'))).toBeNull();
  });

  it('shouldCheck allows the first check and then one per hour', () => {
    expect(shouldCheck(1000, null)).toBe(true);
    expect(shouldCheck(1000 + CHECK_INTERVAL_MS - 1, 1000)).toBe(false);
    expect(shouldCheck(1000 + CHECK_INTERVAL_MS, 1000)).toBe(true);
  });

  it('shouldCheck allows a check when the stored time is in the future', () => {
    expect(shouldCheck(1000, 5000)).toBe(true);
  });

  it('throttle store persists across store instances (a tab hidden overnight)', () => {
    const s = fakeStorage();
    createThrottleStore(() => s).set(1234);
    expect(s.getItem(LAST_CHECK_KEY)).toBe('1234');
    expect(createThrottleStore(() => s).get()).toBe(1234);
  });

  it('throttle store falls back to memory when storage throws', () => {
    const st = createThrottleStore(throwingStorage);
    expect(st.get()).toBeNull();
    st.set(99);
    expect(st.get()).toBe(99);
  });

  it('checkForNewVersion skips without fetching inside the hour', async () => {
    const fetchBuild = vi.fn(async () => 'b');
    const now = 10 * CHECK_INTERVAL_MS;
    expect(await checkForNewVersion(deps({ fetchBuild, store: memStore(now - 1000) }))).toBe('skip');
    expect(fetchBuild).not.toHaveBeenCalled();
  });

  it('checkForNewVersion skips without fetching when the running build is unknown', async () => {
    const fetchBuild = vi.fn(async () => 'b');
    expect(await checkForNewVersion(deps({ fetchBuild, runningBuild: null }))).toBe('skip');
    expect(fetchBuild).not.toHaveBeenCalled();
  });

  it('checkForNewVersion returns same when builds match', async () => {
    expect(await checkForNewVersion(deps())).toBe('same');
  });

  it('a completed check starts the hour, so the next one inside it skips without fetching', async () => {
    // Review of #0185: without this, dropping the pre-fetch store.set(now)
    // left every test green while every focus fetched. It also covers the
    // reload case, so a reload onto a stale-cached shell cannot loop.
    for (const server of ['a+1', 'b+2']) {
      const store = memStore();
      let now = 10 * CHECK_INTERVAL_MS;
      let loadedAt = 0;
      const fetchBuild = vi.fn(async () => server);
      const d = { now: () => now, store, fetchBuild, seen: memSeen() };
      expect(await checkForNewVersion(deps({ ...d, loadedAt }))).toBe(server === 'a+1' ? 'same' : 'reload');
      loadedAt = now + 1; // the tab reloaded, still on a stale shell
      now += CHECK_INTERVAL_MS - 1;
      expect(await checkForNewVersion(deps({ ...d, loadedAt }))).toBe('skip');
      expect(fetchBuild).toHaveBeenCalledTimes(1);
      now += 1;
      await checkForNewVersion(deps({ ...d, loadedAt }));
      expect(fetchBuild).toHaveBeenCalledTimes(2);
    }
  });

  it('checkForNewVersion returns reload when builds differ and nothing is unsaved', async () => {
    expect(await checkForNewVersion(deps({ fetchBuild: async () => 'b+2' }))).toBe('reload');
  });

  it('checkForNewVersion returns notify when builds differ and work is unsaved', async () => {
    expect(await checkForNewVersion(deps({ fetchBuild: async () => 'b+2', hasUnsavedWork: () => true }))).toBe('notify');
  });

  it('a failed check returns failed and allows a retry after five minutes, not an hour', async () => {
    const store = memStore();
    let now = 10 * CHECK_INTERVAL_MS;
    const d = deps({ store, now: () => now, fetchBuild: async () => null });
    expect(await checkForNewVersion(d)).toBe('failed');
    now += RETRY_AFTER_FAILURE_MS - 1;
    expect(shouldCheck(now, store.get())).toBe(false);
    now += 1;
    expect(shouldCheck(now, store.get())).toBe(true);
  });

  it('seen-build store persists across instances and ignores malformed values', () => {
    const s = fakeStorage();
    createSeenBuildStore(() => s).set({ build: 'n+2', at: 5 });
    expect(s.getItem(SEEN_BUILD_KEY)).toBe('{"build":"n+2","at":5}');
    expect(createSeenBuildStore(() => s).get()).toEqual({ build: 'n+2', at: 5 });
    for (const bad of ['not json', '{"build":3,"at":1}', '{"build":"x"}', '{"build":"","at":1}']) {
      s.setItem(SEEN_BUILD_KEY, bad);
      expect(createSeenBuildStore(() => s).get()).toBeNull();
    }
  });

  it('seen-build store falls back to memory when storage throws', () => {
    const st = createSeenBuildStore(throwingStorage);
    expect(st.get()).toBeNull();
    st.set({ build: 'n+2', at: 7 });
    expect(st.get()).toEqual({ build: 'n+2', at: 7 });
  });

  it('a successful check records the server build; a failed check does not', async () => {
    const now = 10 * CHECK_INTERVAL_MS;
    for (const server of ['a+1', 'b+2']) {
      const seen = memSeen();
      await checkForNewVersion(deps({ seen, fetchBuild: async () => server }));
      expect(seen.get()).toEqual({ build: server, at: now });
    }
    const seen = memSeen();
    expect(await checkForNewVersion(deps({ seen, fetchBuild: async () => null }))).toBe('failed');
    expect(seen.get()).toBeNull();
  });

  const tab = (s: Storage, runningBuild: string, loadedAt: number, now: number, fetchBuild: () => Promise<string | null>, unsaved = false) =>
    deps({
      now: () => now, runningBuild, loadedAt, fetchBuild, hasUnsavedWork: () => unsaved,
      store: createThrottleStore(() => s), seen: createSeenBuildStore(() => s),
    });

  it('a second stale tab reloads on focus from the build the first tab saw, without fetching', async () => {
    const s = fakeStorage();
    const T = 10 * CHECK_INTERVAL_MS;
    expect(await checkForNewVersion(tab(s, 'old+1', 0, T, async () => 'new+2'))).toBe('reload');
    const fetchB = vi.fn(async () => 'new+2');
    expect(await checkForNewVersion(tab(s, 'old+1', 0, T + 60_000, fetchB))).toBe('reload');
    expect(await checkForNewVersion(tab(s, 'old+1', 0, T + 60_000, fetchB, true))).toBe('notify');
    expect(fetchB).not.toHaveBeenCalled();
    expect(s.getItem(LAST_CHECK_KEY)).toBe(String(T)); // the seen path does not touch the throttle
  });

  it('a tab already on the seen build still obeys the hour throttle', async () => {
    const s = fakeStorage();
    const T = 10 * CHECK_INTERVAL_MS;
    await checkForNewVersion(tab(s, 'old+1', 0, T, async () => 'new+2'));
    const fetchC = vi.fn(async () => 'new+2');
    expect(await checkForNewVersion(tab(s, 'new+2', 0, T + 60_000, fetchC))).toBe('skip');
    expect(fetchC).not.toHaveBeenCalled();
  });

  it('a seen build recorded before this tab loaded is ignored', async () => {
    const fetchBuild = vi.fn(async () => 'new+2');
    const now = 10 * CHECK_INTERVAL_MS;
    expect(await checkForNewVersion(deps({
      runningBuild: 'new+2', loadedAt: 200, seen: memSeen({ build: 'old+1', at: 100 }),
      store: memStore(now - 1000), fetchBuild,
    }))).toBe('skip');
    expect(fetchBuild).not.toHaveBeenCalled();
  });

  it('a seen build in the future is ignored', () => {
    expect(seenNewerBuild('a', { build: 'b', at: 2000 }, 0, 1000)).toBe(false);
    expect(seenNewerBuild('a', { build: 'b', at: 500 }, 0, 1000)).toBe(true);
    expect(seenNewerBuild('a', { build: 'a', at: 500 }, 0, 1000)).toBe(false);
  });

  it('an old tab and a new tab sharing storage do not ping-pong', async () => {
    const s = fakeStorage();
    const T = 10 * CHECK_INTERVAL_MS;
    expect(await checkForNewVersion(tab(s, 'new+2', 0, T, async () => 'new+2'))).toBe('same');
    expect(await checkForNewVersion(tab(s, 'old+1', 0, T + 1000, async () => 'new+2'))).toBe('reload');
    const oLoadedAt = T + 2000; // O reloaded onto a stale shell
    const before = s.getItem(SEEN_BUILD_KEY);
    const fetchN = vi.fn(async () => 'new+2');
    const fetchO = vi.fn(async () => 'new+2');
    for (let i = 3; i <= 10; i++) {
      const at = T + i * 1000;
      const d = i % 2 ? tab(s, 'new+2', 0, at, fetchN) : tab(s, 'old+1', oLoadedAt, at, fetchO);
      expect(await checkForNewVersion(d)).toBe('skip');
    }
    expect(fetchN).not.toHaveBeenCalled();
    expect(fetchO).not.toHaveBeenCalled();
    expect(s.getItem(SEEN_BUILD_KEY)).toBe(before);
  });

  it('fetchServerBuild requests /api/version with no-store and returns build', async () => {
    const f = vi.fn(async () => resp(true, async () => ({ build: 'x+y' })));
    expect(await fetchServerBuild(f as unknown as typeof fetch)).toBe('x+y');
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/version');
    expect(init.cache).toBe('no-store');
  });

  it('fetchServerBuild returns null on non-OK, bad JSON, missing build, or a thrown error', async () => {
    const cases = [
      async () => resp(false, async () => ({ build: 'x' })),
      async () => resp(true, async () => { throw new SyntaxError('bad'); }),
      async () => resp(true, async () => ({})),
      async () => { throw new TypeError('offline'); },
    ];
    for (const c of cases) expect(await fetchServerBuild(c as unknown as typeof fetch)).toBeNull();
  });

  it('reloadToSameURL calls reload and never assign or replace, and saves scroll', () => {
    const loc = { href: 'https://x/admin#/workshops/3', reload: vi.fn(), assign: vi.fn(), replace: vi.fn() };
    const s = fakeStorage();
    reloadToSameURL(loc, () => s, 420);
    expect(loc.reload).toHaveBeenCalledOnce();
    expect(loc.assign).not.toHaveBeenCalled();
    expect(loc.replace).not.toHaveBeenCalled();
    expect(JSON.parse(s.getItem(RELOAD_SCROLL_KEY)!)).toEqual({ href: loc.href, y: 420 });
  });

  it('restoreScrollAfterReload restores only for the same href and clears the key', () => {
    const s = fakeStorage();
    const scrollTo = vi.fn();
    const raf = (cb: () => void) => cb();
    s.setItem(RELOAD_SCROLL_KEY, JSON.stringify({ href: 'https://x/a', y: 300 }));
    restoreScrollAfterReload({ href: 'https://x/b' }, () => s, scrollTo, raf);
    expect(scrollTo).not.toHaveBeenCalled();
    expect(s.getItem(RELOAD_SCROLL_KEY)).toBeNull();
    s.setItem(RELOAD_SCROLL_KEY, JSON.stringify({ href: 'https://x/a', y: 300 }));
    restoreScrollAfterReload({ href: 'https://x/a' }, () => s, scrollTo, raf);
    expect(scrollTo).toHaveBeenCalledWith(300);
    expect(s.getItem(RELOAD_SCROLL_KEY)).toBeNull();
  });

  function watchHarness(over: Partial<CheckDeps> = {}) {
    const win = new EventTarget();
    const doc = Object.assign(new EventTarget(), { visibilityState: 'hidden' });
    const reload = vi.fn();
    const onNotify = vi.fn();
    const fetchBuild = vi.fn(async () => 'new+2');
    const stop = startVersionWatch({
      onNotify, win, doc, reload,
      deps: { runningBuild: 'old+1', store: memStore(), fetchBuild, hasUnsavedWork: () => false, seen: memSeen(), loadedAt: 0, ...over },
    });
    const flush = () => new Promise((r) => setTimeout(r, 0));
    return { win, doc, reload, onNotify, fetchBuild, stop, flush };
  }
  const pageshow = (persisted: boolean) => Object.assign(new Event('pageshow'), { persisted });

  it('startVersionWatch checks on visible visibilitychange and persisted pageshow only', async () => {
    let h = watchHarness();
    h.doc.dispatchEvent(new Event('visibilitychange')); // still hidden
    h.win.dispatchEvent(pageshow(false));
    await h.flush();
    expect(h.fetchBuild).not.toHaveBeenCalled();
    h.doc.visibilityState = 'visible';
    h.doc.dispatchEvent(new Event('visibilitychange'));
    await h.flush();
    expect(h.fetchBuild).toHaveBeenCalledOnce();
    expect(h.reload).toHaveBeenCalledOnce();
    h.stop();

    h = watchHarness();
    h.win.dispatchEvent(pageshow(true));
    await h.flush();
    expect(h.fetchBuild).toHaveBeenCalledOnce();
    h.stop();
  });

  it('startVersionWatch does not check on start', async () => {
    const h = watchHarness();
    await h.flush();
    expect(h.fetchBuild).not.toHaveBeenCalled();
    h.stop();
  });

  it('startVersionWatch ignores events while a check is in flight', async () => {
    let release: (v: string) => void = () => {};
    const fetchBuild = vi.fn(() => new Promise<string>((r) => (release = r)));
    // A store that never throttles, so only the in-flight guard can stop a second fetch.
    const h = watchHarness({ fetchBuild, store: { get: () => null, set: () => {} } });
    h.win.dispatchEvent(pageshow(true));
    h.win.dispatchEvent(pageshow(true));
    await h.flush();
    expect(fetchBuild).toHaveBeenCalledOnce();
    release('old+1');
    await h.flush();
    h.stop();
  });

  it('startVersionWatch calls onNotify instead of reloading when work is unsaved', async () => {
    const h = watchHarness({ hasUnsavedWork: () => true });
    h.win.dispatchEvent(pageshow(true));
    await h.flush();
    expect(h.onNotify).toHaveBeenCalledOnce();
    expect(h.reload).not.toHaveBeenCalled();
    h.stop();
  });

  it('startVersionWatch reloads on focus from a newer seen build without fetching', async () => {
    const h = watchHarness({ seen: memSeen({ build: 'new+2', at: 5 }), loadedAt: 0, store: memStore(Date.now()) });
    await h.flush();
    expect(h.reload).not.toHaveBeenCalled();
    expect(h.fetchBuild).not.toHaveBeenCalled();
    h.doc.visibilityState = 'visible';
    h.doc.dispatchEvent(new Event('visibilitychange'));
    await h.flush();
    expect(h.reload).toHaveBeenCalledOnce();
    expect(h.fetchBuild).not.toHaveBeenCalled();
    h.stop();
  });

  it('startVersionWatch defaults loadedAt to its start, so a build seen before this page loaded is ignored', async () => {
    const reload = vi.fn();
    const fetchBuild = vi.fn(async () => 'new+2');
    const doc = Object.assign(new EventTarget(), { visibilityState: 'hidden' });
    const stop = startVersionWatch({
      onNotify: vi.fn(), win: new EventTarget(), doc, reload,
      // No loadedAt: the default must be "now", not 0. The seen record predates this page,
      // as after a reload onto a stale cached shell.
      deps: { runningBuild: 'old+1', store: memStore(Date.now()), fetchBuild, hasUnsavedWork: () => false,
        seen: memSeen({ build: 'new+2', at: Date.now() - 1000 }) },
    });
    doc.visibilityState = 'visible';
    doc.dispatchEvent(new Event('visibilitychange'));
    await new Promise((r) => setTimeout(r, 0));
    expect(reload).not.toHaveBeenCalled();
    expect(fetchBuild).not.toHaveBeenCalled();
    stop();
  });

  it('after a notice, the next navigation reloads once the unsaved work is gone (#0185)', async () => {
    let unsaved = true;
    const h = watchHarness({ hasUnsavedWork: () => unsaved });
    h.win.dispatchEvent(pageshow(true));
    await h.flush();
    expect(h.onNotify).toHaveBeenCalledOnce();
    h.win.dispatchEvent(new Event('hashchange')); // still unsaved: stay put
    await h.flush();
    expect(h.reload).not.toHaveBeenCalled();
    unsaved = false;
    h.win.dispatchEvent(new Event('hashchange'));
    expect(h.reload).not.toHaveBeenCalled(); // deferred until the view has swapped
    await h.flush();
    expect(h.reload).toHaveBeenCalledOnce();
    h.win.dispatchEvent(new Event('hashchange')); // only once
    await h.flush();
    expect(h.reload).toHaveBeenCalledOnce();
    h.stop();
  });

  it('a navigation without a pending notice never reloads (#0185)', async () => {
    const h = watchHarness();
    h.win.dispatchEvent(new Event('hashchange'));
    await h.flush();
    expect(h.reload).not.toHaveBeenCalled();
    h.stop();
  });

  it('startVersionWatch cleanup removes both listeners', async () => {
    const h = watchHarness();
    h.stop();
    h.doc.visibilityState = 'visible';
    h.doc.dispatchEvent(new Event('visibilitychange'));
    h.win.dispatchEvent(pageshow(true));
    await h.flush();
    expect(h.fetchBuild).not.toHaveBeenCalled();
  });
});
