<!--
  Sign-in card for Portiko mode (#0133): one button that starts the redirect to
  Portiko, and a "Create an account" link when enrollment is open (Portiko opens
  on account creation). A signed-out deep link skips this card and redirects
  straight away (App.svelte); this is what the bare root, /login, a sign-out and
  a redirect loop land on. The return target is the route that brought the user
  here (see returnToFromHash).
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { getPublicHome } from '../lib/api';
  import { lastHomeInfo, noteHomeInfo } from '../lib/authMode';
  import { portikoLoginURL, portikoSignUpVisible } from '../lib/portiko';
  import type { HomeInfo } from '../lib/home';
  import { getRoute } from '../lib/hashRouter.svelte';
  import Panel from '../lib/Panel.svelte';
  import AuthBrand from '../lib/AuthBrand.svelte';

  let homeInfo = $state<HomeInfo | null>(null);
  const canSignUp = $derived(portikoSignUpVisible(homeInfo));

  const hash = $derived.by(() => {
    const r = getRoute();
    const q = r.query.toString();
    return `#${r.path}${q ? `?${q}` : ''}`;
  });
  const signInHref = $derived(portikoLoginURL(hash));
  const createHref = $derived(portikoLoginURL(hash, { create: true }));

  onMount(() => {
    // App already fetched the answer on a signed-out load; fetch only if it did not.
    const cached = get(lastHomeInfo);
    if (cached) {
      homeInfo = cached;
      return;
    }
    getPublicHome()
      .then((info) => {
        noteHomeInfo(info);
        homeInfo = info;
      })
      .catch(() => (homeInfo = null));
  });
</script>

<main class="login-shell">
  <AuthBrand />

  <Panel>
    <div class="sign-in-form">
      <a class="btn-link btn-link-primary" href={signInHref} data-testid="portiko-sign-in">Sign in with passkey</a>
      <p class="text-muted hint">You sign in on this site's account page and come straight back.</p>
    </div>
  </Panel>

  {#if canSignUp}
    <p class="alt text-muted">
      <span>New here? <a href={createHref} data-testid="portiko-create">Create an account</a></span>
    </p>
  {/if}
</main>

<style>
  .login-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    width: 100%;
  }
  .sign-in-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .hint {
    font-size: var(--fs-sm);
    margin: 0;
  }
  .alt {
    display: flex;
    justify-content: center;
    font-size: var(--fs-sm);
    margin: 0;
  }
  .alt :global(a) {
    color: var(--accent);
    text-decoration: underline;
  }
  @media (max-width: 480px) {
    .login-shell {
      margin-top: var(--space-5);
    }
  }
</style>
