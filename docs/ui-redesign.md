# ShortLinks web UI redesign — design proposals

Scope: the whole Svelte SPA in `web/src`. Reviewed by Brennan on 2026-11-18 (artifact: https://claude.ai/artifact/K8ZFywUoLbQXRN3ckGiLBh). Implementation is tracked in issues #0152–#0173.
Paths are relative to `/Users/brennan/Developer/brennanMKE/ShortLinks`. Line numbers are as of commit `8addb97`.

The organising idea: **the app is built around links, but the user thinks in content.** One guide is shared as 3+ links (one per platform), so every screen that lists links shows the same story several times with a platform suffix, and no screen adds them up. Most of what follows is about making *content* the unit that gets ranked, and demoting per-link plumbing (key, URL, QR, remove) to a second level.

Note on the design reference: `design/ShortLinks.png` is only the app icon (white chain glyph on violet, roughly `#4B00D6`). There is no screen mock-up to conform to. The UI's accent is a different hue (`--accent: #2d5fa3`, `web/src/app.css:22`).

---

## 1. Current problems, per view

### Shell / navigation
- The header + nav tabs are copy-pasted into four views: `views/Dashboard.svelte:357-362`, `views/CampaignsList.svelte:156-163`, `views/Account.svelte:194-199`, `views/Admin.svelte:469-473`. Each also carries its own `handleSignOut`.
- Detail views drop the nav entirely and replace the app title with a generic label: `views/LinkDetail.svelte:215-218` ("Link detail"), `views/CampaignDetail.svelte:434-437` ("Campaign detail"). The only way out is the back button, and the `<h1>` never names the thing you are looking at.
- There is no `/links` route (`App.svelte:31-40`); the link list only exists as the bottom half of `/`.
- Admin sub-sections are local component state, not routes (`views/Admin.svelte:482-513`), so "Admin › Audit log" cannot be linked or reloaded into.

### Home (`views/Dashboard.svelte`)
- Leads with a seven-field create form (`:368-606`), which is the least frequent task. The list of links starts below the fold.
- No analytics at all: no chart, no totals, no time window. The only number is an all-time `click_count` per row (`:658`).
- Table columns (`:623-629`): Short URL + Copy, Destination, Title, Clicks, Status, Created, Deactivate.
  - Destination shows hostname only (`:655`, `lib/links.ts:285`). For a single-site user it is the same string on every row and carries zero information; the *path*, which does differ, is hidden.
  - Title has no width or clamp rule (`:657`), so it gets whatever is left after six `nowrap` columns and wraps.
  - Status is "Active" on nearly every row; a pill that is always green is noise.
  - A red **Deactivate** button on every row (`:677-688`) fires with no confirmation (`:298-314`), and there is no API to undo it (`patchLinkRequest` has no `active` field, `internal/handlers/links.go:809-824`).
- Sorted by creation date only, 20 per page (`:65`); the API has no search, filter or sort (`internal/handlers/links.go:570-598`, `internal/links/store.go:676-688`). You cannot ask "what got the most clicks".
- The whole row is a click target (`:637-641`) while also containing a link and two buttons.

### Campaign detail (`views/CampaignDetail.svelte`)
- Eleven columns in a 960px shell (`:774-797`, `.detail-shell` at `:889-890`): Key, Title, URL, Source, Medium, Placement, Clicks, Share, Created, QR, Remove.
- To make that fit, the columns that identify the row are capped hardest: title 95px (`:1249-1250`), URL 60px (`:1255-1256`), source/medium 80px with a right-to-left front-ellipsis (`:1384-1389`). That is exactly the "ESP32-C…", "http…", "…stodon" in the screenshot. Roughly 250 lines of CSS comments (`:1131-1407`) document the fight to stop this table scrolling; the table is over-subscribed, and no amount of tuning fixes that.
- The columns given full width are the low-value ones: Created, QR (SVG/PNG), Remove.
- Red **Remove** on all 42 rows (`:826-834`), no confirmation (`:408`).
- Sort is clicks only (`:180-190`); no grouping. Three links to the same guide sit in three unrelated rows, so "which content wins" has to be added up by eye.
- The Links table is the sixth panel on the page, after header, Summary, chart, Channel breakdown and Placement (`:453-669`). Batch create and Assign follow it (`:850-884`). One long scroll, no way to jump.
- The window is fixed to the campaign's dates or last 30 days; there is no picker (`:740-744`), though the API accepts `?from=&to=` (`internal/handlers/campaigns.go:488-512`).
- A 6-line caveat paragraph sits permanently under the channel bars (`:621-628`).

### Link detail (`views/LinkDetail.svelte`)
- Metadata-first: a 9-row definition list (`:246-286`) precedes the chart (`:385-387`).
- Campaign is plain text (`:281-282`) even though the API returns `campaign_slug` (`internal/handlers/links.go:614-615`).
- UTM values configured on the link are not shown outside the edit form.
- Breakdown is source/medium/campaign only; no content, referer (`internal/clicks/stats.go:51-57`).
- No sibling links: you cannot see the same content's other platform links.
- Deactivate has no confirmation (`:294-301`).

### Campaigns list (`views/CampaignsList.svelte`)
- Create form first (`:167-253`), list second.
- "Trend" column is a permanent placeholder dash (`:301`).
- Clicks are all-time only (`:285`, `internal/campaigns/store.go` `campaignCountColumns`), so a dead campaign from last spring outranks this month's.

### Charts (`lib/ClicksChart.svelte`, `lib/CampaignClicksChart.svelte`, `lib/UTMBarChart.svelte`)
- No hover or tap read-out in any chart; values are only reachable through the screen-reader table.
- Days are UTC and **today is always excluded** (`internal/clicks/stats.go:179-187`, `:503-530`; `lib/charts.ts:97-100`). For a Pacific-time user, an evening post shows up on the "wrong" day and not at all until the next UTC midnight. A chart that cannot show today's post is the single biggest reason an overview would feel dead.

### Account, tokens, admin, auth
- `confirm()` browser dialogs for revoke/delete (`views/Account.svelte:111`, `:163`; `lib/ApiTokens.svelte:132`; `views/Admin.svelte:247`), while link deactivate/remove have none. Inconsistent, in the wrong direction.
- Passkey rows show "Sign count" (`views/Account.svelte` meta list), an implementation detail.
- Audit log filters by numeric user id (`views/Admin.svelte:867-872`).
- Login is fine structurally (`views/Login.svelte:239-375`); it just lacks the icon and any explanation of what a passkey prompt will look like.

### Global CSS (`web/src/app.css`)
- Base text 13px, secondary 12px (`:97-98`). Small for a tool whose main content is prose titles.
- Tables are global element selectors (`:316-342`) with no column-priority or truncation utilities, so every view invents its own.
- Dark mode is media-query only (`:454`).

---

## 2. Cross-cutting principles

1. **Content is the row; links are the detail.** Wherever links are listed, default to grouping by content and let the user expand to the links.
2. **One identifying column gets the width.** Every table has exactly one flexible column (`width: auto; min-width: 16rem`) holding a two-line cell: line 1 the human title (weight 600, one line, ellipsis, full text in `title`), line 2 muted metadata (`/u/key · /guides/path · mastodon`). Everything else is fixed-width and narrow. Use `table-layout: fixed` + `<colgroup>`; delete the per-cell `max-width` hacks.
3. **Column budget: five.** Identity, one or two dimensions, the metric, an actions menu. Anything else moves to line 2, to the expanded row, or to the CSV.
4. **Truncation.** Titles: end ellipsis, single line in tables, two-line clamp in cards. URLs: show the path, drop scheme and host when the host equals the user's dominant host; middle-ellipsis for long paths. Never front-ellipsis; never truncate a value below about 12 characters, drop the column instead.
5. **Numbers.** Right-aligned, tabular, with an inline bar behind or beside the number so rank is visible without reading. Bar width is relative to the max in the table.
6. **Row actions.** One "⋯" menu per row (Copy short URL, Open destination, QR SVG, QR PNG, Edit, Deactivate/Remove). Copy also appears on hover as an icon, since it is the frequent one. No red buttons in rows. Destructive actions live in the menu and in a bulk bar that appears when rows are checked.
7. **Destructive = undoable, not confirmed.** Replace `confirm()` and the unguarded deactivate with a toast: "Link deactivated · Undo" (8 s). Needs link reactivation in the API (section 5). Keep a real confirm dialog (styled `<dialog>`, not `window.confirm`) only for irreversible things: revoke passkey, revoke token, delete filter rule, sign out everywhere.
8. **Status only when abnormal.** No "Active" pill. Inactive rows are dimmed with an "Inactive" tag; denied rows get the red tag with the reason.
9. **Typography.** Base 14px, small 12.5px, table titles 14px/600, metadata 12.5px muted, page title 20px. Keep system font. Headers stay small caps but lose the grey background fill in favour of a bottom border; less ink.
10. **Colour.** Keep neutral surfaces. One accent. Chart: single accent series for "all", the existing six `--series-*` slots only when comparing named things, grey dashed for the prior period. Consider moving the accent to the icon's violet so app and icon match (`#4b3fd6` light / `#8b84f5` dark both pass 4.5:1 on their panels; verify before adopting).
11. **Time.** Show days in the viewer's time zone and include today as a partial bucket (hollow last point, "so far" label). Dates in tables are relative under 7 days ("3d ago"), absolute otherwise, never wrapping (`white-space: nowrap`).
12. **Empty and loading states.** Skeleton rows instead of "Loading…" text. Empty states say what to do and offer the button ("No clicks in the last 30 days. Share a link or widen the range.").
13. **State lives in the hash query.** Filters, range, tab, sort, grouping. `parseHash` already splits the query (`lib/routing.ts:23-35`) and `Router.svelte` deliberately does not remount on query-only changes, so this needs no router work.
14. **Responsive.** ≥900px tables; below that, the same rows as cards (title, meta line, metric right-aligned, ⋯). Chips scroll horizontally in one line rather than wrapping into a wall. Filter bar collapses to a "Filters (2)" button opening a sheet. Tap targets 40px (already the convention).
15. **Shared components to extract:** `AppShell`, `DataTable` (colgroup, sort headers, two-line cell, row menu, selection), `RowMenu`, `Chip`/`ChipGroup`, `StatTile`, `FilterBar`, `Toast`, `ConfirmDialog`, `DateRange`.

---

## 3. Proposals by screen

### 3.1 Shell and navigation

**Problem.** Duplicated header, no nav on detail pages, no Links destination, creation buried in the home page.

**Proposal.** One `AppShell.svelte` rendered by `App.svelte` around `<Router />`. Routes become:

| Hash | View |
|---|---|
| `#/` | Overview (new) |
| `#/links` | Links list (moved out of Dashboard) |
| `#/links/:key` | Link detail |
| `#/new` | Create link (see 3.6) |
| `#/campaigns`, `#/campaigns/:slug` | unchanged paths |
| `#/account`, `#/admin/:section` | admin sections become routable |

`/new` rather than `/links/new`, because keys are user-chosen and `new` is a legal key.

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ ⛓ Short Links   Overview  Links  Campaigns  Admin        [+ New link]  (B ▾) │
└──────────────────────────────────────────────────────────────────────────────┘
  Campaigns › Guides 2026-11                         ← breadcrumb on detail pages
```

- Avatar menu holds Account and Sign out, freeing a tab slot.
- Detail pages keep the nav; the back button becomes a breadcrumb and the `<h1>` is the item's own name.
- Mobile (≤480px): title + `[+]` + menu on row 1, tabs as a scrolling strip on row 2.
- `[+ New link]` is on every page. Keyboard: `n` opens it, `/` focuses search where present.

### 3.2 Home → Overview (`#/`)

**Problem.** The landing page answers "how do I make a link" when the daily question is "what is getting attention".

**Proposal.** A four-band page: stats, chart with campaign chips, filter bar, ranked content.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ Overview                                             [ Last 30 days ▾ ]  [⤓ CSV]  │
│                                                                                   │
│ ┌ Clicks ────────┐ ┌ Unique visitors ┐ ┌ Best day ───────┐ ┌ Links clicked ────┐  │
│ │ 1,284          │ │ 911             │ │ 96 · Tue Nov 10 │ │ 37 of 58          │  │
│ │ ▲ 18% vs prior │ │ ▲ 12% vs prior  │ │ ESP32 deep sleep│ │ 21 with no clicks │  │
│ └────────────────┘ └─────────────────┘ └─────────────────┘ └───────────────────┘  │
│                                                                                   │
│ Focus: (● All) ( Guides 2026-11 · 902 ) ( Open house · 311 ) ( No campaign · 71 ) │
│                                                                                   │
│  96 ┤                    ╭╮                                                       │
│     │        ╭╮         ╭╯╰╮            ╭─╮                                       │
│  48 ┤   ╭──╮╭╯╰╮   ╭───╯   ╰╮    ╭╮   ╭╯  ╰╮       ╭╮                             │
│     │ ╭─╯  ╰╯  ╰───╯ ┄┄┄┄┄┄ ╰────╯╰───╯ ┄┄┄ ╰───────╯╰──○  ← today so far         │
│   0 ┼┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄                         │
│     Oct 20        Oct 27        Nov 3        Nov 10        Nov 17                 │
│     ── this period   ┄┄ previous 30 days          64 bot clicks excluded          │
│                                                                                   │
│ Filter: [Source ▾] [Medium ▾] [UTM campaign ▾] [Content ▾] [Placement ▾]          │
│         ( source: mastodon ✕ ) ( medium: social ✕ )                   Clear all   │
│                                                                                   │
│ Top content                              Group by: (● Content) ( Link ) ( Source )│
│ ┌──┬────────────────────────────────────────────┬──────────────────┬──────┬─────┐ │
│ │ #│ Content                                    │ By source        │Clicks│  Δ  │ │
│ ├──┼────────────────────────────────────────────┼──────────────────┼──────┼─────┤ │
│ │ 1│ ESP32 deep sleep, measured                 │ ████▓▓▓░░        │  212 │ ▲41%│ │
│ │  │ /guides/esp32-deep-sleep · 3 links         │ M 118 · B 61 · E │      │     │ │
│ │ 2│ Measuring current without blowing the fuse │ ███▓▓░           │  164 │ ▲ 9%│ │
│ │  │ /guides/measuring-current · 3 links        │ M 90 · B 52 · E 22│     │     │ │
│ │ 3│ Reading a datasheet in ten minutes         │ ██▓▓▓            │  131 │ ▼12%│ │
│ │ ▾│ /guides/reading-datasheets · 4 links       │                  │      │     │ │
│ │  │   ├ Mastodon guide post 84   /u/k3Fz9a  social     ·  58     ⧉   ⋯        │ │
│ │  │   ├ Bluesky guide post 84    /u/Qm2x8L  social     ·  41     ⧉   ⋯        │ │
│ │  │   └ Newsletter Nov           /u/news11  email      ·  32     ⧉   ⋯        │ │
│ └──┴────────────────────────────────────────────┴──────────────────┴──────┴─────┘ │
│  Show 10 more · View all links →                                                  │
└───────────────────────────────────────────────────────────────────────────────────┘
```

**The chart.**
- Daily clicks, bots excluded, in the viewer's time zone, default last 30 days *including today*. Today renders as a hollow point with a dotted final segment so a partial day does not read as a drop.
- Line, not bars: 30 points with a comparison series reads better as two lines. Solid accent line with a faint area fill for this period; grey dashed line for the previous 30 days, aligned day-for-day (day 1 over day 1).
- Hover or tap anywhere shows a crosshair and a read-out: "Tue Nov 10 · 96 clicks · previous period 71 · top: ESP32 deep sleep (38)". Arrow keys move the crosshair. This is new behaviour for every chart in the app; build it once in `ClicksChart` and reuse it.
- Range control: 7d / 30d / 90d / custom. Comparison is always "the same-length period immediately before". A 90-day range disables the unique-visitor comparison (see data notes).
- Stat tiles: Clicks, Unique visitors, Best day, Links clicked. Each delta shows direction, percent, and on hover the absolute prior number. No delta when the prior period is zero; show "new" instead.
- "Unique visitors" must be labelled honestly. The IP hash rotates every UTC day (`internal/clicks/iphash.go:17-20`), so the only number available is the sum of daily uniques. Tooltip: "Counted per day; the same person on two days counts twice." If that qualification feels too heavy, drop the tile and show "Clicks per visitor-day" inside the Clicks tile instead. Recommendation: keep it, labelled "Visitors (daily unique)".
- Bot count is a small footnote on the chart, linking to an explanation, rather than a tile.

**Campaign focus chips.**
- One chip per campaign with at least one non-bot click in the window, ordered by clicks, each showing its count. "All" first. "No campaign" last, only if such clicks exist. Campaigns with zero clicks in the window are not shown; archived campaigns are shown if they had clicks.
- Single-select. Choosing a chip re-scopes the tiles, chart, filter facets and top-content list, and adds a "Open campaign →" link at the right of the chip row.
- More than about six chips: show the top five and a "More ▾" menu. On mobile the row scrolls horizontally.
- Alternative: multi-select chips that overlay one line per campaign on the chart. Rejected as default: it turns the overview into a comparison tool and the six-colour legend problem returns. Offer it later as a "Compare" toggle if wanted.
- Counts on the chips respect the UTM filters but not the campaign selection, so they do not all collapse to the selected one.

**UTM filter bar.**
- Five dropdowns: Source, Medium, UTM campaign, Content, Placement. Term is omitted from the bar (rarely set) and reachable under "More".
- Each dropdown lists the values seen in the window with counts, searchable when over eight values, plus "(none)" for clicks with no value. Multi-select within a dropdown.
- Combination rule: **OR within a dimension, AND across dimensions.** `source ∈ {mastodon, bluesky} AND medium = social`. This is what people expect from faceted filters and maps directly to SQL `= ANY($n)`.
- Facet counts are computed with every *other* dimension's filter applied, so a dropdown never offers a value that would produce zero and never hides the alternatives to its own current selection.
- Active filters appear as removable chips under the bar, with "Clear all".
- Filtering applies to clicks: the UTM values on a click are the inbound query value, else the link's stored value (`internal/clicks/recorder.go:118-126`). Placement is a property of the link, so that filter goes through the link.
- Distinguish the two "campaign" concepts in the labels: the focus chips are ShortLinks campaigns; the dropdown is the `utm_campaign` string. Label it "UTM campaign" and hide it by default when every value maps one-to-one onto a ShortLinks campaign.

**URL state.** Everything above serialises into the hash query, so a view can be bookmarked or pasted into a message:

```
#/?range=30d&campaign=guides-2026-11&source=mastodon,bluesky&medium=social&group=content
#/?from=2026-10-01&to=2026-10-31&placement=18th+%26+Texas+board
```

- Keys: `range` (`7d|30d|90d`) or `from`+`to` (`YYYY-MM-DD`); `campaign` (slug, or `none`); `source`, `medium`, `utm_campaign`, `content`, `placement`, `term` (comma-joined, each value `encodeURIComponent`-ed; `~none` for the empty bucket); `group` (`content|link|source`); `sort`.
- Defaults are omitted, so the bare `#/` is the default view.
- Chip and dropdown changes use `navigate()` so Back undoes a filter; typing in a search box uses `replace()`.
- Unknown or stale values (a campaign slug that no longer exists) are ignored with a dismissible notice rather than an error page.
- One pure module, `lib/overviewQuery.ts` (`parse(query) → state`, `serialize(state) → string`), unit-tested alongside `routing.test.ts`, shared by Overview, Links and Campaign detail.

**Top content list.**
- A content group = all of the user's links that point at the same page. Key: the destination URL with `utm_*` parameters, fragment and trailing slash removed and the host lower-cased. That is deterministic and does not depend on how titles were typed.
- Display title for the group: take each member's title, cut it at the last ` — `, ` – `, ` | ` or ` - ` if the tail looks like a channel label (it starts with that link's own `utm_source`, or a known platform name, or matches `/\b(post|flyer|poster|qr|newsletter)\b.*\d*$/i`), then use the most common remaining head. Fallback: the last path segment, humanised. So "Measuring current without blowing the fuse — Mastodon guide post 84" becomes the content title "Measuring current without blowing the fuse", and "Mastodon guide post 84" becomes the label of the child row.
- Each row: rank, content title, path + link count, a stacked mini-bar of clicks by source with a compact legend, total clicks, change vs the prior period. Expanding shows the member links with their own counts, a copy button and the row menu.
- Sort by clicks (default), change, or newest. "Group by" switches the same table to individual links or to sources.
- Shows 10, "Show more" adds 10; "View all links →" goes to `#/links` carrying the current filters.
- Alternative A: group purely by stripped title. Simpler, but two guides with similar titles merge and one typo splits a group. Alternative B: an explicit `content` field on links. Cleanest long-term, but needs a migration and a backfill. **Recommendation:** group by canonical destination now; separately, stop writing the channel into the title at the source (the tool that creates these links should put "Mastodon guide post 84" in `utm_content` or `placement` and leave the title as the content title). The stripping heuristic then only matters for old links.

