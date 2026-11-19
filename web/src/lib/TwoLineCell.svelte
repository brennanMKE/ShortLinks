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
    /** Phone cards: let the title wrap to two lines instead of one. */
    wrap?: boolean;
    /** Wrap to two lines at every width (then ellipsize), e.g. a table whose titles must stay readable. */
    wrapAlways?: boolean;
  }
  let { title, meta = '', href, dim = false, tag = '', wrap = false, wrapAlways = false }: Props = $props();
</script>

<div class="two-line" class:dim class:wrap class:wrap-always={wrapAlways} class:linked={!!href}>
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
  /* Two-line clamp at every width: rows grow only when a title needs the room. */
  .wrap-always .line1 {
    display: -webkit-box;
    -webkit-box-orient: vertical;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    white-space: normal;
    overflow-wrap: anywhere;
    line-height: 1.3;
  }
  a.line1:hover { color: var(--accent); text-decoration: underline; }
  /* Phone: the whole two-line cell is the tap target for the title link (at least 40px),
     without changing how it looks. The overlay's containing block is .two-line, so the
     link's own overflow clipping does not cut it off. */
  @media (max-width: 899px) {
    .two-line.linked {
      position: relative;
      min-height: 40px;
      justify-content: center;
    }
    a.line1::after {
      content: '';
      position: absolute;
      inset: -4px 0;
    }
  }
  @media (max-width: 899px) {
    .wrap .line1 {
      white-space: normal;
      display: -webkit-box;
      -webkit-line-clamp: 2;
      line-clamp: 2;
      -webkit-box-orient: vertical;
    }
  }
  .line2 {
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  /* Dim by colour only (muted token, >=4.5:1), never opacity. */
  .dim .line1 { color: var(--text-muted); font-weight: 500; }
</style>
