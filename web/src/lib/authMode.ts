// Which sign-in system the server runs (#0133): remembered from the
// GET /api/public/home answer so views can branch without each fetching it.
import { get, writable } from 'svelte/store';
import { getPublicHome } from './api';
import { isPortiko } from './portiko';
import type { HomeInfo } from './home';

/** true: Portiko redirect flow; false: passkey screens; null: not known yet. */
export const portikoMode = writable<boolean | null>(null);

/** The last home-info answer anyone fetched, so the sign-in cards need not fetch it again. */
export const lastHomeInfo = writable<HomeInfo | null>(null);

let pending: Promise<boolean> | null = null;

/** Records the mode (and the answer) from a home-info response someone already fetched. */
export function noteHomeInfo(info: HomeInfo | null): void {
  if (info === null) return;
  lastHomeInfo.set(info);
  portikoMode.set(isPortiko(info));
}

/** Resolves the mode, fetching home info once. A failed fetch reads as passkey mode and is retried next time. */
export function loadPortikoMode(): Promise<boolean> {
  const known = get(portikoMode);
  if (known !== null) return Promise.resolve(known);
  if (pending === null) {
    pending = getPublicHome().then(
      (info) => {
        pending = null;
        noteHomeInfo(info);
        return isPortiko(info);
      },
      () => {
        pending = null;
        return false;
      },
    );
  }
  return pending;
}

/** Test seam: forget everything. */
export function resetAuthMode(): void {
  pending = null;
  portikoMode.set(null);
  lastHomeInfo.set(null);
}