**Where create-link goes.** Off this page. It becomes the `[+ New link]` button in the shell (3.6). The Overview keeps one empty-state affordance: a brand-new account with no links sees a centred "Create your first link" card instead of an empty chart.

**The SSE stream** that prepends newly created links (`Dashboard.svelte` `onMount`) moves to the Links page.

### 3.3 Links list (`#/links`)

**Problem.** Unreadable titles, a constant hostname column, a constant status pill, a red button per row, no search or sort.

**Proposal.**

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ Links  58                                                          [+ New link]   │
│ [🔍 Search title, key or URL…        ] [Campaign ▾] [Source ▾] [Status: Active ▾] │
│ Group by: (● Content) ( None )                         Sort: [Clicks, 30 days ▾]  │
│ ┌─┬───────────────────────────────────────────┬──────────────┬─────────┬──────┬──┐│
│ │☐│ Link                                      │ Campaign     │ 30 days │ All  │  ││
│ ├─┼───────────────────────────────────────────┼──────────────┼─────────┼──────┼──┤│
│ │☐│ Measuring current without blowing the fuse│ Guides 11    │ ▇▇▇ 90  │  141 │⧉⋯││
│ │ │ /u/k3Fz9a · mastodon · /guides/measuring… │              │         │      │  ││
│ │☐│ Measuring current without blowing the fuse│ Guides 11    │ ▇▇ 52   │   77 │⧉⋯││
│ │ │ /u/Qm2x8L · bluesky · /guides/measuring…  │              │         │      │  ││
│ │☐│ Open house flyer            ⟨Inactive⟩    │ Open house   │      0  │   19 │⧉⋯││
│ │ │ /u/flyer1 · print · /events/open-house    │              │         │      │  ││
│ └─┴───────────────────────────────────────────┴──────────────┴─────────┴──────┴──┘│
│ 2 selected:  [Copy URLs] [Assign to campaign ▾] [Deactivate]        ‹ 1 2 3 ›     │
└───────────────────────────────────────────────────────────────────────────────────┘
```

- Columns: checkbox, Link (two-line), Campaign, Clicks 30 days (with bar), All-time, actions. Created moves into the sort menu and the row tooltip; it does not need a column.
- Line 2 replaces three old columns: short key (monospace, click to copy), source as a small chip, destination *path*.
- Only the title is the link to the detail page. The row is no longer one big click target.
- With grouping on, rows collapse to content groups exactly as in the Overview's top-content table; this page is the "all of it" version with search and pagination.
- Status filter defaults to Active; Inactive and Denied are a filter choice.
- ⋯ menu: Open destination, Copy short URL, QR SVG, QR PNG, Edit, Deactivate.
- Mobile: each row is a card; filters collapse into a sheet.

**Quick win available today** without any backend change: drop Destination-hostname and Status columns, add the two-line cell using fields already in the list response (`title`, `key`, `utm_source`, `destination_url`), replace the red button with a menu. Search, filter, sort-by-clicks and the 30-day column need the API work in section 5.

### 3.4 Link detail (`#/links/:key`)

