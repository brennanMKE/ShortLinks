<script lang="ts">
  interface Props {
    title: string;
    /** Muted second line, e.g. "/u/key · /guides/path · mastodon". */
    meta?: string;
    /** Makes line 1 a link (hash route or URL). */
    href?: string;
    /** Dim the cell (inactive row). */
    dim?: boolean;
    /** Abnormal-state tag only (e.g. "Inactive"); never a normal-state pill. */
    tag?: string;
  }
  let { title, meta = '', href, dim = false, tag = '' }: Props = $props();
</script>

<div class="two-line" class:dim>
  <span class="l1row">
    {#if href}
      <a class="line1" {href} {title}>{title}</a>
    {:else}
      <span class="line1" {title}>{title}</span>
    {/if}
    {#if tag}<span class="tag">{tag}</span>{/if}
  </span>
  {#if meta}
    <span class="line2" title={meta}>{meta}</span>
  {/if}
</div>

<style>
  .two-line {
    min-width: 0;
    display: flex;
    flex-direction: column;
  }
  .l1row { display: flex; align-items: center; gap: var(--space-2); min-width: 0; }
  .l1row .tag { flex: none; }
  .line1,
  .line2 {
    display: block;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .line1 {
    font-weight: 600;
    color: var(--text);
    text-decoration: none;
  }
  a.line1:hover { color: var(--accent); text-decoration: underline; }
  .line2 {
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  /* Dim by colour only (muted token, >=4.5:1), never opacity. */
  .dim .line1 { color: var(--text-muted); font-weight: 500; }
</style>
