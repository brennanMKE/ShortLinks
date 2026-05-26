import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The SPA is built to `dist/` and embedded into the Go binary via //go:embed at
// compile time. In development the dev server proxies the API, auth, and
// redirect namespaces to the Go service on :8080 so the browser sees everything
// as same-origin (no CORS).
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
      '/account': 'http://localhost:8080',
      '/admin': 'http://localhost:8080',
      '/u': 'http://localhost:8080',
    },
  },
});