**Problem.** Metadata before insight; no link to the campaign; no siblings; no read-out on the chart.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ Links › Measuring current without blowing the fuse                                │
│ Measuring current without blowing the fuse                       [Edit] [⋯]       │
│ Mastodon guide post 84 · mastodon / social · Guides 2026-11 →                     │
│ ┌───────────────────────────────────────────────────────────────┐ ┌─────────────┐ │
│ │ go.example.com/u/k3Fz9a                          [Copy]       │ │  ▄▄▄ ▄ ▄▄▄  │ │
│ │ → www.example.com/guides/measuring-current?utm_source=…  ↗    │ │  █ █ ▄ █ █  │ │
│ └───────────────────────────────────────────────────────────────┘ │  SVG · PNG  │ │
│                                                                   └─────────────┘ │
│ ┌ Clicks 30d ──┐ ┌ All-time ─┐ ┌ Last click ───┐ ┌ Created ─────┐                 │
│ │ 90  ▲ 22%    │ │ 141       │ │ 2 hours ago   │ │ Nov 3, 2026  │                 │
│ └──────────────┘ └───────────┘ └───────────────┘ └──────────────┘                 │
│ [ chart: daily clicks, range control, prior-period line, hover read-out ]         │
│                                                                                   │
│ Same content, other links                         Where clicks came from          │
│  Mastodon guide post 84   ▇▇▇▇ 90  (this link)    Referrer   mastodon.social 61   │
│  Bluesky guide post 84    ▇▇   52                            (direct)        29   │
│  Newsletter Nov           ▇    22                 Source     mastodon        90   │
└───────────────────────────────────────────────────────────────────────────────────┘
```

- Header is title, then the channel label and campaign (a real link).
- The short URL box is the hero: big, one-click copy, with the full destination beneath it and a rendered QR preview beside it (the SVG endpoint already exists: `GET /api/links/{key}/qr.svg`).
- "Same content, other links" answers "is Mastodon or Bluesky doing better for this piece" in one glance, built from the same content-grouping function.
- UTM settings and expiry move into an "Details" disclosure and the Edit form; Edit opens in place of the header card rather than as an extra panel below.
- Deactivate moves into ⋯ with undo toast. An inactive link shows a banner with "Reactivate".

### 3.5 Campaigns list (`#/campaigns`)

