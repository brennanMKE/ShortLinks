<!--
  Home view (#0125). Shown to a signed-out visitor at the bare root when
  registration is closed and the admin has set a primary URL: a short notice,
  then a redirect after HOME_REDIRECT_DELAY_MS. "Go now" skips the wait; the
  small Sign in link leads account holders to /login (and Home's onDestroy
  clears the timer). The redirect uses location.replace so Back from the
  primary site does not land here and bounce again. Nothing animates.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Link from '../lib/Link.svelte';
  import { APP_NAME } from '../lib/branding';
  import { HOME_REDIRECT_DELAY_MS, redirectHost, type HomeInfo } from '../lib/home';

  let { info }: { info: HomeInfo } = $props();

  let timer: number | undefined;

  onMount(() => {
    timer = window.setTimeout(() => {
      window.location.replace(info.redirect_url);
    }, HOME_REDIRECT_DELAY_MS);
  });

  onDestroy(() => {
    if (timer !== undefined) window.clearTimeout(timer);
  });
</script>

<main class="home-shell">
  <header class="brand">
    <h1 class="brand-title">{APP_NAME}</h1>
    <p class="text-muted tagline" role="status" aria-live="polite">
      Taking you to {redirectHost(info.redirect_url)}…
    </p>
  </header>

  <p class="go">
    <a href={info.redirect_url} rel="noopener" class="btn-link btn-link-primary">Go now</a>
  </p>

  <p class="text-muted signin"><Link to="/login">Sign in</Link></p>
</main>

<style>
  .home-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    width: 100%;
    text-align: center;
  }
  .brand {
    margin-bottom: var(--space-4);
  }
  .brand-title {
    font-size: var(--fs-xl);
    font-weight: 600;
    margin: 0;
  }
  .tagline {
    margin: var(--space-1) 0 0;
  }
  .go {
    margin: 0 0 var(--space-5);
  }
  .signin {
    font-size: var(--fs-sm);
    margin: 0;
  }
</style>
