<!--
  NotFound — rendered by the router's catch-all '*' route (#0120) for any
  hash that doesn't match a declared route, e.g. #/nope. Must never be blank,
  crash, or loop: it renders a simple message and a real Link back to the
  dashboard (never a plain <button onclick>, per the rest of this app's
  post-#0120 navigation).
-->
<script lang="ts">
  import Panel from '../lib/Panel.svelte';
  import Link from '../lib/Link.svelte';
</script>

<div class="app-shell">
  <h1 class="page-title">Page not found</h1>
  <Panel title="Page not found">
    <p class="text-muted">There's nothing here. The link may be mistyped or out of date.</p>
    <Link to="/" class="btn-link btn-link-primary">Back to overview</Link>
  </Panel>
</div>
