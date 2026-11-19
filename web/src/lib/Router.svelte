<!--
  Router.svelte (#0120) — renders the component for the current route.

  `{#key route.path}` forces a full destroy/remount whenever the PATH changes
  (a query-only change, e.g. the /login?redirect=… case, does not remount —
  route.path excludes the query). This is what makes "detail views fetch by
  route param on a cold load" true even for a HOT change between two detail
  screens of the same kind (e.g. one link's detail to another's, if that ever
  becomes reachable): the component instance is destroyed and a fresh one
  mounted with the new params, so onMount always re-fires rather than relying
  on a $props()-change effect. It is also what gives Dashboard's SSE
  subscription (opened in its own onMount, closed via the onMount cleanup
  return) a guaranteed unmount the moment the route navigates away from '/' —
  see Dashboard.svelte's onMount comment.

  route.component is typed `unknown` in hashRouter.svelte.ts (the route table
  holds different view components with different prop shapes, and this
  hand-rolled router deliberately doesn't reach for a heavier generic-component
  type for that) — cast once here, at the single place it is rendered.
-->
<script lang="ts">
  import { untrack } from 'svelte';
  import { getRoute } from './hashRouter.svelte';
  import LazyView from './LazyView.svelte';
  import { cachedComponent, isLazyRef, loadComponent } from './routeViews';

  const target = $derived(getRoute());

  // The route actually on screen (#0186). A route whose lazy chunk is not in
  // yet does not replace the current view until the chunk arrives, so moving
  // around the app never flashes a "Loading…" placeholder. $effect.pre makes
  // the first value land before the first render. A failed chunk still
  // switches: LazyView then shows its failure state (the once-per-URL reload
  // has already started inside loadComponent).
  let route = $state.raw(untrack(() => getRoute()));
  $effect.pre(() => {
    const next = target;
    const c = next.component;
    if (!isLazyRef(c) || cachedComponent(c.lazy)) {
      route = next;
      return;
    }
    let live = true;
    void loadComponent(c.lazy).then(() => {
      if (live) route = next;
    });
    return () => {
      live = false;
    };
  });
</script>

{#key route.path}
  {#if isLazyRef(route.component)}
    <!-- A lazy view (#0186): its chunk loads on first use; see routeViews.ts. -->
    <LazyView view={route.component.lazy} {...route.params} />
  {:else if route.component}
    {@const Comp = route.component as any}
    <Comp {...route.params} />
  {/if}
{/key}