**Problem.** Form first; all-time clicks only; placeholder Trend column.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ Campaigns                                  [ Show archived (3) ]  [+ New campaign]│
│ ┌──────────────────────────────┬───────────────┬───────┬──────────┬─────────────┐ │
│ │ Campaign                     │ Dates         │ Links │ 30 days  │ Trend       │ │
│ ├──────────────────────────────┼───────────────┼───────┼──────────┼─────────────┤ │
│ │ Guides 2026-11  ⟨Live⟩       │ Nov 1 – 30    │   42  │ ▇▇▇▇ 902 │ ▁▂▅▃▆▇▅▃▂▄ │ │
│ │ Monthly guide posts          │ 12 days left  │       │ 902 total│             │ │
│ │ Open house                   │ Oct 5 – Nov 8 │    9  │ ▇ 311    │ ▃▅▇▅▂▁▁▁▁▁ │ │
│ │ Ended 10 days ago            │               │       │ 648 total│             │ │
│ └──────────────────────────────┴───────────────┴───────┴──────────┴─────────────┘ │
└───────────────────────────────────────────────────────────────────────────────────┘
```

- "New campaign" is a button opening a dialog (name, dates; description and default UTMs under "More"). The list is the page.
- Sort by recent activity by default, so live campaigns are on top. State tag: Live, Scheduled, Ended, Archived, derived from dates.
- 30-day clicks with a bar, all-time as the secondary line, and a real sparkline in the Trend column.
- Alternative: cards with a larger sparkline each. Better at three campaigns, worse at fifteen. Recommendation: the table; switch to cards under 700px.

### 3.6 Campaign detail (`#/campaigns/:slug`)

