<!--
  Overview view (#0162) — the signed-in home page at #/. Answers "what is
  getting attention": four stat tiles, a daily-clicks chart with the previous
  period, campaign focus chips and the ranked Top content list. All numbers
  come from GET /api/reports/{overview,top-content,top-links,breakdown}
  (#0139, #0140); nothing is added up in the browser except the display of
  what the server returned.

  State lives in the hash query (range, from/to, campaign, the UTM filters,
  group — see lib/viewQuery.ts, #0163). Every control navigates, so Back
  undoes a change and a filtered view can be bookmarked or pasted. The filters
  are applied by the reports themselves, so the tiles, chart, chips and list
  all describe the same filtered clicks.
  A brand-new account (no links at all) sees "Create your first link" instead
  of an empty chart. See docs/ui-redesign.md section 3.2.
-->
<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { currentUser } from '../lib/stores';
  import { subscribeLinks } from '../lib/events';
  import { openNewLink } from '../lib/newLinkDialog';
  import { ApiError, listCampaigns } from '../lib/api';
  import { getRoute, navigate, replace } from '../lib/hashRouter.svelte';
  import {
    getReport,
    type BreakdownData,
    type OverviewData,
    type ReportEnvelope,
    type TopContentData,
    type TopLinksData,
  } from '../lib/reports';
  import {
    clearFilters,
    parse,
    pruneFilters,
    reportQuery,
    viewPath,
    withFilter,
    UTM_KEYS,
    type FacetValue,
    type GroupBy,
    type RangeKey,
    type UtmKey,
    type ViewState,
  } from '../lib/viewQuery';
  import {
    campaignChips,
    changeOf,
    contentRows,
    dayLabel,
    linkRows,
    plural,
    sourceColors,
    sourceRows,
    type TopRow,
  } from '../lib/overview';
  import { getChartTimeZone } from '../lib/preferences';
  import ClicksChart from '../lib/ClicksChart.svelte';
  import StatTile from '../lib/StatTile.svelte';
  import CampaignChips from '../lib/CampaignChips.svelte';
  import FilterBar from '../lib/FilterBar.svelte';
  import TopContent from '../lib/TopContent.svelte';
  import Link from '../lib/Link.svelte';

  const tz = getChartTimeZone();

  // The URL is the state.
  const route = $derived(getRoute());
  const view = $derived<ViewState>(parse(route.rawQuery));
  const overviewPath = (s: ViewState) => viewPath(s, '/');

  // Each report is stored with the fetch key it answers, so a stale answer is
  // recognisable and the previous one can stay on screen (dimmed) until the
  // new one lands. The scope (range, campaign) drives the overview and
  // top-content reports; the grouping only picks which extra report the list
  // needs, so changing it fetches that one report and nothing else.
  let overview = $state<ReportEnvelope<OverviewData> | null>(null);
  let overviewKey = $state('');
  let content = $state<ReportEnvelope<TopContentData> | null>(null);
  let contentKey = $state('');
  let topLinks = $state<ReportEnvelope<TopLinksData> | null>(null);
  let linksKey = $state('');
  let sources = $state<ReportEnvelope<BreakdownData> | null>(null);
  let sourcesKey = $state('');
  let loadError = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let reloadCount = $state(0);

  const scope = $derived<ViewState>({ ...view, group: 'content' });
  const fetchKey = $derived(overviewPath(scope) + '|' + reloadCount);

  // A response for a scope the user has already left must not overwrite a newer one.
  let seqA = 0;
  let seqB = 0;

  function failed(err: unknown, mine: number, seq: number): void {
    if (mine !== seq) return;
    if (err instanceof ApiError && err.status === 401) {
      // App.svelte's guard effect bounces to /login, keeping this route as the redirect target.
      currentUser.set(null);
      return;
    }
    if (err instanceof ApiError && err.status === 404 && scope.campaign) {
      // A stale or mistyped campaign slug: say so and fall back to All.
      notice = `No campaign "${scope.campaign}" was found, so all clicks are shown.`;
      replace(overviewPath({ ...view, campaign: '' }));
      return;
    }
    loadError = 'Could not load the overview. Please try again.';
  }

  $effect(() => {
    const key = fetchKey;
    // `scope` is a new object whenever the route changes (even for a group-only
    // change); only the key may re-run this effect.
    const s = untrack(() => scope);
    const mine = ++seqA;
    loadError = null;
    Promise.all([
      getReport<OverviewData>('overview', reportQuery(s, tz, { limit: String(FACET_LIMIT) })),
      // Always fetched: the "Top content" tile and the source colours read it.
      getReport<TopContentData>('top-content', reportQuery(s, tz, { limit: '50' })),
    ])
      .then(([o, c]) => {
        if (mine !== seqA) return;
        overview = o;
        overviewKey = key;
        content = c;
        contentKey = key;
      })
      .catch((err) => failed(err, mine, seqA));
  });

  $effect(() => {
    const key = fetchKey;
    const group = view.group;
    const s = untrack(() => scope);
    if (group === 'content') return;
    if (untrack(() => (group === 'link' ? linksKey : sourcesKey)) === key) return;
    const mine = ++seqB;
    const req =
      group === 'link'
        ? getReport<TopLinksData>('top-links', reportQuery(s, tz, { limit: '50' })).then((r) => {
            if (mine !== seqB) return;
            topLinks = r;
            linksKey = key;
          })
        : getReport<BreakdownData>('breakdown', reportQuery(s, tz, { dimension: 'source', limit: '50' })).then((r) => {
            if (mine !== seqB) return;
            sources = r;
            sourcesKey = key;
          });
    req.catch((err) => failed(err, mine, seqB));
  });

  const data = $derived(overview?.data ?? null);
  const win = $derived(overview?.window ?? null);
  const days = $derived(data?.timeseries.length ?? 0);
  const prev = $derived(data?.previous ?? null);
  // The page keeps showing the last answer, dimmed, while a newer one loads.
  const loading = $derived(overviewKey !== fetchKey);

  const hasLinks = $derived((data?.totals.links_total ?? 0) > 0);
  // Facet lists are requested at this size; a list that reaches it may be incomplete.
  const FACET_LIMIT = 200;
  const facets = $derived<Record<UtmKey, FacetValue[]>>({
    source: data?.facets?.source ?? [],
    medium: data?.facets?.medium ?? [],
    utm_campaign: data?.facets?.utm_campaign ?? [],
    content: data?.facets?.content ?? [],
    placement: data?.facets?.placement ?? [],
    term: data?.facets?.term ?? [],
  });
  const campaignSlugs = $derived((data?.campaigns ?? []).flatMap((c) => (c.slug ? [c.slug] : [])));
  const filtered = $derived(UTM_KEYS.some((k) => view.filters[k].length > 0));

  const chips = $derived(campaignChips(data?.campaigns ?? [], view.campaign));
  const selectedChip = $derived(chips.find((c) => c.value === view.campaign) ?? null);

  const timeseries = $derived({ days: (data?.timeseries ?? []).map((d) => ({ date: d.date, count: d.clicks })) });
  const comparison = $derived(
    prev && data ? data.previous_timeseries.map((d) => ({ date: d.date, count: d.clicks })) : null,
  );

  const clicksChange = $derived(prev && data ? changeOf(data.totals.clicks, prev.clicks) : null);
  const visitorsChange = $derived(
    prev && data && prev.visitor_days !== null ? changeOf(data.totals.visitor_days, prev.visitor_days) : null,
  );
  // Best day against the previous period's best day.
  const bestChange = $derived.by(() => {
    if (!data?.best_day || !prev) return null;
    const priorBest = Math.max(0, ...data.previous_timeseries.map((d) => d.clicks));
    return changeOf(data.best_day.clicks, priorBest);
  });
  const topGroup = $derived(content?.data.groups[0] ?? null);
  const topChange = $derived(
    topGroup && topGroup.previous_clicks !== null ? changeOf(topGroup.clicks, topGroup.previous_clicks) : null,
  );

  // One colour per source across the whole list, ranked by the content report
  // (always loaded), so a source keeps its colour in every row and grouping.
  const colors = $derived(sourceColors((content?.data.groups ?? []).flatMap((g) => g.by_source)));

  // Rows follow the group whose report has arrived; until the requested
  // group's report lands the previous rows stay (dimmed).
  const wantedReady = $derived(
    view.group === 'link' ? linksKey === fetchKey : view.group === 'source' ? sourcesKey === fetchKey : contentKey === fetchKey,
  );
  let shownGroup = $state<GroupBy>('content');
  $effect(() => {
    if (wantedReady) shownGroup = view.group;
  });
  const rows = $derived.by<TopRow[]>(() => {
    if (shownGroup === 'link' && topLinks) return linkRows(topLinks.data.links, colors);
    if (shownGroup === 'source' && sources) return sourceRows(sources.data, colors);
    return content ? contentRows(content.data.groups, colors) : [];
  });
  const rowsTotal = $derived(
    shownGroup === 'link' ? topLinks?.data.total_links : shownGroup === 'content' ? content?.data.total_groups : undefined,
  );
  const rowsLoading = $derived(loading || !wantedReady);

  const today = $derived(new Date().toLocaleDateString('en-CA', { timeZone: tz }));
  // "last 30 days" for a window ending today; the dates themselves otherwise.
  const scopeLabel = $derived.by(() => {
    if (!win) return '';
    if (view.range !== 'custom' || win.to >= today) return `last ${days} ${plural(days, 'day', 'days')}`;
    return `${dayLabel(win.from)} to ${dayLabel(win.to)}`;
  });

  // The reports treat an unknown campaign slug as "no clicks", so a stale
  // slug in the hash (a deleted or mistyped campaign) is caught here: when the
  // focused campaign has no clicks in the window, check it exists, and if not
  // say so and fall back to All.
  $effect(() => {
    const slug = view.campaign;
    if (!slug || slug === 'none' || overviewKey !== fetchKey) return;
    if (data?.campaigns.some((c) => c.slug === slug)) return;
    listCampaigns()
      .then((r) => {
        if (view.campaign !== slug || r.campaigns.some((c) => c.slug === slug)) return;
        notice = `No campaign "${slug}" was found, so all clicks are shown.`;
        replace(overviewPath({ ...view, campaign: '' }));
      })
      .catch(() => {});
  });

  // A pasted or shared URL may carry filter values that do not exist (a
  // deleted placement, a typo). Checked once, on arrival, against the
  // account's whole history (an unfiltered range=all overview), so a value that
  // is merely quiet in the current window is never dropped, and changing the
  // range or campaign later never touches a selection. Unknown values are
  // removed with a dismissible notice.
  onMount(() => {
    const start = view;
    if (!UTM_KEYS.some((k) => start.filters[k].length > 0)) return;
    const q = new URLSearchParams({ tz, range: 'all', limit: '500' });
    getReport<OverviewData>('overview', q.toString())
      .then((r) => {
        const { filters, dropped } = pruneFilters(start.filters, r.data.facets, 500);
        if (dropped.length === 0) return;
        notice = `Ignored ${dropped.length === 1 ? 'a filter value that does' : 'filter values that do'} not exist: ${dropped.join(', ')}.`;
        // Remove only the unknown values from whatever the URL says now.
        const cur = view;
        const keep = { ...cur.filters };
        for (const k of UTM_KEYS) keep[k] = cur.filters[k].filter((v) => filters[k].includes(v) || !start.filters[k].includes(v));
        replace(overviewPath({ ...cur, filters: keep }));
      })
      .catch(() => {});
  });

  // A link created anywhere (the New link dialog, another tab) refetches.
  onMount(() => subscribeLinks(() => reloadCount++));

  const RANGES: { key: Exclude<RangeKey, 'custom'>; label: string }[] = [
    { key: '7d', label: '7d' },
    { key: '30d', label: '30d' },
    { key: '90d', label: '90d' },
  ];
  const GROUPS: { key: GroupBy; label: string }[] = [
    { key: 'content', label: 'Content' },
    { key: 'link', label: 'Link' },
    { key: 'source', label: 'Source' },
  ];

  const hrefRange = (key: Exclude<RangeKey, 'custom'>) => overviewPath({ ...view, range: key, from: '', to: '' });
  const hrefCampaign = (value: string) => overviewPath({ ...view, campaign: value });
  const hrefGroup = (g: GroupBy) => overviewPath({ ...view, group: g });
  // Dropdown and chip changes push history, so Back undoes them.
  const setFilter = (key: UtmKey, values: string[]) => navigate(overviewPath(withFilter(view, key, values)));
  const clearAll = () => navigate(overviewPath(clearFilters(view)));
  // "View all links" carries the campaign focus where the Links page can use it (#0163).
  const allLinksHref = '/links';

  // Custom range form.
  let customOpen = $state(false);
  let customFrom = $state('');
  let customTo = $state('');
  $effect(() => {
    if (view.range === 'custom') {
      customFrom = view.from;
      customTo = view.to;
    }
  });
  const customValid = $derived(customFrom !== '' && customFrom <= today && (customTo === '' || customTo >= customFrom));
  function applyCustom(e: Event) {
    e.preventDefault();
    if (!customValid) return;
    navigate(overviewPath({ ...view, range: 'custom', from: customFrom, to: customTo }));
    customOpen = false;
  }
</script>

<div class="app-shell overview">
  <div class="head">
    <h1 class="page-title">Overview</h1>
    {#if hasLinks || !data}
    <div class="range" role="group" aria-label="Date range">
      {#each RANGES as r (r.key)}
        <a class="seg" class:on={view.range === r.key} href={'#' + hrefRange(r.key)} aria-current={view.range === r.key ? "true" : undefined}>{r.label}</a>
      {/each}
      <button
        type="button"
        class="seg"
        class:on={view.range === 'custom'}
        aria-pressed={view.range === 'custom'}
        aria-expanded={customOpen}
        onclick={() => (customOpen = !customOpen)}>Custom</button
      >
    </div>
    {/if}
  </div>

  {#if customOpen && hasLinks}
    <form class="custom" onsubmit={applyCustom}>
      <label>From<input type="date" bind:value={customFrom} max={today} required /></label>
      <label>To<input type="date" bind:value={customTo} max={today} min={customFrom || undefined} /></label>
      <button type="submit" class="btn-link btn-link-primary" disabled={!customValid}>Apply</button>
    </form>
  {/if}

  {#if notice}
    <div class="notice" role="status">
      <span>{notice}</span>
      <button type="button" class="dismiss" aria-label="Dismiss" onclick={() => (notice = null)}>×</button>
    </div>
  {/if}

  {#if loadError}
    <div class="card error" role="alert">
      <p class="text-error">{loadError}</p>
      <button type="button" class="btn-link" onclick={() => reloadCount++}>Try again</button>
    </div>
  {:else if !data}
    <div class="skeleton" aria-busy="true" aria-label="Loading the overview">
      <div class="tiles">
        {#each [0, 1, 2, 3] as i (i)}<div class="tile-skel"></div>{/each}
      </div>
      <div class="chart-skel"></div>
    </div>
  {:else if !hasLinks}
    <div class="card first">
      <h2>Create your first link</h2>
      <p class="text-muted">Short links you create show up here with their clicks, charted day by day.</p>
      <button type="button" class="btn-link btn-link-primary" onclick={() => openNewLink()}>New link</button>
    </div>
  {:else}
    <div class="body" class:stale={loading}>
      <div class="tiles">
        <StatTile
          label="Clicks"
          value={data.totals.clicks.toLocaleString()}
          sub="{win?.from ? dayLabel(win.from) : ''} to {win?.to ? dayLabel(win.to) : ''}"
          change={clicksChange}
          changeNote="vs prior"
        />
        <StatTile
          label="Visitors"
          value={data.totals.visitor_days.toLocaleString()}
          sub="daily unique"
          change={visitorsChange}
          changeNote="vs prior"
          unavailable={prev && prev.visitor_days === null ? 'Prior period unavailable' : ''}
          help="Counted per day; the same person on two days counts twice."
        />
        <StatTile
          label="Best day"
          value={data.best_day ? data.best_day.clicks.toLocaleString() : '—'}
          sub={data.best_day ? dayLabel(data.best_day.date) : 'No clicks yet'}
          change={bestChange}
          changeNote="vs prior best"
        />
        <StatTile
          label="Top content"
          value={topGroup ? topGroup.title : '—'}
          valueTitle={topGroup?.title}
          wrap
          sub={topGroup ? `${topGroup.clicks.toLocaleString()} ${plural(topGroup.clicks, 'click', 'clicks')} · ${topGroup.links.length} ${plural(topGroup.links.length, 'link', 'links')}` : 'No clicks yet'}
          change={topChange}
          changeNote="vs prior"
        />
      </div>

      <section class="card" aria-label="Clicks over time">
        <div class="focus">
          <CampaignChips {chips} selected={view.campaign} hrefFor={hrefCampaign} />
          {#if view.campaign && view.campaign !== 'none'}
            <Link to={'/campaigns/' + encodeURIComponent(view.campaign)} class="text-link open-campaign">
              Open campaign →
            </Link>
          {/if}
        </div>
        <div class="chart">
          <ClicksChart
            {timeseries}
            from={win?.from}
            to={win?.to}
            {comparison}
            comparisonFrom={overview?.previous_window?.from}
            partialLast={win?.partial_last_day ?? false}
            title={selectedChip ? `Clicks per day, ${selectedChip.label}` : 'Clicks per day'}
          />
        </div>
        <p class="foot text-muted">
          <span class="key"><i class="swatch solid"></i>This period</span>
          {#if comparison}<span class="key"><i class="swatch dashed"></i>Previous {days} {plural(days, 'day', 'days')}</span>{/if}
          <span class="bots">
            {#if data.totals.excluded_bots > 0}
              {data.totals.excluded_bots.toLocaleString()} bot {plural(data.totals.excluded_bots, 'click', 'clicks')} excluded ·
            {/if}
            times in {tz}
          </span>
        </p>
      </section>

      <section class="card filters" aria-label="Filters">
        <FilterBar {facets} filters={view.filters} {campaignSlugs} onchange={setFilter} onclear={clearAll} />
      </section>

      <section class="card" aria-label="Top content">
        <div class="top-head">
          <h2>Top content <span class="text-muted scope">{scopeLabel}</span></h2>
          <div class="group" role="group" aria-label="Group by">
            <span class="group-label">Group by</span>
            {#each GROUPS as g (g.key)}
              <a class="seg" class:on={view.group === g.key} href={'#' + hrefGroup(g.key)} aria-current={view.group === g.key ? "true" : undefined}>{g.label}</a>
            {/each}
          </div>
        </div>
        {#if data.totals.clicks === 0}
          <p class="empty text-muted">
            {#if filtered}
              No clicks match these filters in this range. Remove a filter or widen the range.
            {:else}
              No clicks {view.campaign ? 'for this campaign ' : ''}in this range. Share a link or widen the range.
            {/if}
          </p>
        {:else}
          <TopContent
            {rows}
            firstHeader={shownGroup === 'link' ? 'Link' : shownGroup === 'source' ? 'Source' : 'Content'}
            showSources={shownGroup !== 'source'}
            total={rowsTotal}
            {allLinksHref}
            loading={rowsLoading}
          />
        {/if}
      </section>
    </div>
  {/if}
</div>

<style>
  .overview {
    padding-top: var(--space-4);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .head .page-title {
    margin: 0;
  }
  .range,
  .group {
    display: inline-flex;
    align-items: center;
    gap: 0;
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    overflow: hidden;
    background: var(--bg-panel);
  }
  .group {
    border: none;
    background: none;
    gap: var(--space-1);
  }
  .group-label {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    margin-right: var(--space-1);
  }
  .seg {
    display: inline-flex;
    align-items: center;
    min-height: 34px;
    padding: 0 var(--space-3);
    background: none;
    border: none;
    color: var(--text);
    font: inherit;
    text-decoration: none;
    cursor: pointer;
  }
  .range .seg + .seg {
    border-left: var(--border-w) solid var(--border);
  }
  .group .seg {
    border: var(--border-w) solid var(--border-strong);
    border-radius: 16px;
    min-height: 30px;
    background: var(--bg-panel);
  }
  .seg:hover {
    background: var(--bg-subtle);
  }
  .seg.on {
    background: var(--accent);
    color: var(--accent-text);
    font-weight: 600;
  }
  .custom {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    padding: var(--space-3);
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .custom label {
    margin: 0;
    flex: 0 1 11rem;
  }
  .custom input {
    margin-top: var(--space-1);
  }
  .custom .btn-link:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .tiles {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .card {
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    margin-bottom: var(--space-3);
    padding: var(--space-4);
  }
  .body {
    transition: opacity 0.15s;
  }
  .body.stale {
    opacity: 0.8;
  }
  .focus {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    min-width: 0;
  }
  .focus :global(.open-campaign) {
    flex: 0 0 auto;
    white-space: nowrap;
  }
  .filters {
    padding-top: 0;
    padding-bottom: var(--space-3);
  }
  .foot {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-1) var(--space-4);
    margin: var(--space-2) 0 0;
    font-size: var(--fs-sm);
  }
  .bots {
    margin-left: auto;
  }
  .key {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
  }
  .swatch {
    display: inline-block;
    width: 18px;
    height: 0;
    border-top: 2px solid var(--accent);
  }
  .swatch.dashed {
    border-top: 2px dashed var(--text-faint);
  }
  .top-head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-2) var(--space-4);
    margin-bottom: var(--space-3);
  }
  h2 {
    font-size: var(--fs-md);
    font-weight: 600;
    margin: 0;
  }
  .scope {
    font-weight: 400;
    font-size: var(--fs-sm);
    margin-left: var(--space-2);
  }
  /* The ranked list runs edge to edge inside its card. */
  section[aria-label='Top content'] {
    padding: var(--space-4) 0 0;
  }
  section[aria-label='Top content'] .top-head {
    padding: 0 var(--space-4);
  }
  .empty {
    margin: 0;
    padding: var(--space-4) var(--space-4) var(--space-5);
  }
  .first {
    text-align: center;
    padding: var(--space-6) var(--space-4);
  }
  .first p {
    margin: var(--space-2) 0 var(--space-4);
  }
  .notice {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--accent-subtle);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .dismiss {
    border: none;
    background: none;
    font-size: 20px;
    line-height: 1;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0 var(--space-1);
  }
  .error {
    display: flex;
    align-items: center;
    gap: var(--space-3);
  }
  .error p {
    margin: 0;
  }
  .tile-skel,
  .chart-skel {
    background: var(--bg-header);
    border-radius: var(--radius);
    opacity: 0.6;
  }
  .tile-skel {
    height: 92px;
  }
  .chart-skel {
    height: 260px;
  }

  @media (max-width: 900px) {
    .tiles {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
  @media (max-width: 600px) {
    .tiles {
      gap: var(--space-2);
    }
    .card {
      padding: var(--space-3);
    }
    section[aria-label='Top content'] {
      padding: var(--space-3) 0 0;
    }
    section[aria-label='Top content'] .top-head {
      padding: 0 var(--space-3);
    }
    .focus {
      flex-direction: column;
      align-items: stretch;
      gap: var(--space-1);
    }
    .bots {
      margin-left: 0;
      flex-basis: 100%;
    }
  }
</style>
