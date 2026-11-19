<script lang="ts">
  import { barPercent } from './dataTable';

  interface Props {
    value: number;
    /** Column maximum; the bar is value/max of the cell width. */
    max: number;
    /** Keep the bar on phones (default hides it below 900px for table cards). */
    alwaysShowBar?: boolean;
  }
  let { value, max, alwaysShowBar = false }: Props = $props();

  const pct = $derived(barPercent(value, max));
</script>

<span class="inline-bar" class:always={alwaysShowBar}>
  {#if pct > 0}<span class="bar" style:width="{pct}%" aria-hidden="true"></span>{/if}
  <span class="num">{value.toLocaleString('en-US')}</span>
</span>

<style>
  .inline-bar {
    position: relative;
    display: block;
    text-align: right;
    padding: 2px calc(var(--space-2) + 4px) 2px var(--space-2);
    border-radius: 2px;
    overflow: hidden;
  }
  .bar {
    position: absolute;
    inset: 0 auto 0 0;
    background: var(--accent-subtle);
    opacity: 0.9;
  }
  .num {
    position: relative;
    font-variant-numeric: tabular-nums;
    font-weight: 600;
  }
  /* Phone cards: the number sits in a narrow cell, so drop the bar behind it. */
  @media (max-width: 899px) {
    .inline-bar:not(.always) .bar { display: none; }
  }
</style>