**Problem.** An eleven-column table truncated into illegibility, at the bottom of a long page, with no way to see which *content* wins.

**Proposal.** Split the page into tabs (in the hash: `?tab=content`), and replace the flat link table with a **content × channel matrix** as the default view.

```
┌───────────────────────────────────────────────────────────────────────────────────┐
│ Campaigns › Guides 2026-11                                                        │
│ Guides 2026-11  ⟨Live⟩   Nov 1 – Nov 30                    [+ Add links ▾] [⋯]    │
│ ┌ Clicks ─────┐ ┌ Per day ──┐ ┌ Links ─────┐ ┌ Top content ───────────────────┐   │
│ │ 902         │ │ 50.1      │ │ 42 · 9 idle│ │ ESP32 deep sleep · 212 (24%)   │   │
│ └─────────────┘ └───────────┘ └────────────┘ └────────────────────────────────┘   │
│  Content   Links   Channels   Placements   Settings          [ Campaign dates ▾ ] │
│ ─────────                                                                         │
│ [ chart: daily clicks; hovering a table row below highlights its line ]           │
│                                                                                   │
│ Columns: (● Source) ( Medium ) ( Placement )           [🔍 Filter content…]       │
│ ┌──┬──────────────────────────────────────────┬─────────┬────────┬───────┬──────┐ │
│ │ #│ Content                                  │Mastodon │Bluesky │ Email │Total▼│ │
│ ├──┼──────────────────────────────────────────┼─────────┼────────┼───────┼──────┤ │
│ │ 1│ ESP32 deep sleep, measured               │ ▓▓▓ 118 │ ▓▓ 61  │ ▓ 33  │  212 │ │
│ │ 2│ Measuring current without blowing the f… │ ▓▓▓  90 │ ▓▓ 52  │ ░ 22  │  164 │ │
│ │ 3│ Reading a datasheet in ten minutes       │ ▓▓   58 │ ▓  41  │ ▓ 32  │  131 │ │
│ │ 4│ Soldering iron tip care                  │ ░    12 │   —    │ ░  4  │   16 │ │
│ ├──┼──────────────────────────────────────────┼─────────┼────────┼───────┼──────┤ │
│ │  │ All content                              │    498  │   261  │  143  │  902 │ │
│ └──┴──────────────────────────────────────────┴─────────┴────────┴───────┴──────┘ │
│  7 clicks came from links since removed from this campaign.                       │
└───────────────────────────────────────────────────────────────────────────────────┘
```

- **Content tab (default).** Rows are content groups, columns are the campaign's sources (or medium, or placement), cells are clicks with a shaded background proportional to the value, a dash where no link exists for that pairing. One table answers both "which guide" and "on which platform". Clicking a cell opens that link's detail; clicking a title expands the row to its links. With more than six sources, show the top five and an "Other" column.
- **Links tab.** The flat list, for management, at the five-column budget:

