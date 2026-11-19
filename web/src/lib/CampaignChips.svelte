<!--
  CampaignChips (#0162) — the Overview's single-select campaign focus row.
  "All" first, then campaigns by clicks, "No campaign" last; after five chips
  the rest sit behind a More menu (the selected one is always promoted into
  the row). On phones the row scrolls sideways instead of wrapping. Chips are
  real anchors (hash links), so Back undoes a focus change and the URL is
  shareable; the owner builds the hrefs so this component knows nothing about
  the rest of the query.
-->
<script lang="ts">
  import { tick } from 'svelte';
  import type { Chip } from './overview';
  import { layoutChips } from './overview';
  import { nextMenuIndex } from './rowMenu';

  interface Props {
    chips: Chip[];
    selected: string;
    /** Hash path (starting with '/') that selects `value`; '' selects All. */
    hrefFor: (value: string) => string;
  }

  let { chips, selected, hrefFor }: Props = $props();

  const layout = $derived(layoutChips(chips, selected));
  let moreOpen = $state(false);
  let moreWrap = $state<HTMLElement | null>(null);

  let moreBtn = $state<HTMLButtonElement | null>(null);

  function items(): HTMLElement[] {
    return moreWrap ? Array.from(moreWrap.querySelectorAll<HTMLElement>('[role="menuitem"]')) : [];
  }
  async function toggleMore() {
    moreOpen = !moreOpen;
    if (moreOpen) {
      await tick();
      items()[0]?.focus();
    }
  }
  function closeMore(refocus: boolean) {
    moreOpen = false;
    if (refocus) moreBtn?.focus();
  }
  function onWindowClick(e: MouseEvent) {
    if (moreOpen && moreWrap && !moreWrap.contains(e.target as Node)) moreOpen = false;
  }
  function onMenuKey(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.preventDefault();
      closeMore(true);
      return;
    }
    if (e.key === 'Tab') {
      closeMore(false);
      return;
    }
    const els = items();
    const next = nextMenuIndex(e.key, els.indexOf(document.activeElement as HTMLElement), els.length);
    if (next !== null) {
      e.preventDefault();
      els[next]?.focus();
    }
  }
  function onBtnKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' && !moreOpen) {
      e.preventDefault();
      toggleMore();
    }
  }
</script>

<svelte:window onclick={onWindowClick} />

<div class="chips" role="group" aria-label="Focus on a campaign">
  <a class="chip" class:on={selected === ''} href={'#' + hrefFor('')} aria-current={selected === '' ? "true" : undefined}>All</a>
  {#each layout.visible as c (c.value)}
    <a
      class="chip"
      class:on={selected === c.value}
      href={'#' + hrefFor(c.value)}
      aria-current={selected === c.value ? "true" : undefined}
      title="{c.label} · {c.clicks.toLocaleString()} clicks"
    >
      <span class="chip-name">{c.label}</span>
      <span class="chip-count">{c.clicks.toLocaleString()}</span>
    </a>
  {/each}
  {#if layout.more.length > 0}
    <div class="more" bind:this={moreWrap}>
      <button
        type="button"
        class="chip more-btn"
        bind:this={moreBtn}
        aria-haspopup="menu"
        aria-expanded={moreOpen}
        onkeydown={onBtnKey}
        onclick={toggleMore}>More ({layout.more.length}) ▾</button
      >
      {#if moreOpen}
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <ul class="more-menu" role="menu" onkeydown={onMenuKey}>
          {#each layout.more as c (c.value)}
            <li role="none">
              <a role="menuitem" tabindex="-1" href={'#' + hrefFor(c.value)} onclick={() => (moreOpen = false)}>
                <span class="chip-name">{c.label}</span>
                <span class="chip-count">{c.clicks.toLocaleString()}</span>
              </a>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>

<style>
  .chips {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--space-2);
    padding: 2px 0 var(--space-1);
  }
  .chips::-webkit-scrollbar {
    display: none;
  }
  .chip {
    flex: 0 0 auto;
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    min-height: 32px;
    padding: 0 var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: 16px;
    background: var(--bg-panel);
    color: var(--text);
    font: inherit;
    font-size: var(--fs-base);
    text-decoration: none;
    cursor: pointer;
    white-space: nowrap;
  }
  .chip:hover {
    background: var(--bg-subtle);
  }
  .chip.on {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .chip-name {
    max-width: 16ch;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .chip-count {
    font-size: var(--fs-sm);
    font-variant-numeric: tabular-nums;
    color: var(--text-muted);
  }
  .chip.on .chip-count {
    color: inherit;
    opacity: 0.85;
  }
  .more {
    position: relative;
    flex: 0 0 auto;
  }
  .more-menu {
    position: absolute;
    z-index: 20;
    top: calc(100% + 4px);
    left: 0;
    min-width: 220px;
    margin: 0;
    padding: var(--space-1) 0;
    list-style: none;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 6px 18px rgba(0, 0, 0, 0.18);
  }
  .more-menu a {
    display: flex;
    justify-content: space-between;
    gap: var(--space-3);
    padding: var(--space-2) var(--space-3);
    min-height: 36px;
    align-items: center;
    color: var(--text);
    text-decoration: none;
  }
  .more-menu a:hover {
    background: var(--bg-subtle);
  }
  @media (max-width: 899px) {
    .chip {
      min-height: 40px;
    }
  }
  @media (max-width: 600px) {
    .chips {
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
      -webkit-overflow-scrolling: touch;
    }
    /* The chip row scrolls, so a popover inside it would be clipped: the More menu opens as a fixed sheet. */
    .more-menu {
      position: fixed;
      left: var(--space-3);
      right: var(--space-3);
      top: auto;
      bottom: var(--space-3);
      max-height: 60vh;
      overflow-y: auto;
    }
  }
</style>
