<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser, pendingVerifyToken } from './lib/stores';
  import { getMe } from './lib/api';
  import { initRouter, getRoute, replace } from './lib/hashRouter.svelte';
  import { loginPathWithRedirect, redirectTargetFrom, type RouteDef } from './lib/routing';
  import Login from './views/Login.svelte';
  import Dashboard from './views/Dashboard.svelte';
  import LinkDetail from './views/LinkDetail.svelte';
  import CampaignsList from './views/CampaignsList.svelte';
  import CampaignDetail from './views/CampaignDetail.svelte';
  import Account from './views/Account.svelte';
  import Admin from './views/Admin.svelte';
  import NotFound from './views/NotFound.svelte';
  import RegisterVerify from './views/RegisterVerify.svelte';
  import RecoverVerify from './views/RecoverVerify.svelte';
  import Router from './lib/Router.svelte';
  import Footer from './lib/Footer.svelte';

  // The route table lives HERE, not in hashRouter.svelte.ts, because it needs
  // the actual view components — hashRouter.svelte.ts stays free of any view
  // import so routing.ts's matching logic can be unit-tested with no Svelte
  // runtime at all (#0120). Match in declaration order; '*' (NotFound) goes last.
  const routes: RouteDef<unknown>[] = [
    { path: '/', component: Dashboard },
    { path: '/login', component: Login },
    { path: '/links/:key', component: LinkDetail },
    { path: '/campaigns', component: CampaignsList },
    { path: '/campaigns/:slug', component: CampaignDetail },
    { path: '/account', component: Account },
    { path: '/admin', component: Admin },
    { path: '*', component: NotFound },
  ];

  // 'loading': the initial /api/me check (or the magic-link path check) is
  // still in flight. 'verify-register'/'verify-recover': a magic-link landing
  // path — rendered directly, standalone, OUTSIDE the hash router (#0120
  // Notes: these stay real paths, not hash routes, since they are landing
  // targets for emailed links). 'app': the hash router is wired up and owns
  // rendering via <Router />.
  type Mode = 'loading' | 'verify-register' | 'verify-recover' | 'app';
  let mode = $state<Mode>('loading');

  // Redirects away from a route currentUser doesn't belong on: signed-out on
  // a protected route bounces to /login (preserving it as the `redirect`
  // target); signed-in on /login bounces onward. Called from enterApp()
  // BEFORE mode flips to 'app', so a signed-out cold load never mounts (and
  // fires the API call of) the protected view — and again from the $effect
  // below on every later hashchange.
  function redirectIfNeeded(): void {
    const route = getRoute();
    if ($currentUser === null && route.path !== '/login') {
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
  function enterApp(initialPath?: string) {
    initRouter(routes);
    if (initialPath) {
      replace(initialPath);
    } else if (window.location.hash === '' || window.location.hash === '#') {
      replace('/');
    }
    redirectIfNeeded();
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
      return;
    }

    if (path === '/recover/verify') {
      const params = new URLSearchParams(window.location.search);
      pendingVerifyToken.set(params.get('token'));
      history.replaceState({}, '', '/recover/verify');
      mode = 'verify-recover';
      return;
    }

    try {
      currentUser.set(await getMe());
    } catch {
      // A 401 (signed out) or any other failure (network/server) both land
      // signed-out; the guard effect below bounces to /login either way.
      currentUser.set(null);
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
    redirectIfNeeded();
  });
</script>

{#if mode === 'loading'}
  <div class="app-shell">
    <p class="text-muted">Loading…</p>
  </div>
{:else if mode === 'verify-register'}
  <RegisterVerify onverified={() => enterApp('/')} ongotologin={() => enterApp('/login')} />
{:else if mode === 'verify-recover'}
  <RecoverVerify onverified={() => enterApp('/')} ongotologin={() => enterApp('/login')} />
{:else}
  <Router />
{/if}

<Footer />