```
│ ☐ │ Link                                         │ Channel         │ Clicks ▼   │   │
│ ☐ │ ESP32 deep sleep, measured                   │ mastodon·social │ ▇▇▇▇ 118   │⧉ ⋯│
│   │ /u/k3Fz9a · Mastodon guide post 91           │                 │ 13%        │   │
```
  Key, destination and channel label fold into line 2; Source and Medium merge into one Channel column; Share becomes the percentage under the bar; Created, QR and Remove leave the grid (sort menu, ⋯ menu, bulk bar). Placement appears as a column only if any link in the campaign has one. Toolbar keeps Copy all, QR zip, CSV, grouped under one "Export ▾" button. Sortable by any column.
- **Channels tab.** The existing source / medium / content / referer bars. The long caveat becomes a one-line note with a "Why?" disclosure.
- **Placements tab.** The existing table, shown only when placements exist.
- **Settings tab.** Edit name/dates, default UTMs, archive.
- **Add links ▾** holds "Create links for this campaign" (the batch form) and "Assign existing links". Both open as dialogs and stop occupying the bottom of the page.
- Range control top-right: "Campaign dates" (default), 7d, 30d, custom, passed to the stats endpoint's existing `from`/`to`.
- Removing a link is "Remove from campaign" in ⋯ or the bulk bar, with undo (re-assign is an existing endpoint).

**Alternative:** keep one page and just fix the table (five columns, grouped). Less work, and worth doing first as the quick win, but the page stays six panels long. **Recommendation:** ship the reduced table plus content grouping in phase 1, tabs and the matrix in phase 2.

### 3.7 Create link and batch create

**Problem.** Seven stacked fields occupy the top of the home page; batch create is a tall form at the bottom of the campaign page with one stacked block of five inputs per row.

**Create link (`#/new`).** A focused page on mobile, a centred dialog over the current page on desktop.

```
┌ New link ────────────────────────────────────────────────┐
│ Destination URL                                          │
│ [ https://www.example.com/guides/measuring-current     ] │
│ Title                                                    │
│ [ Measuring current without blowing the fuse           ] │
│ Campaign  [ Guides 2026-11 ▾ ]   Channel [ mastodon ▾ ]  │
│ ▸ More: custom alias · expiry · UTM fields · placement   │
│                                                          │
│ Will redirect to  …/measuring-current?utm_source=masto…  │
│                                   [Cancel] [Create link] │
└──────────────────────────────────────────────────────────┘
        ↓ on success, the dialog becomes
┌ Link ready ──────────────────────────────────────────────┐
│  go.example.com/u/k3Fz9a                   [Copy] [QR]   │
│  [Create another for this page ▾ bluesky]   [Done]       │
└──────────────────────────────────────────────────────────┘
```

- Pasting a URL anywhere on the Links page while no input is focused opens this with the URL filled.
- "Channel" is a shortcut that fills source and medium from the campaign's conventions; the full UTM fields stay under More.
- "Create another for this page" is the common follow-up (same content, next platform) and keeps URL, title and campaign.
- Title guidance in the field's hint: "Name the content, not the channel."

**Batch create.** Make it a grid, one line per link, with the shared fields on top:

```
┌ Create links for Guides 2026-11 ─────────────────────────────────────────────┐
│ Destination [ https://www.example.com/guides/measuring-current            ]  │
│ Title       [ Measuring current without blowing the fuse                  ]  │
│ ┌────────────┬──────────┬────────────────────┬──────────────────────┬───┐    │
│ │ Source     │ Medium   │ Content / label    │ Placement            │   │    │
│ │ mastodon   │ social   │ guide post 84      │                      │ ✕ │    │
│ │ bluesky    │ social   │ guide post 84      │                      │ ✕ │    │
│ │ poster     │ print    │ qr                 │ 18th & Texas board   │ ✕ │    │
│ │ + Add row    (paste rows from a spreadsheet)                            │    │
│ └────────────┴──────────┴────────────────────┴──────────────────────┴───┘    │
│ [ Preset: Social trio ▾ ]                      [Cancel]  [Create 3 links]    │
└──────────────────────────────────────────────────────────────────────────────┘
```

- One shared title (the content title); the per-row label goes to `utm_content`. This is the structural fix for the title-suffix problem.
- Tab/Enter move through cells; pasting tab-separated text fills rows; presets remember the last-used set of channels (per-viewer `localStorage`).
- Result screen lists the created short URLs with Copy all and QR zip.

### 3.8 Account, passkeys, API tokens

**Problem.** Mostly sound. Issues are polish: jargon, `confirm()`, and three unrelated panels at equal weight.

```
┌ Account ─────────────────────────────────────────────────────────────────────┐
│ user@example.com                                     [Sign out everywhere]   │
├ Passkeys ──────────────────────────────────────────────────────────────────┤
│ 🔑 MacBook Touch ID   ⟨This device⟩   Used 2 hours ago · Added Mar 2026   ⋯  │
│ 🔑 iPhone             Synced          Used 3 days ago · Added Mar 2026    ⋯  │
├ API tokens ────────────────────────────────────────────────── [+ New token] ─┤
│ marketing-agent  ⟨Read-only⟩  a1b2c3…    Used today from 203.0.113.7      ⋯  │
│ Expires in 12 days ⚠                                                         │
│ ▸ 2 revoked or expired tokens                                                │
└──────────────────────────────────────────────────────────────────────────────┘
```

- One line per passkey/token with a ⋯ menu (Rename, Revoke). Drop "Sign count". Mark the passkey used for the current session. Show the backup/synced flag in words.
- Revoke uses the shared confirm dialog; revoking the last passkey is blocked with an explanation rather than a warning.
- Expiring-soon tokens get a warning tag; revoked/expired collapse into a disclosure.
- The new-token secret panel stays as is; it is the right pattern.
- Adding a second passkey from this page would be a natural addition, but there is no endpoint for it today (only list, rename, revoke under `/account/credentials`); out of scope here.
- Add a Preferences block: time zone for charts (default: browser), theme (system / light / dark).

### 3.9 Admin

- Sections become routes: `#/admin/settings`, `/filters`, `/users`, `/audit`. Left-hand sub-nav on desktop, a select on mobile.
- **URL filters:** put "Test a URL" inline above the rule table and highlight the matching rule row on a hit. Rule add/edit in a dialog. Active becomes a toggle in the row.
- **Users:** search by email; ⋯ menu for Deactivate/Reactivate; show link count per user if cheap.
- **Audit log:** filter by email (typeahead over the users list) and by action type and date range instead of a numeric id; Metadata collapses to a one-line summary with expand; relative time with absolute on hover; filters in the hash.

