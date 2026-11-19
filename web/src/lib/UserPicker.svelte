<!--
  UserPicker (#0171) — a typeahead over the users list, so an admin picks an
  account by email instead of typing a numeric id. ARIA combobox with a listbox
  popup: type to narrow, arrows + Enter (or a click) to pick, Escape to close.
  `value` is the committed email ('' = everyone); typing alone never commits, so
  half-typed text can't filter the log.
-->
<script lang="ts">
  import type { AdminUser } from './types';
  import { matchUsers } from './admin';

  interface Props {
    users: AdminUser[];
    /** Committed email, or '' for no user. */
    value: string;
    onchange: (email: string) => void;
    label?: string;
    placeholder?: string;
  }
  let { users, value, onchange, label = 'User', placeholder = 'Any user' }: Props = $props();

  const uid = Math.random().toString(36).slice(2, 8);
  let text = $state('');
  let open = $state(false);
  let active = $state(0);
  let inputEl: HTMLInputElement | undefined = $state();
  // The popup is position: fixed so a panel's overflow: hidden can't clip it.
  let pos = $state({ top: 0, left: 0, width: 0 });

  function place() {
    if (!inputEl) return;
    const r = inputEl.getBoundingClientRect();
    pos = { top: r.bottom + 4, left: r.left, width: r.width };
  }

  $effect(() => {
    if (!open) return;
    place();
    window.addEventListener('scroll', place, true);
    window.addEventListener('resize', place);
    return () => {
      window.removeEventListener('scroll', place, true);
      window.removeEventListener('resize', place);
    };
  });

  // Show the committed value whenever it changes from outside (hash, clear).
  $effect(() => {
    text = value;
  });

  // While the box still shows the committed value, offer everyone; once the
  // admin types, narrow.
  const options = $derived(matchUsers(users, text === value ? '' : text));

  function pick(email: string) {
    text = email;
    open = false;
    if (email !== value) onchange(email);
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!open) {
        open = true;
        return;
      }
      const n = options.length;
      if (n > 0) active = (active + (e.key === 'ArrowDown' ? 1 : n - 1)) % n;
    } else if (e.key === 'Enter') {
      if (open && options[active]) {
        e.preventDefault();
        pick(options[active].email);
      }
    } else if (e.key === 'Escape' && open) {
      e.preventDefault();
      e.stopPropagation();
      open = false;
      text = value;
    }
  }

  function onBlur() {
    open = false;
    const t = text.trim();
    if (t === '') {
      if (value !== '') onchange('');
      return;
    }
    const exact = users.find((u) => u.email.toLowerCase() === t.toLowerCase());
    if (exact) pick(exact.email);
    else text = value;
  }
</script>

<div class="picker">
  <label class="sr-only" for="up-{uid}">{label}</label>
  <input
    id="up-{uid}"
    bind:this={inputEl}
    type="text"
    role="combobox"
    autocomplete="off"
    spellcheck="false"
    aria-expanded={open}
    aria-controls="upl-{uid}"
    aria-autocomplete="list"
    aria-activedescendant={open && options[active] ? `upo-${uid}-${options[active].id}` : undefined}
    {placeholder}
    bind:value={text}
    onfocus={() => {
      open = true;
      active = 0;
    }}
    oninput={() => {
      open = true;
      active = 0;
    }}
    onkeydown={onKey}
    onblur={onBlur}
  />
  {#if value}
    <button
      type="button"
      class="clear"
      aria-label="Clear {label.toLowerCase()} filter"
      onmousedown={(e) => e.preventDefault()}
      onclick={() => pick('')}
    >
      ×
    </button>
  {/if}
  {#if open}
    <ul
      id="upl-{uid}"
      class="list"
      role="listbox"
      aria-label="{label} suggestions"
      style:top="{pos.top}px"
      style:left="{pos.left}px"
      style:min-width="{Math.max(pos.width, 224)}px"
    >
      {#each options as u, i (u.id)}
        <!-- mousedown (not click) so the pick lands before the input's blur. -->
        <li
          id="upo-{uid}-{u.id}"
          role="option"
          aria-selected={i === active}
          class:active={i === active}
          onmousedown={(e) => {
            e.preventDefault();
            pick(u.email);
          }}
        >
          <span class="email">{u.email}</span>
          {#if !u.active}<span class="tag">Inactive</span>{/if}
        </li>
      {:else}
        <li class="none" role="presentation">No matching users</li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .picker {
    position: relative;
    min-width: 0;
  }
  input {
    width: 100%;
    padding-right: 2rem;
  }
  .clear {
    position: absolute;
    right: 4px;
    top: 50%;
    transform: translateY(-50%);
    width: 28px;
    height: 28px;
    border: none;
    background: none;
    color: var(--text-muted);
    font-size: 18px;
    line-height: 1;
    cursor: pointer;
    border-radius: var(--radius);
  }
  .clear:hover {
    color: var(--text);
    background: var(--bg-subtle);
  }
  .list {
    position: fixed;
    z-index: 20;
    margin: 0;
    padding: var(--space-1);
    list-style: none;
    max-height: 16rem;
    overflow-y: auto;
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.18);
  }
  li {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-3);
    border-radius: var(--radius);
    cursor: pointer;
    font-size: var(--fs-base);
  }
  li.active {
    background: var(--accent-subtle);
  }
  .email {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tag {
    flex: none;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .none {
    cursor: default;
    color: var(--text-muted);
  }
</style>
