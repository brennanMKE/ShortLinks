<!--
  LinksList view (#0153, rebuilt in #0156, searchable in #0167) — the user's
  links table at #/links, on the shared DataTable / TwoLineCell / RowMenu /
  InlineBar pieces (#0155).

  Search, Campaign, Source/Medium, Status and Sort are answered by the server
  (GET /api/links, #0144), so they cover every page, not just the one on
  screen. All of that state lives in the hash query (lib/viewQuery.ts), so a
  filtered view can be bookmarked or shared. "Group by Content" folds links
  that share a content key into one expandable row; the API pages links, so
  grouping applies to the page on screen. Checked rows get a bulk bar: Copy
  URLs, Assign to campaign, Deactivate; the last two are reversible and offer
  Undo in a toast (#0165), so no confirmation dialog.

  Columns: select, Link (two lines), Campaign, 30 days (with a bar), All time,
  actions. Live link.created events over SSE are prepended only where the new
  link would belong (newest first, unfiltered, page 1).
-->
<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import {
    searchLinks,
    listCampaigns,
    deactivateLink,
    reactivateLink,
    assignLinksToCampaign,
    unassignLinkFromCampaign,
    ApiError,
  } from '../lib/api';
  import { getReport, type OverviewData } from '../lib/reports';
  import { navigate, replace, getRoute } from '../lib/hashRouter.svelte';
  import { shortUrl, linkStatus, linkMeta, qrSvgUrl, qrPngUrl, deniedReasonLabel } from '../lib/links';
  import { subscribeLinks, prependUniqueByKey } from '../lib/events';
  import { columnMax, type Column } from '../lib/dataTable';
  import { chunkKeys } from '../lib/campaigns';
  import type { MenuItem } from '../lib/rowMenu';
  import type { CampaignWithCounts, Link } from '../lib/types';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import RowMenu from '../lib/RowMenu.svelte';
  import InlineBar from '../lib/InlineBar.svelte';
  import FacetFilter from '../lib/FacetFilter.svelte';
  import FilterSheet from '../lib/FilterSheet.svelte';
  import { createPhone } from '../lib/phone.svelte';
  import Button from '../lib/Button.svelte';
  import { pushToast } from '../lib/toasts';
  import { deactivatedMessage, reactivateErrorMessage } from '../lib/linkActions';
  import Panel from '../lib/Panel.svelte';
  import { pastedUrl } from '../lib/newLink';
  import { openNewLink } from '../lib/newLinkDialog';
  import { getChartTimeZone } from '../lib/preferences';
  import {
    parse,
    viewPath,
    reportQuery,
    withFilter,
    emptyFilters,
    FILTER_LABELS,
    valueLabel,
    DEFAULT_STATE,
    type FacetValue,
    type UtmKey,
    type ViewState,
  } from '../lib/viewQuery';
  import {
    PER_PAGE,
    SORTS,
    STATUSES,
    toListQuery,
    isNarrowed,
    acceptsNewLink,
    groupRows,
    sumClicks,
    plural,
    copiedMessage,
    bulkDeactivatedMessage,
    bulkAssignedMessage,
    bulkFailureMessage,
    previousCampaigns,
    type ListRow,
  } from '../lib/linksList';

  const tz = getChartTimeZone();

  // The URL is the state.
  const route = $derived(getRoute());
  const view = $derived<ViewState>(parse(route.rawQuery));
  const linksPath = (s: ViewState) => viewPath(s, '/links');
  const onLinksPage = $derived(route.path === '/links');

  // ── List state ──────────────────────────────────────────────────────────────
  let loading = $state(true);
  let loadError = $state<string | null>(null);
  let total = $state(0);
  let perPage = $state(PER_PAGE);
  let reloadCount = $state(0);
  let campaigns = $state<CampaignWithCounts[]>([]);
  let reportFacets = $state<{ source: FacetValue[]; medium: FacetValue[] }>({ source: [], medium: [] });
  let selected = $state<Set<string>>(new Set());
  let expanded = $state<Set<string>>(new Set());
  const phone = createPhone();
  let filtersOpen = $state(false);
  $effect(() => {
    if (!phone.matches) filtersOpen = false;
  });

  const totalPages = $derived(Math.max(1, Math.ceil(total / perPage)));
  const hasPrev = $derived(view.page > 1);
  const hasNext = $derived(view.page < totalPages);
  const grouped = $derived(view.group === 'content');
  const rows = $derived(groupRows($links, grouped, expanded));
  const maxWindow = $derived(columnMax($links, (l) => l.clicks_window ?? 0));
  const maxAll = $derived(columnMax($links, (l) => l.click_count));
  const campaignById = $derived(new Map(campaigns.map((c) => [c.id, c])));
  const campaignBySlug = $derived(new Map(campaigns.map((c) => [c.slug, c])));
  const narrowed = $derived(isNarrowed(view));
  const filterCount = $derived(
    (view.campaign ? 1 : 0) + view.filters.source.length + view.filters.medium.length + (view.status ? 1 : 0),
  );

  const columns: Column<ListRow>[] = [
    { key: 'link', label: 'Link', mobile: 'title' },
    { key: 'campaign', label: 'Campaign', width: '10rem', mobile: 'meta' },
    { key: 'd30', label: '30 days', width: '8rem', align: 'right', inset: true, mobile: 'metric' },
    { key: 'all', label: 'All time', width: '6rem', align: 'right', mobile: 'hide' },
    { key: 'actions', label: 'Actions', width: '7rem', hideLabel: true, align: 'right', mobile: 'actions' },
  ];

  // A response for a view the user has already left must not overwrite a newer one.
  let seq = 0;
  // Grouping is applied client-side, so it must not refetch.
  const fetchKey = $derived(linksPath({ ...view, group: DEFAULT_STATE.group }) + '|' + reloadCount);

  $effect(() => {
    const key = fetchKey;
    void key;
    // Only the key may re-run this: `view` is a new object on every route change.
    const v = untrack(() => view);
    if (!untrack(() => onLinksPage)) return;
    const mine = ++seq;
    loading = true;
    loadError = null;
    searchLinks(toListQuery(v, tz))
      .then((res) => {
        if (mine !== seq) return;
        if (res.total > 0 && (res.links ?? []).length === 0 && v.page > 1) {
          // Past the last page (a stale bookmark): land on the last one.
          replace(linksPath({ ...v, page: Math.max(1, Math.ceil(res.total / res.per_page)) }));
          return;
        }
        links.set(res.links ?? []);
        perPage = res.per_page;
        total = res.total;
        loading = false;
      })
      .catch((err) => {
        if (mine !== seq) return;
        if (err instanceof ApiError && err.status === 401) {
          // App.svelte's guard effect reacts to currentUser going null and
          // bounces to /login, preserving this route as the redirect target
          // (#0120) — no navigation call needed here.
          currentUser.set(null);
          return;
        }
        loadError = 'Could not load your links. Please try again.';
        loading = false;
      });
  });

  // Campaigns for the Campaign column, filter and bulk menu. Best effort.
  async function loadCampaigns() {
    try {
      const res = await listCampaigns('name');
      campaigns = res.campaigns ?? [];
    } catch {
      /* the column falls back to the slug on each link */
    }
  }

  // Source/Medium values with their clicks, from the Overview report (best
  // effort: the dev server has none, so the page's own values fill in).
  async function loadFacets() {
    try {
      const r = await getReport<OverviewData>('overview', reportQuery({ ...DEFAULT_STATE, range: '90d', filters: emptyFilters() }, tz, { limit: '200' }));
      reportFacets = { source: r.data.facets.source ?? [], medium: r.data.facets.medium ?? [] };
    } catch {
      /* fall back to the page's values */
    }
  }

  function pageFacet(get: (l: Link) => string): FacetValue[] {
    const counts = new Map<string, number>();
    for (const l of $links) {
      const v = get(l);
      if (v) counts.set(v, (counts.get(v) ?? 0) + l.click_count);
    }
    return [...counts].map(([value, clicks]) => ({ value, clicks })).sort((a, b) => b.clicks - a.clicks);
  }
  const sourceFacet = $derived(reportFacets.source.length ? reportFacets.source : pageFacet((l) => l.utm_source));
  const mediumFacet = $derived(reportFacets.medium.length ? reportFacets.medium : pageFacet((l) => l.utm_medium));

  // ── Navigation helpers: every control writes the hash ─────────────────────
  // A change to what is shown starts over at page 1. Dropdowns push history so
  // Back undoes them; typing in search replaces, so it doesn't fill history.
  const go = (next: ViewState) => navigate(linksPath({ ...next, page: 1 }));
  const setFilter = (key: UtmKey, values: string[]) => go(withFilter(view, key, values));
  const clearAll = () => go({ ...view, q: '', campaign: '', status: '', filters: emptyFilters() });

  let qInput = $state('');
  let searchEl: HTMLInputElement | undefined = $state();
  let qTimer: ReturnType<typeof setTimeout> | undefined;
  $effect(() => {
    const q = view.q;
    untrack(() => {
      // The hash moved (Back, a link): a pending debounce would write the old text back.
      clearTimeout(qTimer);
      if (q !== qInput.trim()) qInput = q;
    });
  });
  function onSearchInput() {
    clearTimeout(qTimer);
    qTimer = setTimeout(() => {
      if (qInput.trim() !== view.q) replace(linksPath({ ...view, q: qInput.trim(), page: 1 }));
    }, 300);
  }
  function onSearchKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && qInput !== '') {
      qInput = '';
      onSearchInput();
    }
  }

  // `/` focuses the search box, like `n` opens New link (docs/ui-redesign.md 3.1).
  function onKeydown(e: KeyboardEvent) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey || !onLinksPage) return;
    const t = e.target as HTMLElement | null;
    const tag = (t?.tagName ?? '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t?.isContentEditable) return;
    if (document.querySelector('dialog[open]')) return;
    e.preventDefault();
    searchEl?.focus();
  }

  // ── Row actions ─────────────────────────────────────────────────────────────
  async function copyText(text: string): Promise<boolean> {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Clipboard may be unavailable (insecure context / permissions).
      return false;
    }
  }

  function detailHref(key: string): string {
    return '#/links/' + encodeURIComponent(key);
  }

  function setActive(keys: string[], active: boolean) {
    const set = new Set(keys);
    links.update((cur) => cur.map((l) => (set.has(l.key) ? { ...l, active } : l)));
  }

  function setCampaign(keys: string[], slug: string) {
    const set = new Set(keys);
    const c = slug ? campaignBySlug.get(slug) : undefined;
    links.update((cur) =>
      cur.map((l) => (set.has(l.key) ? { ...l, campaign_id: c ? c.id : null, campaign_slug: slug || null } : l)),
    );
  }

  const unauthorized = (err: unknown) => {
    if (err instanceof ApiError && err.status === 401) {
      currentUser.set(null);
      return true;
    }
    return false;
  };

  const focusActions = (title: string) => () =>
    Array.from(document.querySelectorAll<HTMLElement>('button')).find((b) => b.getAttribute('aria-label') === `Actions for ${title}`);

  // #0165: Deactivate acts at once and offers Undo in a toast.
  async function handleDeactivate(link: Link) {
    const key = link.key;
    try {
      await deactivateLink(key);
    } catch (err) {
      if (unauthorized(err)) return;
      pushToast({ message: 'Could not deactivate this link. Please try again.' });
      return;
    }
    setActive([key], false);
    pushToast({
      message: deactivatedMessage(),
      actionLabel: 'Undo',
      returnFocus: focusActions(link.title || link.key),
      onaction: async () => {
        try {
          await reactivateLink(key);
        } catch (err) {
          unauthorized(err);
          throw new Error(reactivateErrorMessage(err));
        }
        setActive([key], true);
      },
    });
  }

  function menuItems(link: Link) {
    const items: MenuItem[] = [
      { label: 'Open destination', href: link.destination_url },
      { label: 'Copy short URL', onselect: () => void copyText(shortUrl(link.key)) },
      { label: 'QR code (SVG)', href: qrSvgUrl(link.key) },
      { label: 'QR code (PNG)', href: qrPngUrl(link.key) },
      { label: 'Edit', onselect: () => navigate('/links/' + encodeURIComponent(link.key)) },
    ];
    if (link.active && link.denied_reason === 0) {
      items.push({ label: 'Deactivate', danger: true, onselect: () => void handleDeactivate(link) });
    }
    return items;
  }

  function tagFor(link: Link): string {
    const s = linkStatus(link);
    if (s === 'denied') return link.denied_reason > 0 ? `Denied: ${deniedReasonLabel(link.denied_reason)}` : 'Denied';
    return s === 'inactive' ? 'Inactive' : '';
  }

  function campaignName(l: Link): string {
    if (l.campaign_id !== null) {
      const c = campaignById.get(l.campaign_id);
      if (c) return c.name;
    }
    return l.campaign_slug ?? '';
  }

  function toggleGroup(contentKey: string) {
    const next = new Set(expanded);
    if (next.has(contentKey)) next.delete(contentKey);
    else next.add(contentKey);
    expanded = next;
  }

  // ── Bulk actions ────────────────────────────────────────────────────────────
  const pickLinks = (keys: string[]) => {
    const want = new Set(keys);
    return $links.filter((l) => want.has(l.key));
  };

  async function bulkCopy(keys: string[]) {
    const ok = await copyText(keys.map(shortUrl).join('\n'));
    pushToast({ message: ok ? copiedMessage(keys.length) : 'Could not copy to the clipboard.' });
  }

  async function bulkDeactivate(keys: string[]) {
    const targets = pickLinks(keys).filter((l) => l.active && l.denied_reason === 0);
    if (targets.length === 0) {
      pushToast({ message: 'The selected links are already inactive.' });
      return;
    }
    const results = await Promise.allSettled(targets.map((l) => deactivateLink(l.key)));
    const done = targets.filter((_, i) => results[i].status === 'fulfilled').map((l) => l.key);
    const failed = targets.length - done.length;
    if (results.some((r) => r.status === 'rejected' && unauthorized((r as PromiseRejectedResult).reason))) return;
    if (done.length === 0) {
      pushToast({ message: bulkFailureMessage(0, failed, 'deactivate') });
      return;
    }
    setActive(done, false);
    selected = new Set();
    if (failed > 0) pushToast({ message: bulkFailureMessage(done.length, failed, 'deactivate') });
    pushToast({
      message: bulkDeactivatedMessage(done.length),
      actionLabel: 'Undo',
      returnFocus: () => document.querySelector<HTMLElement>('main h1, h1'),
      onaction: async () => {
        const back = await Promise.allSettled(done.map((k) => reactivateLink(k)));
        const ok = done.filter((_, i) => back[i].status === 'fulfilled');
        if (ok.length > 0) setActive(ok, true);
        const bad = back.find((r) => r.status === 'rejected') as PromiseRejectedResult | undefined;
        if (bad) {
          unauthorized(bad.reason);
          throw new Error(reactivateErrorMessage(bad.reason));
        }
      },
    });
  }

  /**
   * Puts `keys` into `slug`, or ('' slug) takes each out of the campaign it is
   * in. Pushes each key onto `moved` as it succeeds, so a partial failure still
   * knows which links changed.
   */
  async function applyCampaign(slug: string, keys: string[], from: Map<string, string[]>, moved: string[]) {
    if (slug) {
      for (const batch of chunkKeys(keys)) {
        await assignLinksToCampaign(slug, batch);
        moved.push(...batch);
      }
    } else {
      for (const [prev, ks] of from) {
        if (!prev) continue;
        for (const k of ks) {
          await unassignLinkFromCampaign(prev, k);
          moved.push(k);
        }
      }
    }
  }

  async function bulkAssign(slug: string, keys: string[]) {
    const all = pickLinks(keys);
    // Links already there are left alone.
    const targets = all.filter((l) => (l.campaign_slug ?? '') !== slug);
    if (targets.length === 0) {
      pushToast({ message: slug ? 'Those links are already in that campaign.' : 'Those links have no campaign.' });
      return;
    }
    const tkeys = targets.map((l) => l.key);
    const moved: string[] = [];
    let failed = false;
    try {
      await applyCampaign(slug, tkeys, previousCampaigns(targets), moved);
    } catch (err) {
      if (unauthorized(err)) return;
      failed = true;
    }
    if (moved.length === 0) {
      if (failed) pushToast({ message: 'Could not change the campaign. Please try again.' });
      return;
    }
    // Undo covers only the links that actually moved.
    const movedSet = new Set(moved);
    const prev = previousCampaigns(targets.filter((l) => movedSet.has(l.key)));
    setCampaign(moved, slug);
    selected = new Set();
    if (failed) {
      pushToast({ message: `Only ${moved.length} of ${tkeys.length} links could be changed.` });
    }
    const name = slug ? (campaignBySlug.get(slug)?.name ?? slug) : '';
    pushToast({
      message: bulkAssignedMessage(moved.length, name),
      actionLabel: 'Undo',
      returnFocus: () => document.querySelector<HTMLElement>('main h1, h1'),
      onaction: async () => {
        try {
          // Each link goes back to the campaign it had (or to none).
          for (const [p, ks] of prev) {
            if (p) await assignLinksToCampaign(p, ks);
            else for (const k of ks) await unassignLinkFromCampaign(slug, k);
            setCampaign(ks, p);
          }
        } catch (err) {
          unauthorized(err);
          reloadCount++;
          throw new Error('Could not undo that. Please try again.');
        }
      },
    });
  }

  function assignItems(keys: string[]): MenuItem[] {
    const live = campaigns.filter((c) => !c.archived);
    const items: MenuItem[] = live.map((c) => ({ label: c.name, onselect: () => void bulkAssign(c.slug, keys) }));
    items.push({ label: 'No campaign (remove)', onselect: () => void bulkAssign('', keys) });
    return items;
  }

  // Pasting a URL while no input is focused opens New link prefilled (#0158).
  function onPaste(e: ClipboardEvent) {
    if (getRoute().path !== '/links') return;
    const t = e.target as HTMLElement | null;
    const tag = (t?.tagName ?? '').toUpperCase();
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t?.isContentEditable) return;
    if (document.querySelector('dialog[open]')) return;
    const url = pastedUrl(e.clipboardData?.getData('text/plain'));
    if (!url) return;
    e.preventDefault();
    openNewLink(url);
  }

  const campaignOptions = $derived.by(() => {
    const live = campaigns.filter((c) => !c.archived || c.slug === view.campaign);
    const opts = live.map((c) => ({ slug: c.slug, name: c.name }));
    // A slug from a bookmark that isn't in the list still shows and can be cleared.
    if (view.campaign && view.campaign !== 'none' && !opts.some((o) => o.slug === view.campaign)) {
      opts.push({ slug: view.campaign, name: view.campaign });
    }
    return opts;
  });

  onMount(() => {
    loadCampaigns();
    loadFacets();

    // #0034 SSE live updates: prepend a created link where it belongs.
    const unsubscribe = subscribeLinks((link) => {
      if (!acceptsNewLink(view)) return;
      links.update((cur) => prependUniqueByKey(cur, link));
    });

    return () => {
      clearTimeout(qTimer);
      unsubscribe();
    };
  });
