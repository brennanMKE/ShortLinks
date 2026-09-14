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
  import { APP_NAME } from '../lib/branding';
</script>

<div class="app-shell">
  <header class="app-header">
    <h1 class="app-title">{APP_NAME}</h1>
  </header>

  <Panel title="Page not found">
    <p class="text-muted">There's nothing here. The link may be mistyped or out of date.</p>
    <Link to="/" class="btn-link btn-link-primary">Back to dashboard</Link>
  </Panel>
</div>
