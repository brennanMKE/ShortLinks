<!--
  PublicHome (#0177) — the signed-out home page for `about` and `activity`
  mode. Used by views/Home.svelte for visitors and by the Admin → Settings
  preview, so the preview is the real page, fed with unsaved values.

  Safety: `title` and `about` are plain text printed with Svelte's `{…}`
  escaping, never {@html}. The about text is split into text and link
  segments by lib/home.ts, and only https:// URLs become links.

  Layout uses container queries, not media queries, so the same component
  reads correctly in the narrow admin preview frame and on a real phone.
  `preview` turns the Sign in / Register links and the short links into inert
  elements.
-->
<script lang="ts">
  import Link from './Link.svelte';
  import ClicksChart from './ClicksChart.svelte';
  import {
    aboutParagraphs,
    homeHeading,
    homeMode,
    weekClicksLabel,
    type HomeInfo,
  } from './home';

  let { info, host, preview = false }: { info: HomeInfo; host: string; preview?: boolean } =
    $props();

  const mode = $derived(homeMode(info));
  const heading = $derived(homeHeading(info.title, host));
  const paragraphs = $derived(aboutParagraphs(info.about ?? ''));
  const hero = $derived(info.hero ?? null);
  const activity = $derived(mode === 'activity' ? (info.activity ?? null) : null);
  const series = $derived({
    days: (activity?.timeseries ?? []).map((d) => ({ date: d.date, count: d.clicks })),
  });
</script>

<div class="pub" class:preview>
  <svelte:element this={preview ? 'div' : 'main'} class="pub-main">
    {#if hero}
      <!-- width/height reserve the space (aspect-ratio) while the image loads. -->
      <figure class="hero" style="--hero-ratio: {hero.width} / {hero.height}">
        <img src={hero.url} alt={hero.alt} width={hero.width} height={hero.height} />
      </figure>
    {/if}

    <header class="pub-head">
      {#if !hero}<img class="brand-icon" src="/apple-touch-icon.png?v=3" alt="" width="56" height="56" />{/if}
      <svelte:element this={preview ? 'h2' : 'h1'} class="pub-title">{heading}</svelte:element>
    </header>

    {#if paragraphs.length > 0}
      <div class="about">
        {#each paragraphs as segments, i (i)}
          <p>
            {#each segments as seg, j (j)}
              {#if seg.kind === 'link'}
                <a href={seg.href} rel="noopener noreferrer">{seg.text}</a>
              {:else}{seg.text}{/if}
            {/each}
          </p>
        {/each}
      </div>
    {/if}

    {#if mode === 'activity'}
      <section class="activity" aria-label="Activity this week">
        {#if activity === null}
          <p class="text-muted empty">Activity isn't available right now.</p>
        {:else if activity.total_clicks > 0}
          <p class="week-total">{weekClicksLabel(activity.total_clicks)}</p>
          <div class="chart">
            <ClicksChart
              timeseries={series}
              from={activity.window.from}
              to={activity.window.to}
              partialLast={activity.window.partial_last_day}
              title="Clicks per day, last 7 days"
            />
          </div>
          {#if activity.top_links.length > 0}
            <h2 class="sec-title popular">Popular this week</h2>
            <ol class="cards">
              {#each activity.top_links as l, i (i)}
                <li>
                  {#if preview}
                    <span class="card">
                      <span class="card-title">{l.title}</span>
                      <span class="card-url">{l.short_url}</span>
                      <span class="card-n">{l.clicks.toLocaleString()} {l.clicks === 1 ? 'click' : 'clicks'}</span>
                    </span>
                  {:else}
                    <a class="card" href={l.short_url} rel="noopener noreferrer">
                      <span class="card-title">{l.title}</span>
                      <span class="card-url">{l.short_url}</span>
                      <span class="card-n">{l.clicks.toLocaleString()} {l.clicks === 1 ? 'click' : 'clicks'}</span>
                    </a>
                  {/if}
                </li>
              {/each}
            </ol>
          {/if}
        {:else}
          <p class="text-muted empty">No clicks yet this week.</p>
        {/if}
      </section>
    {/if}

    <nav class="actions" aria-label="Account">
      {#if preview}
        <span class="btn-link btn-link-primary">Sign in</span>
        {#if info.registrations_enabled}<span class="btn-link">Register</span>{/if}
      {:else}
        <Link to="/login" class="btn-link btn-link-primary">Sign in</Link>
        {#if info.registrations_enabled}
          <Link to="/login?mode=register" class="btn-link">Register</Link>
        {/if}
      {/if}
    </nav>
  </svelte:element>
</div>

<style>
  .pub {
    container-type: inline-size;
    container-name: pub;
    position: relative;
    width: 100%;
  }
  .pub-main {
    max-width: 960px;
    margin: 0 auto;
    padding: var(--space-5) var(--space-4) var(--space-4);
  }
  .preview .pub-main {
    padding-top: var(--space-4);
  }

  /* Hero: full width of the column, cropped with cover. The aspect ratio comes
     from the stored size, so the box exists before the image arrives. */
  .hero {
    margin: 0 0 var(--space-5);
    border-radius: 8px;
    overflow: hidden;
    background: var(--bg-subtle);
    aspect-ratio: var(--hero-ratio);
  }
  .hero img {
    display: block;
    width: 100%;
    height: 100%;
    object-fit: cover;
    object-position: center;
  }

  .pub-head {
    text-align: center;
    margin-bottom: var(--space-4);
  }
  .brand-icon {
    display: block;
    margin: 0 auto var(--space-3);
    border-radius: 13px;
  }
  .pub-title {
    font-size: 30px;
    line-height: 1.2;
    font-weight: 650;
    margin: 0;
    overflow-wrap: anywhere;
  }

  .about {
    max-width: 40rem;
    margin: 0 auto var(--space-5);
    font-size: var(--fs-md);
    line-height: 1.6;
  }
  .about p {
    margin: 0 0 var(--space-3);
    white-space: pre-line;
    overflow-wrap: anywhere;
  }
  .about a {
    color: var(--accent);
  }

  .activity {
    margin: 0 0 var(--space-5);
    padding: var(--space-4);
    background: var(--bg-panel);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
  }
  .sec-title {
    font-size: var(--fs-md);
    font-weight: 600;
    margin: 0 0 var(--space-2);
  }
  .week-total + .chart { margin-top: var(--space-2); }
  .sec-title.popular {
    margin-top: var(--space-5);
  }
  .week-total {
    margin: 0 0 var(--space-2);
    font-size: var(--fs-lg);
    font-weight: 600;
    font-variant-numeric: tabular-nums;
  }
  .empty {
    margin: 0;
  }

  .cards {
    list-style: none;
    margin: 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: var(--space-3);
  }
  .card {
    display: flex;
    flex-direction: column;
    gap: var(--space-1);
    height: 100%;
    padding: var(--space-3);
    background: var(--bg-subtle);
    border: var(--border-w) solid var(--border);
    border-radius: var(--radius);
    color: var(--text);
    text-decoration: none;
    min-width: 0;
  }
  a.card:hover {
    border-color: var(--accent);
  }
  .card-title {
    font-weight: 600;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    overflow-wrap: anywhere;
  }
  .card-url {
    font-size: var(--fs-sm);
    color: var(--accent);
    overflow-wrap: anywhere;
  }
  .card-n {
    margin-top: auto;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    font-variant-numeric: tabular-nums;
  }

  .actions {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: var(--space-3);
  }
  .actions :global(.btn-link) {
    min-width: 8rem;
    justify-content: center;
    text-align: center;
  }

  @container pub (max-width: 640px) {
    /* Phones: the hero is cropped to a 4:3 centre. */
    .hero {
      aspect-ratio: 4 / 3;
      border-radius: 6px;
    }
    .pub-title {
      font-size: 24px;
    }
    .cards {
      grid-template-columns: minmax(0, 1fr);
    }
    .activity {
      padding: var(--space-3);
    }
  }
</style>
