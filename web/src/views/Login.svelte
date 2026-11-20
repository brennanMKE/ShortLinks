<!--
  Login route (#0133). One SPA build serves both sign-in systems, so this picks
  the screen from what the server said (GET /api/public/home `portiko`): the
  Portiko card when it runs the redirect flow, the unchanged passkey screens
  (PasskeyLogin.svelte, #0172) otherwise. The mode is not always known by the
  time this mounts (a mid-session 401 on a signed-in cold load), so it is asked
  for here and a loading state shows meanwhile, never the wrong screen.
  #0136 removes the passkey branch.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { portikoMode, loadPortikoMode } from '../lib/authMode';
  import PasskeyLogin from './PasskeyLogin.svelte';
  import PortikoLogin from './PortikoLogin.svelte';

  // A failed fetch resolves false without setting the store: show the passkey screens.
  let failed = $state(false);

  onMount(() => {
    if ($portikoMode === null) {
      loadPortikoMode().then((on) => {
        if (!on) failed = true;
      });
    }
  });
</script>

{#if $portikoMode === true}
  <PortikoLogin />
{:else if $portikoMode === false || failed}
  <PasskeyLogin />
{:else}
  <main class="login-loading"><p class="text-muted" role="status">Loading…</p></main>
{/if}

<style>
  .login-loading {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
  }
</style>
