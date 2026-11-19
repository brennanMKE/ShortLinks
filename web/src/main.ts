import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import { initTheme } from './lib/preferences';
import { installChunkErrorRecovery, isRecoveryReloading, preloadForLocation } from './lib/routeViews';
import { routes } from './lib/routeTable';

// Apply the saved theme (#0170) before mounting so there is no flash of the
// wrong palette.
initTheme();

// A stale tab asking for a chunk the newest deploy deleted reloads once (#0186).
installChunkErrorRecovery();

// Svelte 5 mounts the root component imperatively. The #app element is defined
// in index.html.
//
// #0186: the first route's lazy chunk is awaited before mounting, so a direct
// load or a reload goes from the page's own markup to the view without a blank
// shell or a second placeholder. A failed chunk has already started the
// once-per-URL reload; if that was spent, mount anyway and LazyView shows its
// failure state.
void preloadForLocation(routes).then(() => {
  // A recovery reload is already on its way; mounting would only paint the old
  // build's page for a moment before it is replaced.
  if (isRecoveryReloading()) return;
  mount(App, {
    target: document.getElementById('app')!,
  });
});
