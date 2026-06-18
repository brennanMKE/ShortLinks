# Portal-Style Design Guide for a Minimal Svelte SPA

A practical reference for taking a plain Svelte app to a polished-but-simple,
portal-era aesthetic: defined boundaries, consistent controls, no widgets.
Inspired by GitHub Primer, early Facebook, and classic Yahoo/portal layouts.

---

## 1. Design Principles

- **Boundaries over decoration.** Use 1px borders and subtle background shifts
  instead of shadows, gradients, or animation.
- **One accent color.** Everything interactive shares a single accent. Neutrals
  do the rest.
- **Systematic spacing.** Everything snaps to an 8px grid.
- **Tight type scale.** 3–4 sizes, one font stack.
- **Reusable primitives.** A `Panel`, a `Button`, a styled `table`. Build once,
  reuse everywhere.

---

## 2. Design Tokens

Drop this into a global stylesheet (e.g. `src/app.css`) and never use raw
values again.

```css
:root {
  /* Color — neutrals */
  --bg-page:    #f6f7f8;
  --bg-panel:   #ffffff;
  --bg-subtle:  #f0f1f3;
  --bg-header:  #eceef0;

  /* Color — text */
  --text:        #1c1e21;
  --text-muted:  #65676b;
  --text-faint:  #8a8d91;

  /* Color — borders */
  --border:        #d3d6da;
  --border-strong: #b7bbc0;

  /* Color — accent (single) */
  --accent:        #2d5fa3;
  --accent-hover:  #244e87;
  --accent-text:   #ffffff;
  --accent-subtle: #e7eef7;

  /* Status (use sparingly) */
  --success: #2e7d32;
  --danger:  #c62828;
  --warning: #b26a00;

  /* Spacing — 8px grid */
  --space-1: 4px;
  --space-2: 8px;
  --space-3: 12px;
  --space-4: 16px;
  --space-5: 24px;
  --space-6: 32px;

  /* Typography */
  --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica,
          Arial, sans-serif;
  --fs-sm:   12px;
  --fs-base: 13px;
  --fs-md:   15px;
  --fs-lg:   18px;
  --fs-xl:   22px;
  --lh:      1.45;

  /* Shape */
  --radius:  4px;
  --border-w: 1px;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg-page);
  color: var(--text);
  font-family: var(--font);
  font-size: var(--fs-base);
  line-height: var(--lh);
}
```

---

## 3. Layout

A simple bounded, centered column — the portal staple.

```css
.app-shell {
  max-width: 1040px;
  margin: 0 auto;
  padding: var(--space-5) var(--space-4);
}

.app-header {
  background: var(--bg-panel);
  border: var(--border-w) solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3) var(--space-4);
  margin-bottom: var(--space-4);
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.app-title {
  font-size: var(--fs-lg);
  font-weight: 600;
  margin: 0;
}

/* Two-column portal layout (optional) */
.app-columns {
  display: grid;
  grid-template-columns: 1fr 280px;
  gap: var(--space-4);
}

@media (max-width: 720px) {
  .app-columns { grid-template-columns: 1fr; }
}
```

---

## 4. Panel — the bounded box primitive

`src/lib/Panel.svelte`

```svelte
<script>
  export let title = "";
</script>

<section class="panel">
  {#if title}
    <header class="panel-header">{title}</header>
  {/if}
  <div class="panel-body">
    <slot />
  </div>
</section>

<style>
  .panel {
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    margin-bottom: var(--space-4);
    overflow: hidden;
  }
  .panel-header {
    background: var(--bg-header);
    border-bottom: var(--border-w) solid var(--border);
    padding: var(--space-2) var(--space-3);
    font-size: var(--fs-base);
    font-weight: 600;
    color: var(--text);
  }
  .panel-body {
    padding: var(--space-4);
  }
</style>
```

Usage:

```svelte
<Panel title="Account">
  <p>Anything goes here.</p>
</Panel>
```

---

## 5. Buttons — consistent control system

`src/lib/Button.svelte`

