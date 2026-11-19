import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// The SPA is built to `dist/` and embedded into the Go binary via //go:embed at
// compile time. In development the dev server proxies the API, auth, and
// redirect namespaces to the Go service on :8080 so the browser sees everything
// as same-origin (no CORS).
// Override with SHORTLINKS_API=http://localhost:8182 when :8080 is taken.
const api = process.env.SHORTLINKS_API ?? 'http://localhost:8080';

export default defineConfig({
  plugins: [svelte()],
  // Component tests (jsdom) need Svelte's browser build, not the SSR one.
  resolve: process.env.VITEST ? { conditions: ['browser'] } : undefined,
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': api,
      '/auth': api,
      '/account': api,
      '/admin': api,
      '/u': api,
    },
  },
});
