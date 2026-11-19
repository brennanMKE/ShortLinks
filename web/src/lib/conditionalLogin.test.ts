import { describe, it, expect, vi } from 'vitest';
import { createConditionalLogin } from './conditionalLogin';

describe('createConditionalLogin', () => {
  it('never calls credentials.get when stopped during the availability check', async () => {
    const get = vi.fn();
    let resolveAvailable!: (v: boolean) => void;
    const available = () => new Promise<boolean>((r) => (resolveAvailable = r));
    const c = createConditionalLogin({
      available,
      run: async (signal) => {
        get({ mediation: 'conditional', signal });
      },
      onError: () => {},
    });
    const started = c.start();
    c.stop();
    resolveAvailable(true);
    await started;
    expect(get).not.toHaveBeenCalled();
  });

  it('a newer start supersedes one still awaiting detection', async () => {
    const run = vi.fn(async () => {});
    const resolvers: ((v: boolean) => void)[] = [];
    const c = createConditionalLogin({
      available: () => new Promise<boolean>((r) => resolvers.push(r)),
      run,
      onError: () => {},
    });
    const first = c.start();
    const second = c.start();
    resolvers.forEach((r) => r(true));
    await Promise.all([first, second]);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('runs the ceremony when available and not stopped', async () => {
    const run = vi.fn(async () => {});
    const c = createConditionalLogin({ available: async () => true, run, onError: () => {} });
    await c.start();
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('does nothing when conditional mediation is unavailable', async () => {
    const run = vi.fn(async () => {});
    const c = createConditionalLogin({ available: async () => false, run, onError: () => {} });
    await c.start();
    expect(run).not.toHaveBeenCalled();
  });

  it('reports errors except ones caused by its own abort', async () => {
    const onError = vi.fn();
    const c = createConditionalLogin({
      available: async () => true,
      run: async (signal) => {
        c.stop();
        expect(signal.aborted).toBe(true);
        throw new DOMException('x', 'AbortError');
      },
      onError,
    });
    await c.start();
    expect(onError).not.toHaveBeenCalled();
    const c2 = createConditionalLogin({
      available: async () => true,
      run: async () => {
        throw new Error('boom');
      },
      onError,
    });
    await c2.start();
    expect(onError).toHaveBeenCalledTimes(1);
  });
});
