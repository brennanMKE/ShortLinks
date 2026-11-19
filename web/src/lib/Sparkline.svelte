<!--
  Sparkline — 110×22 inline SVG trend line with no axes (#0161).

  Pass `days` (sparse DayBucket[]) with `from`/`to` (inclusive) to gap-fill
  the window, or omit `from`/`to` to plot `days` as given. With `partialLast`
  the final segment is dotted and the last point hollow. The accessible name
  carries the total, e.g. "Last 30 days: 142 clicks". No interactivity: it is
  a glanceable summary, the full chart carries the read-out.
-->
<script lang="ts">
  import type { DayBucket } from './types';
  import { pointsForWindow, toTimeseriesPoints } from './charts';

  interface Props {
    days: DayBucket[] | undefined | null;
    from?: string;
    to?: string;
    partialLast?: boolean;
    /** Accessible name prefix, e.g. "Last 30 days". */
    label?: string;
  }

  let { days, from, to, partialLast = false, label = 'Clicks' }: Props = $props();

  const W = 110;
  const H = 22;
  const PAD = 3;

  const pts = $derived(from && to ? pointsForWindow(days, from, to) : toTimeseriesPoints(days));
  const total = $derived(pts.reduce((s, p) => s + p.value, 0));
  const max = $derived(Math.max(1, ...pts.map((p) => p.value)));

  function x(i: number): number {
    return pts.length === 1 ? W / 2 : PAD + (i / (pts.length - 1)) * (W - PAD * 2);
  }
  function y(v: number): number {
    return PAD + (H - PAD * 2) - (v / max) * (H - PAD * 2);
  }
  const coord = (i: number) => `${x(i).toFixed(1)},${y(pts[i].value).toFixed(1)}`;

  const hollow = $derived(partialLast && pts.length > 1);
  const solid = $derived(pts.map((_, i) => coord(i)).slice(0, hollow ? -1 : undefined).join(' '));
  const tail = $derived(hollow ? `${coord(pts.length - 2)} ${coord(pts.length - 1)}` : '');
</script>

<svg
  class="spark"
  role="img"
  aria-label="{label}: {total.toLocaleString()} {total === 1 ? 'click' : 'clicks'}"
  viewBox="0 0 {W} {H}"
  width={W}
  height={H}
  xmlns="http://www.w3.org/2000/svg"
>
  {#if pts.length === 0}
    <line class="empty" x1={PAD} x2={W - PAD} y1={H - PAD} y2={H - PAD} />
  {:else}
    <polyline class="line" points={solid} />
    {#if hollow}
      <polyline class="line tail" points={tail} />
      <circle class="end hollow" cx={x(pts.length - 1)} cy={y(pts[pts.length - 1].value)} r="2.5" />
    {:else}
      <circle class="end" cx={x(pts.length - 1)} cy={y(pts[pts.length - 1].value)} r="2.2" />
    {/if}
  {/if}
</svg>

<style>
  .spark {
    display: inline-block;
    vertical-align: middle;
    flex: 0 0 auto;
    overflow: visible;
  }
  .line {
    fill: none;
    stroke: var(--accent);
    stroke-width: 1.5;
    stroke-linejoin: round;
    stroke-linecap: round;
  }
  .tail {
    stroke-dasharray: 1 3;
  }
  .end {
    fill: var(--accent);
  }
  .end.hollow {
    fill: var(--bg-panel);
    stroke: var(--accent);
    stroke-width: 1.5;
  }
  .empty {
    stroke: var(--border-strong);
    stroke-width: 1;
    stroke-dasharray: 2 3;
  }
</style>
