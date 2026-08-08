<!--
  UtmField — one labeled input in the UTM builder, sharing its copy (label,
  placeholder, persistent hint, optional-reason) between Dashboard.svelte's
  create builder and LinkDetail.svelte's edit builder (#0095). The two
  builders bind this to different `$state` objects (create's `utmParams`,
  edit's `editUtmParams`), so this component owns presentation only — no
  UTM composition/validation logic lives here, matching #0095's
  presentation-only scope and utm.ts's existing behavior untouched.
-->
<script lang="ts">
  import type { UtmKey } from './utm';
  import { UTM_FIELD_GUIDES } from './utmFieldGuide';

  interface Props {
    /** Which of the five UTM keys this input edits. */
    fieldKey: UtmKey;
    /** DOM id for the input, so the two builders can keep distinct ids (e.g. "utm-source" vs "edit-utm-source"). */
    id: string;
    value: string;
    disabled?: boolean;
  }

  let { fieldKey, id, value = $bindable(''), disabled = false }: Props = $props();

  const guide = $derived(UTM_FIELD_GUIDES[fieldKey]);
  const hintId = $derived(`${id}-hint`);
</script>

<div class="field">
  <label for={id}>
    {guide.label}
    <span class="text-faint"
      >({fieldKey}{guide.optionalReason ? `, optional — ${guide.optionalReason}` : ''})</span
    >
  </label>
  <input {id} type="text" placeholder={guide.placeholder} bind:value {disabled} aria-describedby={hintId} />
  <p id={hintId} class="text-muted utm-hint">{guide.hint}</p>
</div>

<style>
  .utm-hint {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
    line-height: 1.4;
  }
</style>
