// Pure logic for the signed-out home page (#0125). No DOM, no Svelte runtime,
// so it is unit-tested directly in home.test.ts.

/** How long the home page waits before sending the visitor on. Fixed, not a setting. */
export const HOME_REDIRECT_DELAY_MS = 3000;

export type HomeMode = 'default' | 'about' | 'activity';

/** The hero image the admin uploaded (#0176). */
export interface HomeHero {
  url: string;
  alt: string;
  width: number;
  height: number;
}

/** Site-wide 7-day summary (#0175); only present in `activity` mode. */
export interface HomeActivity {
  window: { from: string; to: string; tz: string; partial_last_day: boolean };
  total_clicks: number;
  timeseries: { date: string; clicks: number }[];
  top_links: { title: string; short_url: string; clicks: number }[];
}

/**
 * GET /api/public/home body: exactly these facts, no other settings. The
 * content fields (#0174) are optional so an older server, or a test that only
 * cares about the redirect, can omit them; they read as `default` mode.
 */
export interface HomeInfo {
  redirect_url: string;
  registrations_enabled: boolean;
  mode?: HomeMode;
  title?: string;
  about?: string;
  hero?: HomeHero | null;
  activity?: HomeActivity;
}

/** The mode the page is in; anything unknown reads as `default`. */
export function homeMode(info: HomeInfo | null): HomeMode {
  const m = info?.mode;
  return m === 'about' || m === 'activity' ? m : 'default';
}

/**
 * Show a home page for a signed-out visitor at the bare root. In `about` and
 * `activity` mode that is the content page. In `default` mode it is only the
 * redirect page, when enrollment is closed and a primary URL is set; any other
 * combination keeps today's behavior (the sign-in bounce).
 */
export function shouldShowHome(
  signedIn: boolean,
  path: string,
  info: HomeInfo | null,
): boolean {
  if (signedIn || path !== '/' || info === null) return false;
  if (homeMode(info) !== 'default') return true;
  return info.redirect_url !== '' && !info.registrations_enabled;
}

/**
 * The route-table component enterApp should wait for once auth is known, or
 * null when nothing should load: the public home is about to show, and it is
 * eager, so fetching Overview for it would cost every signed-out visitor
 * the chunk (#0186).
 */
export function componentToPreload<T>(
  signedIn: boolean,
  route: { path: string; component: T },
  info: HomeInfo | null,
): T | null {
  return shouldShowHome(signedIn, route.path, info) ? null : route.component;
}

/** True when the home page is the content page rather than the redirect page. */
export function isContentHome(info: HomeInfo | null): boolean {
  return homeMode(info) !== 'default';
}

// ── About text (#0177) ──────────────────────────────────────────────────────
// The about text is admin-written plain text. It is rendered as structured
// segments, never as HTML: the view prints each segment with Svelte's `{…}`
// escaping, so nothing in the text can become markup. Only bare https:// URLs
// become links, so `javascript:`, `http:` and `data:` never do.

export type AboutSegment =
  | { kind: 'text'; text: string }
  | { kind: 'link'; text: string; href: string };

const URL_RE = /https:\/\/[^\s<>"'`]+/g;
const CLOSERS: Record<string, string> = { ')': '(', ']': '[', '}': '{' };

function count(s: string, ch: string): number {
  return s.split(ch).length - 1;
}

/** Drop sentence punctuation and unbalanced closing brackets from the end of a matched URL. */
function trimUrl(url: string): string {
  let u = url;
  for (;;) {
    const last = u[u.length - 1];
    if (last !== undefined && '.,;:!?'.includes(last)) {
      u = u.slice(0, -1);
    } else if (last !== undefined && CLOSERS[last] && count(u, last) > count(u, CLOSERS[last])) {
      u = u.slice(0, -1);
    } else return u;
  }
}

/** Split one paragraph into text and link segments. */
export function linkifyParagraph(paragraph: string): AboutSegment[] {
  const out: AboutSegment[] = [];
  let last = 0;
  for (const m of paragraph.matchAll(URL_RE)) {
    const start = m.index ?? 0;
    const text = trimUrl(m[0]);
    let href = '';
    try {
      const u = new URL(text);
      // Credentials in a URL are never linked: https://user:pass@host.
      if (u.protocol === 'https:' && u.host !== '' && u.username === '' && u.password === '') {
        href = u.href;
      }
    } catch {
      href = '';
    }
    if (href === '') continue;
    if (start > last) out.push({ kind: 'text', text: paragraph.slice(last, start) });
    out.push({ kind: 'link', text, href });
    last = start + text.length;
  }
  if (last < paragraph.length) out.push({ kind: 'text', text: paragraph.slice(last) });
  return out;
}

/** Blank lines separate paragraphs; single newlines stay inside one. */
export function aboutParagraphs(about: string): AboutSegment[][] {
  return about
    .replace(/\r\n?/g, '\n')
    .split(/\n{2,}/)
    .map((p) => p.trim())
    .filter((p) => p !== '')
    .map(linkifyParagraph);
}

/** The page heading: the admin's title, else the instance host. */
export function homeHeading(title: string | undefined, host: string): string {
  const t = (title ?? '').trim();
  return t !== '' ? t : host;
}

/** "{total} clicks this week", with the singular handled. */
export function weekClicksLabel(total: number): string {
  return `${total.toLocaleString()} ${total === 1 ? 'click' : 'clicks'} this week`;
}

/** Host shown in "Taking you to <host>…"; falls back to the raw string if it does not parse. */
export function redirectHost(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

/**
 * Whether Login offers "Register". Only when the server said enrollment is
 * open: a closed answer or a failed fetch (null) hides it, because a sign-up
 * the server will refuse is worse than none. The server-side gate in
 * /auth/register/start is the enforcement; this is UX only.
 */
export function registerOptionVisible(info: HomeInfo | null): boolean {
  return info !== null && info.registrations_enabled;
}
