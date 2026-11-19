<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, pendingVerifyToken } from './lib/stores';
  import { getMe, getPublicHome } from './lib/api';
  import { initRouter, getRoute, replace } from './lib/hashRouter.svelte';
  import {
    loginPathWithRedirect,
    parseHash,
    redirectTargetFrom,
  } from './lib/routing';
  import { componentToPreload, shouldShowHome, type HomeInfo } from './lib/home';
  import Home from './views/Home.svelte';
  import { routes } from './lib/routeTable';
  import { preloadComponent } from './lib/routeViews';
  import LazyView from './lib/LazyView.svelte';
  import Router from './lib/Router.svelte';
  import AppShell from './lib/AppShell.svelte';
  import Footer from './lib/Footer.svelte';
  import Toast from './lib/Toast.svelte';
  import Button from './lib/Button.svelte';
  import { reloadToSameURL, restoreScrollAfterReload, startVersionWatch } from './lib/versionCheck';
  import { setUnsaved } from './lib/unsavedWork';

  // 'loading': the initial /api/me check (or the magic-link path check) is
  // still in flight. 'verify-register'/'verify-recover': a magic-link landing
  // path — rendered directly, standalone, OUTSIDE the hash router (#0120
  // Notes: these stay real paths, not hash routes, since they are landing
  // targets for emailed links). 'app': the hash router is wired up and owns
  // rendering via <Router />.
  type Mode = 'loading' | 'verify-register' | 'verify-recover' | 'app';
  let mode = $state<Mode>('loading');

  // #0185: set when a newer build is out but unsaved input (an open dialog, a
  // dirty form, a focused field) holds the reload back.
  let updateAvailable = $state(false);

  // Show the notice in the top layer so an open modal dialog cannot cover it.
  function topLayer(node: HTMLElement) {
    try {
      node.showPopover?.();
    } catch {
      // already shown, or unsupported: the fixed-position CSS still shows it
    }
  }

  onMount(() => {
    restoreScrollAfterReload();
    return startVersionWatch({ onNotify: () => (updateAvailable = true) });
  });

  // Public home info (#0125): fetched only on a signed-out cold load at the
  // bare root, cleared the moment someone signs in so a later sign-out falls
  // through to the normal /login bounce rather than ejecting to another site.
  let homeInfo = $state<HomeInfo | null>(null);
  const showHome = $derived(
    shouldShowHome($currentUser !== null, getRoute().path, homeInfo),
  );

  // Redirects away from a route currentUser doesn't belong on: signed-out on
  // a protected route bounces to /login (preserving it as the `redirect`
  // target); signed-in on /login bounces onward. Called from enterApp()
  // BEFORE mode flips to 'app', so a signed-out cold load never mounts (and
  // fires the API call of) the protected view — and again from the $effect
  // below on every later hashchange.
  function redirectIfNeeded(): void {
    const route = getRoute();
    if (
      $currentUser === null &&
      route.path !== '/login' &&
      !shouldShowHome(false, route.path, homeInfo)
    ) {
      replace(loginPathWithRedirect(route.path));
    } else if ($currentUser !== null && route.path === '/login') {
      replace(redirectTargetFrom(route.query));
    }
  }

  // Enters (or re-enters) hash-routed mode: wires the route table into the
  // router — idempotent, initRouter only ever registers its hashchange
  // listener once — and, on FIRST entry, normalizes an empty hash to '/'
  // before the first render so the wrong screen never flashes (#0120 spec).
  // RegisterVerify/RecoverVerify call this (via the callback props below)
  // once their ceremony finishes, to hand control from their standalone
  // rendering over to the router.
  async function enterApp(initialPath?: string) {
    initRouter(routes);
    setUnsaved('verify-ceremony', false);
    if (initialPath) {
      replace(initialPath);
    } else if (window.location.hash === '' || window.location.hash === '#') {
      replace('/');
    }
    redirectIfNeeded();
    // Wait for the chunk of the view that will show, now that auth has settled
    // and any redirect has landed, so the first paint is the view itself and
    // not a second "Loading..." (#0186). `mode` is still 'loading', which shows
    // the same text, so nothing flashes. A failed chunk has already started the
    // once-per-URL reload; if that is spent, LazyView shows its failure state.
    // The public home is eager: when it is what will show, load nothing.
    await preloadComponent(componentToPreload($currentUser !== null, getRoute(), homeInfo));
    mode = 'app';
  }

  // On load, check the current URL PATH first (not the hash).
  //
  // Magic-link landing paths (/register/verify and /recover/verify) arrive
  // here via the SPA catch-all in the Go mux ("GET /" serves index.html for
  // every unmatched non-API path). They are detected — and handled — before
  // the hash router is ever wired up: a user who follows an email link
  // without an active session is routed straight to the verification view
  // rather than bounced to login (#0041).
  //
  // For every other path, GET /api/me decides whether a session exists;
  // enterApp() below wires up the router and redirects if needed.
  onMount(async () => {
    const path = window.location.pathname;

    if (path === '/register/verify') {
      const params = new URLSearchParams(window.location.search);
      pendingVerifyToken.set(params.get('token'));
      // Strip the token from the URL immediately after capturing it into the
      // store. A mid-ceremony reload will land on /register/verify with no
      // token in the URL (and thus an empty store), showing "No verification
      // token found" — correct behavior.
      history.replaceState({}, '', '/register/verify');
      mode = 'verify-register';
      setUnsaved('verify-ceremony', true); // never reload mid-ceremony (#0185)
      return;
    }

    if (path === '/recover/verify') {
      const params = new URLSearchParams(window.location.search);
      pendingVerifyToken.set(params.get('token'));
      history.replaceState({}, '', '/recover/verify');
      mode = 'verify-recover';
      setUnsaved('verify-ceremony', true);
      return;
    }

    try {
      currentUser.set(await getMe());
    } catch {
      // A 401 (signed out) or any other failure (network/server) both land
      // signed-out; the guard effect below bounces to /login either way.
      currentUser.set(null);
      // Only the bare root can show the home page; any failure here means
      // today's behavior (bounce to /login).
      if (parseHash(window.location.hash).path === '/') {
        try {
          homeInfo = await getPublicHome();
        } catch {
          homeInfo = null;
        }
      }
    }
    enterApp();
  });

  // Re-runs redirectIfNeeded() on every hash change once the router is live —
  // enterApp() already covers the first render. Gated on `mode === 'app'` so
  // it can never fire before the router exists; redirectIfNeeded() itself
  // can't loop, since each branch's own condition goes false the moment its
  // replace() lands.
  $effect(() => {
    if (mode !== 'app') return;
    if ($currentUser !== null) homeInfo = null;
    redirectIfNeeded();
  });
