<!--
  CampaignRange (#0164, meets #0124) — the campaign page's date range:
  Campaign dates (the default), last 7 days, last 30 days or a custom from/to.
  Every choice is a hash link, so the range survives a reload and a pasted
  link, and Back undoes it. Days are UTC days (the campaign stats resolve
  their window in UTC); the label under the control says so.
-->
<script lang="ts">
  import type { CampaignView, RangeKey } from './campaignView';

  interface Props {
    view: CampaignView;
    /** Hash href (with '#') for a preset range. */
    hrefFor: (range: Exclude<RangeKey, 'custom'>) => string;
    /** Applies a custom window. `to` may be ''. */
    onapply: (from: string, to: string) => void;
    /** Latest selectable day, YYYY-MM-DD. */
    today: string;
  }
  let { view, hrefFor, onapply, today }: Props = $props();

  const PRESETS: { key: Exclude<RangeKey, 'custom'>; label: string; short: string }[] = [
    { key: 'campaign', label: 'Campaign dates', short: 'Campaign' },
    { key: '7d', label: 'Last 7 days', short: '7d' },
    { key: '30d', label: 'Last 30 days', short: '30d' },
  ];

  let open = $state(false);
  let from = $state('');
  let to = $state('');
  $effect(() => {
    if (view.range === 'custom') {
      from = view.from;
      to = view.to;
    }
  });
  const valid = $derived(from !== '' && from <= today && (to === '' || (to >= from && to <= today)));
  function apply(e: Event) {
    e.preventDefault();
    if (!valid) return;
    onapply(from, to);
    open = false;
  }
</script>

<div class="range-wrap">
  <div class="range" role="group" aria-label="Date range">
    {#each PRESETS as p (p.key)}
      <a
        class="seg"
        class:on={view.range === p.key}
        href={hrefFor(p.key)}
        aria-current={view.range === p.key ? 'true' : undefined}
        aria-label={p.label}><span class="long">{p.label}</span><span class="short">{p.short}</span></a
      >
    {/each}
    <button
      type="button"
      class="seg"
      class:on={view.range === 'custom'}
      aria-pressed={view.range === 'custom'}
      aria-expanded={open}
      onclick={() => (open = !open)}>Custom</button
    >
  </div>
  {#if open}
    <form class="custom" onsubmit={apply}>
      <label>From<input type="date" bind:value={from} max={today} required /></label>
      <label>To<input type="date" bind:value={to} max={today} min={from || undefined} /></label>
      <button type="submit" class="btn-link btn-link-primary" disabled={!valid}>Apply</button>
    </form>
  {/if}
</div>

<style>
  .range-wrap {
    position: relative;
  }
  .range {
    display: inline-flex;
    align-items: center;
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    overflow: hidden;
    background: var(--bg-panel);
  }
  .seg {
    display: inline-flex;
    align-items: center;
    min-height: 34px;
    padding: 0 var(--space-3);
    background: none;
    border: none;
    color: var(--text);
    font: inherit;
    text-decoration: none;
    cursor: pointer;
    white-space: nowrap;
  }
  .seg + .seg {
    border-left: var(--border-w) solid var(--border);
  }
  .seg:hover {
    background: var(--bg-subtle);
  }
  .seg.on {
    background: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .short {
    display: none;
  }
  .custom {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: var(--space-3);
    margin-top: var(--space-2);
    padding: var(--space-3);
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .custom label {
    margin: 0;
    flex: 0 1 11rem;
  }
  .custom input {
    margin-top: var(--space-1);
  }
  .custom .btn-link:disabled {
    opacity: 0.5;
    cursor: default;
  }
  @media (max-width: 899px) {
    .seg {
      min-height: 40px;
    }
  }
  @media (max-width: 560px) {
    .long {
      display: none;
    }
    .short {
      display: inline;
    }
  }
</style>
