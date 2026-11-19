<!--
  Preferences block on the Account view (#0170): chart time zone and theme.
  Both are stored per browser (localStorage); see lib/preferences.ts.
-->
<script lang="ts">
  import Panel from './Panel.svelte';
  import {
    THEMES,
    TZ_BROWSER,
    browserTimeZone,
    getTheme,
    getTimeZoneChoice,
    setTheme,
    setTimeZoneChoice,
    timeZoneOptions,
    type Theme,
  } from './preferences';

  let theme = $state<Theme>(getTheme());
  let tz = $state(getTimeZoneChoice());
  const zones = timeZoneOptions();
  const browserTz = browserTimeZone();
</script>

<Panel title="Preferences">
  <p class="text-muted intro">Saved in this browser only; they do not follow you to another device.</p>
  <div class="prefs">
    <div class="field">
      <label for="pref-tz">Chart time zone</label>
      <select
        id="pref-tz"
        bind:value={tz}
        onchange={() => setTimeZoneChoice(tz)}
      >
        <option value={TZ_BROWSER}>Browser default ({browserTz})</option>
        {#each zones as z (z)}
          <option value={z}>{z}</option>
        {/each}
      </select>
      <p class="text-muted hint">Used by the Overview and campaign charts.</p>
    </div>

    <fieldset class="field theme">
      <legend>Theme</legend>
      <div class="choices">
        {#each THEMES as t (t.value)}
          <label class="choice">
            <input
              type="radio"
              name="pref-theme"
              value={t.value}
              bind:group={theme}
              onchange={() => setTheme(theme)}
            />
            {t.label}
          </label>
        {/each}
      </div>
    </fieldset>
  </div>
</Panel>

<style>
  .intro {
    margin: 0 0 var(--space-3);
  }
  .prefs {
    display: flex;
    flex-direction: column;
    gap: var(--space-4);
    max-width: 28rem;
  }
  .field {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    border: 0;
    padding: 0;
    margin: 0;
    min-width: 0;
  }
  .field label,
  .field legend {
    font-weight: 600;
    font-size: var(--fs-sm);
    padding: 0;
  }
  .hint {
    margin: var(--space-1) 0 0;
    font-size: var(--fs-sm);
  }
  .choices {
    display: flex;
    gap: var(--space-4);
    flex-wrap: wrap;
    margin-top: var(--space-1);
  }
  .choice {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    font-weight: 400;
    font-size: var(--fs-base);
  }
</style>