```
┌ Admin ───────┬───────────────────────────────────────────────────────────────┐
│ Settings     │ Audit log    [User ▾] [Action ▾] [Last 7 days ▾]       [⤓ CSV] │
│ URL filters  │ 2h ago   link.created     user@example.com   /u/k3Fz9a     ▸   │
│ Users        │ 3h ago   campaign.updated user@example.com   guides-2026-11 ▸  │
│ ● Audit log  │ 1d ago   auth.login       user@example.com   passkey: MacBook ▸│
└──────────────┴───────────────────────────────────────────────────────────────┘
```

### 3.10 Auth screens (Login, Register verify, Recover verify, signed-out Home)

- Add the icon above the title; keep the single narrow card.
- Primary action first: a "Sign in with passkey" button that works with an empty email field (discoverable credentials) if the backend supports it; otherwise keep email → passkey but support `autocomplete="username webauthn"` so the browser offers the passkey in the field.
- Register and Recover as text links under the card rather than two more full-width buttons.
- Verify screens: a three-step indicator (Email confirmed → Create passkey → Done) and a plain-language line about what the system prompt will ask.
- Error states say what to do next ("That link expired. Send a new one.") with the action inline.

```
            ⛓
        Short Links
┌──────────────────────────┐
│ Email                    │
│ [ you@example.com      ] │
│ [  Sign in with passkey ]│
└──────────────────────────┘
 New here? Register · Lost your passkey?
```

---

## 4. Charts: shared changes

Applies to `ClicksChart`, `CampaignClicksChart`, `UTMBarChart` and the new sparkline.

- Hover/tap/keyboard read-out with crosshair (none exists today).
- Optional `comparison` series prop (grey dashed).
- `partialLast` prop to draw today hollow.
- Explicit `from`/`to` props so the component stops deriving its own window (`lib/charts.ts:100`), which currently has to mirror the server's.
- Direct end-of-line labels in the campaign chart instead of a legend when four or fewer series.
- `Sparkline.svelte`: 80×20, no axes, last point dotted, `aria-label` with the total.
- Bars in `UTMBarChart` become clickable where a filter exists (click "mastodon" to add `source=mastodon`).

---

## 5. Data feasibility

### Already supported (frontend-only)

| Proposal | Source |
|---|---|
| Shell, routes, breadcrumbs, admin sub-routes | Router only (`App.svelte`, `lib/routing.ts`) |
| Links list two-line cell, path instead of host, row menu, no Active pill | `GET /api/links` already returns `title`, `key`, `destination_url`, `utm_*`, `placement`, `campaign_id`, `click_count` (`internal/handlers/links.go:182-201`) |
| Campaign Links table reduced to five columns | `GET /api/campaigns/{slug}` + `/stats` `by_link` |
| Campaign content grouping and the content × source matrix | Same two calls: every campaign link (with `destination_url`, `utm_source`, `utm_medium`, `placement`) plus per-link windowed counts. Grouping is a pure client function. No pagination on campaign links, so it is complete. |
| Campaign range control | `/stats?from=&to=` exists (`internal/handlers/campaigns.go:488-512`); the detail call would need the same params or the view should read everything from `/stats` |
| Link detail: campaign link, QR preview | `campaign_slug` and `qr.svg` exist |
| Link detail siblings (within a campaign) | Campaign links call |
| Hash-encoded filter state | `parseHash` query + no remount on query change (`lib/Router.svelte`) |
| Chart hover, sparkline component, comparison line rendering | Client only |
| Create dialog, batch grid, shared batch title | Existing `POST /api/links` and `POST /api/campaigns/{slug}/links/batch` (per-row `title` already optional) |

### Needs backend work

**A. Overview endpoint (the core of the brief).** Nothing user-wide exists today: per-link series (`ClicksOverTime`) and per-campaign rollups (`CampaignRollup`) only. Fetching 58 link details from the browser is not acceptable.

```
GET /api/stats/overview
    ?from=YYYY-MM-DD&to=YYYY-MM-DD        (default: last 30 days incl. today)
    &tz=America/Los_Angeles
    &campaign=<slug>|none
    &source=a,b&medium=…&utm_campaign=…&content=…&term=…&placement=…
```
```json
{
  "window":   {"from": "2026-10-20", "to": "2026-11-18", "tz": "America/Los_Angeles", "partial_last_day": true},
  "totals":   {"clicks": 1284, "visitor_days": 911, "excluded_bots": 64, "links_clicked": 37, "links_total": 58},
  "previous": {"clicks": 1088, "visitor_days": 813},
  "timeseries":          {"days": [{"date": "2026-10-20", "count": 31}]},
  "previous_timeseries": {"days": []},
  "campaigns": [{"slug": "guides-2026-11", "name": "Guides 2026-11", "count": 902}, {"slug": null, "count": 71}],
  "facets":   {"source": [{"value": "mastodon", "count": 498}], "medium": [], "utm_campaign": [], "content": [], "placement": []},
  "by_link":  [{"key": "k3Fz9a", "title": "…", "destination_url": "…", "utm_source": "mastodon",
                "campaign_slug": "guides-2026-11", "count": 90, "previous_count": 74}]
}
```
- Scope: `clicks` has no `user_id`, so every query joins `links l ON l.id = k.link_id WHERE l.user_id = $1 AND k.is_bot = FALSE AND k.clicked_at >= $2 AND k.clicked_at < $3`. `idx_clicks_clicked_at` (migration 000003) covers the range scan; at this volume that is fine. If it ever is not, add `(link_id, clicked_at)`.
- Filters: `k.utm_source = ANY($n)` etc.; `(none)` maps to `IS NULL OR = ''` as `dimensionBreakdown` already does (`internal/clicks/stats.go:1014`). Placement and campaign-by-current-membership filter on `l.`; campaign attribution should use `k.campaign_id`, consistent with the existing rollups.
- Facets: one grouped query per dimension, each with the other dimensions' filters. Reuse `dimensionBreakdown` after generalising its filter argument from a single column/id to a predicate builder.
- `campaigns`: `GROUP BY k.campaign_id` with UTM filters but without the campaign filter.
- `by_link` is returned ungrouped and the browser does the content grouping. Keeps the heuristic in one tested TypeScript module and avoids a schema change. Cap at 500 rows.
- Previous period: same queries with the window shifted back by its own length. Run everything in one `REPEATABLE READ` read-only transaction, as `beginCampaignReadTx` does.
- Visitor-days: `COUNT(DISTINCT (k.ip_hash, date))` where `ip_hash IS NOT NULL`. Constraint: `ip_hash` is nulled after 90 days (`internal/clicks/retention.go:12-14`), so current + previous must fit in 90 days. A 30-day range (60 total) works; for a 90-day range return `previous.visitor_days: null`.
- Effort: M–L (new store method, handler, tests in the style of `stats_test.go`).

