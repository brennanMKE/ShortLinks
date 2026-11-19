<!--
  AuthBrand (#0172) — the app icon above the product name, shared by every
  signed-out screen (Login, the verify screens, the signed-out Home).
-->
<script lang="ts">
  import { APP_NAME } from './branding';

  let { tagline = '' }: { tagline?: string } = $props();
</script>

<header class="brand">
  <img src="/apple-touch-icon.png?v=3" alt="" width="56" height="56" class="brand-icon" />
  <h1 class="brand-title">{APP_NAME}</h1>
  {#if tagline}<p class="text-muted tagline">{tagline}</p>{/if}
</header>

<style>
  .brand {
    text-align: center;
    margin-bottom: var(--space-5);
  }
  .brand-icon {
    display: block;
    margin: 0 auto var(--space-2);
    border-radius: 13px;
  }
  .brand-title {
    font-size: var(--fs-xl);
    font-weight: 600;
    margin: 0;
  }
  .tagline {
    margin: var(--space-1) 0 0;
  }
</style>
