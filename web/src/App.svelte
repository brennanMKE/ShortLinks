<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, pendingVerifyToken } from './lib/stores';
  import { getMe, ApiError } from './lib/api';
  import Login from './views/Login.svelte';
  import Dashboard from './views/Dashboard.svelte';
  import LinkDetail from './views/LinkDetail.svelte';
  import Account from './views/Account.svelte';
  import Admin from './views/Admin.svelte';
  import RegisterVerify from './views/RegisterVerify.svelte';
  import RecoverVerify from './views/RecoverVerify.svelte';

  let loading = $state(true);

  // On load, check the current URL path first.
  //
  // Magic-link landing paths (/register/verify and /recover/verify) arrive here
  // via the SPA catch-all in the Go mux ("GET /" serves index.html for every
  // unmatched non-API path). We detect them before calling /api/me so that a
  // user who follows an email link without an active session is routed to the
  // correct verification view rather than bounced straight to login (#0041).
  //
  // For all other paths, GET /api/me decides the initial view: a valid session
  // lands on the dashboard, a 401 falls back to login. The profile also gates
  // the admin tab (currentUser.is_admin), per the PRD.
  onMount(async () => {
    const path = window.location.pathname;

    if (path === '/register/verify') {
      const params = new URLSearchParams(window.location.search);
      const token = params.get('token');
      pendingVerifyToken.set(token);
      currentView.set('register-verify');
      loading = false;
      return;
    }

    if (path === '/recover/verify') {
      const params = new URLSearchParams(window.location.search);
      const token = params.get('token');
      pendingVerifyToken.set(token);
      currentView.set('recover-verify');
      loading = false;
      return;
    }

    try {
      const user = await getMe();
      currentUser.set(user);
      currentView.set('dashboard');
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
      } else {
        // Any other failure (network/server) also lands on login; the user can
        // retry the passkey ceremony from there.
        currentUser.set(null);
        currentView.set('login');
      }
    } finally {
      loading = false;
    }
  });
</script>

{#if loading}
  <p>Loading…</p>
{:else if $currentView === 'login'}
  <Login />
{:else if $currentView === 'dashboard'}
  <Dashboard />
{:else if $currentView === 'link-detail'}
  <LinkDetail />
{:else if $currentView === 'account'}
  <Account />
{:else if $currentView === 'admin'}
  <Admin />
{:else if $currentView === 'register-verify'}
  <RegisterVerify />
{:else if $currentView === 'recover-verify'}
  <RecoverVerify />
{/if}
