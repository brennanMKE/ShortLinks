<!--
  Dashboard view (#0033) — TEMPORARY after #0153. The create form is now a
  dialog (#0158) and the links table lives in LinksList.svelte (#/links);
  this view keeps showing the table at #/ until #0162 replaces it with
  the Overview. The header, nav tabs and sign-out moved to lib/AppShell.svelte.
-->
<script lang="ts">
  import LinksList from './LinksList.svelte';
</script>

<div class="app-shell overview-head">
  <h1 class="page-title">Overview</h1>
</div>
<LinksList showTitle={false} />

<style>
  .overview-head {
    padding-bottom: 0;
  }
  .overview-head .page-title {
    margin: 0;
  }
</style>
