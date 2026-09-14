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
  import { getRoute } from './hashRouter.svelte';

  const route = $derived(getRoute());
</script>

{#key route.path}
  {#if route.component}
    {@const Comp = route.component as any}
    <Comp {...route.params} />
  {/if}
{/key}
