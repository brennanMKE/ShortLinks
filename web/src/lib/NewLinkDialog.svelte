<!--
  NewLinkDialog (#0158) — the centred modal that "+ New link" (and the `n`
  shortcut, and pasting a URL on the Links page) opens on desktop. A native
  <dialog> gives focus trapping, Escape-to-close and a backdrop for free.
  Mounted once, by AppShell. Phones use the #/new page instead (see
  newLinkDialog.ts).
-->
<script lang="ts">
  import { newLinkDialog, closeNewLink } from './newLinkDialog';
  import NewLinkForm from './NewLinkForm.svelte';

  let dlg = $state<HTMLDialogElement | null>(null);
  const open = $derived($newLinkDialog);

  // Open the native modal when state appears; close it when state clears.
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) dlg.showModal();
    if (!open && dlg.open) dlg.close();
  });

  function onBackdrop(e: MouseEvent) {
    if (e.target === dlg) closeNewLink();
  }
</script>

<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
<dialog
  bind:this={dlg}
  class="nl-dialog"
  aria-labelledby="new-link-title"
  onclose={closeNewLink}
  onclick={onBackdrop}
>
  {#if open}
    <div class="nl-body">
      <NewLinkForm initialUrl={open.url} onclose={closeNewLink} />
    </div>
  {/if}
</dialog>

<style>
  .nl-dialog {
    width: min(560px, calc(100vw - 2 * var(--space-4)));
    max-height: calc(100vh - 2 * var(--space-5));
    margin: auto;
    padding: 0;
    background: var(--bg-panel);
    color: var(--text);
    border: var(--border-w) solid var(--border-strong);
    border-radius: 8px;
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.3);
    overflow-y: auto;
  }
  /* Padding lives here, not on the <dialog>: a click in the dialog's own padding
     would otherwise have e.target === dlg and read as a backdrop click. */
  .nl-body {
    padding: var(--space-5);
  }
  .nl-dialog::backdrop {
    background: rgba(0, 0, 0, 0.45);
  }
</style>
