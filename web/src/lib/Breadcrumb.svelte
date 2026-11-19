<!--
  Breadcrumb (#0153) — "Campaigns › Guides 2026-11" above a detail page's <h1>.
  The last crumb is the current page (no link, aria-current).
-->
<script lang="ts">
  import Link from './Link.svelte';

  interface Crumb {
    label: string;
    to?: string;
  }
  let { crumbs }: { crumbs: Crumb[] } = $props();
</script>

<nav class="breadcrumb" aria-label="Breadcrumb">
  <ol>
    {#each crumbs as crumb, i (i)}
      <li>
        {#if crumb.to && i < crumbs.length - 1}
          <Link to={crumb.to} class="crumb-link">{crumb.label}</Link>
        {:else}
          <span aria-current={i === crumbs.length - 1 ? 'page' : undefined}>{crumb.label}</span>
        {/if}
      </li>
    {/each}
  </ol>
</nav>

<style>
  .breadcrumb ol {
    list-style: none;
    margin: 0 0 var(--space-2);
    padding: 0;
    display: flex;
    flex-wrap: nowrap;
    align-items: center;
    gap: var(--space-1);
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  li {
    white-space: nowrap;
    flex: 0 0 auto;
  }
  /* The current page's crumb is the one that shrinks, with an ellipsis. */
  li:last-child {
    flex: 0 1 auto;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  li + li::before {
    content: '\203A';
    margin-right: var(--space-1);
    color: var(--text-faint);
  }
  li span {
    color: var(--text);
  }
  :global(.crumb-link) {
    color: var(--text-muted);
    text-decoration: none;
  }
  :global(.crumb-link:hover) {
    color: var(--accent);
    text-decoration: underline;
  }
</style>
