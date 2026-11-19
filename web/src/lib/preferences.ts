// Per-browser preferences (#0170): chart time zone and theme. Stored in
// localStorage for now (no server column), so they follow the browser, not the
// account. Pure helpers take an injectable Storage so they unit-test without a DOM.

export type Theme = 'system' | 'light' | 'dark';
export const THEMES: { value: Theme; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
];

export const THEME_KEY = 'shortlinks.theme';
export const TZ_KEY = 'shortlinks.chartTimeZone';
/** Stored value meaning "follow the browser's time zone". */
export const TZ_BROWSER = 'browser';

function store(s?: Storage): Storage | null {
  try {
    return s ?? (typeof localStorage === 'undefined' ? null : localStorage);
  } catch {
    return null;
  }
}

export function isTheme(v: unknown): v is Theme {
  return v === 'system' || v === 'light' || v === 'dark';
}

export function getTheme(s?: Storage): Theme {
  try {
    const v = store(s)?.getItem(THEME_KEY);
    return isTheme(v) ? v : 'system';
  } catch {
    return 'system';
  }
}

/** Set `data-theme` on the root element; "system" removes it so the OS preference applies. */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  if (theme === 'system') root.removeAttribute('data-theme');
  else root.setAttribute('data-theme', theme);
}

export function setTheme(theme: Theme, s?: Storage, root?: HTMLElement): void {
  try {
    const st = store(s);
    if (theme === 'system') st?.removeItem(THEME_KEY);
    else st?.setItem(THEME_KEY, theme);
  } catch {
    // Storage unavailable: the choice still applies for this page view.
  }
  applyTheme(theme, root);
}

export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

export function isValidTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat(undefined, { timeZone: tz });
    return true;
  } catch {
    return false;
  }
}

/** The stored choice: TZ_BROWSER or an IANA zone name. */
export function getTimeZoneChoice(s?: Storage): string {
  try {
    const v = store(s)?.getItem(TZ_KEY);
    return v && v !== TZ_BROWSER && isValidTimeZone(v) ? v : TZ_BROWSER;
  } catch {
    return TZ_BROWSER;
  }
}

export function setTimeZoneChoice(choice: string, s?: Storage): void {
  try {
    const st = store(s);
    if (choice === TZ_BROWSER || !isValidTimeZone(choice)) st?.removeItem(TZ_KEY);
    else st?.setItem(TZ_KEY, choice);
  } catch {
    // ignore
  }
}

/** The IANA zone charts should bucket and label days in. */
export function getChartTimeZone(s?: Storage): string {
  const c = getTimeZoneChoice(s);
  return c === TZ_BROWSER ? browserTimeZone() : c;
}

/** Zones offered in the picker: the runtime's full list when available, else a short fallback. */
export function timeZoneOptions(): string[] {
  const fallback = ['UTC', 'America/Los_Angeles', 'America/Denver', 'America/Chicago', 'America/New_York', 'Europe/London', 'Europe/Berlin', 'Asia/Tokyo', 'Australia/Sydney'];
  try {
    const fn = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    const list = fn ? fn('timeZone') : fallback;
    return list.includes('UTC') ? list : ['UTC', ...list];
  } catch {
    return fallback;
  }
}

/** Apply the stored theme at startup, before the app mounts, to avoid a flash. */
export function initTheme(): void {
  applyTheme(getTheme());
}
