<!--
  Campaign Detail view (#0103, restructured into tabs by #0164). Reads which
  campaign to show from the `slug` route param (#0120) and fetches its own
  record on mount, since Router.svelte remounts it whenever the path changes.

  Layout (docs/ui-redesign.md section 3.6): breadcrumb, header (name, status
  tag, dates, "+ Add links" and a ⋯ menu), four tiles, then tabs kept in the
  hash (`?tab=`): Content (default), Links, Channels, Placements (only when a
  link has a placement) and Settings. A range control (campaign dates, 7d,
  30d, custom; `?range=` or `?from=&to=`) sits beside the tabs and applies to
  every request on the page (#0124).

  Data, on purpose from several calls:
    - GET /api/campaigns/{slug}        (getCampaign): metadata, all-time
      link_count/total_clicks and every assigned link.
    - GET /api/campaigns/{slug}/stats  (getCampaignStats, with the range's
      from/to): the windowed click count, channel bars, per-link counts,
      placements and the chart series. A failure is non-fatal.
    - GET /api/reports/campaign-performance and campaign-comparison (#0143):
      the content x channel matrix, the tiles' links/idle/top content and the
      change against the previous period. Not mounted in STORAGE=json dev
      mode; the Content tab then says so and the rest of the page still works.
  Days are civil days in the Account time zone (getChartTimeZone, #0190): the
  stats, the CSV and the report calls all send the same `tz`, so the matrix,
  chart and table agree. The stats window is half-open.

  FOUR NUMBERS, TWO TIME SCALES (#0102 constraint 2): the all-time
  link_count/total_clicks are never reconciled with the windowed click count.

  Links tab (#0157): five columns on #0155's DataTable, grouped by content
  from the Go `top-content` report (`?group=0` turns grouping off), bulk
  actions, Export menu and #0165's undo toast on Remove. QR, Remove, Open,
  Copy and Edit live in the ⋯ menu.

  Add links (batch create #0105, assign #0099) opens as a dialog; the assign
  chunking and partial-failure handling is unchanged.
-->
<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { currentUser } from '../lib/stores';
  import {
    getCampaign,
    getCampaignStats,
    getCampaignContentGroups,
    getCampaignPerformance,
    getCampaignComparison,
    updateCampaign,
    assignLinksToCampaign,
    unassignLinkFromCampaign,
    ApiError,
  } from '../lib/api';
  import {
    campaignDateRangeLabel,
    campaignStatusLabel,
    windowLabel,
    windowDayCount,
    clicksPerDayAverage,
    buildLinkRows,
    unlistedClickCount,
    chunkKeys,
    parseKeysInput,
    copyAllShortUrlsText,
    campaignQrZipUrl,
    campaignExportCsvUrl,
    MAX_QR_ZIP_KEYS,
    toDateInput,
    toIsoDate,
    joinSentences,
    campaignUtmDimensions,
    isEmptyCampaignChannelStats,
    hasNamedPlacement,
  } from '../lib/campaigns';
  import { qrSvgUrl, qrPngUrl, shortUrl, shortUrlBase } from '../lib/links';
  import {
    buildViewGroups,
    sortViewGroups,
    flattenGroups,
    flatRows,
    channelText,
    linkMeta,
    groupChannels,
    groupChannelsShort,
    hasAnyPlacement,
    reportWindow,
    selectedLinksCsv,
    type TableRow,
    type TopContentGroup,
    type SortKey,
  } from '../lib/campaignLinks';
  import {
    TABS,
    campaignPath,
    campaignStatusOf,
    comparisonRow,
    idleLinks,
    parseCampaignView,
    statsRange,
    topContentTile,
    type CampaignView,
    type RangeKey,
    type Tab,
    type MatrixBy,
  } from '../lib/campaignView';
  import type { CampaignComparisonRow, CampaignPerformanceData, ReportEnvelope } from '../lib/reports';
  import { changeOf, plural } from '../lib/overview';
  import { getRoute, navigate, replace } from '../lib/hashRouter.svelte';
  import { columnMax, type Column, type SortState } from '../lib/dataTable';
  import type { MenuItem } from '../lib/rowMenu';
  import type { CampaignDetail, CampaignRollup } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import DataTable from '../lib/DataTable.svelte';
  import TwoLineCell from '../lib/TwoLineCell.svelte';
  import RowMenu from '../lib/RowMenu.svelte';
  import InlineBar from '../lib/InlineBar.svelte';
  import { pushToast } from '../lib/toasts';
  import { removedFromCampaignMessage } from '../lib/linkActions';
  import Panel from '../lib/Panel.svelte';
  import Link from '../lib/Link.svelte';
  import Breadcrumb from '../lib/Breadcrumb.svelte';
  import CampaignClicksChart from '../lib/CampaignClicksChart.svelte';
  import UTMBarChart from '../lib/UTMBarChart.svelte';
  import BatchCreateLinks from '../lib/BatchCreateLinks.svelte';
  import StatTile from '../lib/StatTile.svelte';
  import Dialog from '../lib/Dialog.svelte';
  import CampaignRange from '../lib/CampaignRange.svelte';
  import { getChartTimeZone } from '../lib/preferences';
  import CampaignMatrix, { type ChildLink } from '../lib/CampaignMatrix.svelte';

  interface Props {
    /** The campaign slug — a route param from /campaigns/:slug (#0120). */
    slug: string;
  }

  let { slug }: Props = $props();

  const MAX_NAME_LENGTH = 255;

  // ── View state: the hash query is the state (#0164) ─────────────────────
  const route = $derived(getRoute());
  const view = $derived<CampaignView>(parseCampaignView(route.rawQuery));
  // Days are counted in the Account time zone, so "today" is the viewer's.
  const tz = getChartTimeZone();
  const today = new Intl.DateTimeFormat('en-CA', { timeZone: tz }).format(new Date());
  // Campaign dates sends no days: the server's default is right (#0190).
  const rangeOf = () => statsRange(view, today);
  const keyOf = (r: { from?: string; to?: string }) => `${r.from ?? ''}|${r.to ?? ''}`;

  const hrefTab = (t: Tab) => '#' + campaignPath(slug, route.rawQuery, { tab: t });
  const hrefRange = (r: Exclude<RangeKey, 'custom'>) => '#' + campaignPath(slug, route.rawQuery, { range: r, from: '', to: '' });
  const hrefBy = (by: MatrixBy) => '#' + campaignPath(slug, route.rawQuery, { by });
  function applyCustom(from: string, to: string) {
    navigate(campaignPath(slug, route.rawQuery, { range: 'custom', from, to }));
  }

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<CampaignDetail | null>(null);
  const rangeKey = $derived(keyOf(rangeOf()));
  // The windowed stats for the selected range; null until the first answer.
  let rollup = $state<CampaignRollup | null>(null);
  let statsFailed = $state(false);
  // A range change keeps the old numbers on screen, dimmed, until the new ones land.
  let refreshing = $state(false);

  const stats = $derived(rollup ?? detail?.stats);
  const byLink = $derived(rollup?.by_link ?? []);
  const byPlacement = $derived(rollup?.by_placement ?? []);
  const seriesByLink = $derived(rollup?.series_by_link ?? []);
  const timeseries = $derived(rollup?.timeseries ?? detail?.timeseries);

  // ── Charts (#0104) ────────────────────────────────────────────────────
  const channelDimensions = $derived(campaignUtmDimensions(stats));
  const noChannelData = $derived(isEmptyCampaignChannelStats(stats));

  // ── Links table (#0157) ────────────────────────────────────────────────
  const rows = $derived(buildLinkRows(detail?.links ?? [], byLink));
  // Clicks in byLink attributed to a link no longer assigned to this
  // campaign — excluded from both the numerator and denominator of the
  // shares, so it is surfaced separately rather than silently making the
  // visible shares look like they cover everything (#0103 fix 3).
  const unlistedClicks = $derived(unlistedClickCount(detail?.links ?? [], byLink));

  // Content groups from the Go top-content report; null = unavailable.
  let contentGroups = $state<TopContentGroup[] | null>(null);
  // The report hit its row cap, so grouping may be incomplete.
  let groupsTruncated = $state(false);
  const viewGroups = $derived(buildViewGroups(rows, contentGroups));
  const totalClicks = $derived(rows.reduce((sum, r) => sum + r.clicksInWindow, 0));

  // "Group by content" lives in the hash query (?group=0 turns it off).
  const groupOn = $derived(view.group === 'content');
  const canGroup = $derived(contentGroups !== null);
  const grouped = $derived(groupOn && canGroup);
  function setGroupOn(on: boolean) {
    replace(campaignPath(slug, route.rawQuery, { group: on ? 'content' : 'link' }));
  }

  let sort = $state<SortState | null>({ key: 'clicks', dir: 'desc' });
  const sortKey = $derived<SortKey>(sort?.key === 'link' ? 'link' : 'clicks');
  const sortDir = $derived(sort?.dir ?? 'desc');
  let expanded = $state<Set<string>>(new Set());
  let selected = $state<Set<string>>(new Set());

  const tableRows = $derived<TableRow[]>(
    grouped
      ? flattenGroups(sortViewGroups(viewGroups, sortKey, sortDir), expanded, totalClicks)
      : flatRows(viewGroups, sortKey, sortDir, totalClicks),
  );
  const barMax = $derived(columnMax(tableRows, (r) => (r.kind === 'group' ? r.group.clicks : r.link.clicksInWindow)));
  const showPlacement = $derived(hasAnyPlacement(rows));
  const linksSummary = $derived(
    `${rows.length} link${rows.length === 1 ? '' : 's'}` +
      (grouped && viewGroups.some((g) => g.links.length > 1)
        ? ` · ${viewGroups.length} content group${viewGroups.length === 1 ? '' : 's'}`
        : ''),
  );

  const columns = $derived<Column<TableRow>[]>([
    { key: 'link', label: 'Link', sortable: true, mobile: 'title' },
    { key: 'channel', label: 'Channel', width: '9.5rem', mobile: 'meta' },
    ...(showPlacement ? [{ key: 'placement', label: 'Placement', width: '8rem' } as Column<TableRow>] : []),
    { key: 'clicks', label: 'Clicks', width: '7.5rem', align: 'right' as const, sortable: true, mobile: 'metric' as const },
    { key: 'actions', label: 'Actions', width: '6.5rem', hideLabel: true, mobile: 'actions' as const },
  ]);

  function toggleSort() {
    sort = { key: 'clicks', dir: sortKey === 'clicks' && sortDir === 'desc' ? 'asc' : 'desc' };
  }
  function toggleGroup(id: string) {
    const next = new Set(expanded);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    expanded = next;
  }

  let copiedAll = $state(false);
  async function writeClipboard(text: string): Promise<boolean> {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Clipboard may be unavailable; ignore.
      return false;
    }
  }
  async function copyAll() {
    const text = copyAllShortUrlsText(rows);
    if (text === '') return;
    if (await writeClipboard(text)) {
      copiedAll = true;
      setTimeout(() => {
        copiedAll = false;
      }, 1500);
    }
  }

  const exportItems = $derived<MenuItem[]>([
    { label: copiedAll ? 'Copied!' : 'Copy all short URLs', onselect: copyAll, disabled: rows.length === 0 },
    { label: 'Download all QR codes (zip)', href: campaignQrZipUrl(slug) },
    { label: 'Download CSV', href: campaignExportCsvUrl(slug, tz) },
  ]);

  function linkMenu(row: Extract<TableRow, { kind: 'link' }>): MenuItem[] {
    const l = row.link;
    return [
      { label: 'Edit link', onselect: () => navigate('/links/' + encodeURIComponent(l.key)) },
      { label: 'Copy short URL', onselect: () => void writeClipboard(shortUrl(l.key)) },
      { label: 'Open destination', href: l.destination_url },
      { label: 'Download QR (SVG)', href: qrSvgUrl(l.key) },
      { label: 'Download QR (PNG)', href: qrPngUrl(l.key) },
      { label: 'Remove from campaign', danger: true, onselect: () => void removeLinks([l.key]) },
    ];
  }

  function rowsForKeys(keys: string[]) {
    return rows.filter((r) => keys.includes(r.key));
  }
  async function copySelected(keys: string[]) {
    const text = copyAllShortUrlsText(rowsForKeys(keys));
    if (text) await writeClipboard(text);
  }
  // #0191: the selection's QR codes. Group rows (content) select their links'
  // keys, so only keys that are links of this campaign go in. The server caps
  // the filter, so the whole campaign goes without keys (one download) and a
  // partial selection over the cap is refused in the bulk bar.
  function qrZipKeys(keys: string[]): string[] | undefined {
    const linkKeys = rowsForKeys(keys).map((r) => r.key);
    return linkKeys.length >= rows.length ? undefined : linkKeys;
  }
  function qrZipTooMany(keys: string[]): boolean {
    const k = qrZipKeys(keys);
    return k !== undefined && k.length > MAX_QR_ZIP_KEYS;
  }
  function downloadQrZip(keys: string[]) {
    const a = document.createElement('a');
    a.href = campaignQrZipUrl(slug, qrZipKeys(keys));
    a.download = '';
    document.body.appendChild(a);
    a.click();
    a.remove();
  }
  function exportSelected(keys: string[]) {
    const blob = new Blob([selectedLinksCsv(rowsForKeys(keys))], { type: 'text/csv' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `${slug}-selected-links.csv`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  }

  // ── Reports: matrix and tiles (#0164) ────────────────────────────────────
  type PerfState = 'loading' | 'ready' | 'unavailable';
  let perf = $state<ReportEnvelope<CampaignPerformanceData> | null>(null);
  let perfState = $state<PerfState>('loading');
  let comparison = $state<CampaignComparisonRow | null>(null);
  // The content row being pointed at; its links' lines are emphasised in the chart.
  let highlightKeys = $state<string[]>([]);
  // Bumped after assign/remove so the reports refetch with the links' new membership.
  let reportTick = $state(0);
  let perfSeq = 0;
  let cmpSeq = 0;

  // The reports follow the stats window of the selected range. Until that
  // answer arrives (or when the stats call failed) detail.stats is the same
  // days only for the default window, so it is only a fallback after a failure.
  const win = $derived(reportWindow(rollup ?? (statsFailed ? detail?.stats : undefined)));
  const winKey = $derived(win ? `${win.from}|${win.to}` : '');

  $effect(() => {
    // Re-run on a new window, a new column dimension or a membership change.
    void reportTick;
    const key = winKey;
    const by = view.by;
    const s = untrack(() => slug);
    const w = untrack(() => win);
    if (!w || !key) {
      perf = null;
      perfState = statsFailed ? 'unavailable' : 'loading';
      return;
    }
    const mine = ++perfSeq;
    perfState = 'loading';
    getCampaignPerformance(s, w.from, w.to, by, tz)
      .then((r) => {
        if (mine !== perfSeq) return;
        perf = r;
        perfState = 'ready';
      })
      .catch((err) => {
        if (mine !== perfSeq) return;
        if (err instanceof ApiError && err.status === 401) {
          currentUser.set(null);
          return;
        }
        perf = null;
        perfState = 'unavailable';
      });
  });

  $effect(() => {
    void reportTick;
    const key = winKey;
    const s = untrack(() => slug);
    const w = untrack(() => win);
    if (!w || !key) {
      comparison = null;
      return;
    }
    const mine = ++cmpSeq;
    getCampaignComparison(s, w.from, w.to, tz)
      .then((r) => {
        if (mine === cmpSeq) comparison = comparisonRow(r.data.campaigns, s);
      })
      .catch(() => {
        if (mine === cmpSeq) comparison = null;
      });
  });

  const perfData = $derived(perf?.data ?? null);
  const clicksInWindow = $derived(perfData?.totals.clicks ?? stats?.click_count ?? 0);
  // Under Campaign dates the "previous period" is the days before the campaign
  // began, which says nothing useful, so the change is only shown for the
  // 7d / 30d / custom ranges.
  const clicksChange = $derived(
    view.range === 'campaign'
      ? null
      : comparison && comparison.previous_clicks !== null
      ? changeOf(comparison.clicks, comparison.previous_clicks)
      : perfData?.totals.previous_clicks != null
        ? changeOf(perfData.totals.clicks, perfData.totals.previous_clicks)
        : null,
  );
  const perDay = $derived(
    comparison?.clicks_per_active_day ??
      perfData?.totals.per_active_day ??
      clicksPerDayAverage(stats?.click_count ?? 0, windowDayCount(stats)),
  );
  const idle = $derived(perfData ? idleLinks(perfData.totals.links, perfData.totals.links_clicked) : null);
  const topTile = $derived(
    perfData
      ? topContentTile(perfData.matrix.rows, perfData.totals.clicks)
      : comparison?.top_content
        ? {
            title: comparison.top_content.title,
            clicks: comparison.top_content.clicks,
            sharePct: comparison.clicks > 0 ? Math.round((comparison.top_content.clicks / comparison.clicks) * 100) : 0,
          }
        : null,
  );

  /** The links of an expanded matrix row, most clicks first. */
  function childLinks(keys: string[]): ChildLink[] {
    return keys
      .map((k) => {
        const r = rows.find((x) => x.key === k);
        return {
          key: k,
          title: r?.title || k,
          clicks: r ? r.clicksInWindow : null,
          meta: linkMeta(k, [r?.utm_source, r?.utm_medium].filter(Boolean).join(' · '), ''),
        };
      })
      .sort((a, b) => (b.clicks ?? 0) - (a.clicks ?? 0));
  }

  // ── Load ───────────────────────────────────────────────────────────────
  // load() also runs as a REFRESH after assign/unassign and when the range
  // changes, not just on first mount. `loading` — which swaps the whole view
  // for "Loading campaign…" — is only touched on the initial load (detail
  // still null); a refresh keeps the existing page rendered with the old data
  // until the new data arrives (#0103 nit 10).
  let loadSeq = 0;
  async function load(slug: string) {
    const initial = detail === null;
    const mine = ++loadSeq;
    if (initial) loading = true;
    else refreshing = true;
    notFound = false;
    loadError = null;
    let r: { from?: string; to?: string } = {};
    try {
      const d = await getCampaign(slug, tz);
      if (mine !== loadSeq) return;
      detail = d;
      r = untrack(() => rangeOf());
      loadedRange = keyOf(r);
    } catch (err) {
      if (mine !== loadSeq) return;
      refreshing = false;
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      if (err instanceof ApiError && err.status === 404) {
        notFound = true;
        if (initial) loading = false;
        return;
      }
      loadError = 'Could not load this campaign. Please try again.';
      if (initial) loading = false;
      return;
    }

    // Non-fatal: the page degrades to 0 clicks / 0% share rather than
    // blocking on a second request (#0102 constraint 5: dev mode's stats
    // provider always returns empty/zero here anyway).
    let next: CampaignRollup | null = null;
    try {
      next = await getCampaignStats(slug, tz, r.from, r.to);
    } catch {
      next = null;
    }
    if (mine !== loadSeq) return;
    rollup = next;
    statsFailed = next === null;

    // Content groups: also non-fatal. Without them (dev mode has no
    // /api/reports, or the call fails) the table is the flat link list.
    const w = reportWindow(next ?? detail?.stats);
    let groups: TopContentGroup[] | null = null;
    let truncated = false;
    if (w) {
      try {
        const data = (await getCampaignContentGroups(slug, w.from, w.to, tz)).data;
        groups = data.groups;
        truncated = !!data.truncated;
      } catch {
        groups = null;
      }
    }
    if (mine !== loadSeq) return;
    contentGroups = groups;
    groupsTruncated = truncated;

    if (initial) {
      loading = false;
      resetEdit();
    }
    refreshing = false;
  }

  // A new range refetches the windowed numbers (not the first run: onMount loads).
  let loadedRange: string | null = null;
  $effect(() => {
    const key = rangeKey;
    if (loadedRange === null || key === loadedRange) return;
    untrack(() => void load(slug));
  });

  // ── Settings: edit, default UTMs, archive ────────────────────────────────
  let editName = $state('');
  let editDescription = $state('');
  let editStartsAt = $state('');
  let editEndsAt = $state('');
  let editUtm = $state({ source: '', medium: '', campaign: '', term: '', content: '' });
  let saving = $state(false);
  let saveError = $state<string | null>(null);
  let saved = $state(false);

  const editNameLength = $derived([...editName.trim()].length);
  const editNameTooLong = $derived(editNameLength > MAX_NAME_LENGTH);
  const editDatesInvalid = $derived(
    editStartsAt !== '' && editEndsAt !== '' && editEndsAt < editStartsAt,
  );
  const canSaveEdit = $derived(
    !saving && editName.trim() !== '' && !editNameTooLong && !editDatesInvalid,
  );

  function resetEdit() {
    if (!detail) return;
    editName = detail.name;
    editDescription = detail.description;
    editStartsAt = toDateInput(detail.starts_at);
    editEndsAt = toDateInput(detail.ends_at);
    editUtm = {
      source: detail.default_utm_source,
      medium: detail.default_utm_medium,
      campaign: detail.default_utm_campaign,
      term: detail.default_utm_term,
      content: detail.default_utm_content,
    };
    saveError = null;
    saved = false;
  }

  async function saveEdit() {
    if (!detail || !canSaveEdit) return;
    saving = true;
    saveError = null;
    saved = false;
    try {
      const updated = await updateCampaign(detail.slug, {
        name: editName.trim(),
        description: editDescription.trim(),
        starts_at: toIsoDate(editStartsAt),
        ends_at: toIsoDate(editEndsAt),
        default_utm_source: editUtm.source.trim(),
        default_utm_medium: editUtm.medium.trim(),
        default_utm_campaign: editUtm.campaign.trim(),
        default_utm_term: editUtm.term.trim(),
        default_utm_content: editUtm.content.trim(),
      });
      detail = { ...detail, ...updated };
      resetEdit();
      saved = true;
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      saveError = err instanceof ApiError ? err.message : 'Could not save changes. Please try again.';
    } finally {
      saving = false;
    }
  }

  let archiving = $state(false);
  async function handleArchiveToggle() {
    if (!detail) return;
    archiving = true;
    try {
      const updated = await updateCampaign(detail.slug, { archived: !detail.archived });
      detail = { ...detail, ...updated };
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      loadError = 'Could not update the campaign. Please try again.';
    } finally {
      archiving = false;
    }
  }

  // ── Header menus ──────────────────────────────────────────────────────────
  // "+ Add links" opens one of two dialogs; there is no campaign delete in the
  // API, so the ⋯ menu offers Edit and Archive only.
  let dialog = $state<'batch' | 'assign' | null>(null);
  const addItems = $derived<MenuItem[]>([
    { label: 'Create links for this campaign…', onselect: () => (dialog = 'batch') },
    { label: 'Assign existing links…', onselect: () => (dialog = 'assign') },
  ]);
  const moreItems = $derived<MenuItem[]>([
    { label: 'Edit campaign', onselect: () => navigate(campaignPath(slug, route.rawQuery, { tab: 'settings' })) },
    {
      label: detail?.archived ? 'Unarchive campaign' : 'Archive campaign',
      onselect: () => void handleArchiveToggle(),
      disabled: archiving,
    },
  ]);

  // ── Assign links (#0099) ──────────────────────────────────────────────────
  let assignInput = $state('');
  let assigning = $state(false);
  let assignError = $state<string | null>(null);
  let assignNotice = $state<string | null>(null);
  const parsedKeys = $derived(parseKeysInput(assignInput));

  async function handleAssign() {
    if (!detail || parsedKeys.length === 0) return;
    const campaignSlug = detail.slug;
    assigning = true;
    assignError = null;
    assignNotice = null;
    const chunks = chunkKeys(parsedKeys);
    let succeeded = 0;
    let redirected = false;
    try {
      for (const chunk of chunks) {
        // Count success from the RESPONSE's links array, not chunk.length —
        // the server's own record of what actually landed, rather than an
        // assumption that a non-throwing request assigned every key it sent.
        const res = await assignLinksToCampaign(campaignSlug, chunk);
        succeeded += res.links.length;
      }
      assignNotice = `Assigned ${succeeded} link${succeeded === 1 ? '' : 's'}.`;
      assignInput = '';
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        redirected = true;
      } else {
        const reason = err instanceof ApiError ? err.message : 'Could not reach the server.';
        // AssignLinks (internal/handlers/campaigns.go) is documented NOT
        // atomic across keys: it stops at the first failing key, leaving
        // every key before it — INCLUDING keys earlier in the SAME request —
        // already assigned. `succeeded` can therefore only count fully
        // completed prior chunks — it is a lower bound, not the true count.
        // The unconditional reload below is what makes the table correct
        // regardless; this message is just the best available explanation.
        // joinSentences because `reason` is the server's message verbatim and
        // is not guaranteed to end in punctuation (#0103 fix 4).
        assignError =
          succeeded > 0
            ? joinSentences(
                reason,
                `${succeeded} link${succeeded === 1 ? ' was' : 's were'} assigned before the failure; the rest were not.`,
              )
            : joinSentences(
                reason,
                'Some links in this batch may have been assigned before the failure — the list below reflects the current state.',
              );
      }
    } finally {
      assigning = false;
    }
    // Always reload after a non-401 error too, not just on success: a
    // mid-chunk failure can leave links assigned server-side that `succeeded`
    // never counted, so only a reload shows the true state (#0103 fix 2).
    if (!redirected) {
      await load(campaignSlug);
      reportTick++;
    }
  }

  // ── Remove from campaign ─────────────────────────────────────────────────
  // #0165: Remove acts at once (row menu or bulk bar) and offers one Undo
  // toast that re-assigns every link removed by that action.
  function removeLinks(keys: string[]) {
    if (keys.length > 0) void performUnassign(keys);
  }

  async function performUnassign(keys: string[]) {
    if (!detail) return;
    const campaignSlug = detail.slug;
    const campaignName = detail.name;
    const removed: string[] = [];
    let failed = false;
    try {
      for (const key of keys) {
        await unassignLinkFromCampaign(campaignSlug, key);
        removed.push(key);
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        return;
      }
      failed = true;
    }
    selected = new Set();
    await load(campaignSlug);
    reportTick++;
    if (removed.length > 0) {
      pushToast({
        message:
          removed.length === 1
            ? removedFromCampaignMessage(campaignName)
            : `${removed.length} links removed from ${campaignName}.`,
        actionLabel: 'Undo',
        returnFocus: () => document.querySelector<HTMLElement>('h1.detail-title'),
        onaction: async () => {
          try {
            for (const chunk of chunkKeys(removed)) await assignLinksToCampaign(campaignSlug, chunk);
          } catch (err) {
            if (err instanceof ApiError && err.status === 401) currentUser.set(null);
            throw new Error(removed.length === 1 ? 'Could not put the link back. Please try again.' : 'Could not put the links back. Please try again.');
          }
          if (detail?.slug === campaignSlug) {
            await load(campaignSlug);
            reportTick++;
          }
        },
      });
    }
    if (failed) {
      pushToast({
        message:
          removed.length > 0
            ? `Could not remove every link; ${removed.length} of ${keys.length} were removed.`
            : 'Could not remove this link. Please try again.',
      });
    }
  }

  // ── Tabs ─────────────────────────────────────────────────────────────────
  const hasPlacements = $derived(hasNamedPlacement(byPlacement));
  const tabs = $derived(TABS.filter((t) => t.key !== 'placements' || hasPlacements));
  // A tab that is not offered (placements on a campaign without any) shows Content.
  const tab = $derived<Tab>(tabs.some((t) => t.key === view.tab) ? view.tab : 'content');
  let whyOpen = $state(false);

  const status = $derived(detail ? campaignStatusOf(detail, today) : 'live');
  function statusBadge(status: string): string {
    return status === 'live' ? 'badge badge-success' : status === 'scheduled' ? 'badge' : 'badge badge-muted';
  }

  onMount(() => {
    load(slug);
  });
</script>

<div class="app-shell detail-shell">
  <Breadcrumb crumbs={[{ label: 'Campaigns', to: '/campaigns' }, { label: detail?.name || slug }]} />

  {#if loading}
    <p class="text-muted" role="status">Loading campaign…</p>
  {:else if notFound}
    <Panel title="Campaign not found">
      <p class="text-muted">This campaign no longer exists, or you don't have access to it.</p>
      <Link to="/campaigns" class="btn-link btn-link-primary">Back to campaigns</Link>
    </Panel>
  {:else if loadError && !detail}
    <Panel>
      <p class="text-error" role="alert">{loadError}</p>
      <Button variant="primary" onclick={() => load(slug)}>Retry</Button>
    </Panel>
  {:else if detail}
    <!-- Header -->
    <header class="head">
      <div class="head-text">
        <div class="title-row">
          <h1 class="detail-title">{detail.name}</h1>
          <span class={statusBadge(status)}>{campaignStatusLabel(status)}</span>
          <span class="dates text-muted">{campaignDateRangeLabel(detail)}</span>
        </div>
        {#if detail.description}
          <p class="text-muted description">{detail.description}</p>
        {/if}
      </div>
      <div class="head-actions">
        <RowMenu text="+ Add links" label="Add links to this campaign" items={addItems} />
        <RowMenu label="More actions for this campaign" items={moreItems} />
      </div>
    </header>

    {#if loadError}
      <p class="text-error" role="alert">{loadError}</p>
    {/if}

    <!-- Tiles -->
    <div class="tiles" class:stale={refreshing}>
      <StatTile
        label="Clicks"
        value={clicksInWindow.toLocaleString()}
        sub={windowLabel(stats)}
        change={clicksChange}
        changeNote="vs prior"
      />
      <StatTile
        label="Per day"
        value={String(perDay)}
        sub={comparison?.clicks_per_active_day != null || perfData?.totals.per_active_day != null ? 'per active day' : 'average over window'}
      />
      <StatTile
        label="Links"
        value={String(perfData ? perfData.totals.links : detail.link_count)}
        sub={idle !== null ? `${idle} idle in this range` : 'current membership'}
      />
      <StatTile
        label="Top content"
        value={topTile ? topTile.title : '—'}
        valueTitle={topTile?.title}
        wrap
        sub={topTile ? `${topTile.clicks.toLocaleString()} ${plural(topTile.clicks, 'click', 'clicks')} · ${topTile.sharePct}%` : 'No clicks in this range'}
      />
    </div>

    <!-- Tabs and the range -->
    <div class="tabbar">
      <nav class="tabs" aria-label="Campaign sections">
        {#each tabs as t (t.key)}
          <a class="tab" class:on={tab === t.key} href={hrefTab(t.key)} aria-current={tab === t.key ? 'page' : undefined}>{t.label}</a>
        {/each}
      </nav>
      <CampaignRange {view} hrefFor={hrefRange} onapply={applyCustom} {today} />
    </div>

    <div class="tab-body" class:stale={refreshing}>
      {#if tab === 'content'}
        <Panel title="Clicks over time">
          <p class="text-faint chart-window-label">{windowLabel(stats)}</p>
          <CampaignClicksChart
            {timeseries}
            {seriesByLink}
            windowFrom={stats?.window_from ?? ''}
            windowTo={stats?.window_to ?? ''}
            {highlightKeys}
          />
        </Panel>

        <Panel title="Content by channel">
          {#if perfState === 'ready' && perfData}
            <CampaignMatrix
              matrix={perfData.matrix}
              totalClicks={perfData.totals.clicks}
              by={view.by}
              byHref={hrefBy}
              {childLinks}
              onhover={(keys) => (highlightKeys = keys)}
            />
            {#if perfData.removed_links_clicks > 0}
              <p class="text-faint note">
                {perfData.removed_links_clicks.toLocaleString()} {plural(perfData.removed_links_clicks, 'click', 'clicks')} came from links since removed from this campaign.
              </p>
            {/if}
            {#if perfData.truncated}
              <p class="text-faint note" role="status">The report hit its row limit, so some content may be missing from the table.</p>
            {/if}
          {:else if perfState === 'loading'}
            <p class="text-muted" role="status">Loading the content breakdown…</p>
          {:else}
            <p class="text-muted">
              The content breakdown needs the reports API, which this server does not provide. The Links tab still lists every link.
            </p>
          {/if}
        </Panel>
      {:else if tab === 'links'}
        <Panel title="Links" noPadding>
          <div class="links-toolbar row spread">
            <span class="text-muted links-count">
              {linksSummary}
            </span>
            <div class="row toolbar-actions">
              {#if canGroup}
                <label class="group-toggle">
                  <input type="checkbox" checked={groupOn} onchange={(e) => setGroupOn(e.currentTarget.checked)} />
                  Group by content
                </label>
              {/if}
              <!-- The phone card layout has no header row, so sorting needs its own control. -->
              <span class="mobile-sort-toggle">
                <Button
                  variant="subtle"
                  onclick={toggleSort}
                  aria-label={`Sort by clicks, currently ${sortKey === 'clicks' && sortDir === 'asc' ? 'lowest first' : 'highest first'}`}
                >
                  Sort: Clicks {sortKey === 'clicks' && sortDir === 'asc' ? '▲' : '▼'}
                </Button>
              </span>
              <RowMenu text="Export" label="Export links" items={exportItems} />
            </div>
          </div>

          {#if grouped && groupsTruncated}
            <p class="text-faint links-toolbar-error" role="status">
              Grouping is partial: the report hit its row limit, so some links may not be grouped with their content.
            </p>
          {/if}

          {#if rows.length === 0}
            <p class="text-muted empty-msg">No links assigned yet. Use “+ Add links” above.</p>
          {:else}
            {#snippet linkCell(row: TableRow)}
              {#if row.kind === 'group'}
                <div class="group-cell">
                  <button
                    type="button"
                    class="twisty"
                    aria-expanded={row.expanded}
                    aria-label={`${row.expanded ? 'Collapse' : 'Expand'} ${row.group.title}`}
                    onclick={() => toggleGroup(row.group.id)}
                  >
                    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" class:open={row.expanded}><path d="M6 3.5l4.5 4.5L6 12.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
                  </button>
                  <TwoLineCell wrapAlways title={row.group.title} meta={`${row.group.links.length} links · ${row.group.path}`} />
                </div>
              {:else}
                <div class="link-cell" class:child={row.child}>
                  <TwoLineCell
                    wrapAlways
                    title={row.title}
                    meta={linkMeta(row.link.key, row.child ? '' : row.link.label, row.child ? '' : row.path)}
                    href={'#/links/' + encodeURIComponent(row.link.key)}
                    dim={!row.link.active}
                    tag={row.link.active ? '' : 'Inactive'}
                  />
                </div>
              {/if}
            {/snippet}
            {#snippet cell(row: TableRow, col: Column<TableRow>)}
              {#if col.key === 'link'}
                {@render linkCell(row)}
              {:else if col.key === 'channel'}
                {#if row.kind === 'group'}
                  {@const ch = groupChannels(row.group.links)}
                  <span class="text-muted grp-ch" title={ch}>{groupChannelsShort(row.group.links) || '—'}</span>
                {:else}
                  {@const ch = channelText(row.link)}
                  <span class="text-muted bidi-ltr" title={ch}>{ch || '—'}</span>
                {/if}
              {:else if col.key === 'placement'}
                {#if row.kind === 'link'}
                  <span class="text-muted" title={row.link.placement}>{row.link.placement || '—'}</span>
                {/if}
              {:else if col.key === 'clicks'}
                {@const clicks = row.kind === 'group' ? row.group.clicks : row.link.clicksInWindow}
                {@const share = row.kind === 'group' ? row.group.share : row.share}
                <div class="clicks-cell">
                  <InlineBar value={clicks} max={barMax} />
                  <span class="share">{share}%</span>
                </div>
              {:else if col.key === 'actions'}
                {#if row.kind === 'link'}
                  <RowMenu items={linkMenu(row)} label={`Actions for ${row.title}`} copyText={shortUrl(row.link.key)} />
                {/if}
              {/if}
            {/snippet}
            {#snippet bulk(keys: string[])}
              <Button variant="subtle" onclick={() => copySelected(keys)}>Copy URLs</Button>
              <Button variant="subtle" onclick={() => exportSelected(keys)}>Export</Button>
              <Button
                variant="subtle"
                disabled={qrZipTooMany(keys)}
                title={qrZipTooMany(keys) ? `Select up to ${MAX_QR_ZIP_KEYS} links for a QR zip` : undefined}
                onclick={() => downloadQrZip(keys)}>QR zip</Button
              >
              {#if qrZipTooMany(keys)}
                <span class="text-faint">Select up to {MAX_QR_ZIP_KEYS} links for a QR zip</span>
              {/if}
              <Button variant="subtle" onclick={() => removeLinks(keys)}>Remove from campaign</Button>
            {/snippet}
            <DataTable
              {columns}
              rows={tableRows}
              rowKey={(r) => r.id}
              {cell}
              caption="Links in this campaign"
              bind:sort
              serverSort
              firstDir="asc"
              selectable
              bind:selected
              {bulk}
              rowSelectable={(r) => r.kind === 'link'}
              selectAllKeys={rows.map((r) => r.key)}
              rowLabel={(r) => (r.kind === 'link' ? r.title : r.group.title)}
              rowClass={(r) => (r.kind === 'group' ? 'group-row' : '')}
            />
            {#if unlistedClicks > 0}
              <p class="text-faint unlisted-note">
                {unlistedClicks} click{unlistedClicks === 1 ? '' : 's'} in this window came from links no longer
                assigned to this campaign and are not listed above.
              </p>
            {/if}
          {/if}
        </Panel>
      {:else if tab === 'channels'}
        <Panel title="Channel breakdown">
          <p class="text-faint chart-window-label">{windowLabel(stats)}</p>
          {#if noChannelData}
            <p class="text-muted">No click data yet — share this campaign's links to start collecting stats.</p>
          {:else}
            <div class="utm-grid">
              {#each channelDimensions as dim (dim.dimension)}
                <div class="utm-dim">
                  <h3 class="utm-dim-title">{dim.label}</h3>
                  <UTMBarChart buckets={dim.buckets} dimension={dim.dimension} label={dim.label} />
                </div>
              {/each}
            </div>
            <!-- Inside the has-data branch: the note explains how to read the BARS. -->
            <p class="text-faint honest-comparison-note">
              Bar height is not a verdict on which channel performed better.
              <button type="button" class="why" aria-expanded={whyOpen} onclick={() => (whyOpen = !whyOpen)}>Why?</button>
            </p>
            {#if whyOpen}
              <p class="text-faint honest-comparison-note why-text">
                Bot filtering reduces but does not eliminate social-channel inflation — some preview crawlers
                don't identify themselves. Print scans carry no bot noise and are the cleanest signal per click,
                but naturally show fewer clicks in raw comparison, since impression volume (how many people saw
                the post vs. how many walked past the poster) differs by orders of magnitude between channels,
                and ShortLinks has no way to measure impressions.
              </p>
            {/if}
          {/if}
        </Panel>
      {:else if tab === 'placements'}
        <!-- Clicks by placement (#0122). Rows arrive sorted from the server. -->
        <Panel title="Clicks by placement" noPadding>
          <p class="text-faint chart-window-label placement-window-label">{windowLabel(stats)}</p>
          <div class="table-scroll">
            <table class="placement-table">
              <colgroup>
                <col />
                <col class="placement-num-col" />
                <col class="placement-num-col" />
              </colgroup>
              <thead>
                <tr>
                  <th>Placement</th>
                  <th class="num">Links</th>
                  <th class="num">Clicks</th>
                </tr>
              </thead>
              <tbody>
                <!-- Keyed by index: placement text is not unique (a link may be literally "(none)"). -->
                {#each byPlacement as row, i (i)}
                  <tr>
                    <td class="placement-name" class:text-muted={row.is_none} title={row.placement}>{row.placement}</td>
                    <td class="num">{row.link_count}</td>
                    <td class="num">{row.count}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </Panel>
      {:else if tab === 'settings'}
        <Panel title="Campaign settings">
          <form
            onsubmit={(e) => {
              e.preventDefault();
              saveEdit();
            }}
          >
            <div class="field">
              <label for="edit-campaign-name">Name</label>
              <input
                id="edit-campaign-name"
                type="text"
                bind:value={editName}
                disabled={saving}
                required
                aria-invalid={editNameTooLong}
                class:input-error={editNameTooLong}
              />
              {#if editNameTooLong}
                <p class="text-error" role="alert">Name must be at most {MAX_NAME_LENGTH} characters.</p>
              {/if}
            </div>

            <div class="field">
              <label for="edit-campaign-description">Description <span class="text-faint">(optional)</span></label>
              <input id="edit-campaign-description" type="text" bind:value={editDescription} disabled={saving} />
            </div>

            <div class="date-row">
              <div class="field">
                <label for="edit-campaign-starts">Starts <span class="text-faint">(optional)</span></label>
                <input id="edit-campaign-starts" type="date" bind:value={editStartsAt} disabled={saving} />
              </div>
              <div class="field">
                <label for="edit-campaign-ends">Ends <span class="text-faint">(optional)</span></label>
                <input
                  id="edit-campaign-ends"
                  type="date"
                  bind:value={editEndsAt}
                  disabled={saving}
                  aria-invalid={editDatesInvalid}
                  class:input-error={editDatesInvalid}
                />
              </div>
            </div>
            {#if editDatesInvalid}
              <p class="text-error" role="alert">End date must not be before the start date.</p>
            {/if}

            <h3 class="settings-sub">Default UTM values</h3>
            <p class="text-faint settings-note">Prefilled on new links for this campaign; each link can still change them.</p>
            <div class="utm-defaults">
              <div class="field">
                <label for="edit-utm-source">Source</label>
                <input id="edit-utm-source" type="text" bind:value={editUtm.source} disabled={saving} />
              </div>
              <div class="field">
                <label for="edit-utm-medium">Medium</label>
                <input id="edit-utm-medium" type="text" bind:value={editUtm.medium} disabled={saving} />
              </div>
              <div class="field">
                <label for="edit-utm-campaign">Campaign</label>
                <input id="edit-utm-campaign" type="text" bind:value={editUtm.campaign} disabled={saving} />
              </div>
              <div class="field">
                <label for="edit-utm-term">Term</label>
                <input id="edit-utm-term" type="text" bind:value={editUtm.term} disabled={saving} />
              </div>
              <div class="field">
                <label for="edit-utm-content">Content</label>
                <input id="edit-utm-content" type="text" bind:value={editUtm.content} disabled={saving} />
              </div>
            </div>

            <p class="text-faint slug-edit-note">
              The URL slug (<span class="mono">{detail.slug}</span>) is fixed and will not change, even if you rename the campaign.
            </p>

            {#if saveError}
              <p class="text-error" role="alert">{saveError}</p>
            {/if}
            {#if saved}
              <p class="text-notice" role="status">Saved.</p>
            {/if}

            <div class="row" style="gap: var(--space-2);">
              <Button type="submit" variant="primary" disabled={!canSaveEdit}>
                {saving ? 'Saving…' : 'Save changes'}
              </Button>
              <Button type="button" variant="default" onclick={resetEdit} disabled={saving}>Reset</Button>
            </div>
          </form>
        </Panel>

        <Panel title={detail.archived ? 'Archived' : 'Archive'}>
          <p class="text-muted settings-note">
            {detail.archived
              ? 'This campaign is archived. Its links and clicks are kept; unarchive it to bring it back to the live list.'
              : 'Archiving hides the campaign from the live list. Its links and clicks are kept, and you can unarchive it at any time.'}
          </p>
          <Button variant={detail.archived ? 'default' : 'danger'} disabled={archiving} onclick={handleArchiveToggle}>
            {archiving ? 'Saving…' : detail.archived ? 'Unarchive campaign' : 'Archive campaign'}
          </Button>
        </Panel>
      {/if}
    </div>

    {#if dialog === 'batch'}
      <Dialog title="Create links for this campaign" wide onclose={() => (dialog = null)}>
        <BatchCreateLinks
          campaign={detail}
          oncreated={() => {
            void load(detail!.slug).then(() => reportTick++);
          }}
          onclose={() => (dialog = null)}
        />
      </Dialog>
    {:else if dialog === 'assign'}
      <Dialog title="Assign existing links" onclose={() => (dialog = null)}>
        <div class="field">
          <label for="assign-keys">Link keys or short URLs</label>
          <textarea
            id="assign-keys"
            rows="3"
            placeholder={`abc123, ${shortUrlBase()}/u/def456`}
            bind:value={assignInput}
            disabled={assigning}
          ></textarea>
          <p class="text-faint">
            Separate multiple with commas, spaces, or new lines. Sent in batches of up to 50.
          </p>
        </div>

        {#if assignError}
          <p class="text-error" role="alert">{assignError}</p>
        {/if}
        {#if assignNotice}
          <p class="text-notice" role="status">{assignNotice}</p>
        {/if}

        <div class="dlg-actions">
          <Button variant="default" onclick={() => (dialog = null)}>Close</Button>
          <Button variant="primary" onclick={handleAssign} disabled={assigning || parsedKeys.length === 0}>
            {assigning
              ? 'Assigning…'
              : parsedKeys.length > 0
                ? `Assign ${parsedKeys.length} link${parsedKeys.length === 1 ? '' : 's'}`
                : 'Assign links'}
          </Button>
        </div>
      </Dialog>
    {/if}
  {/if}
</div>

<style>
  .detail-shell {
    max-width: 960px;
  }

  /* ── Header ─────────────────────────────────────────────────────────── */
  .head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
  }
  .head-text {
    min-width: 0;
  }
  .head-actions {
    display: flex;
    align-items: center;
    gap: var(--space-2);
    flex: none;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    flex-wrap: wrap;
  }
  .detail-title {
    font-size: var(--fs-title);
    font-weight: 600;
    line-height: 1.25;
    margin: 0;
    overflow-wrap: anywhere;
  }
  .dates {
    font-size: var(--fs-sm);
  }
  .description {
    margin: var(--space-1) 0 0;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
  }
  .slug-edit-note {
    display: block;
    font-size: var(--fs-sm);
    margin: var(--space-3) 0;
  }
  .input-error {
    border-color: var(--danger) !important;
  }
  .date-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }
  .utm-defaults {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr));
    gap: 0 var(--space-3);
  }
  .settings-sub {
    margin: var(--space-4) 0 var(--space-1);
    font-size: var(--fs-md);
    font-weight: 600;
  }
  .settings-note {
    margin: 0 0 var(--space-3);
    font-size: var(--fs-sm);
  }
  .dlg-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--space-2);
    margin-top: var(--space-4);
  }

  /* ── Tiles, tabs ────────────────────────────────────────────────────── */
  .tiles {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--space-3);
    margin-bottom: var(--space-4);
  }
  .tiles.stale,
  .tab-body.stale {
    opacity: 0.7;
    transition: opacity 0.15s;
  }
  .tabbar {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--space-3);
    margin-bottom: var(--space-3);
    border-bottom: var(--border-w) solid var(--border);
  }
  .tabs {
    display: flex;
    flex-wrap: wrap;
    gap: var(--space-1);
    margin-bottom: -1px;
  }
  .tab {
    display: inline-flex;
    align-items: center;
    min-height: 40px;
    padding: 0 var(--space-3);
    border-bottom: 2px solid transparent;
    color: var(--text-muted);
    text-decoration: none;
  }
  .tab:hover {
    color: var(--text);
  }
  .tab.on {
    color: var(--text);
    font-weight: 600;
    border-bottom-color: var(--accent);
  }
  .tabbar :global(.range-wrap) {
    margin-bottom: var(--space-2);
  }

  /* ── Charts (#0104) ─────────────────────────────────────────────────── */
  .chart-window-label {
    margin: 0 0 var(--space-3);
    font-size: var(--fs-sm);
  }
  .note {
    margin: var(--space-3) 0 0;
    font-size: var(--fs-sm);
  }
  .utm-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
    gap: var(--space-5);
  }
  .honest-comparison-note {
    margin: var(--space-4) 0 0;
    font-size: var(--fs-sm);
    max-width: 60rem;
  }
  .why-text {
    margin-top: var(--space-2);
  }
  .why {
    border: none;
    background: none;
    padding: 0;
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
  }
  .utm-dim-title {
    margin: 0 0 var(--space-2);
    font-size: var(--fs-md);
    font-weight: 600;
  }

  /* ── Links table (#0157) ────────────────────────────────────────────── */
  .links-toolbar {
    padding: var(--space-3) var(--space-4);
    border-bottom: var(--border-w) solid var(--border);
  }
  .links-toolbar-error {
    padding: var(--space-2) var(--space-4) var(--space-3);
    margin: 0;
  }
  .toolbar-actions {
    flex-wrap: wrap;
    gap: var(--space-3);
    align-items: center;
  }
  .group-toggle {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    font-size: var(--fs-base);
    cursor: pointer;
  }
  .group-toggle input { width: 16px; height: 16px; margin: 0; accent-color: var(--accent); }
  /* Hidden on desktop: the Link / Clicks headers sort. Shown on the phone card layout. */
  .mobile-sort-toggle { display: none; }
  .link-cell,
  .group-cell { min-width: 0; }
  .group-cell { display: flex; align-items: center; gap: var(--space-2); }
  .group-cell :global(.two-line) { flex: 1; }
  .link-cell.child { padding-left: var(--space-5); }
  .twisty {
    flex: none;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 28px;
    height: 28px;
    padding: 0;
    border: none;
    border-radius: var(--radius);
    background: transparent;
    color: var(--text-muted);
    cursor: pointer;
  }
  .twisty:hover { background: var(--bg-subtle); color: var(--text); }
  .twisty:focus-visible { outline: 2px solid var(--accent); }
  .twisty svg { transition: transform 0.1s; }
  .twisty svg.open { transform: rotate(90deg); }
  .clicks-cell { display: flex; flex-direction: column; align-items: stretch; gap: 1px; }
  .share {
    padding: 0 var(--space-2);
    text-align: right;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }
  .bidi-ltr { direction: ltr; unicode-bidi: isolate; }
  :global(tr.group-row) { background: var(--bg-subtle); }
  .empty-msg {
    padding: var(--space-4);
  }
  /* Clicks by placement (#0122): fixed layout + 100% width means the table can
     never exceed .table-scroll at any viewport (#0103 zero horizontal scroll). */
  .placement-table {
    width: 100%;
    table-layout: fixed;
  }
  .placement-window-label {
    padding: var(--space-3) var(--space-4) 0;
  }
  .placement-num-col {
    width: 5.5rem;
  }
  .placement-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .unlisted-note {
    padding: 0 var(--space-4) var(--space-3);
    font-size: var(--fs-sm);
  }

  @media (max-width: 900px) {
    .tiles {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }

  @media (max-width: 480px) {
    .date-row {
      grid-template-columns: 1fr;
    }
    .head {
      flex-wrap: wrap;
    }
  }

  @media (max-width: 899px) {
    .mobile-sort-toggle { display: inline-flex; }
    .link-cell.child { padding-left: var(--space-4); }
    .clicks-cell { align-items: flex-end; }
    /* Line the group's channel summary up under its title, past the twisty. */
    .grp-ch { display: block; padding-left: calc(40px + var(--space-2)); }
    .twisty { width: 40px; height: 40px; margin: -6px 0; }
    .links-toolbar { flex-direction: column; align-items: stretch; gap: var(--space-2); }
    .toolbar-actions { justify-content: flex-start; gap: var(--space-2) var(--space-3); }
    .group-toggle { min-height: 40px; }
    .group-toggle input { width: 20px; height: 20px; }
  }
</style>
