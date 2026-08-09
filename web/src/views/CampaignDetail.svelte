<!--
  Campaign Detail view (#0103). Reads which campaign to show from the shared
  `selectedCampaignSlug` store (CampaignsList sets it when a row is opened —
  there is no URL router, mirroring LinkDetail/selectedLinkKey).

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

  Links table: short key, title, destination, source, medium, placement,
  windowed clicks, share of total, created date — sortable by clicks
  (stable, lib/campaigns.ts sortLinkRows), each row linking to the existing
  link-detail view. "Copy all short URLs" copies every row's short URL.

  QR column (#0106): a genuine 11th column (the table was already ten —
  #0103 downstream constraint 5) rather than folding the download link into
  the existing Actions cell, so "download this link's code" reads as its
  own affordance next to Created, not as a third control crowding Remove.

  BOTH SVG and PNG links, restored (second review-fix round): an earlier
  round dropped PNG down to a single "QR" (SVG-only) link to fix a
  zero-scroll regression, but that measurement was taken against a campaign
  with short, thin content and did not hold — re-measured with realistic
  content (placement names like "Rockwell Automation Lobby", titles/
  destinations that actually reach the cells' max-width caps), the
  single-link version still overflowed `.table-scroll` to 940 / 926 (14px)
  at every desktop width, clipping part of Remove. Dropping PNG bought
  nothing and cost a stated deliverable (issue: both formats "available from
  the campaign detail links table"), so it is restored here alongside SVG.
  With both links back in the cell (still at the 4px `.qr-th`/`.qr-cell`
  padding below), the same adversarial dataset overflowed to 975 / 926
  (49px) — reclaimed by tightening `.title-cell` (120px → 95px) and
  `.dest-cell` (150px → 126px), the two free-text columns with real room
  that already ellipsize, so nothing becomes newly hidden. 49px is the
  measured minimum: one pixel less (96px/126px) landed at 927 / 926, one
  pixel over. `.placement-cell`'s own max-width (90px) was left alone — its
  `<th>` has no cap, and the header word "Placement" alone sets the column
  to ~102px, making that max-width inert as a squeeze target; noted as a
  follow-up rather than folded into that pass.

  THIRD review round: that follow-up turned out to be load-bearing, not
  optional. `table-layout: auto` means a `<td>`'s max-width only bounds a
  column when nothing ELSE in the column demands more — `.table-scroll`
  reading `926 == 926` was `max(926, min-content)`, which can be a hard
  overflow OR a min-content sitting a fraction of a pixel under the
  threshold with zero real headroom (confirmed by measuring
  `.links-table` at `width: min-content` directly). It was the latter:
  generated keys are 6 chars, but custom aliases are a documented 1–12
  url-safe-char feature, and a realistic 44-row dataset using 12-char
  aliases pushed min-content to 967.98px against the 926px container —
  41.98px over. Fixed by capping `.placement-cell`'s `<th>` (the exact gap
  identified above) and adding a matching `.key-cell` cap to the short-key
  column, which had NO cap at all.

  FOURTH review round: the third round's own first allocation of the
  recovered budget was backwards — it spent width on `.title-cell`/
  `.dest-cell` readability (one extra character each, changing nothing
  legible) while leaving `.placement-cell` too narrow for its OWN header
  ("Placement" ellipsized to "PLAC…") and `.key-cell` too narrow for even
  the DEFAULT 6-character generated key, not just a custom alias. Corrected
  by cutting `.dest-cell` hard instead — cheap because #0113 (filed
  separately) established the column carries no information at any width
  it can afford — and spending the freed budget on `.placement-cell` (wide
  enough that its header no longer truncates) and `.key-cell` (wide enough
  for a FULL 12-character alias, not just the 6-char floor). See the
  `.title-cell`/`.dest-cell`/`.placement-cell`/`.key-cell` rule's own
  comment below for the full before/after numbers, the final values, and
  the fit checks (6-char default key, 12-char alias, and the Placement
  header, all measured `clientWidth == scrollWidth`). Below the 900px
  breakpoint the column stacks like every other cell (data-label="QR"),
  unaffected by the desktop width math above; the in-cell SVG/PNG links
  there are also sized to a real ≥40px tap target below that breakpoint now
  (see `.qr-links` further down) — they were 24×17px and 25×17px, inherited
  unchanged from the desktop inline-text styling, which is fine on desktop
  but not on a phone.

  Bulk QR download: a "Download all QR codes (zip)" link in the toolbar,
  next to "Copy all short URLs" — same zip archive whether the campaign has
  zero or many links (internal/handlers/campaigns.go's QRZip degrades to an
  empty-but-valid archive rather than an error).

  CSV export (#0107): a "Download CSV" link in the same toolbar, next to the
  QR zip link — the per-link rollup (internal/handlers/campaigns_export.go's
  Export) over the SAME default window this view already shows (no
  from/to picker exists here, so nothing for the two requests to disagree
  about). Also degrades cleanly for an empty campaign (header row, no data
  rows, never an error).

  Assign/unassign against #0099's endpoints: the assign form accepts pasted
  keys or short URLs (lib/campaigns.ts parseKeysInput), chunks anything over
  the server's 50-key cap (chunkKeys), and surfaces partial success/failure
  since AssignLinks is documented non-atomic across keys.

  Charts (#0104) are a deliberate empty slot below the summary.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { currentView, currentUser, selectedCampaignSlug, selectedLinkKey } from '../lib/stores';
  import {
    getCampaign,
    getCampaignStats,
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
    sortLinkRows,
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
  } from '../lib/campaigns';
  import { qrSvgUrl, qrPngUrl } from '../lib/links';
  import { formatDate } from '../lib/linkDetail';
  import type { CampaignDetail, LinkBucket, LinkSeries } from '../lib/types';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import CampaignClicksChart from '../lib/CampaignClicksChart.svelte';
  import UTMBarChart from '../lib/UTMBarChart.svelte';
  import BatchCreateLinks from '../lib/BatchCreateLinks.svelte';

  const MAX_NAME_LENGTH = 255;

  let loading = $state(true);
  let notFound = $state(false);
  let loadError = $state<string | null>(null);
  let detail = $state<CampaignDetail | null>(null);
  let byLink = $state<LinkBucket[]>([]);
  let seriesByLink = $state<LinkSeries[]>([]);

  // ── Charts (#0104) ────────────────────────────────────────────────────
  const channelDimensions = $derived(campaignUtmDimensions(detail?.stats));
  const noChannelData = $derived(isEmptyCampaignChannelStats(detail?.stats));

  // ── Links table ────────────────────────────────────────────────────────
  const rows = $derived(buildLinkRows(detail?.links ?? [], byLink));
  let sortDirection = $state<'desc' | 'asc'>('desc');
  const sortedRows = $derived(sortLinkRows(rows, sortDirection));
  // Clicks in byLink attributed to a link no longer assigned to this
  // campaign — excluded from both the numerator and denominator of "Share
  // of listed links" above, so it is surfaced separately rather than
  // silently making the visible shares look like they cover everything
  // (#0103 fix 3).
  const unlistedClicks = $derived(unlistedClickCount(detail?.links ?? [], byLink));

  function toggleSort() {
    sortDirection = sortDirection === 'desc' ? 'asc' : 'desc';
  }

  let copiedAll = $state(false);
  async function copyAll() {
    const text = copyAllShortUrlsText(sortedRows);
    if (text === '') return;
    try {
      await navigator.clipboard.writeText(text);
      copiedAll = true;
      setTimeout(() => {
        copiedAll = false;
      }, 1500);
    } catch {
      // Clipboard may be unavailable; ignore.
    }
  }

  function openLink(key: string) {
    selectedLinkKey.set(key);
    currentView.set('link-detail');
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
        currentView.set('login');
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
      seriesByLink = rollup.series_by_link;
    } catch {
      byLink = [];
      seriesByLink = [];
    }

    if (initial) loading = false;
  }

  function goList() {
    selectedCampaignSlug.set(null);
    currentView.set('campaigns');
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
        currentView.set('login');
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
        currentView.set('login');
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
    const slug = detail.slug;
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
        const res = await assignLinksToCampaign(slug, chunk);
        succeeded += res.links.length;
      }
      assignNotice = `Assigned ${succeeded} link${succeeded === 1 ? '' : 's'}.`;
      assignInput = '';
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
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
      await load(slug);
    }
  }

  // ── Unassign ─────────────────────────────────────────────────────────────
  let unassigning = $state<Record<string, boolean>>({});
  let unassignError = $state<string | null>(null);

  async function handleUnassign(key: string) {
    if (!detail) return;
    const slug = detail.slug;
    unassigning = { ...unassigning, [key]: true };
    unassignError = null;
    try {
      await unassignLinkFromCampaign(slug, key);
      await load(slug);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        currentUser.set(null);
        currentView.set('login');
        return;
      }
      unassignError = 'Could not remove this link. Please try again.';
    } finally {
      const { [key]: _removed, ...rest } = unassigning;
      unassigning = rest;
    }
  }

  onMount(() => {
    const slug = $selectedCampaignSlug;
    if (!slug) {
      loading = false;
      notFound = true;
      return;
    }
    load(slug);
  });
