<!--
  Campaign Detail view (#0103). Reads which campaign to show from the `slug`
  route param (#0120 — CampaignsList navigates to /campaigns/{slug}; this
  component fetches its own record by that param on mount, a cold load, since
  Router.svelte remounts it whenever the path changes).

  Two API calls back this view, on purpose:
    - GET /api/campaigns/{slug}       (getCampaign)      — metadata, the
      ALL-TIME link_count/total_clicks, every currently-assigned link, and
      (when a stats provider is wired) WINDOWED CampaignStats for the
      summary numbers.
    - GET /api/campaigns/{slug}/stats (getCampaignStats)  — the same window's
      per-link breakdown (`by_link`), which the first endpoint deliberately
      omits (#0098 downstream constraint 8: campaignDetailView is additive
      over campaignView, not a full CampaignRollup). The links table's
      "clicks in window" and "share of campaign total" columns come from
      here, matched to each link by key (lib/campaigns.ts buildLinkRows).
  A failure fetching the stats call is NON-FATAL to the page — the links
  table still renders with 0/0% in that case rather than blocking on a
  request the campaign/links data does not need.

  FOUR NUMBERS, TWO TIME SCALES (#0102 constraint 2): the summary shows
  link_count/total_clicks (ALL-TIME) beside stats.click_count (WINDOWED,
  labelled with the resolved window) — see types.ts's CampaignDetail doc
  comment. They are never reconciled into one number.

  Header: name, description, date range, and Edit/Archive controls. The edit
  form states that the slug never changes (#0098 downstream constraint 4).

  Links table (#0157): five columns on #0155's DataTable — select, Link
  (two-line: title, then `/u/key` · channel label · path), Channel
  (`source · medium`), Clicks (InlineBar, share under it) and a ⋯ menu. A
  Placement column appears only when some link has one. Rows are grouped by
  content by default (toggle in the toolbar, kept in the hash as `?group=0`):
  the groups come from the Go `top-content` report (#0140) filtered to this
  campaign over the same window as the stats; a multi-link group is one row
  that expands to its links, a lone link is a plain row. When the report is
  unavailable (dev mode has no /api/reports) the toggle is hidden and the
  table is the flat list. QR SVG/PNG, Remove, Open, Copy and Edit live in the
  ⋯ menu; the bulk bar has Copy URLs, Remove and Export; "Export ▾" in the
  toolbar merges Copy all, the QR zip and the CSV. Sortable by Link or
  Clicks (stable, lib/campaignLinks.ts). Every link row goes to the existing
  link-detail view.

  Bulk QR download and CSV export (#0106, #0107): the same endpoints as
  before, now reached from the Export ▾ menu. Both stay enabled for an empty
  campaign (empty archive / header-only CSV, never an error).

  Assign/unassign against #0099's endpoints: the assign form accepts pasted
  keys or short URLs (lib/campaigns.ts parseKeysInput), chunks anything over
  the server's 50-key cap (chunkKeys), and surfaces partial success/failure
  since AssignLinks is documented non-atomic across keys.

  Charts (#0104) are a deliberate empty slot below the summary.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentUser } from '../lib/stores';
  import {
    getCampaign,
    getCampaignStats,
    getCampaignContentGroups,
    updateCampaign,
    assignLinksToCampaign,
    unassignLinkFromCampaign,
    ApiError,
  } from '../lib/api';
  import {
    campaignDateRangeLabel,
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
  import { getRoute, navigate, replace } from '../lib/hashRouter.svelte';
  import { columnMax, type Column, type SortState } from '../lib/dataTable';
  import type { MenuItem } from '../lib/rowMenu';
  import type { CampaignDetail, LinkBucket, LinkSeries, PlacementBucket } from '../lib/types';
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

  interface Props {
    /** The campaign slug — a route param from /campaigns/:slug (#0120). */
    slug: string;
  }

  let { slug }: Props = $props();

  const MAX_NAME_LENGTH = 255;

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<CampaignDetail | null>(null);
  let byLink = $state<LinkBucket[]>([]);
  let byPlacement = $state<PlacementBucket[]>([]);
  let seriesByLink = $state<LinkSeries[]>([]);

  // ── Charts (#0104) ────────────────────────────────────────────────────
  const channelDimensions = $derived(campaignUtmDimensions(detail?.stats));
  const noChannelData = $derived(isEmptyCampaignChannelStats(detail?.stats));

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
  const groupOn = $derived(getRoute().query.get('group') !== '0');
  const canGroup = $derived(contentGroups !== null);
  const grouped = $derived(groupOn && canGroup);
  function setGroupOn(on: boolean) {
    const q = new URLSearchParams(getRoute().query);
    if (on) q.delete('group');
    else q.set('group', '0');
    const qs = q.toString();
    replace(`/campaigns/${encodeURIComponent(slug)}${qs ? `?${qs}` : ''}`);
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
    { label: 'Download CSV', href: campaignExportCsvUrl(slug) },
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

  // ── Load ───────────────────────────────────────────────────────────────
  // load() also runs as a REFRESH after assign/unassign, not just on first
  // mount. `loading` — which swaps the whole view for "Loading campaign…" —
  // is only touched on the initial load (detail still null); a refresh
  // keeps the existing table/summary rendered with stale data until the new
  // detail arrives, rather than blanking the page on every edit (#0103 nit
  // 10 — a full-page spinner on every assign/unassign read as broken).
  async function load(slug: string) {
    const initial = detail === null;
    if (initial) loading = true;
    notFound = false;
    loadError = null;
    try {
      detail = await getCampaign(slug);
    } catch (err) {
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

    // Non-fatal: the links table degrades to 0 clicks / 0% share rather than
    // blocking the whole page on a second request (#0102 constraint 5: dev
    // mode's stats provider always returns empty/zero here anyway).
    try {
      const rollup = await getCampaignStats(slug);
      byLink = rollup.by_link;
      byPlacement = rollup.by_placement ?? [];
      seriesByLink = rollup.series_by_link;
    } catch {
      byLink = [];
      byPlacement = [];
      seriesByLink = [];
    }

    // Content groups: also non-fatal. Without them (dev mode has no
    // /api/reports, or the call fails) the table is the flat link list.
    const win = reportWindow(detail?.stats);
    if (win) {
      try {
        const data = (await getCampaignContentGroups(slug, win.from, win.to)).data;
        contentGroups = data.groups;
        groupsTruncated = !!data.truncated;
      } catch {
        contentGroups = null;
        groupsTruncated = false;
      }
    } else {
      contentGroups = null;
      groupsTruncated = false;
    }

    if (initial) loading = false;
  }

  // ── Edit ───────────────────────────────────────────────────────────────
  let editing = $state(false);
  let editName = $state('');
  let editDescription = $state('');
  let editStartsAt = $state('');
  let editEndsAt = $state('');
  let saving = $state(false);
  let saveError = $state<string | null>(null);

  const editNameLength = $derived([...editName.trim()].length);
  const editNameTooLong = $derived(editNameLength > MAX_NAME_LENGTH);
  const editDatesInvalid = $derived(
    editStartsAt !== '' && editEndsAt !== '' && editEndsAt < editStartsAt,
  );
  const canSaveEdit = $derived(
    !saving && editName.trim() !== '' && !editNameTooLong && !editDatesInvalid,
  );

  function startEdit() {
    if (!detail) return;
    editName = detail.name;
    editDescription = detail.description;
    editStartsAt = toDateInput(detail.starts_at);
    editEndsAt = toDateInput(detail.ends_at);
    saveError = null;
    editing = true;
  }

  function cancelEdit() {
    editing = false;
    saveError = null;
  }

  async function saveEdit() {
    if (!detail || !canSaveEdit) return;
    saving = true;
    saveError = null;
    try {
      const updated = await updateCampaign(detail.slug, {
        name: editName.trim(),
        description: editDescription.trim(),
        starts_at: toIsoDate(editStartsAt),
        ends_at: toIsoDate(editEndsAt),
      });
      detail = { ...detail, ...updated };
      editing = false;
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

  // ── Archive / unarchive ──────────────────────────────────────────────────
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
        // already assigned. A failure partway through a single chunk (e.g.
        // spr001, nosuchkey, spr002: spr001 lands, nosuchkey 404s, spr002 is
        // never attempted) never returns a links array for that chunk, so
        // `succeeded` here can only count fully-completed prior chunks — it
        // is a lower bound, not the true count. The unconditional reload
        // below is what makes the table correct regardless; this message is
        // just the best available explanation of what happened.
        // joinSentences (not a bare template-literal space) because `reason`
        // is the server's message verbatim and is not guaranteed to end in
        // punctuation — e.g. "link not found: nosuchkey" — which previously
        // ran straight into the next sentence with no separator at all
        // (#0103 fix 4).
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
    // Refresh outside the try/finally so a redirect-to-login never triggers
    // an extra load() the destroyed view can't use. Always reload after a
    // non-401 error too (not just on success / succeeded > 0): AssignLinks'
    // non-atomic, mid-chunk failures can leave links assigned server-side
    // that `succeeded` never counted (see comment above), so only a reload
    // — never the client's own tally — can show the true state (#0103 fix
    // 2; previously gated on succeeded > 0, so this exact partial-failure
    // case reported "0 links" and never refreshed).
    if (!redirected) {
      await load(campaignSlug);
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
          if (detail?.slug === campaignSlug) await load(campaignSlug);
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
    <Panel>
      <div class="title-row">
        <h1 class="detail-title">{detail.name}</h1>
        {#if detail.archived}<span class="badge badge-muted">Archived</span>{/if}
      </div>
      {#if detail.description}
        <p class="text-muted description">{detail.description}</p>
      {/if}

      <dl class="fields">
        <dt>Date range</dt>
        <dd>{campaignDateRangeLabel(detail)}</dd>

        <dt>Short URL slug</dt>
        <dd>
          <span class="mono">{detail.slug}</span>
          <span class="text-faint slug-note">— fixed at creation; renaming this campaign will not change it.</span>
        </dd>
      </dl>

      {#if loadError}
        <p class="text-error" role="alert">{loadError}</p>
      {/if}

      <div class="row" style="gap: var(--space-2); margin-top: var(--space-3);">
        <Button variant="default" onclick={startEdit} disabled={editing}>Edit</Button>
        <Button
          variant={detail.archived ? 'default' : 'danger'}
          disabled={archiving}
          onclick={handleArchiveToggle}
        >
          {archiving ? 'Saving…' : detail.archived ? 'Unarchive campaign' : 'Archive campaign'}
        </Button>
      </div>
    </Panel>

    {#if editing}
      <Panel title="Edit campaign">
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

          <p class="text-faint slug-edit-note">
            The URL slug (<span class="mono">{detail.slug}</span>) is fixed and will not change, even if you rename the campaign.
          </p>

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

    <!-- Summary -->
    <Panel title="Summary">
      <div class="summary-grid">
        <div class="stat">
          <span class="stat-label">Links</span>
          <span class="stat-value">{detail.link_count}</span>
          <span class="stat-sub text-faint">current membership</span>
        </div>
        <div class="stat">
          <span class="stat-label">Total clicks</span>
          <span class="stat-value">{detail.total_clicks}</span>
          <span class="stat-sub text-faint">all-time</span>
        </div>
        <div class="stat">
          <span class="stat-label">Clicks in window</span>
          <span class="stat-value">{detail.stats?.click_count ?? 0}</span>
          <span class="stat-sub text-faint">{windowLabel(detail.stats)}</span>
        </div>
        <div class="stat">
          <span class="stat-label">Clicks / day</span>
          <span class="stat-value">
            {clicksPerDayAverage(detail.stats?.click_count ?? 0, windowDayCount(detail.stats))}
          </span>
          <span class="stat-sub text-faint">average over window</span>
        </div>
        {#if (detail.stats?.excluded_bot_count ?? 0) > 0}
          <div class="stat stat-warn">
            <span class="stat-label">Bots excluded</span>
            <span class="stat-value">{detail.stats?.excluded_bot_count}</span>
            <span class="stat-sub text-faint">not counted above</span>
          </div>
        {/if}
      </div>
    </Panel>

    <!-- Clicks over time (#0104) -->
    <Panel title="Clicks over time">
      <p class="text-faint chart-window-label">{windowLabel(detail.stats)}</p>
      <CampaignClicksChart
        timeseries={detail.timeseries}
        {seriesByLink}
        windowFrom={detail.stats?.window_from ?? ''}
        windowTo={detail.stats?.window_to ?? ''}
      />
    </Panel>

    <!-- Channel breakdown (#0104) -->
    <Panel title="Channel breakdown">
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
        <!-- Inside the has-data branch on purpose: the note explains how to read
             the BARS, so on an empty campaign it would follow "No click data yet"
             with a paragraph ending "Relative bar height here does not by itself
             say which channel performed better" — about bars that aren't on
             screen (#0108 review nit 3). -->
        <p class="text-faint honest-comparison-note">
          Bot filtering reduces but does not eliminate social-channel inflation — some preview crawlers
          don't identify themselves. Print scans carry no bot noise and are the cleanest signal per click,
          but naturally show fewer clicks in raw comparison, since impression volume (how many people saw
          the post vs. how many walked past the poster) differs by orders of magnitude between channels,
          and ShortLinks has no way to measure impressions. Relative bar height here does not by itself
          say which channel performed better.
        </p>
      {/if}
    </Panel>

    <!-- Clicks by placement (#0122). Rows arrive sorted from the server. -->
    <Panel title="Clicks by placement" noPadding>
      <p class="text-faint chart-window-label placement-window-label">{windowLabel(detail.stats)}</p>
      {#if byPlacement.length === 0}
        <p class="text-muted empty-msg">No links or clicks yet.</p>
      {:else if !hasNamedPlacement(byPlacement)}
        <p class="text-muted empty-msg">
          No link in this campaign has a placement. Set one on each link (for a poster, the shop name) to see clicks by placement.
        </p>
      {:else}
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
      {/if}
    </Panel>

    <!-- Links table (#0157) -->
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
        <p class="text-muted empty-msg">No links assigned yet — add one below.</p>
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
              <span class="text-muted" title={ch}>{groupChannelsShort(row.group.links) || '—'}</span>
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

    <!-- Batch create (#0105) -->
    <Panel title="Batch create links">
      <BatchCreateLinks campaign={detail} oncreated={() => load(detail!.slug)} />
    </Panel>

    <!-- Assign -->
    <Panel title="Assign links">
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

      <Button variant="primary" onclick={handleAssign} disabled={assigning || parsedKeys.length === 0}>
        {assigning
          ? 'Assigning…'
          : parsedKeys.length > 0
            ? `Assign ${parsedKeys.length} link${parsedKeys.length === 1 ? '' : 's'}`
            : 'Assign links'}
      </Button>
    </Panel>
  {/if}
</div>

<style>
  .detail-shell {
    max-width: 960px;
  }
  .title-row {
    display: flex;
    align-items: center;
    gap: var(--space-3);
    margin-bottom: var(--space-2);
    flex-wrap: wrap;
  }
  .detail-title {
    font-size: var(--fs-lg);
    font-weight: 600;
    margin: 0;
  }
  .description {
    margin: 0 0 var(--space-3);
  }
  .fields {
    display: grid;
    grid-template-columns: 9rem 1fr;
    gap: var(--space-2) var(--space-4);
    margin: 0 0 var(--space-2);
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
  .mono {
    font-family: var(--font-mono);
    font-size: var(--fs-sm);
  }
  .slug-note,
  .slug-edit-note {
    display: block;
    font-size: var(--fs-sm);
    margin-top: var(--space-1);
  }
  .input-error {
    border-color: var(--danger) !important;
  }
  .date-row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: var(--space-3);
  }

  /* ── Summary stat grid ──────────────────────────────────────────────── */
  .summary-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(9.5rem, 1fr));
    gap: var(--space-4);
  }
  .stat {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .stat-label {
    font-size: var(--fs-sm);
    font-weight: 600;
    color: var(--text-muted);
    text-transform: uppercase;
    letter-spacing: 0.03em;
  }
  .stat-value {
    font-size: var(--fs-xl);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .stat-sub {
    font-size: var(--fs-sm);
  }
  .stat-warn .stat-value {
    color: var(--warning);
  }

  /* ── Charts (#0104) ─────────────────────────────────────────────────── */
  .chart-window-label {
    margin: 0 0 var(--space-3);
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

  @media (max-width: 480px) {
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
    .date-row {
      grid-template-columns: 1fr;
    }
    .summary-grid {
      grid-template-columns: repeat(2, 1fr);
    }
  }

  @media (max-width: 899px) {
    .mobile-sort-toggle { display: inline-flex; }
    .link-cell.child { padding-left: var(--space-4); }
    .clicks-cell { align-items: flex-end; }
  }
</style>