**B. Time zone and "today".** Add `tz` to the day-bucketing (`date_trunc('day', clicked_at AT TIME ZONE $tz)`) and make the default `to` exclusive-tomorrow rather than today-midnight in `ClicksOverTime` and `campaignWindow` (`internal/clicks/stats.go:179-187`, `:503-530`). This changes documented behaviour (`docs/analytics.md:338-349`) and the client helper `defaultDateRange`; `clicksPerDayAverage` must then count the partial day as a fraction or exclude it. Effort: M. Worth doing before or with A, because the Overview is where a missing "today" hurts most.

**C. Links list query.** `GET /api/links` gains `q` (ILIKE over title, key, destination), `campaign`, `source`, `status`, `sort` (`created|clicks|clicks_window|title`), `window=30d`, returning `clicks_window` beside `click_count`. Today it is fixed `ORDER BY created_at DESC` with all-time counts (`internal/links/store.go:676-688`). Effort: M. Cheaper alternative: `per_page` up to 500 and do it all client-side; acceptable for one user with under a few hundred links, not as the long-term answer.

**D. Reactivate a link.** `PATCH /api/links/{key}` accepting `active: true|false` (or `POST …/reactivate`), with redirect-cache eviction like the existing delete path. Required for undo toasts and the inactive-link banner. Effort: S.

**E. Campaign list activity.** Add `clicks_30d` and a 30-point `sparkline` array to `GET /api/campaigns` items (one grouped query over `clicks` by `campaign_id, day`). Effort: S–M. Could instead be read from endpoint A's `campaigns` for counts, leaving only the sparkline.

**F. Link detail extras.** Add `by_content` and `by_referer` to `UTMStats` (the campaign version already has them), `last_clicked_at`, and `from`/`to`/`tz` on `GET /api/links/{key}`. Siblings outside a campaign need either C's `q`/destination filter or a `siblings` array. Effort: S–M.

**G. Admin audit filters.** Check `GET /admin/audit` for action/date/email filters; today the UI only sends a user id. Likely S.

**H. Optional, later: a stored content key.** A `content_key` (canonical destination) column on `links` with an index would let the server group and paginate by content. Only needed if link counts outgrow client-side grouping.

**Not needed:** no change to click recording. Resolved UTM values are already stored per click.

---

## 6. Roadmap

### Phase 1 — quick wins, frontend only

| # | Change | Effort |
|---|---|---|
| 1 | Campaign Links table → five columns with two-line title cell; QR/Remove into ⋯ menu; delete the max-width/RTL workarounds | M |
| 2 | Content grouping in campaign detail (pure `lib/content.ts`: canonical destination + title-suffix stripping, with tests); "Group by content" on by default | M |
| 3 | Links list: two-line cell, path instead of hostname, drop Active pill and Created column, ⋯ menu instead of red Deactivate | S |
| 4 | `AppShell` with shared nav on every page, breadcrumbs, real `<h1>` on detail pages | S |
| 5 | Split routes: `#/links` list, `#/new` create dialog, `[+ New link]` in the shell; `#/` temporarily shows the list until phase 2 | M |
| 6 | Type scale to 14/12.5, `nowrap` dates, relative dates, table header restyle | S |
| 7 | Link detail: campaign as link, QR preview, stat tiles above the chart | S |
| 8 | Styled `ConfirmDialog` replacing `window.confirm`; confirm on deactivate/remove until undo exists | S |
| 9 | Chart hover/tap read-out in `ClicksChart` and `CampaignClicksChart` | M |
| 10 | Admin sections as routes | S |

Items 1–3 fix both screenshots. Item 2 alone answers "which content gets attention" inside a campaign.

### Phase 2 — the Overview

| # | Change | Effort |
|---|---|---|
| 11 | Time zone + include today (backend B) | M |
| 12 | `GET /api/stats/overview` (backend A) | L |
| 13 | Overview page: stat tiles, 30-day chart with prior-period line, campaign chips, top content | L |
| 14 | UTM filter bar + `lib/overviewQuery.ts` hash state | M |
| 15 | Campaign detail tabs + content × source matrix + range control | M |
| 16 | Link reactivation (backend D) + undo toasts | S |
| 17 | Campaign list: 30-day clicks, sparkline, state tags, create-in-dialog (backend E) | M |

### Phase 3 — depth and polish

| # | Change | Effort |
|---|---|---|
| 18 | Links list search/filter/sort/bulk actions (backend C) | M |
| 19 | Batch create as a grid with shared title, paste, presets | M |
| 20 | Link detail siblings, referer/content breakdown, last click (backend F) | M |
| 21 | Account restyle, preferences (time zone, theme), token expiry warnings | S |
| 22 | Admin audit filters, URL filter inline test (backend G) | M |
| 23 | Auth screen polish, passkey autofill | S |
| 24 | Mobile card layouts and filter sheet across all tables | M |
| 25 | Change the link-creating tool to write content titles and put the channel label in `utm_content`; optional one-off cleanup of existing titles | S |
| 26 | Optional: accent colour aligned with the icon; "Compare campaigns" overlay; stored `content_key` (backend H) | S–M |

### Open questions for the owner

1. Is "visitors (daily unique)" worth a tile given its caveat, or should the Overview show clicks only?
2. Should chart days follow the browser's time zone or one fixed account time zone? (Shared links to a view render differently per viewer with the former.)
3. Are link titles written by hand or by the publishing tool? If by the tool, item 25 is the real fix and should move to phase 1.
4. Is there ever more than one destination host? If not, the host can be dropped everywhere, not just when dominant.