```svelte
<script>
  export let variant = "default"; // default | primary | subtle | danger
  export let type = "button";
  export let disabled = false;
</script>

<button {type} {disabled} class="btn btn-{variant}" on:click>
  <slot />
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
  .btn:hover { background: var(--bg-header); }
  .btn:active { background: var(--border); }
  .btn:disabled { opacity: 0.5; cursor: default; }

  .btn-primary {
    background: var(--accent);
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .btn-primary:hover { background: var(--accent-hover); border-color: var(--accent-hover); }

  .btn-subtle {
    background: transparent;
    border-color: transparent;
    color: var(--accent);
  }
  .btn-subtle:hover { background: var(--accent-subtle); }

  .btn-danger {
    background: var(--bg-subtle);
    border-color: var(--border-strong);
    color: var(--danger);
  }
  .btn-danger:hover { background: #fbeaea; }
</style>
```

Usage:

```svelte
<Button variant="primary" on:click={save}>Save</Button>
<Button on:click={cancel}>Cancel</Button>
<Button variant="subtle">Details</Button>
```

---

## 6. Form Controls

Global styles so every input matches without per-field work.

```css
input[type="text"],
input[type="email"],
input[type="password"],
input[type="search"],
input[type="number"],
select,
textarea {
  font-family: var(--font);
  font-size: var(--fs-base);
  color: var(--text);
  background: var(--bg-panel);
  border: var(--border-w) solid var(--border-strong);
  border-radius: var(--radius);
  padding: var(--space-2) var(--space-3);
  width: 100%;
}

input:focus,
select:focus,
textarea:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 2px var(--accent-subtle);
}

label {
  display: block;
  font-size: var(--fs-sm);
  font-weight: 600;
  color: var(--text-muted);
  margin-bottom: var(--space-1);
}

.field { margin-bottom: var(--space-3); }
```

---

## 7. Tables — dense and bordered

The portal/early-Facebook table look: gray header bar, hairline row dividers,
subtle hover.

```css
table {
  width: 100%;
  border-collapse: collapse;
  font-size: var(--fs-base);
}

thead th {
  text-align: left;
  background: var(--bg-header);
  color: var(--text-muted);
  font-weight: 600;
  font-size: var(--fs-sm);
  text-transform: uppercase;
  letter-spacing: 0.03em;
  padding: var(--space-2) var(--space-3);
  border-bottom: var(--border-w) solid var(--border);
}

tbody td {
  padding: var(--space-2) var(--space-3);
  border-bottom: var(--border-w) solid var(--border);
}

tbody tr:hover { background: var(--bg-subtle); }

tbody tr:last-child td { border-bottom: none; }
```

Put a table inside a `Panel` with no body padding for the cleanest result:

```svelte
<section class="panel">
  <header class="panel-header">Members</header>
  <table>...</table>
</section>
```

---

## 8. Small Utilities

```css
.text-muted { color: var(--text-muted); }
.text-faint { color: var(--text-faint); }
.divider {
  height: var(--border-w);
  background: var(--border);
  border: none;
  margin: var(--space-3) 0;
}
.row { display: flex; gap: var(--space-2); align-items: center; }
.spread { justify-content: space-between; }
.badge {
  display: inline-block;
  font-size: var(--fs-sm);
  padding: 1px var(--space-2);
  border-radius: 10px;
  background: var(--accent-subtle);
  color: var(--accent);
}
```

---

## 9. Adoption Checklist

1. Paste tokens into `src/app.css`, import it once in your root.
2. Replace raw colors/spacing in existing components with token vars.
3. Add `Panel.svelte` and wrap each section of the page in it.
4. Add `Button.svelte`, swap all `<button>`s to it.
5. Apply the global form + table styles; delete per-component overrides.
6. Pick the single accent and adjust `--accent*` only — leave neutrals alone.

---

## 10. Reference Design Systems

- **GitHub Primer** — primer.style — closest match; study Box and Table.
- **Atlassian Design System** — atlassian.design — bounded panels, forms.
- **IBM Carbon** — carbondesignsystem.com — grid and sharp boundaries.
- **PureCSS (Yahoo)** — purecss.io — tiny, unopinionated module set.
- **Ant Design** — ant.design — dense, table-heavy, portal-adjacent.

Keep the rule of thumb: borders define structure, one accent signals action,
neutrals carry everything else.
