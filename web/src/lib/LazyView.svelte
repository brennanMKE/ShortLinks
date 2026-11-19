<script lang="ts">
  // Renders a lazily loaded view by its routeViews key (#0186), passing every
  // other prop through to it. While the chunk loads it shows a small
  // placeholder; if the chunk fails and the once-per-URL reload has already
  // been spent, it shows a reload prompt instead of a blank page.
  import Button from './Button.svelte';
  import { cachedComponent, clearChunkRecovery, defaultDeps, loadComponent } from './routeViews';
  import { reloadToSameURL } from './versionCheck';

  let { view, ...rest }: { view: string; [prop: string]: unknown } = $props();

  // $effect.pre so a cached view is set before the first paint: no
  // placeholder flash for a chunk that is already in.
  let View = $state<ReturnType<typeof cachedComponent>>(null);
  let failed = $state(false);

  $effect.pre(() => {
    const k = view;
    const hit = cachedComponent(k);
    failed = false;
    if (hit) {
      View = hit;
      return;
    }
    View = null;
    let live = true;
    void loadComponent(k).then((c) => {
      if (!live) return;
      if (c) View = c;
      else failed = true;
    });
    return () => {
      live = false;
    };
  });

  function retry() {
    clearChunkRecovery(defaultDeps);
    reloadToSameURL();
  }
</script>

{#if View}
  <View {...rest} />
{:else if failed}
  <div class="app-shell" role="alert">
    <h1 class="page-title">This page didn't load</h1>
    <p>Your connection may have dropped, or the site was just updated.</p>
    <Button onclick={retry}>Reload</Button>
  </div>
{:else}
  <div class="app-shell">
    <p class="text-muted" role="status">Loading…</p>
  </div>
{/if}
