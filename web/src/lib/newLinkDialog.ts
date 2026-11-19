// Open/close state for the New link dialog (#0158). AppShell mounts one
// NewLinkDialog and reads this store; anything that wants to start a link
// calls openNewLink(). On phones it navigates to the full page at #/new
// instead of opening the dialog.

import { writable } from 'svelte/store';
import { navigate } from './hashRouter.svelte';
import { PHONE_MAX_WIDTH } from './newLink';

/** null while closed; otherwise the destination URL to prefill ('' for none). */
export const newLinkDialog = writable<{ url: string } | null>(null);

function isPhone(): boolean {
  return typeof window !== 'undefined' && window.matchMedia(`(max-width: ${PHONE_MAX_WIDTH}px)`).matches;
}

/** Start a new link: the dialog on desktop, the #/new page on a phone. */
export function openNewLink(url = ''): void {
  if (isPhone()) {
    navigate(url ? '/new?url=' + encodeURIComponent(url) : '/new');
    return;
  }
  newLinkDialog.set({ url });
}

export function closeNewLink(): void {
  newLinkDialog.set(null);
}
