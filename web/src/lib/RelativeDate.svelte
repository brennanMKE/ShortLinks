<script lang="ts">
  import { formatRelativeDate, formatTimestamp } from './relativeDate';

  interface Props {
    date: Date | string | number;
    /** Injected for tests; defaults to the current time. */
    now?: Date;
  }
  let { date, now }: Props = $props();

  const d = $derived(date instanceof Date ? date : new Date(date));
  const valid = $derived(!Number.isNaN(d.getTime()));
  const text = $derived(formatRelativeDate(d, now));
  const stamp = $derived(valid ? formatTimestamp(d) : '');
</script>

{#if valid}
  <time class="rel-date" datetime={d.toISOString()} title={stamp}>{text}</time>
{/if}

<style>
  .rel-date {
    white-space: nowrap;
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
</style>
