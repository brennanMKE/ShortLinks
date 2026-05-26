import { mount } from 'svelte';
import App from './App.svelte';

// Svelte 5 mounts the root component imperatively. The #app element is defined
// in index.html.
const app = mount(App, {
  target: document.getElementById('app')!,
});

export default app;
