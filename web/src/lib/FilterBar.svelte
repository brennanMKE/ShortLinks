<!--
  FilterBar (#0163) — the Overview's UTM filter bar: Source, Medium, UTM
  campaign, Content and Placement dropdowns (Term behind "More"), and the
  active filters as removable chips with Clear all. The "UTM campaign"
  dropdown is hidden while every value is the slug of a ShortLinks campaign
  (the focus chips already cover that), unless it already has a selection.
  The owner turns a change into navigation; see FacetFilter.svelte.
-->
<script lang="ts">
  import FacetFilter from './FacetFilter.svelte';
  import {
    FILTER_LABELS,
    UTM_KEYS,
    hasFilters,
    utmCampaignRedundant,
    valueLabel,
    type FacetValue,
    type UtmFilters,
    type UtmKey,
  } from './viewQuery';

  interface Props {
    facets: Record<UtmKey, FacetValue[]>;
    filters: UtmFilters;
    /** Slugs of the user's ShortLinks campaigns, for the one-to-one test. */
    campaignSlugs: string[];
    onchange: (key: UtmKey, values: string[]) => void;
    onclear: () => void;
  }

  let { facets, filters, campaignSlugs, onchange, onclear }: Props = $props();

  const hideUtmCampaign = $derived(
    filters.utm_campaign.length === 0 && utmCampaignRedundant(facets.utm_campaign, campaignSlugs),
  );
  const BAR: UtmKey[] = ['source', 'medium', 'utm_campaign', 'content', 'placement'];
  const bar = $derived(BAR.filter((k) => k !== 'utm_campaign' || !hideUtmCampaign));

  let moreOpen = $state(false);
  const termShown = $derived(moreOpen || filters.term.length > 0);

  const active = $derived(hasFilters(filters));
  const chips = $derived(
    UTM_KEYS.flatMap((k) => filters[k].map((v) => ({ key: k, value: v, text: `${FILTER_LABELS[k].toLowerCase()}: ${valueLabel(v)}` }))),
  );
</script>

<div class="bar" role="group" aria-label="Filter by UTM parameters">
  <span class="lead">Filter</span>
  {#each bar as k (k)}
    <FacetFilter
      label={FILTER_LABELS[k]}
      values={facets[k]}
      selected={filters[k]}
      onchange={(v) => onchange(k, v)}
    />
  {/each}
  {#if termShown}
    <FacetFilter
      label={FILTER_LABELS.term}
      values={facets.term}
      selected={filters.term}
      onchange={(v) => onchange('term', v)}
    />
  {:else}
    <button type="button" class="more" aria-expanded="false" onclick={() => (moreOpen = true)}>More</button>
  {/if}
</div>

{#if active}
  <div class="chips" role="list" aria-label="Active filters">
    {#each chips as c (c.key + '\u0000' + c.value)}
      <span class="chip" role="listitem">
        <span class="text" title={c.text}>{c.text}</span>
        <button
          type="button"
          aria-label="Remove filter {c.text}"
          onclick={() => onchange(c.key, filters[c.key].filter((v) => v !== c.value))}>×</button
        >
      </span>
    {/each}
    <button type="button" class="clear btn-link" onclick={onclear}>Clear all</button>
  </div>
{/if}

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-3);
  }
  .lead {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    margin-right: var(--space-1);
  }
  .more {
    min-height: 34px;
    padding: 0 var(--space-2);
    border: none;
    background: none;
    color: var(--accent);
    font: inherit;
    cursor: pointer;
  }
  .more:hover {
    text-decoration: underline;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
    margin-top: var(--space-2);
  }
  .chip {
    display: inline-flex;
    align-items: center;
    max-width: 100%;
    min-height: 28px;
    padding: 0 2px 0 var(--space-3);
    border: var(--border-w) solid var(--accent);
    border-radius: 14px;
    background: var(--accent-subtle);
    font-size: var(--fs-sm);
  }
  .text {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .chip button {
    flex: 0 0 auto;
    width: 26px;
    height: 26px;
    border: none;
    border-radius: 13px;
    background: none;
    color: var(--text-muted);
    font-size: 18px;
    line-height: 1;
    cursor: pointer;
  }
  .chip button:hover {
    background: var(--bg-subtle);
    color: var(--text);
  }
  .clear {
    margin-left: auto;
  }
</style>
