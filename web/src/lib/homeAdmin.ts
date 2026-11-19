// Pure logic for the Admin → Settings "Home page" section (#0177). No DOM, no
// Svelte runtime; unit-tested in homePage.test.ts.

import type { HomeActivity, HomeMode } from './home';
import type { Setting } from './types';

/** Server limits (#0174, #0176). The server enforces them; these keep the form honest. */
export const HOME_TITLE_MAX = 80;
export const HOME_ABOUT_MAX = 2000;
export const HERO_MAX_BYTES = 2 << 20;

export const HOME_MODES: { value: HomeMode; label: string }[] = [
  { value: 'default', label: 'Sign-in screen (or redirect)' },
  { value: 'about', label: 'About page' },
  { value: 'activity', label: 'About page with weekly activity' },
];

export interface HomeDraft {
  mode: HomeMode;
  title: string;
  about: string;
}

/** Read the three home settings out of the settings list; missing rows are unset. */
export function homeSettingsFrom(settings: Setting[]): HomeDraft {
  const get = (key: string) => settings.find((s) => s.key === key)?.value ?? '';
  const m = get('home_mode');
  return {
    mode: m === 'about' || m === 'activity' ? m : 'default',
    title: get('home_title'),
    about: get('home_about'),
  };
}

/**
 * The [key, value] pairs to PATCH: only what differs from what is saved. The
 * server trims and normalises line endings, so compare on the trimmed value to
 * keep Save from lighting up after a save.
 */
export function changedHomeSettings(saved: HomeDraft, draft: HomeDraft): [string, string][] {
  const out: [string, string][] = [];
  if (draft.mode !== saved.mode) out.push(['home_mode', draft.mode]);
  if (draft.title.trim() !== saved.title) out.push(['home_title', draft.title.trim()]);
  const about = draft.about.replace(/\r\n?/g, '\n').trim();
  if (about !== saved.about) out.push(['home_about', about]);
  return out;
}

/** Size used for the preview of a chosen image: the shape is clamped to the 2:1 to 4:1 box the server accepts. */
export function previewHeroSize(width: number, height: number): { width: number; height: number } {
  if (width <= 0 || height <= 0) return { width: 2400, height: 800 };
  const ratio = Math.min(4, Math.max(2, width / height));
  return { width, height: Math.round(width / ratio) };
}

/** Whether the Primary URL applies: only in the first mode (#0174). */
export function redirectUrlApplies(mode: HomeMode): boolean {
  return mode === 'default';
}

/** Sample 7-day numbers for the preview, used until the mode is saved and real data exists. */
export function sampleActivity(now: Date): HomeActivity {
  const counts = [12, 18, 9, 27, 22, 31, 14];
  const days = counts.map((clicks, i) => {
    const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - (6 - i));
    const date = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    return { date, clicks };
  });
  return {
    window: { from: days[0].date, to: days[6].date, tz: 'local', partial_last_day: true },
    total_clicks: counts.reduce((a, b) => a + b, 0),
    timeseries: days,
    top_links: [
      { title: 'A popular guide', short_url: 'https://go.example.com/u/Abc123', clicks: 48 },
      { title: 'Another page people share', short_url: 'https://go.example.com/u/Def456', clicks: 31 },
      { title: 'A third favourite', short_url: 'https://go.example.com/u/Ghi789', clicks: 22 },
    ],
  };
}
