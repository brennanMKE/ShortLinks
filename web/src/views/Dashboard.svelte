<!--
  Dashboard view (#0033) — TEMPORARY after #0153. The create form and the
  links table now live in NewLink.svelte (#/new) and LinksList.svelte
  (#/links); this view keeps showing both at #/ until #0162 replaces it with
  the Overview. The header, nav tabs and sign-out moved to lib/AppShell.svelte.
-->
<script lang="ts">
  import NewLink from './NewLink.svelte';
  import LinksList from './LinksList.svelte';
</script>

<div class="app-shell overview-head">
  <h1 class="page-title">Overview</h1>
</div>
<NewLink showTitle={false} />
<LinksList showTitle={false} />

<style>
  .overview-head {
    padding-bottom: 0;
  }
  .overview-head .page-title {
    margin: 0;
  }
</style>
