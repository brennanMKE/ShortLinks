// main.ts must not mount App until the first route's lazy chunk is in (#0186).
// Mounting first would show a blank shell or a placeholder while the chunk
// downloads. This runs main.ts itself with mount() and the preload stubbed.
import { afterEach, describe, expect, it, vi } from 'vitest';

const mount = vi.fn();
let release: () => void = () => {};
const preloadForLocation = vi.fn(() => new Promise<void>((r) => (release = r)));
const installChunkErrorRecovery = vi.fn();
let reloading = false;

vi.mock('svelte', async (orig) => ({ ...(await orig<typeof import('svelte')>()), mount }));
vi.mock('../App.svelte', () => ({ default: {} }));
vi.mock('../app.css', () => ({}));
vi.mock('./preferences', () => ({ initTheme: () => {} }));
vi.mock('./routeTable', () => ({ routes: [] }));
vi.mock('./routeViews', () => ({ preloadForLocation, installChunkErrorRecovery, isRecoveryReloading: () => reloading }));

afterEach(() => vi.unstubAllGlobals());

describe('main.ts waits for the first route before mounting (#0186)', () => {
  it('installs chunk recovery and mounts only after preloadForLocation settles', async () => {
    vi.stubGlobal('document', { getElementById: () => ({}) });
    await import('../main');
    expect(installChunkErrorRecovery).toHaveBeenCalledTimes(1);
    expect(preloadForLocation).toHaveBeenCalledTimes(1);
    await Promise.resolve();
    expect(mount).not.toHaveBeenCalled();
    release();
    await vi.waitFor(() => expect(mount).toHaveBeenCalledTimes(1));
  });

  it('does not mount while a stale-chunk reload is in progress', async () => {
    vi.resetModules();
    mount.mockClear();
    reloading = true;
    vi.stubGlobal('document', { getElementById: () => ({}) });
    let done: () => void = () => {};
    preloadForLocation.mockImplementationOnce(() => new Promise<void>((r) => (done = r)));
    await import('../main');
    done();
    await new Promise((r) => setTimeout(r, 20));
    expect(mount).not.toHaveBeenCalled();
    reloading = false;
  });
});
