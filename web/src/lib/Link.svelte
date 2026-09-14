<!--
  Link.svelte (#0120) — every in-app navigation goes through this component,
  never a bare `<a href="/campaigns">` or a `<button onclick>`. It renders a
  REAL anchor with `href={'#' + to}`, so middle-click, cmd-click, and "copy
  link address" all behave correctly with no special-casing: a plain click
  changes `location.hash` exactly the way `navigate()` in hashRouter.svelte.ts
  does, so there is no click handler here at all — the browser's native
  anchor behavior IS the navigation.

  Adds an `active` class (and `aria-current="page"` for assistive tech) when
  the current route matches `to`: exact match for `/`, prefix match for
  everything else (isActiveLink, routing.ts) — so e.g. a "Campaigns" nav link
  stays active while viewing a specific campaign's detail screen.

  `class` and any other attribute (style, title, data-*, …) pass through to
  the rendered `<a>` via `rest`. A caller's own scoped styles can't reach it
  though — see app.css's ".nav-tab" comment for why, and reach for a global
  class or :global() instead.
-->
<script lang="ts">
  import type { Snippet } from 'svelte';
  import { getRoute } from './hashRouter.svelte';
  import { isActiveLink } from './routing';

  interface Props {
    to: string;
    class?: string;
    children?: Snippet;
    [key: string]: unknown;
  }

  let { to, class: className = '', children, ...rest }: Props = $props();

  const route = $derived(getRoute());
  const active = $derived(isActiveLink(route.path, to));
  const classes = $derived([className, active ? 'active' : ''].filter(Boolean).join(' '));
</script>

<a href={'#' + to} class={classes || undefined} aria-current={active ? 'page' : undefined} {...rest}>
  {#if children}{@render children()}{/if}
</a>
