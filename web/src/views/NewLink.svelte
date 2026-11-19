<!--
  NewLink view (#0153, reworked by #0158) — the full-page create-link form at
  #/new. Phones land here from "+ New link"; on desktop the same form opens as
  a dialog (lib/NewLinkDialog.svelte) and this route is a direct-link fallback.
  `?url=` prefills the destination. Cancel and Done go back to the Links page.
-->
<script lang="ts">
  import { getRoute, navigate } from '../lib/hashRouter.svelte';
  import NewLinkForm from '../lib/NewLinkForm.svelte';
  import Panel from '../lib/Panel.svelte';

  const initialUrl = getRoute().query.get('url') ?? '';
</script>

<div class="app-shell">
  <h1 class="page-title">New link</h1>
  <Panel>
    <NewLinkForm mode="page" {initialUrl} onclose={() => navigate('/links')} />
  </Panel>
</div>
