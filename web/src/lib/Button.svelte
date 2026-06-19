<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    variant?: 'default' | 'primary' | 'subtle' | 'danger';
    type?: 'button' | 'submit' | 'reset';
    disabled?: boolean;
    onclick?: (e: MouseEvent) => void;
    children: Snippet;
    [key: string]: unknown;
  }

  let {
    variant = 'default',
    type = 'button',
    disabled = false,
    onclick,
    children,
    ...rest
  }: Props = $props();
</script>

<button {type} {disabled} class="btn btn-{variant}" {onclick} {...rest}>
  {@render children()}
</button>

<style>
  .btn {
    font-family: var(--font);
    font-size: var(--fs-base);
    line-height: 1;
    padding: var(--space-2) var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-subtle);
    color: var(--text);
    cursor: pointer;
    user-select: none;
  }
  .btn:hover:not(:disabled) { background: var(--bg-header); }
  .btn:active:not(:disabled) { background: var(--border); }
  .btn:disabled { opacity: 0.5; cursor: default; }

  .btn-primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .btn-primary:hover:not(:disabled) { background: var(--accent-hover); border-color: var(--accent-hover); }

  .btn-subtle {
    background: transparent;
    border-color: transparent;
    color: var(--accent);
  }
  .btn-subtle:hover:not(:disabled) { background: var(--accent-subtle); }

  .btn-danger {
    background: var(--bg-subtle);
    border-color: var(--border-strong);
    color: var(--danger);
  }
  .btn-danger:hover:not(:disabled) { background: #fbeaea; }

  /* Increase tap target height on mobile (≥40px) without changing desktop layout */
  @media (max-width: 480px) {
    .btn {
      padding: var(--space-3) var(--space-3);
      min-height: 40px;
    }
  }
</style>
