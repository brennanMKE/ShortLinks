import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { get } from 'svelte/store';
import {
  toasts, pushToast, dismissToast, pauseToast, resumeToast, runToastAction, resetToasts, TOAST_MS, MAX_TOASTS,
} from './toasts';

beforeEach(() => { vi.useFakeTimers(); resetToasts(); });
afterEach(() => { resetToasts(); vi.useRealTimers(); });

describe('toasts', () => {
  it('expires after 8 seconds', () => {
    pushToast({ message: 'a' });
    vi.advanceTimersByTime(TOAST_MS - 1);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(get(toasts)).toHaveLength(0);
  });

  it('keeps at most three, dropping the oldest', () => {
    for (const m of ['1', '2', '3', '4']) pushToast({ message: m });
    expect(get(toasts).map((t) => t.message)).toEqual(['2', '3', '4']);
    expect(MAX_TOASTS).toBe(3);
  });

  it('pauses on hover/focus and resumes with the time left', () => {
    const id = pushToast({ message: 'a' });
    vi.advanceTimersByTime(5000);
    pauseToast(id);
    vi.advanceTimersByTime(60000);
    expect(get(toasts)).toHaveLength(1);
    resumeToast(id);
    vi.advanceTimersByTime(2999);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(get(toasts)).toHaveLength(0);
  });

  it('runs the action once and closes on success', async () => {
    const fn = vi.fn().mockResolvedValue(undefined);
    const id = pushToast({ message: 'a', actionLabel: 'Undo', onaction: fn });
    await runToastAction(id);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(get(toasts)).toHaveLength(0);
  });

  it('stays open with the error when the action fails', async () => {
    const id = pushToast({ message: 'a', actionLabel: 'Undo', onaction: () => Promise.reject(new Error('nope')) });
    await runToastAction(id);
    const t = get(toasts)[0];
    expect(t.error).toBe('nope');
    expect(t.busy).toBe(false);
  });

  it('dismiss removes it and clears the timer', () => {
    const id = pushToast({ message: 'a' });
    dismissToast(id);
    expect(get(toasts)).toHaveLength(0);
    vi.advanceTimersByTime(TOAST_MS * 2);
    expect(get(toasts)).toHaveLength(0);
  });

  it('does not restart the timer on resume while the action runs', async () => {
    let release: () => void = () => {};
    const id = pushToast({ message: 'a', actionLabel: 'Undo', onaction: () => new Promise<void>((r) => (release = r)) });
    const run = runToastAction(id);
    resumeToast(id);
    vi.advanceTimersByTime(TOAST_MS * 2);
    expect(get(toasts)).toHaveLength(1);
    release();
    await run;
    expect(get(toasts)).toHaveLength(0);
  });
});
