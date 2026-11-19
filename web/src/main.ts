import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import { initTheme } from './lib/preferences';

// Apply the saved theme (#0170) before mounting so there is no flash of the
// wrong palette.
initTheme();

// Svelte 5 mounts the root component imperatively. The #app element is defined
// in index.html.
const app = mount(App, {
  target: document.getElementById('app')!,
});

export default app;
