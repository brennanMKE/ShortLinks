<!--
  Link Detail view (#0035, #0049). Shows the full stats for a single link,
  read by the `key` route param (#0120 — Dashboard/CampaignDetail navigate to
  /links/{key}; this component fetches its own record by that param on
  mount, a cold load, since Router.svelte remounts it whenever the path
  changes).

  On mount it loads GET /api/links/{key} via getLink, surfacing loading / error
  states (a 404 maps to a "not found" message with a Back action). It displays
  the short URL (with a copy-to-clipboard button), destination, title, status
  (active / inactive / denied-with-reason), created + expiry dates, total click
  count, a clicks-over-time line chart (#0049), and the #0030 UTM breakdown
  (by_source / by_medium / by_campaign, now as horizontal bar charts with
  proportional widths). A Deactivate action calls deactivateLink and reflects the
  new status here and in the shared `links` store; it is hidden once the link is
  inactive/denied. A Back action returns to the dashboard via a real Link.

  An Edit action (#0099) opens a form that REPOPULATES the UTM builder from the
  link's discrete utm_*/placement columns via utmParamsFromLink (lib/utm.ts) —
  closing the #0048 limitation this view used to be unable to offer at all. A
  link created before that migration has all-NULL columns, which utmParamsFromLink
  maps to empty fields rather than erroring. Saving PATCHes destination_url (the
  builder's re-composed URL) together with the five discrete fields and
  placement, so the two stay in lockstep the same way create does.

  QR code (#0106): SVG/PNG download links sit right next to the Short URL
  row, since that's the exact value the code encodes — see docs/campaigns.md
  for the library/error-correction/DPI decisions and internal/qr's package
  doc comment for why it's the short URL, never destination_url.
-->
<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { currentUser, links } from '../lib/stores';
  import { getLink, getLinkPerformance, updateLink, deactivateLink, ApiError } from '../lib/api';
  import { getRoute, replace } from '../lib/hashRouter.svelte';
  import { getChartTimeZone } from '../lib/preferences';
  import {
    RANGE_PRESETS,
    parseRange,
    rangeQuery,
    reportQuery,
    toDayBuckets,
    percentChange,
    siblingRows,
    referrerLabel,
    stripScheme,
    urlSegments,
    rangeLabel,
    type LinkPerformance,
    type RangeSel,
  } from '../lib/linkReport';
  import InlineBar from '../lib/InlineBar.svelte';
  import RelativeDate from '../lib/RelativeDate.svelte';
  import { shortUrl, qrSvgUrl, qrPngUrl, linkStatus, isValidHttpUrl } from '../lib/links';
  import {
    isEmptyStats,
    utmDimensions,
    statusLabel,
    formatDate,
    windowClicks,
    headerMetaParts,
    destinationDisplay,
  } from '../lib/linkDetail';
  import { emptyUtmParams, composeUtmUrl, isUtmEmpty, utmParamsFromLink } from '../lib/utm';
  import type { UtmParams } from '../lib/utm';
  import type { LinkDetail } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import Link from '../lib/Link.svelte';
  import Breadcrumb from '../lib/Breadcrumb.svelte';
  import ClicksChart from '../lib/ClicksChart.svelte';
  import UTMBarChart from '../lib/UTMBarChart.svelte';
  import UtmField from '../lib/UtmField.svelte';
  import UtmConventions from '../lib/UtmConventions.svelte';
  import { defaultDateRange } from '../lib/charts';

  interface Props {
    /** The link key — a route param from /links/:key (#0120). */
    key: string;
  }

  let { key }: Props = $props();

  // The server's timeseries window is the 30 days ending yesterday (UTC); the
  // chart takes it as explicit props rather than deriving it itself (#0161).
  // Recomputed whenever `detail` reloads so a tab left open past midnight
  // does not keep a stale window.
  const chartRange = $derived.by(() => {
    void detail;
    return defaultDateRange(30);
  });

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<LinkDetail | null>(null);

  // ── link-performance report (#0169) ────────────────────────────────────────
  // The range lives in the hash query (?range=7d|90d, or custom with from/to),
  // so a reload or a pasted link keeps it. The report supplies the window's
  // clicks and chart, the previous-period change, last click, siblings and
  // referrers. The in-memory dev server has no reports: then `report` stays
  // null and the view falls back to the link record's own 30-day series.
  const sel = $derived<RangeSel>(parseRange(getRoute().query));
  let report = $state<LinkPerformance | null>(null);
  let reportLoading = $state(false);
  // Set once a report has loaded, so the range control stays after a later fetch fails.
  let hadReport = $state(false);
  let reportFailed = $state(false);
  let reportSeq = 0;
  let customFrom = $state('');
  let customTo = $state('');

  $effect(() => {
    const q = reportQuery(key, sel, getChartTimeZone());
    const seq = ++reportSeq;
    reportLoading = true;
    getLinkPerformance(q)
      .then((r) => {
        if (seq === reportSeq) { report = r; hadReport = true; reportFailed = false; }
      })
      .catch((err) => {
        if (seq !== reportSeq) return;
        if (err instanceof ApiError && err.status === 401) currentUser.set(null);
        report = null;
        reportFailed = true;
      })
      .finally(() => {
        if (seq === reportSeq) reportLoading = false;
      });
  });

  $effect(() => {
    if (sel.kind === 'custom') {
      customFrom = sel.from;
      customTo = sel.to;
    }
  });

  function setRange(next: RangeSel) {
    const q = rangeQuery(next);
    replace(getRoute().path + (q ? '?' + q : ''));
  }
  function applyCustom() {
    if (customFrom && customTo && customFrom <= customTo) {
      setRange({ kind: 'custom', from: customFrom, to: customTo });
    }
  }
  let showCustom = $state(false);
  const customOpen = $derived(showCustom || sel.kind === 'custom');
  const customValid = $derived(!!customFrom && !!customTo && customFrom <= customTo);
  // Today's civil date in the chart time zone (the report's windows use it too).
  const todayIso = new Intl.DateTimeFormat('en-CA', { timeZone: getChartTimeZone() }).format(new Date());

  const rep = $derived(report?.data ?? null);
  const periodClicks = $derived(rep ? rep.totals.clicks : detail ? windowClicks(detail.timeseries) : 0);
  const change = $derived(rep ? percentChange(rep.totals.clicks, rep.totals.previous_clicks) : null);
  // Without a report the view shows the link record's own 30-day series.
  const periodLabel = $derived(report ? rangeLabel(sel, report.window) : '30 days');
  const chartSeries = $derived(rep ? { days: toDayBuckets(rep.timeseries) } : detail?.timeseries);
  const chartWindow = $derived<[string, string]>(report ? [report.window.from, report.window.to] : chartRange);
  const siblings = $derived(
    rep && detail
      ? siblingRows(
          { key: detail.key, label: detail.placement || rep.link.label, campaign_slug: detail.campaign_slug, clicks: rep.totals.clicks },
          rep.siblings,
        )
      : [],
  );
  const siblingMax = $derived(Math.max(1, ...siblings.map((s) => s.clicks)));
  const referrers = $derived(rep?.by_referer ?? []);
  const referrerMax = $derived(Math.max(1, ...referrers.map((r) => r.clicks)));
  const shortSegments = $derived(detail ? urlSegments(stripScheme(shortUrl(detail.key))) : []);

  let titleEl = $state<HTMLElement | null>(null);
  async function focusTitle() {
    await tick();
    titleEl?.focus();
  }

  let deactivating = $state(false);
  let copied = $state(false);

  const status = $derived(detail ? linkStatus(detail) : 'active');
  const shareUrl = $derived(detail ? shortUrl(detail.key) : '');
  const dimensions = $derived(detail ? utmDimensions(detail.utm_stats) : []);
  const noStats = $derived(detail ? isEmptyStats(detail.utm_stats) : true);
  const canDeactivate = $derived(status === 'active');
  const metaParts = $derived(detail ? headerMetaParts(detail) : { channel: '', sourceMedium: '' });

  // ⋯ menu (#0159): Deactivate lives here instead of as a red button.
  let menuOpen = $state(false);
  let menuWrap = $state<HTMLElement | null>(null);
  let moreBtn = $state<HTMLElement | null>(null);
  function onDocClick(e: MouseEvent) {
    if (menuOpen && menuWrap && !menuWrap.contains(e.target as Node)) menuOpen = false;
  }
  // Escape closes the disclosure and puts focus back on its trigger.
  function onDocKey(e: KeyboardEvent) {
    if (e.key === 'Escape' && menuOpen) {
      menuOpen = false;
      moreBtn?.focus();
    }
  }

  // ── Edit mode (#0099) ────────────────────────────────────────────────────────
  let editing = $state(false);
  let editTitle = $state('');
  let editDestinationUrl = $state('');
  let editUtmParams = $state<UtmParams>(emptyUtmParams());
  // editUtmParamsAtStart is a SNAPSHOT of editUtmParams taken once in
  // startEdit and never touched again — composeUtmUrl's `previous` argument
  // (review fix: an earlier version deleted any blank UTM key already baked
  // into editDestinationUrl, unconditionally. For a pre-migration link
  // (all-NULL columns), editUtmParams starts as emptyUtmParams(), so a
  // TITLE-ONLY save — the UTM builder never touched — would strip any
  // ?utm_source=... the link's destination_url already carried from an
  // external source. Passing this snapshot instead of editUtmParams itself
  // means composeUtmUrl only deletes a key the BUILDER demonstrably owned at
  // the start of this edit session, never one it never saw).
  let editUtmParamsAtStart = $state<UtmParams>(emptyUtmParams());
  let editUtmOpen = $state(false);
  let editPlacement = $state('');
  let saving = $state(false);
  let saveError = $state<string | null>(null);

  const editComposedUrl = $derived(composeUtmUrl(editDestinationUrl, editUtmParams, editUtmParamsAtStart));
  const editUrlInvalid = $derived(
    editDestinationUrl.trim() !== '' && !isValidHttpUrl(editDestinationUrl),
  );
  const canSaveEdit = $derived(!saving && editDestinationUrl.trim() !== '' && !editUrlInvalid);

  // Repopulate every field from the CURRENT detail — including the UTM builder
  // from utmParamsFromLink, the point of #0099's edit-repopulation fix.
  function startEdit() {
    if (!detail) return;
    editTitle = detail.title;
    editDestinationUrl = detail.destination_url;
    editUtmParams = utmParamsFromLink(detail);
    // A second, independent call (utmParamsFromLink returns a fresh object
    // every time) so later edits to editUtmParams can never alias this
    // snapshot.
    editUtmParamsAtStart = utmParamsFromLink(detail);
    editPlacement = detail.placement;
    editUtmOpen = !isUtmEmpty(editUtmParams);
    saveError = null;
    editing = true;
  }

  function cancelEdit() {
    editing = false;
    saveError = null;
    focusTitle();
  }

  async function saveEdit() {
    if (!detail || !canSaveEdit) return;
    saving = true;
    saveError = null;
    try {
      const updated = await updateLink(detail.key, {
        title: editTitle.trim(),
        destination_url: editComposedUrl || editDestinationUrl.trim(),
        utm_source: editUtmParams.utm_source.trim(),
        utm_medium: editUtmParams.utm_medium.trim(),
        utm_campaign: editUtmParams.utm_campaign.trim(),
        utm_term: editUtmParams.utm_term.trim(),
        utm_content: editUtmParams.utm_content.trim(),
        placement: editPlacement.trim(),
      });
      // Merge rather than replace: `updated` (a plain Link) carries no
      // utm_stats/timeseries/campaign_name/campaign_slug, which detail's
      // extra LinkDetail fields still hold and PATCH cannot have changed.
      detail = { ...detail, ...updated };
      links.update((cur) => cur.map((l) => (l.key === updated.key ? { ...l, ...updated } : l)));
      editing = false;
      focusTitle();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      saveError = 'Could not save changes. Please try again.';
    } finally {
      saving = false;
    }
  }

  async function load(key: string) {
    loading = true;
    notFound = false;
    loadError = null;
    detail = null;
    try {
      detail = await getLink(key);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      if (err instanceof ApiError && err.status === 404) {
        notFound = true;
        return;
      }
      loadError = 'Could not load this link. Please try again.';
    } finally {
      loading = false;
    }
  }

  async function copyShortUrl() {
    if (!shareUrl) return;
    try {
      await navigator.clipboard.writeText(shareUrl);
      copied = true;
      setTimeout(() => {
        copied = false;
      }, 1500);
    } catch {
      // Clipboard may be unavailable; ignore.
    }
  }

  async function handleDeactivate() {
    if (!detail || !canDeactivate) return;
    const key = detail.key;
    deactivating = true;
    try {
      await deactivateLink(key);
      if (detail) detail = { ...detail, active: false };
      links.update((cur) => cur.map((l) => (l.key === key ? { ...l, active: false } : l)));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      loadError = 'Could not deactivate this link. Please try again.';
    } finally {
      deactivating = false;
    }
  }

  onMount(() => {
    load(key);
  });
</script>

<svelte:document onclick={onDocClick} onkeydown={onDocKey} />

<div class="app-shell detail-shell">
  <Breadcrumb crumbs={[{ label: 'Links', to: '/links' }, { label: detail?.title || '/u/' + key }]} />

  {#if loading}
    <p class="text-muted" role="status">Loading link…</p>
  {:else if notFound}
    <Panel title="Link not found">
      <p class="text-muted">This link no longer exists, or you don't have access to it.</p>
      <Link to="/links" class="btn-link btn-link-primary">Back to links</Link>
    </Panel>
  {:else if loadError && !detail}
    <Panel>
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="primary" onclick={() => load(key)}>Retry</Button>
    </Panel>
  {:else if detail}
    {#if status === 'inactive'}
      <div class="banner" role="status">
        <strong>This link is inactive.</strong> Visitors who open it are not redirected.
      </div>
    {:else if status === 'denied'}
      <div class="banner banner-danger" role="status">
        <strong>{statusLabel(detail)}.</strong> This link is blocked.
      </div>
    {/if}

    {#if !editing}
      <Panel>
        <div class="title-row">
          <h1 class="detail-title" tabindex="-1" bind:this={titleEl} title={detail.title || undefined}>{detail.title || '/u/' + detail.key}</h1>
          <div class="head-actions">
            <Button variant="default" onclick={startEdit}>Edit</Button>
            {#if canDeactivate}
              <div class="menu-wrap" bind:this={menuWrap}>
                <button
                  type="button"
                  class="more-btn"
                  aria-label="More actions"
                  aria-expanded={menuOpen}
                  aria-controls="more-actions"
                  bind:this={moreBtn}
                  onclick={() => (menuOpen = !menuOpen)}
                >⋯</button>
                {#if menuOpen}
                  <div class="menu" id="more-actions">
                    <button
                      type="button"
                      class="menu-item menu-item-danger"
                      disabled={deactivating}
                      onclick={() => { menuOpen = false; handleDeactivate(); }}
                    >{deactivating ? 'Deactivating…' : 'Deactivate link'}</button>
                  </div>
                {/if}
              </div>
            {/if}
          </div>
        </div>

        <p class="meta-line">
          {#if metaParts.channel}<span>{metaParts.channel}</span>{/if}
          {#if metaParts.sourceMedium}<span>{metaParts.sourceMedium}</span>{/if}
          {#if detail.campaign_slug}
            <span><Link to={'/campaigns/' + encodeURIComponent(detail.campaign_slug)} class="text-link">{detail.campaign_name || detail.campaign_slug}</Link></span>
          {/if}
        </p>

        {#if loadError}
          <p class="text-error" role="alert">{loadError}</p>
        {/if}

        <div class="hero">
          <div class="url-box">
            <div class="url-row">
              <a class="short-url" href={shareUrl} target="_blank" rel="noreferrer" aria-label={shareUrl}>{#each shortSegments as seg}{seg}<wbr />{/each}</a>
              <Button variant="primary" onclick={copyShortUrl}>{copied ? 'Copied!' : 'Copy'}</Button>
            </div>
            <a
              class="dest-link"
              href={detail.destination_url}
              target="_blank"
              rel="noreferrer"
              title={detail.destination_url}
            >→ {destinationDisplay(detail.destination_url)}</a>
          </div>
          <div class="qr-box">
            <img class="qr-img" src={qrSvgUrl(detail.key)} alt={'QR code for ' + shareUrl} width="120" height="120" />
            <div class="qr-links">
              <a class="qr-link" href={qrSvgUrl(detail.key)} download>SVG</a>
              <span aria-hidden="true">·</span>
              <a class="qr-link" href={qrPngUrl(detail.key)} download>PNG</a>
            </div>
          </div>
        </div>

        <div class="tiles">
          <div class="tile">
            <span class="tile-label">Clicks, {periodLabel}</span>
            <span class="tile-value">
              {periodClicks.toLocaleString('en-US')}
              {#if change}
                <span class="change change-{change.dir}" aria-label={change.label}>{change.text}</span>
              {/if}
            </span>
          </div>
          <div class="tile"><span class="tile-label">All time</span><span class="tile-value">{(rep?.totals.all_time ?? detail.click_count).toLocaleString('en-US')}</span></div>
          {#if rep}
            <div class="tile">
              <span class="tile-label">Last click</span>
              <span class="tile-value tile-date">
                {#if rep.totals.last_clicked_at}<RelativeDate date={rep.totals.last_clicked_at} />{:else}Never{/if}
              </span>
            </div>
          {/if}
          <div class="tile"><span class="tile-label">Created</span><span class="tile-value tile-date">{formatDate(detail.created_at)}</span></div>
        </div>
      </Panel>
    {:else}
      <Panel title="Edit link">
        <form
          onsubmit={(e) => {
            e.preventDefault();
            saveEdit();
          }}
        >
          <div class="field">
            <label for="edit-title">Title</label>
            <input id="edit-title" type="text" bind:value={editTitle} disabled={saving} />
          </div>

          <div class="field">
            <label for="edit-dest-url">Destination URL</label>
            <input
              id="edit-dest-url"
              type="url"
              inputmode="url"
              bind:value={editDestinationUrl}
              disabled={saving}
              required
              aria-invalid={editUrlInvalid}
              class:input-error={editUrlInvalid}
            />
            {#if editUrlInvalid}
              <p class="text-warn" role="status">Enter a valid http(s) URL.</p>
            {/if}
          </div>

          <div class="utm-section">
            <button
              type="button"
              class="utm-toggle"
              aria-expanded={editUtmOpen}
              onclick={() => { editUtmOpen = !editUtmOpen; }}
            >
              <span class="utm-toggle-chevron" class:open={editUtmOpen}>▶</span>
              Campaign / UTM parameters
            </button>

            {#if editUtmOpen}
              <div class="utm-fields">
                <UtmConventions />

                {#if editDestinationUrl.trim() !== ''}
                  <div class="utm-preview">
                    <p class="utm-preview-label">Destination preview</p>
                    <p class="utm-preview-url" title={editComposedUrl}>{editComposedUrl}</p>
                  </div>
                {/if}

                <UtmField fieldKey="utm_source" id="edit-utm-source" bind:value={editUtmParams.utm_source} disabled={saving} />
                <UtmField fieldKey="utm_medium" id="edit-utm-medium" bind:value={editUtmParams.utm_medium} disabled={saving} />
                <UtmField fieldKey="utm_campaign" id="edit-utm-campaign" bind:value={editUtmParams.utm_campaign} disabled={saving} />
                <UtmField fieldKey="utm_term" id="edit-utm-term" bind:value={editUtmParams.utm_term} disabled={saving} />
                <UtmField fieldKey="utm_content" id="edit-utm-content" bind:value={editUtmParams.utm_content} disabled={saving} />
                <div class="field">
                  <label for="edit-placement">Placement <span class="text-faint">(optional)</span></label>
                  <input id="edit-placement" type="text" bind:value={editPlacement} disabled={saving} />
                </div>
              </div>
            {/if}
          </div>

          {#if saveError}
            <p class="text-error" role="alert">{saveError}</p>
          {/if}

          <div class="row" style="gap: var(--space-2);">
            <Button type="submit" variant="primary" disabled={!canSaveEdit}>
              {saving ? 'Saving…' : 'Save changes'}
            </Button>
            <Button type="button" variant="default" onclick={cancelEdit} disabled={saving}>Cancel</Button>
          </div>
        </form>
      </Panel>
    {/if}

    <Panel title="Clicks over time">
      {#if report || hadReport}
        <div class="range" role="group" aria-label="Date range">
          {#each RANGE_PRESETS as r (r)}
            <button
              type="button"
              class="range-btn"
              class:active={sel.kind === r}
              aria-pressed={sel.kind === r}
              onclick={() => { showCustom = false; setRange({ kind: r, from: '', to: '' }); }}
            >{r.replace('d', ' days')}</button>
          {/each}
          <button
            type="button"
            class="range-btn"
            class:active={sel.kind === 'custom'}
            aria-pressed={sel.kind === 'custom'}
            aria-expanded={customOpen}
            aria-controls="custom-range"
            onclick={() => { showCustom = !showCustom; }}
          >Custom</button>
        </div>
        {#if customOpen}
          <form class="custom-range" id="custom-range" onsubmit={(e) => { e.preventDefault(); applyCustom(); }}>
            <label>From <input type="date" bind:value={customFrom} max={todayIso} /></label>
            <label>To <input type="date" bind:value={customTo} max={todayIso} /></label>
            <Button type="submit" variant="default" disabled={!customValid}>Apply</Button>
          </form>
        {/if}
      {/if}
      {#if reportFailed && hadReport}
        <p class="text-error" role="alert">Could not load this range. Showing the 30-day series.</p>
      {/if}
      <div class="chart-wrap" class:loading={reportLoading}>
        <ClicksChart
          timeseries={chartSeries}
          from={chartWindow[0]}
          to={chartWindow[1]}
          comparison={rep ? toDayBuckets(rep.previous_timeseries) : null}
          comparisonFrom={report?.previous_window?.from}
          partialLast={report?.window.partial_last_day ?? false}
          title={'Clicks, ' + periodLabel}
        />
      </div>
    </Panel>

    {#if rep}
      <div class="insights" class:loading={reportLoading}>
        <Panel title="Same content, other links">
          {#if siblings.length <= 1}
            <p class="text-muted">No other links point to this content.</p>
          {:else}
            <ul class="rank">
              {#each siblings as s (s.key)}
                <li class="rank-row">
                  <div class="rank-name">
                    {#if s.current}
                      <span class="rank-label">{s.label} <span class="text-faint">(this link)</span></span>
                    {:else}
                      <Link to={'/links/' + encodeURIComponent(s.key)} class="text-link rank-label">{s.label}</Link>
                    {/if}
                    {#if s.campaign}<span class="rank-sub">{s.campaign}</span>{/if}
                  </div>
                  <div class="rank-bar"><InlineBar value={s.clicks} max={siblingMax} alwaysShowBar /></div>
                </li>
              {/each}
            </ul>
            {#if rep.truncated}<p class="text-faint rank-note">Showing the most recent links only.</p>{/if}
          {/if}
        </Panel>
        <Panel title="Where clicks came from">
          {#if referrers.length === 0}
            <p class="text-muted">No clicks in this range.</p>
          {:else}
            <ul class="rank">
              {#each referrers as r (r.value)}
                <li class="rank-row">
                  <div class="rank-name"><span class="rank-label">{referrerLabel(r.value)}</span></div>
                  <div class="rank-bar"><InlineBar value={r.clicks} max={referrerMax} alwaysShowBar /></div>
                </li>
              {/each}
            </ul>
          {/if}
        </Panel>
      </div>
    {/if}

    <details class="details-panel">
      <summary>Details</summary>
      <dl class="fields">
        <dt>Status</dt>
        <dd>{statusLabel(detail)}</dd>
        <dt>Expires</dt>
        <dd>{formatDate(detail.expires_at)}</dd>
        <dt>Placement</dt>
        <dd>{detail.placement || '—'}</dd>
        <dt>utm_source</dt>
        <dd>{detail.utm_source || '—'}</dd>
        <dt>utm_medium</dt>
        <dd>{detail.utm_medium || '—'}</dd>
        <dt>utm_campaign</dt>
        <dd>{detail.utm_campaign || '—'}</dd>
        <dt>utm_term</dt>
        <dd>{detail.utm_term || '—'}</dd>
        <dt>utm_content</dt>
        <dd>{detail.utm_content || '—'}</dd>
        <dt>Destination</dt>
        <dd class="break">{detail.destination_url}</dd>
      </dl>
    </details>

    <Panel title="UTM breakdown">
      {#if noStats}
        <p class="text-muted">No click data yet — share this link to start collecting stats.</p>
      {:else}
        <div class="utm-grid">
          {#each dimensions as dim (dim.dimension)}
            <div class="utm-dim">
              <h3 class="utm-dim-title">{dim.label}</h3>
              <UTMBarChart buckets={dim.buckets} dimension={dim.dimension} label={dim.label} />
            </div>
          {/each}
        </div>
      {/if}
    </Panel>
  {/if}
</div>

<style>
  .detail-shell {
    max-width: 760px;
  }
  .title-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-4);
    margin-bottom: var(--space-4);
    flex-wrap: wrap;
  }
  .detail-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0;
    min-width: 0;
    flex: 1 1 14rem;
    overflow-wrap: anywhere;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    /* An explicit line-height plus bottom padding stops the clamp's overflow
       box from clipping descenders (g, y, p) on the last visible line. */
    line-height: 1.35;
    padding-bottom: 0.2em;
  }
  .detail-title:focus { outline: none; }
  .detail-title:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: 2px; }
  .head-actions { display: flex; gap: var(--space-2); align-items: center; }
  .menu-wrap { position: relative; }
  .more-btn {
    min-height: 32px; min-width: 32px; background: var(--bg-panel); color: var(--text);
    border: var(--border-w) solid var(--border-strong); border-radius: var(--radius);
    font-size: var(--fs-lg); line-height: 1; cursor: pointer;
  }
  .more-btn:hover { background: var(--bg-subtle); }
  .menu {
    position: absolute; right: 0; top: calc(100% + 4px); z-index: 20; min-width: 11rem;
    background: var(--bg-panel); border: var(--border-w) solid var(--border-strong);
    border-radius: var(--radius); padding: var(--space-1); box-shadow: 0 4px 12px rgba(0,0,0,.18);
  }
  .menu-item {
    display: block; width: 100%; text-align: left; background: none; border: none;
    padding: var(--space-2) var(--space-3); font: inherit; color: var(--text);
    cursor: pointer; border-radius: var(--radius); min-height: 36px;
  }
  .menu-item:hover { background: var(--bg-subtle); }
  .menu-item-danger { color: var(--danger); }
  .banner {
    background: var(--bg-subtle); border: var(--border-w) solid var(--border-strong);
    border-left: 4px solid var(--warning); border-radius: var(--radius);
    padding: var(--space-3); margin-bottom: var(--space-4);
  }
  .banner-danger { border-left-color: var(--danger); }
  .meta-line {
    display: flex; flex-wrap: wrap; gap: var(--space-1) 0; margin: 0 0 var(--space-4);
    color: var(--text-muted); font-size: var(--fs-sm);
  }
  .meta-line > span + span::before { content: '·'; margin: 0 var(--space-2); }
  .hero { display: flex; gap: var(--space-4); align-items: stretch; margin-bottom: var(--space-4); flex-wrap: wrap; }
  .url-box {
    flex: 1 1 18rem; min-width: 0; background: var(--bg-subtle);
    border: var(--border-w) solid var(--border); border-radius: var(--radius);
    padding: var(--space-4); display: flex; flex-direction: column; gap: var(--space-3); justify-content: center;
  }
  .url-row { display: flex; gap: var(--space-3); align-items: center; justify-content: space-between; flex-wrap: wrap; }
  /* Breaks only at the <wbr> after each "/" — never mid-word. */
  .url-box .short-url { font-size: var(--fs-lg); font-weight: 600; min-width: 0; word-break: normal; overflow-wrap: normal; }
  .url-box .dest-link {
    display: block; color: var(--text-muted); font-size: var(--fs-sm); word-break: normal;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .qr-box { display: flex; flex-direction: column; align-items: center; gap: var(--space-2); }
  .qr-img { background: #fff; border: var(--border-w) solid var(--border); border-radius: var(--radius); padding: 4px; }
  .qr-links { display: flex; gap: var(--space-2); font-size: var(--fs-sm); color: var(--text-muted); }
  .tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(8.5rem, 1fr)); gap: var(--space-3); }
  .change { font-size: var(--fs-sm); font-weight: 600; margin-left: var(--space-1); white-space: nowrap; }
  .change-up { color: var(--success, var(--accent)); }
  .change-down { color: var(--danger); }
  .change-flat { color: var(--text-muted); }
  .range { display: flex; flex-wrap: wrap; gap: var(--space-1); margin-bottom: var(--space-3); }
  .range-btn {
    min-height: 32px; padding: 0 var(--space-3); font: inherit; font-size: var(--fs-sm);
    background: var(--bg-panel); color: var(--text); cursor: pointer;
    border: var(--border-w) solid var(--border-strong); border-radius: var(--radius);
  }
  .range-btn:hover { background: var(--bg-subtle); }
  .range-btn.active { background: var(--accent-subtle); border-color: var(--accent); font-weight: 600; }
  .custom-range { display: flex; flex-wrap: wrap; gap: var(--space-3); align-items: flex-end; margin-bottom: var(--space-3); }
  .custom-range label { display: flex; flex-direction: column; gap: var(--space-1); font-size: var(--fs-sm); color: var(--text-muted); }
  .chart-wrap.loading, .insights.loading { opacity: 0.6; transition: opacity 0.15s; }
  .insights { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 var(--space-4); }
  .rank { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: var(--space-2); }
  .rank-row { display: grid; grid-template-columns: minmax(0, 1fr) 7rem; gap: var(--space-3); align-items: center; }
  .rank-name { min-width: 0; display: flex; flex-direction: column; }
  .rank-label { overflow-wrap: anywhere; }
  .rank-sub { color: var(--text-muted); font-size: var(--fs-sm); }
  .rank-note { margin: var(--space-2) 0 0; font-size: var(--fs-sm); }
  .tile {
    border: var(--border-w) solid var(--border); border-radius: var(--radius);
    padding: var(--space-3); display: flex; flex-direction: column; gap: var(--space-1); min-width: 0;
  }
  .tile-label { color: var(--text-muted); font-size: var(--fs-sm); }
  .tile-value { font-size: var(--fs-xl); font-weight: 600; font-variant-numeric: tabular-nums; }
  .tile-date { font-size: var(--fs-md); line-height: 1.6; }
  .details-panel {
    background: var(--bg-panel); border: var(--border-w) solid var(--border);
    border-radius: var(--radius); margin-bottom: var(--space-4);
  }
  .details-panel summary { padding: var(--space-3) var(--space-4); font-weight: 600; cursor: pointer; }
  .details-panel .fields { padding: 0 var(--space-4) var(--space-3); }
  .break { overflow-wrap: anywhere; }
  @media (max-width: 480px) {
    .tiles { gap: var(--space-2); }
    .tile { padding: var(--space-2); }
    .tile-date { font-size: var(--fs-base); line-height: 2; }
    .qr-box { flex-direction: row; width: 100%; justify-content: flex-start; gap: var(--space-3); }
    .hero { gap: var(--space-3); }
    .url-box { padding: var(--space-3); }
    .rank-row { grid-template-columns: minmax(0, 1fr) 5.5rem; }
  }
  @media (max-width: 760px) {
    .insights { grid-template-columns: 1fr; }
  }
  .fields {
    display: grid;
    grid-template-columns: 9rem 1fr;
    gap: var(--space-2) var(--space-4);
    margin: 0 0 var(--space-3);
  }
  .fields dt {
    color: var(--text-muted);
    font-weight: 600;
    font-size: var(--fs-sm);
    align-self: center;
  }
  .fields dd {
    margin: 0;
    overflow-wrap: anywhere;
  }
  .short-url {
    font-family: var(--font-mono);
    color: var(--accent);
    word-break: break-all;
  }
  .dest-link {
    color: var(--accent);
    word-break: break-all;
  }
  .qr-link {
    color: var(--accent);
    text-decoration: underline;
  }
  .utm-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--space-5);
  }
  .utm-dim-title {
    margin: 0 0 var(--space-2);
    font-size: var(--fs-md);
    font-weight: 600;
  }

  @media (max-width: 480px) {
    /* Stack the dt/dd pairs vertically so labels don't compete with values */
    .fields {
      grid-template-columns: 1fr;
      gap: var(--space-1);
    }
    .fields dt {
      margin-top: var(--space-2);
    }
    .fields dt:first-child {
      margin-top: 0;
    }
    /* UTM grid: already auto-fit minmax(14rem) — 1 col at 375px; no change needed */
  }

  /* ── Edit form (#0099) — mirrors Dashboard.svelte's UTM builder styling ──── */
  .input-error {
    border-color: var(--danger) !important;
  }
  .utm-section {
    margin-bottom: var(--space-3);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .utm-toggle {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    width: 100%;
    background: var(--bg-subtle);
    border: none;
    padding: var(--space-2) var(--space-3);
    font-family: var(--font);
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
    cursor: pointer;
    text-align: left;
  }
  .utm-toggle:hover {
    background: var(--bg-header);
    color: var(--text);
  }
  .utm-toggle-chevron {
    font-size: 9px;
    display: inline-block;
    transition: transform 0.15s ease;
    color: var(--text-faint);
  }
  .utm-toggle-chevron.open {
    transform: rotate(90deg);
  }
  .utm-fields {
    padding: var(--space-3) var(--space-3) var(--space-2);
    border-top: var(--border-w) solid var(--border);
    background: var(--bg-panel);
  }
  .utm-preview {
    margin-top: var(--space-2);
    margin-bottom: var(--space-3);
    padding: var(--space-2) var(--space-3);
    background: var(--bg-subtle);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .utm-preview-label {
    margin: 0 0 var(--space-1);
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
  }
  .utm-preview-url {
    margin: 0;
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
    color: var(--accent);
    word-break: break-all;
    overflow-wrap: anywhere;
  }
</style>
