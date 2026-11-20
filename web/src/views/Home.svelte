<!--
  Home view (#0125, #0177). In `about` and `activity` mode (#0174) this is the
  content page (lib/PublicHome.svelte). In `default` mode: shown to a signed-out visitor at the bare root when
  registration is closed and the admin has set a primary URL: a short notice,
  then a redirect after HOME_REDIRECT_DELAY_MS. "Go now" skips the wait; the
  small Sign in link leads account holders to /login (and Home's onDestroy
  clears the timer). The redirect uses location.replace so Back from the
  primary site does not land here and bounce again. Nothing animates.
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import Link from '../lib/Link.svelte';
  import AuthBrand from '../lib/AuthBrand.svelte';
  import { portikoLoginURL, isPortiko } from '../lib/portiko';
  import { HOME_REDIRECT_DELAY_MS, isContentHome, redirectHost, type HomeInfo } from '../lib/home';

  let { info }: { info: HomeInfo } = $props();

  let timer: number | undefined;

  onMount(() => {
    // The content page never redirects.
    if (isContentHome(info)) return;
    timer = window.setTimeout(() => {
      window.location.replace(info.redirect_url);
    }, HOME_REDIRECT_DELAY_MS);
  });

  onDestroy(() => {
    if (timer !== undefined) window.clearTimeout(timer);
  });
</script>

{#if isContentHome(info)}
  <!-- Loaded on demand so the chart code stays out of the entry chunk. -->
  {#await import('../lib/PublicHome.svelte') then { default: PublicHome }}
    <PublicHome {info} host={window.location.host} />
  {/await}
{:else}
<main class="home-shell">
  <AuthBrand />
  <p class="text-muted tagline" role="status" aria-live="polite">
    Taking you to {redirectHost(info.redirect_url)}…
  </p>

  <p class="go">
    <a href={info.redirect_url} rel="noopener" class="btn-link btn-link-primary">Go now</a>
  </p>

  <p class="text-muted signin">
    {#if isPortiko(info)}
      <a href={portikoLoginURL('#/')}>Sign in</a>
    {:else}
      <Link to="/login">Sign in</Link>
    {/if}
  </p>
</main>
{/if}

<style>
  .home-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    width: 100%;
    text-align: center;
  }
  .tagline {
    margin: calc(-1 * var(--space-3)) 0 var(--space-4);
  }
  .go {
    margin: 0 0 var(--space-5);
  }
  .signin {
    font-size: var(--fs-sm);
    margin: 0;
  }
</style>
