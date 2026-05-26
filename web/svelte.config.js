import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  // Vite + svelte-check both consume the TypeScript preprocessor so `<script
  // lang="ts">` blocks are type-checked and compiled.
  preprocess: vitePreprocess(),
};