</script>

<svelte:window onpaste={onPaste} onkeydown={onKeydown} />

<div class="app-shell">
  <h1 class="page-title">
    Links
    {#if !loading && !loadError}<span class="count text-muted">{total.toLocaleString()}</span>{/if}
  </h1>

  <div class="toolbar">
    <div class="row1">
      <label class="search">
        <span class="sr-only">Search links</span>
        <input
          type="search"
          bind:this={searchEl}
          bind:value={qInput}
          oninput={onSearchInput}
          onkeydown={onSearchKey}
          placeholder="Search title, key or URL… (press /)"
          autocomplete="off"
          enterkeyhint="search"
        />
      </label>
      {#if phone.matches}
      <button
        type="button"
        class="filters-toggle"
        class:active={filterCount > 0}
        aria-haspopup="dialog"
        onclick={() => (filtersOpen = true)}>Filters{filterCount ? ` (${filterCount})` : ''}</button
      >
      {/if}
    </div>

    {#if !phone.matches}
      <div class="filters">
      <label class="sel">
        <span>Campaign</span>
        <select value={view.campaign} onchange={(e) => go({ ...view, campaign: e.currentTarget.value })}>
          <option value="">All</option>
          <option value="none">No campaign</option>
          {#each campaignOptions as c (c.slug)}<option value={c.slug}>{c.name}</option>{/each}
        </select>
      </label>
      <FacetFilter label="Source" values={sourceFacet} selected={view.filters.source} onchange={(v) => setFilter('source', v)} />
      <FacetFilter label="Medium" values={mediumFacet} selected={view.filters.medium} onchange={(v) => setFilter('medium', v)} />
      <label class="sel">
        <span>Status</span>
        <select value={view.status} onchange={(e) => go({ ...view, status: e.currentTarget.value })}>
          {#each STATUSES as s (s.key)}<option value={s.key}>{s.label}</option>{/each}
        </select>
      </label>
      </div>
    {:else if filtersOpen}
      <FilterSheet count={filterCount} onclear={clearAll} onclose={() => (filtersOpen = false)}>
        <div class="sheet-fields">
      <label class="sel stack">
        <span>Campaign</span>
        <select value={view.campaign} onchange={(e) => go({ ...view, campaign: e.currentTarget.value })}>
          <option value="">All</option>
          <option value="none">No campaign</option>
          {#each campaignOptions as c (c.slug)}<option value={c.slug}>{c.name}</option>{/each}
        </select>
      </label>
        </div>
        <FacetFilter inline label="Source" values={sourceFacet} selected={view.filters.source} onchange={(v) => setFilter('source', v)} />
        <FacetFilter inline label="Medium" values={mediumFacet} selected={view.filters.medium} onchange={(v) => setFilter('medium', v)} />
        <div class="sheet-fields">
      <label class="sel stack">
        <span>Status</span>
        <select value={view.status} onchange={(e) => go({ ...view, status: e.currentTarget.value })}>
          {#each STATUSES as s (s.key)}<option value={s.key}>{s.label}</option>{/each}
        </select>
      </label>
        </div>
      </FilterSheet>
    {/if}

    <div class="row3">
      <div class="seg-group" role="group" aria-label="Group by">
        <span class="lead">Group by</span>
        <button type="button" class="seg" class:on={grouped} aria-pressed={grouped} onclick={() => go({ ...view, group: 'content' })}>Content</button>
        <button type="button" class="seg" class:on={!grouped} aria-pressed={!grouped} onclick={() => go({ ...view, group: 'link' })}>None</button>
      </div>
      <label class="sel sort">
        <span>Sort</span>
        <select value={view.sort} onchange={(e) => go({ ...view, sort: e.currentTarget.value })}>
          {#each SORTS as s (s.key)}<option value={s.key}>{s.label}</option>{/each}
        </select>
      </label>
    </div>

    {#if view.filters.source.length || view.filters.medium.length}
      <div class="chips" role="list" aria-label="Active filters">
        {#each ['source', 'medium'] as const as k (k)}
          {#each view.filters[k] as v (k + '\u0000' + v)}
            <span class="chip" role="listitem">
              <span class="text" title="{FILTER_LABELS[k].toLowerCase()}: {valueLabel(v)}">{FILTER_LABELS[k].toLowerCase()}: {valueLabel(v)}</span>
              <button type="button" aria-label="Remove filter {FILTER_LABELS[k].toLowerCase()}: {valueLabel(v)}" onclick={() => setFilter(k, view.filters[k].filter((x) => x !== v))}>×</button>
            </span>
          {/each}
        {/each}
        <button type="button" class="btn-link clear" onclick={clearAll}>Clear all</button>
      </div>
    {/if}
  </div>

  <Panel title="Your links" noPadding={!loadError}>
    {#if loadError}
      <div class="err">
        <p class="text-error" role="alert">{loadError}</p>
        <Button variant="subtle" onclick={() => reloadCount++}>Retry</Button>
      </div>
    {:else}
      <DataTable
        {columns}
        {rows}
        rowKey={(r) => r.id}
        caption="Your links"
        {loading}
        selectable
        bind:selected
        rowSelectable={(r) => r.kind === 'link'}
        selectAllKeys={$links.map((l) => l.key)}
        rowClass={(r) => (r.kind === 'link' ? (linkStatus(r.link) === 'active' ? (r.child ? 'is-child' : '') : 'is-inactive') : 'is-group')}
        rowLabel={(r) => (r.kind === 'link' ? r.link.title || r.link.key : r.label)}
      >
        {#snippet bulk(keys)}
          <Button variant="subtle" onclick={() => void bulkCopy(keys)}>Copy URLs</Button>
          <RowMenu text="Assign to campaign" label="Assign {keys.length} selected {plural(keys.length, 'link', 'links')} to a campaign" items={assignItems(keys)} />
          <Button variant="subtle" onclick={() => void bulkDeactivate(keys)}>Deactivate</Button>
        {/snippet}
        {#snippet cell(r, col)}
          {#if r.kind === 'group'}
            {#if col.key === 'link'}
              <button
                type="button"
                class="group-toggle"
                aria-expanded={r.open}
                aria-label="{r.open ? 'Collapse' : 'Expand'} {r.label}, {r.links.length} links"
                onclick={() => toggleGroup(r.contentKey)}
              >
                <span class="chev" aria-hidden="true">{r.open ? '▾' : '▸'}</span>
                <span class="gtext">
                  <span class="gtitle" title={r.label}>{r.label}</span>
                  <span class="gmeta">{r.links.length} links on this page</span>
                </span>
              </button>
            {:else if col.key === 'd30'}
              <InlineBar value={sumClicks(r.links, (l) => l.clicks_window ?? 0)} max={maxWindow} />
            {:else if col.key === 'all'}
              <span class="num">{sumClicks(r.links, (l) => l.click_count).toLocaleString()}</span>
            {/if}
          {:else}
            {@const l = r.link}
            {#if col.key === 'link'}
              <div class:child={r.child}>
                <TwoLineCell
                  title={l.title || l.key}
                  meta={linkMeta(l)}
                  href={detailHref(l.key)}
                  dim={linkStatus(l) !== 'active'}
                  tag={tagFor(l)}
                />
              </div>
            {:else if col.key === 'campaign'}
              {#if campaignName(l)}
                <span title={campaignName(l)}>{campaignName(l)}</span>
              {:else}
                <span class="text-muted">—</span>
              {/if}
            {:else if col.key === 'd30'}
              <InlineBar value={l.clicks_window ?? 0} max={maxWindow} />
            {:else if col.key === 'all'}
              <span class="num">{l.click_count.toLocaleString()}</span>
            {:else}
              <RowMenu label={`Actions for ${l.title || l.key}`} copyText={shortUrl(l.key)} items={menuItems(l)} />
            {/if}
          {/if}
        {/snippet}
        {#snippet empty()}
          {#if narrowed}
            <span class="text-muted">No links match.</span>
            <button type="button" class="btn-link" onclick={clearAll}>Clear search and filters</button>
          {:else}
            <span class="text-muted">No links yet — use + New link to create your first one.</span>
          {/if}
        {/snippet}
      </DataTable>

      {#if !loading && totalPages > 1}
        <div class="pager">
          <Button disabled={!hasPrev} onclick={() => navigate(linksPath({ ...view, page: view.page - 1 }))}>Previous</Button>
          <span class="text-muted">Page {view.page} of {totalPages} ({total} links)</span>
          <Button disabled={!hasNext} onclick={() => navigate(linksPath({ ...view, page: view.page + 1 }))}>Next</Button>
        </div>
      {/if}
    {/if}
  </Panel>
</div>

<style>
  .count {
    margin-left: var(--space-2);
    font-size: var(--fs-base);
    font-weight: 400;
  }
  .toolbar {
    margin-top: var(--space-2);
    display: flex;
    flex-direction: column;
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .row1 {
    display: flex;
    gap: var(--space-2);
  }
  .search {
    flex: 1 1 auto;
    min-width: 0;
    margin: 0;
  }
  .search input {
    width: 100%;
    margin: 0;
  }
  .filters-toggle {
    display: inline-flex;
    align-items: center;
    flex: 0 0 auto;
    min-height: 40px;
    padding: 0 var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--bg-panel);
    color: var(--text);
    font: inherit;
    cursor: pointer;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
  }
  .row3 {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    justify-content: space-between;
    gap: var(--space-3);
  }
  .sel {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    margin: 0;
    font-weight: 400;
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .sel select {
    width: auto;
    min-height: 34px;
    margin: 0;
    color: var(--text);
    font-size: var(--fs-base);
  }
  .lead {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    margin-right: var(--space-1);
  }
  .seg-group {
    display: inline-flex;
    align-items: center;
    gap: 0;
  }
  .seg {
    min-height: 34px;
    padding: 0 var(--space-3);
    border: var(--border-w) solid var(--border-strong);
    background: var(--bg-panel);
    color: var(--text);
    font: inherit;
    cursor: pointer;
  }
  .seg:first-of-type {
    border-radius: var(--radius) 0 0 var(--radius);
  }
  .seg:last-of-type {
    border-left: none;
    border-radius: 0 var(--radius) var(--radius) 0;
  }
  .seg.on {
    background: var(--accent-subtle);
    border-color: var(--accent);
    font-weight: 600;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--space-2);
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
  .chip .text {
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
  .clear {
    margin-left: auto;
  }
  .err {
    padding: var(--space-4);
  }
  .num {
    font-variant-numeric: tabular-nums;
    padding-right: var(--space-3);
  }
  .child {
    padding-left: var(--space-5);
  }
:global(.dt tr.is-selected .inline-bar .bar) {
    background: var(--accent);
    opacity: 0.3;
  }
  :global(tr.is-group) {
    background: var(--bg-subtle);
  }
  .group-toggle {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    min-height: 40px;
    padding: 0;
    border: none;
    background: none;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .chev {
    flex: 0 0 auto;
    width: 1em;
    color: var(--text-muted);
  }
  .gtext {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .gtitle {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-weight: 600;
  }
  .gmeta {
    font-size: var(--fs-sm);
    color: var(--text-muted);
  }
  .pager {
    display: flex;
    align-items: center;
    gap: var(--space-4);
    padding: var(--space-3) var(--space-4);
    justify-content: center;
    border-top: var(--border-w) solid var(--border);
    flex-wrap: wrap;
  }
  .filters-toggle.active {
    border-color: var(--accent);
    background: var(--accent-subtle);
    font-weight: 600;
  }
  .sheet-fields {
    padding: var(--space-2) 0;
    border-bottom: var(--border-w) solid var(--border);
  }
  .sel.stack {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: var(--space-1);
    margin: var(--space-2) 0;
    font-size: var(--fs-base);
    font-weight: 600;
    color: var(--text);
  }
  .sel.stack select {
    width: 100%;
    min-height: 44px;
  }
  /* Phone: the filter button is the only filter control on the toolbar; chips scroll on one line. */
  @media (max-width: 899px) {
    .filters-toggle {
      min-height: 44px;
    }
    .search input {
      min-height: 44px;
    }
    .seg {
      min-height: 40px;
    }
    .sel select {
      min-height: 40px;
    }
    .chips {
      flex-wrap: nowrap;
      overflow-x: auto;
      scrollbar-width: none;
      -webkit-overflow-scrolling: touch;
    }
    .chip {
      flex: 0 0 auto;
      min-height: 40px;
      max-width: 80vw;
    }
    .chip button {
      width: 40px;
      height: 40px;
      border-radius: 20px;
    }
    .clear {
      flex: 0 0 auto;
      margin-left: 0;
      min-height: 40px;
    }
    .sort {
      margin-left: auto;
    }
  }
</style>
