<script lang="ts">
  import { barPercent } from './dataTable';

  interface Props {
    value: number;
    /** Column maximum; the bar is value/max of the cell width. */
    max: number;
  }
  let { value, max }: Props = $props();

  const pct = $derived(barPercent(value, max));
</script>

<span class="inline-bar">
  {#if pct > 0}<span class="bar" style:width="{pct}%" aria-hidden="true"></span>{/if}
  <span class="num">{value.toLocaleString('en-US')}</span>
</span>

<style>
  .inline-bar {
    position: relative;
    display: block;
    text-align: right;
    padding: 2px var(--space-2);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--accent-subtle);
    border-right: 2px solid var(--accent);
    opacity: 0.9;
  }
  .num {
    position: relative;
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  /* Phone cards: the number sits in a narrow cell, so drop the bar behind it. */
  @media (max-width: 899px) {
    .bar { display: none; }
  }
</style>
