// Reactive media-query flag (#0173). `phone.matches` is true below 900px, the
// width where DataTable turns into cards and filter bars fold into a sheet.
// Safe where matchMedia is missing (jsdom, SSR): it reads false.
import { onDestroy } from 'svelte';

export const PHONE_QUERY = '(max-width: 899px)';

export function createPhone(query: string = PHONE_QUERY): { readonly matches: boolean } {
  const mq = typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(query) : null;
  let matches = $state(mq?.matches ?? false);
  if (mq) {
    const on = (e: MediaQueryListEvent) => (matches = e.matches);
    mq.addEventListener('change', on);
    onDestroy(() => mq.removeEventListener('change', on));
  }
  return {
    get matches() {
      return matches;
    },
  };
}
