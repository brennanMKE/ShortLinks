<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser } from './lib/stores';
  import { getMe, ApiError } from './lib/api';
  import Login from './views/Login.svelte';
  import Dashboard from './views/Dashboard.svelte';
  import LinkDetail from './views/LinkDetail.svelte';
  import Account from './views/Account.svelte';
  import Admin from './views/Admin.svelte';

  let loading = $state(true);

  // On load, GET /api/me decides the initial view: a valid session lands on the
  // dashboard, a 401 falls back to login. The profile also gates the admin tab
  // (currentUser.is_admin), per the PRD.
  onMount(async () => {
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
{/if}