</script>

{#if mode === 'loading'}
  <div class="app-shell">
    <p class="text-muted">Loading…</p>
  </div>
{:else if mode === 'verify-register'}
  <LazyView view="register-verify" onverified={() => enterApp('/')} ongotologin={(path?: string) => enterApp(path ?? '/login')} />
{:else if mode === 'verify-recover'}
  <LazyView view="recover-verify" onverified={() => enterApp('/')} ongotologin={(path?: string) => enterApp(path ?? '/login')} />
{:else if showHome && homeInfo}
  <Home info={homeInfo} />
{:else if $currentUser !== null && getRoute().path !== '/login'}
  <AppShell>
    <Router />
  </AppShell>
{:else}
  <Router />
{/if}

<Footer />
<Toast />

<!-- A live region must exist before its text changes to be announced, so it stays
     mounted and visually hidden; the visible bar below appears separately (#0185). -->
<p class="sr-only" role="status">{updateAvailable ? 'A new version is available. Reload to update.' : ''}</p>

{#if updateAvailable}
  <!-- Reload is unclickable while a modal is open: the modal makes everything
       outside it inert, so the bar is informational until the modal closes. -->
  <div class="update-notice" popover="manual" use:topLayer>
    <span aria-hidden="true">A new version is available.</span>
    <Button variant="primary" onclick={() => reloadToSameURL()}>Reload</Button>
  </div>
{/if}

<style>
  .update-notice {
    position: fixed;
    inset: auto 0 var(--space-4) 0;
    margin-inline: auto;
    margin-block: 0;
    width: max-content;
    z-index: 50;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-3);
    max-width: min(36rem, calc(100% - 2 * var(--space-4)));
    padding: var(--space-3) var(--space-4);
    background: var(--bg-panel);
    color: var(--text);
    border: var(--border-w) solid var(--border-strong);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.25);
  }
  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    margin: -1px;
    padding: 0;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
    border: 0;
  }
</style>
