<!--
  The "⋯" row menu used on the Account page (#0170): one button that opens a
  small list of actions, closed by Escape, an outside click, or choosing one.
-->
<script lang="ts">
  interface Item {
    label: string;
    onselect: () => void;
    danger?: boolean;
    disabled?: boolean;
  }
  interface Props {
    label: string;
    items: Item[];
    busy?: boolean;
  }
  let { label, items, busy = false }: Props = $props();
  const uid = $props.id();
  const menuId = `row-menu-${uid}`;

  let open = $state(false);
  let root: HTMLElement | undefined = $state();

  function onWindowClick(e: MouseEvent) {
    if (open && root && !root.contains(e.target as Node)) open = false;
  }
  function onKey(e: KeyboardEvent) {
    if (open && e.key === 'Escape') {
      open = false;
      root?.querySelector<HTMLButtonElement>('.trigger')?.focus();
    }
  }
</script>

<svelte:window onclick={onWindowClick} onkeydown={onKey} />

<span class="row-menu" bind:this={root}>
  <button
    type="button"
    class="trigger"
    aria-controls={menuId}
    aria-expanded={open}
    aria-label={label}
    disabled={busy}
    onclick={() => (open = !open)}
  >
    {busy ? '…' : '⋯'}
  </button>
  {#if open}
    <ul class="menu" id={menuId}>
      {#each items as item (item.label)}
        <li>
          <button
            type="button"
            class:danger={item.danger}
            disabled={item.disabled}
            onclick={() => {
              open = false;
              // Refocus the ⋯ button now: this item is about to unmount, and a
              // dialog opened by onselect (#0160) returns focus to the trigger.
              root?.querySelector<HTMLButtonElement>('.trigger')?.focus();
              item.onselect();
            }}
          >
            {item.label}
          </button>
        </li>
      {/each}
    </ul>
  {/if}
</span>

<style>
  .row-menu {
    position: relative;
    display: inline-block;
  }
  .trigger {
    font-family: var(--font);
    font-size: var(--fs-base);
    line-height: 1;
    min-width: 2.25rem;
    min-height: 2.25rem;
    border: var(--border-w) solid transparent;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .trigger:hover:not(:disabled),
  .trigger[aria-expanded='true'] {
    background: var(--bg-subtle);
    border-color: var(--border);
    color: var(--text);
  }
  .menu {
    position: absolute;
    right: 0;
    top: calc(100% + 2px);
    z-index: 20;
    list-style: none;
    margin: 0;
    padding: var(--space-1);
    min-width: 9rem;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 4px 12px rgba(0, 0, 0, 0.18);
  }
  .menu button {
    display: block;
    width: 100%;
    text-align: left;
    font-family: var(--font);
    font-size: var(--fs-base);
    padding: var(--space-2) var(--space-3);
    border: 0;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text);
    cursor: pointer;
  }
  .menu button:hover:not(:disabled) {
    background: var(--bg-subtle);
  }
  .menu button.danger {
    color: var(--danger);
  }
  .menu button:disabled {
    color: var(--text-faint);
    cursor: not-allowed;
  }
</style>