</script>

<div class="app-shell detail-shell">
  <header class="app-header">
    <Button onclick={goList}>&larr; Campaigns</Button>
    <h1 class="app-title">Campaign detail</h1>
  </header>

  {#if loading}
    <p class="text-muted" role="status">Loading campaign…</p>
  {:else if notFound}
    <Panel title="Campaign not found">
      <p class="text-muted">This campaign no longer exists, or you don't have access to it.</p>
      <Button variant="primary" onclick={goList}>Back to campaigns</Button>
    </Panel>
  {:else if loadError && !detail}
    <Panel>
      <p class="text-error" role="alert">{loadError}</p>
      {#if $selectedCampaignSlug}
        <Button variant="primary" onclick={() => load($selectedCampaignSlug!)}>Retry</Button>
      {/if}
    </Panel>
  {:else if detail}
    <!-- Header -->
    <Panel>
      <div class="title-row">
        <h2 class="detail-title">{detail.name}</h2>
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

    <!-- Links table -->
    <Panel title="Links" noPadding>
      <div class="links-toolbar row spread">
        <span class="text-muted">
          {sortedRows.length} link{sortedRows.length === 1 ? '' : 's'}
        </span>
        <div class="row toolbar-actions">
          <!--
            Visible sort control for the stacked-card layout (#0103 fix 1):
            below the breakpoint the <thead> — and the sort button that lives
            inside it — is sr-only-clipped, not display:none, so aria-sort
            keeps working for assistive tech, but a SIGHTED mobile user had
            no way to reach "sortable by clicks" (an explicit AC) at all.
            This button is hidden above the breakpoint (CSS) and drives the
            exact same `sortDirection` state as the header button — one
            source of truth, two entry points.
          -->
          <span class="mobile-sort-toggle">
            <Button
              variant="subtle"
              onclick={toggleSort}
              aria-label={`Sort by clicks, currently ${sortDirection === 'desc' ? 'highest first' : 'lowest first'}`}
            >
              Sort: Clicks {sortDirection === 'desc' ? '▼' : '▲'}
            </Button>
          </span>
          <Button variant="subtle" onclick={copyAll} disabled={sortedRows.length === 0}>
            {copiedAll ? 'Copied!' : 'Copy all short URLs'}
          </Button>
          <!--
            Bulk QR download (#0106): a plain <a>, NOT a Button (Button.svelte
            only ever renders a <button>, which cannot trigger a same-origin
            file download the way a real anchor does) — pointing straight at
            the zip endpoint, no fetch/blob JS needed since every API route
            here is same-origin and cookie-authenticated (see
            lib/campaigns.ts's campaignQrZipUrl doc comment).

            Styled by the LOCAL .qr-zip-link rule below, not Button's
            `.btn`/`.btn-subtle` classes (review fix): Svelte scopes a
            component's <style> block to elements IT renders, hashing class
            names accordingly — `.btn`/`.btn-subtle` only exist, post-hash,
            on Button.svelte's own <button>. Putting those class names on an
            <a> in THIS component matches nothing, so the anchor previously
            rendered as an unstyled `#0000EE` underlined UA-default link (a
            colour that appears nowhere else in the app) rather than looking
            like the "Copy all short URLs" button beside it. .qr-zip-link
            duplicates the relevant subset of Button's .btn + .btn-subtle
            rules locally so this component owns styling for markup it
            renders directly.

            Left enabled even with zero links: the endpoint returns a valid,
            empty archive rather than an error (see
            internal/handlers/campaigns.go's QRZip), so there's nothing wrong
            to guard against by disabling it.
          -->
          <a class="qr-zip-link" href={campaignQrZipUrl(detail.slug)} download>
            Download all QR codes (zip)
          </a>
          <!--
            Per-link CSV export (#0107): same pattern as the QR zip link
            above — a plain <a>, not a Button, pointing straight at the
            export endpoint (same-origin, cookie-authenticated, no
            fetch/blob JS needed — see lib/campaigns.ts's
            campaignExportCsvUrl doc comment). Lives in the TOOLBAR, not the
            links table, deliberately: the table is already at its measured
            min-content headroom against the zero-horizontal-scroll
            invariant (see the geometry comment in <style> below), and a
            download control costs no table width sitting here instead.

            No ?from=/?to= query params: this view never lets the user pick
            an explicit window (getCampaignStats(slug) above is called with
            none either), so the export always resolves the SAME default
            window the on-screen table is already showing — the reason the
            AC's reconciliation holds with no extra wiring here.

            Styled by the LOCAL .export-csv-link rule below, duplicating
            .qr-zip-link's own duplicated subset of Button's .btn/
            .btn-subtle rules (see that rule's comment for why a shared
            class can't just be reused across components) — kept in sync BY
            HAND with .qr-zip-link/Button.svelte.

            Left enabled even with zero links: an empty campaign exports a
            valid header-only CSV, not an error (internal/handlers/
            campaigns_export.go's Export), the same "empty collection, not a
            failure" contract the QR zip link already relies on.
          -->
          <a class="export-csv-link" href={campaignExportCsvUrl(detail.slug)} download>
            Download CSV
          </a>
        </div>
      </div>

      {#if unassignError}
        <p class="text-error links-toolbar-error" role="alert">{unassignError}</p>
      {/if}

      {#if sortedRows.length === 0}
        <p class="text-muted empty-msg">No links assigned yet — add one below.</p>
      {:else}
        <div class="table-scroll">
          <table class="links-table">
            <thead>
              <tr>
                <th scope="col" class="key-cell">Key</th>
                <th scope="col" class="title-cell">Title</th>
                <!-- "URL", not "Destination": .dest-cell is capped at 60px
                     (see the geometry comment in <style>), and "Destination"
                     ellipsizes to "DE…" at that width — a truncated column
                     HEADER removes the label telling you what the column is,
                     which reads as a bug rather than as a narrow column. "URL"
                     measures 60px == 60px, so it renders whole at the existing
                     cap with no geometry change. Same move as "Short key" →
                     "Key" on .key-cell. The td keeps data-label="Destination",
                     so the stacked mobile layout still shows the full word. -->
                <th scope="col" class="dest-cell" title="Destination">URL</th>
                <th scope="col" class="source-cell">Source</th>
                <th scope="col" class="medium-cell">Medium</th>
                <th scope="col" class="placement-cell" title="Placement">Placement</th>
                <th scope="col" aria-sort={sortDirection === 'desc' ? 'descending' : 'ascending'}>
                  <button type="button" class="sort-btn" onclick={toggleSort}>
                    Clicks {sortDirection === 'desc' ? '▼' : '▲'}
                  </button>
                </th>
                <th scope="col">Share of listed links</th>
                <th scope="col">Created</th>
                <th scope="col" class="qr-th">QR</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
              </tr>
            </thead>
            <tbody>
              {#each sortedRows as row (row.key)}
                <tr>
                  <td class="mono key-cell" data-label="Short key">
                    <button
                      type="button"
                      class="link-key-btn"
                      onclick={() => openLink(row.key)}
                      title={row.key}
                    >
                      {row.key}
                    </button>
                  </td>
                  <td class="title-cell" data-label="Title" title={row.title}>{row.title || '—'}</td>
                  <td class="dest-cell" data-label="Destination" title={row.destination_url}>{row.destination_url}</td>
                  <td class="text-muted source-cell" data-label="Source" title={row.utm_source}><span class="bidi-ltr">{row.utm_source || '—'}</span></td>
                  <td class="text-muted medium-cell" data-label="Medium" title={row.utm_medium}><span class="bidi-ltr">{row.utm_medium || '—'}</span></td>
                  <td class="placement-cell" data-label="Placement" title={row.placement}>{row.placement || '—'}</td>
                  <td class="num" data-label="Clicks">{row.clicksInWindow}</td>
                  <td class="num" data-label="Share">{row.shareOfTotal}%</td>
                  <td class="text-muted" data-label="Created">{formatDate(row.created_at)}</td>
                  <td class="qr-cell" data-label="QR">
                    <span class="qr-links">
                      <a class="qr-link" href={qrSvgUrl(row.key)} download title="Download QR code (SVG)">
                        SVG
                      </a>
                      <a class="qr-link" href={qrPngUrl(row.key)} download title="Download QR code (PNG)">
                        PNG
                      </a>
                    </span>
                  </td>
                  <td class="actions-cell" data-label="Actions">
                    <Button
                      variant="danger"
                      disabled={unassigning[row.key]}
                      onclick={() => handleUnassign(row.key)}
                    >
                      {unassigning[row.key] ? '…' : 'Remove'}
                    </Button>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
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
          placeholder={'abc123, https://go.sstools.co/u/def456'}
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

  /* ── Links table ────────────────────────────────────────────────────── */
  .links-toolbar {
    padding: var(--space-3) var(--space-4);
    border-bottom: var(--border-w) solid var(--border);
  }
  .links-toolbar-error {
    padding: 0 var(--space-4) var(--space-3);
  }
  .toolbar-actions {
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  /*
   * Duplicates the relevant subset of Button.svelte's `.btn` + `.btn-subtle`
   * rules (review fix — see the toolbar markup comment above for why this
   * can't just reuse those class names). Kept in sync BY HAND with
   * Button.svelte; if that component's subtle variant changes, this rule
   * should change with it.
   */
  .qr-zip-link {
    display: inline-flex;
    align-items: center;
    font-family: var(--font);
    font-size: var(--fs-base);
    line-height: 1;
    padding: var(--space-2) var(--space-3);
    border: var(--border-w) solid transparent;
    border-radius: var(--radius);
    background: transparent;
    color: var(--accent);
    text-decoration: none;
    cursor: pointer;
    user-select: none;
  }
  .qr-zip-link:hover {
    background: var(--accent-subtle);
  }
  /*
   * CSV export (#0107): identical rule to .qr-zip-link above, duplicated
   * rather than shared for the same reason that rule gives — this is a
   * SEPARATE <a> this component renders directly, so it needs its own
   * class. Kept in sync BY HAND with .qr-zip-link/Button.svelte's subtle
   * variant.
   */
  .export-csv-link {
    display: inline-flex;
    align-items: center;
    font-family: var(--font);
    font-size: var(--fs-base);
    line-height: 1;
    padding: var(--space-2) var(--space-3);
    border: var(--border-w) solid transparent;
    border-radius: var(--radius);
    background: transparent;
    color: var(--accent);
    text-decoration: none;
    cursor: pointer;
    user-select: none;
  }
  .export-csv-link:hover {
    background: var(--accent-subtle);
  }
  /*
   * Hidden above the stacked-card breakpoint — the header's own sort button
   * (inside <thead>) is visible and sufficient on desktop. Shown below it in
   * the @media (max-width: 900px) block further down, alongside the switch
   * to the stacked-card layout (#0103 fix 1).
   */
  .mobile-sort-toggle {
    display: none;
  }
  .empty-msg {
    padding: var(--space-4);
  }
  .sort-btn {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    text-transform: inherit;
    letter-spacing: inherit;
    color: inherit;
    cursor: pointer;
  }
  .link-key-btn {
    background: none;
    border: none;
    padding: 0;
    margin: 0;
    font: inherit;
    font-family: var(--font-mono);
    color: var(--accent);
    cursor: pointer;
    text-decoration: underline;
    /*
     * display:block (not the default inline) + max-width:100% so a long
     * custom-key alias (up to 12 chars, #0106 third round) ellipsizes
     * WITHIN the already-capped .key-cell <td> instead of just being
     * clipped with no "…" — a plain inline button's overflow is invisible
     * to a hidden-overflow ancestor's text-overflow rule.
     */
    display: block;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  /*
   * Column-priority decision (#0103 fix 6): the links table is the widest
   * thing in the app (10 columns) and, unconstrained, ran to ~1100px —
   * wider than even the desktop .table-scroll container (926px), so
   * Created/Remove were cut off at EVERY width, not just narrow ones.
   * Title/Destination/Placement are the three free-text columns with real
   * room to give back, so they take the squeeze here; Clicks and Share are
   * the numbers the page exists to show and are left unconstrained.
   * Placement gets its own (tighter) class rather than reusing .dest-cell —
   * the two were accidentally sharing one 200px budget, which was most of
   * the desktop overflow on its own.
   *
   * #0106 THIRD review round — table-layout is `auto` (no explicit
   * table-layout/colgroup), so a `<td>`'s max-width only bounds a column
   * when nothing else in that column demands more. Two things this table
   * was still getting wrong, found by measuring `.links-table` at
   * `width: min-content` directly (the real per-column demand, independent
   * of the 926px container) rather than trusting a scrollWidth == 926
   * reading alone — 926 is `max(926, min-content)`, so a scrollWidth match
   * can still mean zero real headroom:
   *
   *   1. .placement-cell's max-width applied to the <td> only. Its <th> —
   *      plain text, no class — sized itself to "Placement"'s own
   *      max-content (~101.56px), WIDER than the 90px td cap, so the column
   *      rendered at the header's width regardless of what the cap said.
   *      The cap was provably inert. Fixed by putting the SAME class on
   *      both <th> and <td> for this column (and title/dest/key below) so
   *      header and data share one real bound — the header word now
   *      ellipsizes too when it doesn't fit, exactly like the data already
   *      did (a `title="Placement"` IS added to this <th>, since unlike the
   *      data cells' own `title` — which mirrors visible, already-known
   *      text — a truncated "Placem…" has no other way to recover the full
   *      label on hover; the accessible name read by assistive tech is the
   *      text node either way, unaffected by the CSS clip).
   *   2. Short key had NO cap at all. Generated keys are 6 chars, but
   *      custom aliases are a documented 1–12 url-safe-char feature
   *      (internal/handlers/links.go's validKey) — a 12-char alias grows
   *      this column directly, and did so unconstrained. .key-cell caps it
   *      the same way, with the row's `title` on the button (matching the
   *      existing Title/Destination/Placement pattern) so a truncated key
   *      is still recoverable on hover without leaving the table. Its <th>
   *      shares the same class, so the header text was shortened from
   *      "Short key" to "Key" instead of letting it ellipsize — "Short key"
   *      truncates to an illegible "SHO…" at any cap tight enough to matter,
   *      whereas "Placement"'s single-word truncation ("Placem…") stays
   *      readable; the mobile stacked-card view keeps the full "Short key"
   *      label via `data-label`, which is a separate attribute unaffected by
   *      this rename.
   *
   * All four caps were tuned by measuring `.links-table` at
   * `width: min-content` (the browser's real per-column demand, independent
   * of the 926px container — 926 is `max(926, min-content)`, so a
   * scrollWidth match alone can hide a min-content that is already AT the
   * threshold) against a 44-row adversarial dataset built for THIS round:
   * realistic long titles/destinations/placements at their caps, and a
   * 12-CHARACTER custom-key alias on every row — the worst case
   * `validKey` allows, not the 6-char generated default the previous round
   * tested. Reproduced this round, in order:
   *   - This file's second-round shipped state (95px/126px title/dest, a
   *     90px .placement-cell cap on the <td> only, no key cap) against the
   *     new dataset: min-content 967.98px, i.e. 41.98px PAST the 926px
   *     container — confirms the coordinator's finding that the second
   *     round's "926 == 926" reading had ~0 real headroom and did not
   *     survive a longer key.
   *   - Capping both <th>s (.placement-cell at 90px, .key-cell at 84px)
   *     while leaving .title-cell/.dest-cell at their second-round 95px/
   *     126px: min-content 929.72px — that's 3.72px PAST the 926px
   *     container (confirmed by scrollWidth reading 930/926 at this stage),
   *     i.e. STILL BROKEN, not "3.72px of margin" as an earlier version of
   *     this comment mis-stated — a sign error that made a failing step
   *     read as a barely-passing one. Left in this list because the
   *     progression itself is real and reproducible; only the earlier
   *     wording of this line was wrong.
   *   - Tightening .placement-cell to 80px and .key-cell to 66px: 901.72px,
   *     24.28px of REAL margin (926 − 901.72) — past the ~5px floor a
   *     genuine fix needs.
   *   - #0106 fourth review round: the round-three "final" allocation
   *     (.title-cell 105px, .dest-cell 136px, .placement-cell 75px,
   *     .key-cell 60px) spent its recovered budget on the wrong columns —
   *     75px still forces "Placement"'s <th> to ellipsize to "PLAC…" (its
   *     unconstrained max-content is ~101.56px, measured above), and 60px
   *     truncates even the DEFAULT 6-character generated key (needs 43px of
   *     content box + 24px of td padding = 67px minimum,
   *     `.link-key-btn`'s clientWidth 36px < scrollWidth 43px at that cap)
   *     — the common case, not the adversarial one. Meanwhile the 10px/10px
   *     given to .title-cell/.dest-cell bought one more character each and
   *     changed nothing readable. Reallocated: .title-cell back to 95px
   *     (its second-round value); .dest-cell cut hard to 60px — cheap
   *     because #0113 (filed separately) established the column conveys no
   *     information at any width it can afford (`https://www.ex…` /
   *     `https://www.so…`, indistinguishable either way) — freeing enough
   *     to widen .placement-cell to 110px (past its 101.56px unconstrained
   *     header width, so "Placement" renders whole, not just its data) and
   *     .key-cell to 111px (fits a FULL 12-character alias — the `87px`
   *     content box `.link-key-btn` needs at 12 chars, plus 24px padding —
   *     not just the 6-char floor). Verified both directions: a 6-char
   *     generated key (`.link-key-btn` clientWidth 43px == scrollWidth
   *     43px, 3 links checked) and a 12-char alias (same equality, all 44
   *     rows checked, 0 truncated) now render in full, and `th.placement-
   *     cell`'s clientWidth (110px) == scrollWidth (110px) — "Placement"
   *     no longer ellipsizes. Final measured min-content: 910.42px against
   *     the 926px container — **15.58px of real headroom** — at all of
   *     1024/1280/1440/1920px, with the same 44-row/12-char-key dataset,
   *     Remove fully inside `.table-scroll` at every width.
   *
   *   - #0112: this round's own adversarial dataset (12 rows, every row a
   *     12-char custom-key alias, long titles/destinations/placements at
   *     their existing caps, PLUS long/realistic UTM source+medium values
   *     this round specifically targets — `partner_newsletter_q3`,
   *     `print_qr_poster`, `sponsored_content_native`,
   *     `partner_co_marketing_newsletter`, `affiliate_network_referral`,
   *     etc.) measured `.links-table` at `width: min-content`: **917.03px**
   *     against the 926px container — **8.97px of real headroom** — at all
   *     of 1024/1280/1440/1920px, `.table-scroll` reading `926 == 926` at
   *     every one, with `.source-cell`/`.medium-cell` capped at 80px (see
   *     that rule's own comment below for why 80px and for the truncation
   *     direction chosen, and the `.bidi-ltr` rule below IT for a review-
   *     round fix to a character-reordering bug the first version of this
   *     cap introduced). Tighter than the 15.58px/24.28px margins earlier
   *     rounds landed on, and past the ~5px floor a genuine fix needs FOR
   *     THIS DATASET specifically — not an unconditional floor: `.num`
   *     (Clicks/Share) is uncapped on `main` too, and a click count reaching
   *     7 digits (`1234567`) pushes min-content to 927.45px against this
   *     same 80px Source/Medium cap, i.e. headroom goes negative. That is a
   *     pre-existing `.num`-width gap, not something this round's Source/
   *     Medium caps introduced or could fix, and is out of scope here (see
   *     the issue's own out-of-scope list); noted so a future round doesn't
   *     re-derive it from scratch. 375px unaffected: `document.scrollWidth
   *     == 375`, stacked cards still show every value in full.
   */
  .title-cell {
    max-width: 95px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dest-cell {
    max-width: 60px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .placement-cell {
    max-width: 110px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .key-cell {
    max-width: 111px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  /*
   * #0112: Source and Medium were the two remaining text columns in this
   * table with NO cap at all — every other free-text column (.title-cell,
   * .dest-cell, .placement-cell, .key-cell above) got one across #0103/
   * #0106, but these two were added later (#0099/#0100) and never joined
   * that pass. An ordinary UTM value blows past what the table has to give:
   * `utm_source=partner_newsletter_q3` alone (no other adversarial content)
   * widened the Source column enough to push `.table-scroll` into
   * horizontal overflow — the exact #0103/#0106 invariant this table is
   * supposed to hold. Fixed the same way as every other capped column: the
   * SAME class on both <th> and <td> (the `.placement-cell` lesson from
   * #0106's third round — a `<td>` max-width alone is inert under
   * `table-layout: auto` whenever the uncapped `<th>`'s own text is wider;
   * "Source"/"Medium" are short enough that this cap never actually
   * truncates the HEADER, but the class still has to be on both cells so
   * nothing upstream can widen the column past it).
   *
   * TRUNCATION DIRECTION: leading ellipsis, not trailing. UTM values in this
   * app are conventionally prefixed and disambiguated at the END —
   * `partner_newsletter` vs `partner_newsletter_q3`, `print_qr_poster` vs
   * `print_qr_poster_v2` — so trailing truncation collapses exactly the
   * pairs a reader most needs to tell apart down to an identical
   * "partner_newsle…" for both. Truncating from the front instead
   * (`direction: rtl` + `text-align: left` on the cell, a standard CSS-only
   * front-ellipsis: the element's paragraph direction flips so the
   * browser's own text-overflow clips from the visual left and places the
   * "…" there) keeps the differentiating suffix on screen without any JS
   * string-slicing, for exactly the shape of value the issue itself names.
   *
   * REVIEW FIX — `direction: rtl` on the cell does not just move the
   * ellipsis, it runs the UNICODE BIDI ALGORITHM over the cell's actual
   * content, and that algorithm reorders characters, not just the ellipsis
   * position, whenever the content isn't a single uninterrupted run of
   * strong-LTR characters. Caught by measuring rendered character order
   * with `Range.getClientRects()` (sorted by screen x), not by eyeballing:
   * a leading digit run, a leading/trailing neutral character (`-`, `.`,
   * `!`), or mixed script all reorder — `2026_q3_2027` rendered
   * `q3_2027_2026` (a value the string never contained), `promo_2026!`
   * rendered `!promo_2026` (the `!` relocated to the far left, then clipped
   * away by the ellipsis instead of the digits it was meant to punctuate).
   * 0 of the first round's 24 plain-snake_case cells reordered, which is
   * exactly why this shipped once already looking correct — UTM values are
   * not guaranteed to be plain snake_case (a leading campaign-year prefix
   * like `2026_spring_launch` is an entirely ordinary one). Fixed by
   * wrapping the <td>'s value in `<span class="bidi-ltr">` (`direction:
   * ltr; unicode-bidi: isolate`, see that rule below): the isolate makes
   * the span's contents an opaque, independently-LTR-ordered unit as far as
   * the surrounding rtl paragraph's bidi algorithm is concerned, so the
   * PARENT cell's rtl-ness still controls where text-overflow clips from
   * (the front) while the SPAN's own content always renders in the order it
   * was written, regardless of digits/punctuation/script. Only the two
   * <td>s need this — the <th>s' content is always the literal word
   * "Source"/"Medium", never data, so there is nothing for the bidi
   * algorithm to reorder there.
   *
   * NOT A COMPLETE GUARANTEE, stated plainly rather than glossed over: this
   * round's own adversarial dataset also produced two pairs that render
   * IDENTICALLY at 80px despite being genuinely different values —
   * `partner_newsletter` and `partner_co_marketing_newsletter` both render
   * "…sletter" (they share the literal 11-character suffix "_newsletter",
   * so no amount of trailing text short of ~12+ characters — far more than
   * an 80px cell holds — reaches back far enough to show where they
   * diverge); `print_flyer_poster` and `print_qr_poster` both render
   * "…poster" the same way. A head+tail "middle ellipsis" was considered to
   * close this, but the same two pairs need ~9 leading AND ~7+ trailing
   * characters simultaneously to disambiguate (`partner_` is an
   * 8-character shared prefix; `_poster`/`_newsletter` are 7/11-character
   * shared suffixes) — roughly 130–150px, which this table does not have
   * to give without pulling another column back below a floor #0106 spent
   * four review rounds establishing. This is the same class of limitation
   * `.title-cell`/`.dest-cell`/`.placement-cell`/`.key-cell` above already
   * accept for adversarial SHARED-PREFIX pairs (trailing ellipsis has no
   * defense against those either) — front truncation trades that failure
   * mode for a rarer shared-prefix-AND-suffix one, while fixing the
   * common, issue-cited case (tail-differentiated values) outright.
   *
   * Recovery for the residual case is the same two-part story the rest of
   * this table relies on: `title` on the <td> (kept below, secondary — not
   * sufficient alone per the issue's own AC, but still a real desktop
   * affordance), and — genuinely touch-reachable, unlike hover — tapping
   * the row's own key opens LinkDetail, then tapping Edit there opens the
   * UTM builder (`#edit-utm-source`/`#edit-utm-medium`), which shows the
   * full, unclipped value (measured: 666px == 666px, no truncation).
   * REVIEW FIX: this is three taps (key, then Edit), not one — LinkDetail's
   * own READ view shows Destination/Placement/Campaign/Clicks/Created/
   * Expires and a click-derived UTM breakdown, but not the raw
   * utm_source/utm_medium fields themselves; an earlier version of this
   * comment overstated that as a one-tap "read view" recovery. The AC
   * ("recoverable some way that works on touch") still holds — it does not
   * require the recovery to be one tap — but the affordance is the edit
   * form specifically, not the page in general. The accessible name read by
   * assistive tech is the cell's own text node either way (inside the
   * `bidi-ltr` span, unaffected by the CSS clip, the direction flip, or the
   * isolate — same reasoning as .placement-cell's <th> above) — a screen
   * reader never sees only "…sletter" regardless of which of the two rows
   * above it is announcing.
   *
   * The mobile stacked-card layout (<900px) shows the value in full
   * regardless (`.links-table td`'s `max-width: none` below already lifts
   * every column's cap there), so the front-ellipsis — and the residual
   * collision risk above — is a desktop/tablet-width-only concern;
   * `direction: ltr` is restored for these two cells in that breakpoint
   * purely so the row's flex layout (`.links-table td { display: flex;
   * ... }`) doesn't itself mirror right-to-left from the lingering
   * `direction: rtl` once nothing is being truncated.
   *
   * Cap value and measured headroom: see the min-content accounting at the
   * end of the .title-cell comment above for the running total this column
   * budget was tuned against.
   */
  .source-cell,
  .medium-cell {
    max-width: 80px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    direction: rtl;
    text-align: left;
  }
  /*
   * Review fix — see the "REVIEW FIX" paragraph in the comment above this
   * rule's cell for the bug this closes: `direction: rtl` on the cell
   * reorders the VALUE's own characters (digits, punctuation, mixed
   * script), not just the ellipsis, whenever the value isn't pure
   * strong-LTR text. `unicode-bidi: isolate` makes this span's content an
   * independently-ordered unit the surrounding rtl paragraph cannot
   * reorder; `direction: ltr` is redundant with the isolate for the actual
   * character order but kept explicit so the span's own start/end (were
   * anything inside it ever right-aligned) doesn't inherit the cell's rtl.
   * Applies only inside .source-cell/.medium-cell's <td>s (see the markup)
   * — the <th>s have no dynamic content to reorder.
   */
  .source-cell .bidi-ltr,
  .medium-cell .bidi-ltr {
    direction: ltr;
    unicode-bidi: isolate;
  }
  .num {
    text-align: right;
    font-variant-numeric: tabular-nums;
    white-space: nowrap;
  }
  .actions-cell {
    text-align: right;
    white-space: nowrap;
  }
  /*
   * QR column (#0106): SVG and PNG download links (see the header comment
   * for the round(s) this table's zero-scroll invariant was re-measured and
   * fixed against realistic content). Padding here is var(--space-1) (4px
   * each side) rather than the table's base var(--space-3) (12px each
   * side, from app.css's generic `tbody td`/`thead th` rules) — switching
   * back to the base padding costs 16px (2 sides × 8px), pure arithmetic
   * independent of any other column's width, which is real money against a
   * budget this tight; see .title-cell's comment below for the full
   * min-content accounting and the final measured headroom this leaves.
   */
  .qr-th,
  .qr-cell {
    padding-left: var(--space-1);
    padding-right: var(--space-1);
  }
  .qr-cell {
    white-space: nowrap;
  }
  .qr-link {
    color: var(--accent);
    text-decoration: underline;
    font-size: var(--fs-sm);
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
    /*
     * Mirrors Button.svelte's own ≤480px rule (`.btn { padding: var(--space-3)
     * var(--space-3); min-height: 40px; }`) — missed in the initial hand-sync
     * (review fix). Measured before this fix: 31px tall at 420px viewport vs
     * the Button beside it (Copy all short URLs) at 40px — a visibly
     * mismatched, sub-40px tap target on exactly the input where mobile tap
     * targets matter. .qr-zip-link duplicates .btn/.btn-subtle rather than
     * reusing those class names (see the toolbar markup comment above for
     * why), so this mobile rule has to be hand-copied too; if Button.svelte's
     * mobile rule changes, this one should change with it.
     */
    .qr-zip-link {
      padding: var(--space-3) var(--space-3);
      min-height: 40px;
    }
    /* Mirrors .qr-zip-link's own ≤480px rule immediately above — same 40px
       tap-target reasoning, hand-copied for the same "duplicates, does not
       share, a class" reason (#0107). */
    .export-csv-link {
      padding: var(--space-3) var(--space-3);
      min-height: 40px;
    }
    .summary-grid {
      grid-template-columns: repeat(2, 1fr);
    }
  }

  /*
   * Stacked-card breakpoint for the links table (#0103 fix 2): originally
   * 480px, but a real-browser measurement at 481px found
   * documentElement.scrollWidth 855 against clientWidth 481 (and
   * .table-scroll 924 against 447) — the ten-column table still forced
   * horizontal panning across a wide 481px–~1000px band even though desktop
   * (above that band) and the old sub-480px stacked layout were both clean.
   * Raised to 900px so nothing between "phone" and "narrow laptop" pans.
   * This is a SEPARATE @media block from the 480px one above — fields/
   * date-row/summary-grid don't overflow and stay stacking at the original
   * 480px; only the table layout moves.
   *
   * Because more of the viewport range now uses the stacked layout, the
   * visible .mobile-sort-toggle in .links-toolbar (see above) matters more,
   * not less — it is the ONLY sighted way to reach "sortable by clicks"
   * anywhere in this wider band, since the <thead> (and the sort button
   * living inside it) is still sr-only-clipped here, not display:none.
   */
  @media (max-width: 900px) {
    .mobile-sort-toggle {
      display: inline-flex;
    }

    /*
     * Ten columns can't fit any priority scheme below this breakpoint
     * without either hiding data the AC requires (Clicks/Share/Remove all
     * reachable) or forcing a horizontal scroll with no affordance under
     * macOS overlay scrollbars (the reviewer's original complaint). Instead
     * of picking which columns to drop, the table becomes a stacked card
     * per link: every field stays present, in the SAME priority order as
     * the desktop columns (short key/title/destination first, clicks/
     * share/created next, actions last), just read top-to-bottom instead
     * of left-to-right. This guarantees Clicks, Share, and Remove are
     * always reachable with page (vertical) scrolling only — never
     * horizontal.
     */
    .table-scroll {
      overflow-x: visible;
    }
    .links-table thead {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
    }
    .links-table,
    .links-table tbody,
    .links-table tr {
      display: block;
      width: 100%;
    }
    .links-table tbody tr {
      border: var(--border-w) solid var(--border);
      border-radius: var(--radius);
      margin: var(--space-3);
      padding: var(--space-1) 0;
    }
    .links-table tbody tr:last-child {
      margin-bottom: var(--space-3);
    }
    .links-table td {
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: var(--space-3);
      padding: var(--space-2) var(--space-3);
      border-bottom: none;
      max-width: none;
      overflow-wrap: anywhere;
      white-space: normal;
      text-align: right;
    }
    /*
     * #0112: .source-cell/.medium-cell's desktop `direction: rtl` (see that
     * rule's comment above) is a front-truncation trick that only makes
     * sense while the cell is actually clipped to a fixed max-width. Below
     * this breakpoint `.links-table td` above already lifts `max-width` to
     * `none` and wraps normally, so nothing truncates — but `direction` is
     * a DIFFERENT property, untouched by that rule, and would otherwise keep
     * flowing as rtl here too. Left alone, that flips which side of this
     * flex row (`display: flex` a few lines up) counts as the main-axis
     * start, mirroring the data-label/value pair right-to-left for exactly
     * these two cells while every other stacked card reads left-to-right.
     * Reset back to ltr here, scoped to the same two cells.
     */
    .source-cell,
    .medium-cell {
      direction: ltr;
    }
    .links-table td::before {
      content: attr(data-label);
      flex: 0 0 auto;
      font-weight: 600;
      font-size: var(--fs-sm);
      color: var(--text-muted);
      text-align: left;
    }
    .links-table .actions-cell {
      justify-content: flex-end;
    }
    .links-table .actions-cell::before {
      content: none;
    }
    /*
     * Mobile QR tap targets (review fix): at desktop widths .qr-link is a
     * plain inline text link inside the tightest column budget in the app
     * (see the QR column header comment) — deliberately small there. Below
     * this breakpoint the stacked card gives each row the FULL viewport
     * width, so the same tight sizing carries over for no reason and
     * measured 24.3×17.4px / 25.5×17.4px, well under the 40px minimum this
     * file already applies to .qr-zip-link's own ≤480px rule — the same
     * defect class, just unfixed in this cell. .qr-links (the wrapper
     * around the two <a> tags, added so this rule has something to lay out
     * without disturbing the desktop inline-text styling above) becomes a
     * flex row of two real tap targets, each sized like Button's own mobile
     * rule.
     */
    .qr-links {
      display: flex;
      gap: var(--space-2);
    }
    .links-table .qr-cell .qr-link {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 40px;
      min-width: 40px;
      padding: var(--space-2) var(--space-3);
      border: var(--border-w) solid var(--border-strong);
      border-radius: var(--radius);
      text-decoration: none;
      font-size: var(--fs-base);
    }
  }
</style>
