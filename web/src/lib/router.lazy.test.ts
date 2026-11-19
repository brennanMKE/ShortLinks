// @vitest-environment jsdom
// In-app navigation to a lazy view keeps the current view on screen until the
// next chunk arrives, so there is no "Loading…" flash (#0186).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/svelte';
import { tick } from 'svelte';
import Router from './Router.svelte';
import NotFound from '../views/NotFound.svelte';
import Footer from './Footer.svelte';
import { initRouter, navigate } from './hashRouter.svelte';
import { VIEW_LOADERS, __resetForTest, lazy } from './routeViews';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  __resetForTest();
});

describe('Router with lazy views', () => {
  it('keeps the previous view mounted until the next chunk arrives', async () => {
    let release: () => void = () => {};
    vi.spyOn(VIEW_LOADERS, 'account').mockImplementation(
      () => new Promise((r) => (release = () => r({ default: Footer } as never))),
    );
    window.location.hash = '#/';
    initRouter([
      { path: '/', component: NotFound },
      { path: '/next', component: lazy('account') },
      { path: '*', component: NotFound },
    ]);
    render(Router);
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy();

    navigate('/next');
    await tick();
    await Promise.resolve();
    // Chunk still loading: old view stays, no placeholder.
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy();
    expect(screen.queryByText('Loading…')).toBeNull();

    release();
    await vi.waitFor(() => expect(screen.queryByRole('heading', { level: 1, name: 'Page not found' })).toBeNull());
    expect(screen.queryByText('Loading…')).toBeNull();
    expect(document.body.textContent).toContain('GitHub');
  });

  it('shows the next view at once when its chunk is already cached', async () => {
    const spy = vi.spyOn(VIEW_LOADERS, 'account').mockResolvedValue({ default: Footer } as never);
    window.location.hash = '#/';
    initRouter([
      { path: '/', component: NotFound },
      { path: '/next', component: lazy('account') },
    ]);
    render(Router);
    navigate('/next');
    await vi.waitFor(() => expect(document.body.textContent).toContain('GitHub'));
    navigate('/');
    await tick();
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy();
    navigate('/next');
    await tick();
    expect(document.body.textContent).toContain('GitHub');
    expect(spy).toHaveBeenCalledTimes(1);
  });
});
