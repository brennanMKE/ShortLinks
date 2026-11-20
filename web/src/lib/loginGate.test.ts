// @vitest-environment jsdom
// The Login gate (#0133 review): a mid-session 401 on a signed-in cold load
// reaches /login with the mode still unknown. It must ask, and show a loading
// state meanwhile, never the passkey screen of a Portiko instance.
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest';
import { render, screen, cleanup, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';

const getPublicHome = vi.fn();
vi.mock('./api', async (orig) => ({ ...(await orig<typeof import('./api')>()), getPublicHome: () => getPublicHome() }));

import Login from '../views/Login.svelte';
import { AUTO_REDIRECT_KEY, clearAutoRedirectGuard } from './portiko';
import { portikoMode, lastHomeInfo, resetAuthMode, loadPortikoMode } from './authMode';

const home = (portiko: boolean) => ({ redirect_url: '', registrations_enabled: true, portiko });

beforeEach(() => {
  resetAuthMode();
  getPublicHome.mockReset();
});
afterEach(cleanup);

describe('Login gate', () => {
  it('shows a loading state, then the Portiko card, when the mode is unknown', async () => {
    let resolve!: (v: unknown) => void;
    getPublicHome.mockReturnValue(new Promise((r) => (resolve = r)));
    render(Login);
    expect(screen.getByRole('status').textContent).toContain('Loading');
    expect(document.querySelector('#login-email')).toBeNull();
    expect(screen.queryByTestId('portiko-sign-in')).toBeNull();
    resolve(home(true));
    await waitFor(() => expect(screen.getByTestId('portiko-sign-in')).toBeTruthy());
    expect(document.querySelector('#login-email')).toBeNull();
  });

  it('shows the passkey screen when the server is not in Portiko mode', async () => {
    getPublicHome.mockResolvedValue(home(false));
    render(Login);
    await waitFor(() => expect(document.querySelector('#login-email')).not.toBeNull());
    expect(screen.queryByTestId('portiko-sign-in')).toBeNull();
  });

  it('falls back to the passkey screen when the fetch fails', async () => {
    getPublicHome.mockRejectedValue(new Error('offline'));
    render(Login);
    await waitFor(() => expect(document.querySelector('#login-email')).not.toBeNull());
  });

  it('does not fetch again when the mode is already known, and the card reuses the home info', async () => {
    portikoMode.set(true);
    lastHomeInfo.set(home(true));
    render(Login);
    expect(screen.getByTestId('portiko-sign-in')).toBeTruthy();
    await waitFor(() => expect(screen.getByTestId('portiko-create')).toBeTruthy());
    expect(getPublicHome).not.toHaveBeenCalled();
  });
});

describe('loadPortikoMode', () => {
  it('fetches once and records the answer', async () => {
    getPublicHome.mockResolvedValue(home(true));
    expect(await loadPortikoMode()).toBe(true);
    expect(await loadPortikoMode()).toBe(true);
    expect(getPublicHome).toHaveBeenCalledTimes(1);
    expect(get(portikoMode)).toBe(true);
  });
});

describe('loop guard', () => {
  it('is cleared after a successful sign-in', () => {
    sessionStorage.setItem(AUTO_REDIRECT_KEY, String(Date.now()));
    clearAutoRedirectGuard();
    expect(sessionStorage.getItem(AUTO_REDIRECT_KEY)).toBeNull();
  });
});
