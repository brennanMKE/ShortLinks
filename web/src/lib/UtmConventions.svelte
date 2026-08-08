<!--
  UtmConventions — the single "lowercase, hyphens, no PII" line shown once at
  the top of the expanded UTM section (#0095), shared between Dashboard.svelte's
  create builder and LinkDetail.svelte's edit builder so its copy AND its
  styling live in one place — a byte-identical `.utm-conventions` CSS rule in
  both views was the same "next change updates one and misses the other"
  hazard the copy itself was extracted to avoid.
-->
<script lang="ts">
  import { UTM_CONVENTIONS_LINE } from './utmFieldGuide';
</script>

<p class="text-muted utm-conventions">{UTM_CONVENTIONS_LINE}</p>

<style>
  .utm-conventions {
    margin: 0 0 var(--space-3);
    font-size: var(--fs-sm);
    line-height: 1.4;
  }
</style>
