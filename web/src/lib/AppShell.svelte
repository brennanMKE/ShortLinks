<!--
  AppShell (#0153) — the one header every signed-in screen shares, rendered by
  App.svelte around <Router />. Replaces the header/nav copies that Dashboard,
  CampaignsList, Account and Admin each carried (and the bare "Link detail" /
  "Campaign detail" headers on the detail pages).

  Desktop: brand, tabs (Overview, Links, Campaigns, Admin for admins), a
  "+ New link" button and an avatar menu (Account, Sign out). Phone: row 1 is
  brand, + and menu; row 2 is a horizontally scrolling tab strip.
  Keyboard: `n` opens New link when focus isn't in a text field.
  See docs/ui-redesign.md section 3.1.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { currentUser, links } from './stores';
  import { logout } from './api';
  import { navigate, replace } from './hashRouter.svelte';
  import Link from './Link.svelte';
  import { APP_NAME } from './branding';
  import { navTabs, avatarInitial, isNewLinkShortcut } from './shell';

  let { children }: { children: Snippet } = $props();

  const tabs = $derived(navTabs($currentUser?.is_admin === true));
  let menuOpen = $state(false);
  let menuWrap = $state<HTMLElement | null>(null);
  let avatarBtn = $state<HTMLButtonElement | null>(null);

  async function handleSignOut() {
    menuOpen = false;
    try {
      await logout();
    } catch {
      // Drop local state and return to login even on failure.
    }
    currentUser.set(null);
    links.set([]);
    // Explicit sign-out: replace straight to a bare /login with no redirect
    // target, distinct from an involuntary 401 where clearing currentUser lets
    // App.svelte's guard effect preserve the current route.
    replace('/login');
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && menuOpen) {
      menuOpen = false;
      avatarBtn?.focus();
      return;
    }
    // Skip key auto-repeat and any open modal dialog (an `n` typed there
    // must not navigate away from it).
    if (e.repeat || document.querySelector('dialog[open], [aria-modal="true"]')) return;
    if (isNewLinkShortcut(e)) {
      e.preventDefault();
      navigate('/new');
    }
  }

  function onWindowClick(e: MouseEvent) {
    if (menuOpen && menuWrap && !menuWrap.contains(e.target as Node)) menuOpen = false;
  }
</script>

<svelte:window onkeydown={onKeydown} onclick={onWindowClick} />

<div class="shell-bar">
  <header class="shell-inner">
    <Link to="/" class="shell-brand" aria-label="{APP_NAME} home">
      <img src="/favicon.png?v=3" alt="" width="24" height="24" class="brand-mark" />
      <span class="brand-name">{APP_NAME}</span>
    </Link>

    <nav class="shell-tabs" aria-label="Primary">
      {#each tabs as tab (tab.to)}
        <Link to={tab.to} class="nav-tab">{tab.label}</Link>
      {/each}
    </nav>

    <Link to="/new" class="new-link-btn" title="New link (n)" aria-label="New link">
      <span class="plus" aria-hidden="true">+</span><span class="new-label">New link</span>
    </Link>

    <div class="avatar-wrap" bind:this={menuWrap}>
      <button
        type="button"
        class="avatar"
        bind:this={avatarBtn}
        aria-expanded={menuOpen}
        aria-controls="account-menu"
        aria-label="Account menu"
        title={$currentUser?.email}
        onclick={() => (menuOpen = !menuOpen)}
      >{avatarInitial($currentUser?.email)}</button>
      {#if menuOpen}
        <div class="menu" id="account-menu">
          <p class="menu-email">{$currentUser?.email}</p>
          <a class="menu-item" href="#/account" onclick={() => (menuOpen = false)}>Account</a>
          <button type="button" class="menu-item" onclick={handleSignOut}>Sign out</button>
        </div>
      {/if}
    </div>
  </header>
</div>

<main class="shell-main">
  {@render children()}
</main>

<style>
  .shell-bar {
    background: var(--bg-panel);
    border-bottom: var(--border-w) solid var(--border);
  }
  .shell-inner {
    max-width: 1040px;
    margin: 0 auto;
    padding: var(--space-2) var(--space-4);
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  :global(.shell-brand) {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    color: var(--text);
    text-decoration: none;
    font-size: var(--fs-lg);
    font-weight: 600;
    white-space: nowrap;
  }
  .brand-mark {
    border-radius: 5px;
  }
  .shell-tabs {
    display: flex;
    gap: var(--space-1);
    flex: 1;
    min-width: 0;
  }
  :global(.new-link-btn) {
    display: inline-flex;
    align-items: center;
    min-height: 36px;
    padding: 0 var(--space-3);
    border-radius: var(--radius);
    background: var(--accent);
    color: var(--accent-text);
    text-decoration: none;
    font-size: var(--fs-base);
    font-weight: 600;
    white-space: nowrap;
  }
  :global(.new-link-btn:hover) {
    background: var(--accent-hover);
  }
  .plus {
    font-size: var(--fs-lg);
    line-height: 1;
    margin-right: var(--space-1);
  }
  .avatar-wrap {
    position: relative;
  }
  .avatar {
    width: 36px;
    height: 36px;
    border-radius: 50%;
    border: var(--border-w) solid var(--border-strong);
    background: var(--bg-subtle);
    color: var(--text);
    font-family: var(--font);
    font-size: var(--fs-base);
    font-weight: 600;
    cursor: pointer;
  }
  .avatar:hover,
  .avatar[aria-expanded='true'] {
    background: var(--bg-header);
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + var(--space-1));
    z-index: 20;
    min-width: 200px;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.18);
    padding: var(--space-1);
    display: flex;
    flex-direction: column;
  }
  .menu-email {
    margin: 0;
    padding: var(--space-2) var(--space-3);
    font-size: var(--fs-sm);
    color: var(--text-muted);
    border-bottom: var(--border-w) solid var(--border);
    overflow-wrap: anywhere;
  }
  .menu-item {
    display: flex;
    align-items: center;
    min-height: 40px;
    padding: 0 var(--space-3);
    background: none;
    border: none;
    border-radius: var(--radius);
    color: var(--text);
    font-family: var(--font);
    font-size: var(--fs-base);
    text-align: left;
    text-decoration: none;
    cursor: pointer;
  }
  .menu-item:hover {
    background: var(--bg-subtle);
  }

  @media (max-width: 600px) {
    .shell-inner {
      flex-wrap: wrap;
      padding: var(--space-2) var(--space-3);
      row-gap: var(--space-1);
    }
    /* Row 1: brand grows, then + and the avatar. Row 2: the tab strip. */
    :global(.shell-brand) {
      flex: 1;
    }
    .new-label {
      display: none;
    }
    .plus {
      margin-right: 0;
    }
    :global(.new-link-btn) {
      width: 36px;
      justify-content: center;
      padding: 0;
    }
    .shell-tabs {
      order: 3;
      flex: 0 0 100%;
      overflow-x: auto;
      scrollbar-width: none;
    }
    .shell-tabs::-webkit-scrollbar {
      display: none;
    }
    .shell-tabs :global(.nav-tab) {
      flex: 0 0 auto;
      font-size: var(--fs-base);
      padding: var(--space-1) var(--space-3);
    }
  }
</style>
