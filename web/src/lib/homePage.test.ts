// @vitest-environment jsdom
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import PublicHome from './PublicHome.svelte';
import AdminHome from './AdminHome.svelte';

vi.mock('./api', () => ({
  ApiError: class extends Error {},
  getPublicHome: vi.fn(async () => ({ redirect_url: '', registrations_enabled: false, hero: null })),
  updateSetting: vi.fn(),
  uploadHero: vi.fn(),
  deleteHero: vi.fn(),
}));
import {
  aboutParagraphs,
  homeHeading,
  homeMode,
  linkifyParagraph,
  shouldShowHome,
  weekClicksLabel,
  type HomeInfo,
} from './home';
import {
  changedHomeSettings,
  homeSettingsFrom,
  previewHeroSize,
  redirectUrlApplies,
  sampleActivity,
} from './homeAdmin';

afterEach(cleanup);

// ClicksChart measures its box; jsdom has no ResizeObserver.
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const base: HomeInfo = { redirect_url: '', registrations_enabled: false };

describe('linkifyParagraph', () => {
  it('links a bare https URL and keeps the text around it', () => {
    expect(linkifyParagraph('See https://example.com/a for more')).toEqual([
      { kind: 'text', text: 'See ' },
      { kind: 'link', text: 'https://example.com/a', href: 'https://example.com/a' },
      { kind: 'text', text: ' for more' },
    ]);
  });
  it('never links javascript:, data:, http: or bare hosts', () => {
    for (const t of [
      'javascript:alert(1)',
      'JAVASCRIPT:alert(1) https_x',
      'data:text/html,<b>x</b>',
      'http://example.com',
      'ftp://example.com',
      'www.example.com',
      'https://',
    ]) {
      expect(linkifyParagraph(t).every((s) => s.kind === 'text')).toBe(true);
    }
  });
  it('links a URL that merely contains "javascript:" only as an https link', () => {
    const segs = linkifyParagraph('https://example.com/?u=javascript:alert(1)');
    const link = segs.find((s) => s.kind === 'link');
    expect(link && link.kind === 'link' && link.href.startsWith('https://')).toBe(true);
  });
  it('leaves trailing sentence punctuation out of the link', () => {
    const segs = linkifyParagraph('Go to https://example.com/x, or https://example.org/y.');
    const links = segs.filter((s) => s.kind === 'link');
    expect(links.map((l) => l.text)).toEqual(['https://example.com/x', 'https://example.org/y']);
    expect(segs[segs.length - 1]).toEqual({ kind: 'text', text: '.' });
  });
  it('drops an unbalanced closing bracket but keeps a balanced one', () => {
    const a = linkifyParagraph('(see https://example.com/x)');
    expect(a.find((s) => s.kind === 'link')?.text).toBe('https://example.com/x');
    const b = linkifyParagraph('https://en.wikipedia.org/wiki/Foo_(bar)');
    expect(b.find((s) => s.kind === 'link')?.text).toBe('https://en.wikipedia.org/wiki/Foo_(bar)');
  });
  it('never links a URL with credentials', () => {
    expect(linkifyParagraph('https://user:pass@example.com/x').every((s) => s.kind === 'text')).toBe(true);
    expect(linkifyParagraph('https://user@example.com/').every((s) => s.kind === 'text')).toBe(true);
  });
  it('stops at quotes and angle brackets', () => {
    const segs = linkifyParagraph('<a href="https://example.com/x">x</a>');
    expect(segs.find((s) => s.kind === 'link')?.text).toBe('https://example.com/x');
  });
  it('keeps HTML in the text as text (escaping happens when it is rendered)', () => {
    expect(linkifyParagraph('<script>alert(1)</script>')).toEqual([
      { kind: 'text', text: '<script>alert(1)</script>' },
    ]);
  });
});

describe('aboutParagraphs', () => {
  it('splits on blank lines and drops empty paragraphs', () => {
    expect(aboutParagraphs('One\n\n\nTwo\r\n\r\nThree\n\n')).toHaveLength(3);
    expect(aboutParagraphs('  \n\n ')).toEqual([]);
  });
});

describe('mode switching', () => {
  it('reads an unknown or missing mode as default', () => {
    expect(homeMode(null)).toBe('default');
    expect(homeMode(base)).toBe('default');
    expect(homeMode({ ...base, mode: 'weird' as never })).toBe('default');
  });
  it('shows the content page in about and activity mode, even with registration open', () => {
    for (const mode of ['about', 'activity'] as const) {
      expect(shouldShowHome(false, '/', { ...base, mode, registrations_enabled: true })).toBe(true);
    }
  });
  it('keeps the #0125 rule in default mode', () => {
    expect(shouldShowHome(false, '/', { ...base, mode: 'default' })).toBe(false);
    expect(
      shouldShowHome(false, '/', { ...base, mode: 'default', redirect_url: 'https://x.test/' }),
    ).toBe(true);
  });
  it('never shows it signed in or off the root', () => {
    expect(shouldShowHome(true, '/', { ...base, mode: 'about' })).toBe(false);
    expect(shouldShowHome(false, '/login', { ...base, mode: 'about' })).toBe(false);
  });
  it('the redirect URL applies only in the first mode', () => {
    expect(redirectUrlApplies('default')).toBe(true);
    expect(redirectUrlApplies('about')).toBe(false);
    expect(redirectUrlApplies('activity')).toBe(false);
  });
});

describe('small helpers', () => {
  it('falls back to the host for the title', () => {
    expect(homeHeading('  ', 'go.example.com')).toBe('go.example.com');
    expect(homeHeading(undefined, 'go.example.com')).toBe('go.example.com');
    expect(homeHeading('Hello', 'go.example.com')).toBe('Hello');
  });
  it('words the week total', () => {
    expect(weekClicksLabel(1)).toBe('1 click this week');
    expect(weekClicksLabel(1234)).toBe('1,234 clicks this week');
  });
});

describe('admin draft', () => {
  it('reads settings, with missing rows unset', () => {
    expect(homeSettingsFrom([])).toEqual({ mode: 'default', title: '', about: '' });
    expect(
      homeSettingsFrom([
        { key: 'home_mode', value: 'activity' },
        { key: 'home_title', value: 'T' },
        { key: 'home_about', value: 'A' },
      ]),
    ).toEqual({ mode: 'activity', title: 'T', about: 'A' });
  });
  it('sends only what changed, trimmed', () => {
    const saved = { mode: 'default' as const, title: 'T', about: 'A' };
    expect(changedHomeSettings(saved, { ...saved })).toEqual([]);
    expect(changedHomeSettings(saved, { ...saved, title: ' T ', about: 'A\r\n' })).toEqual([]);
    expect(changedHomeSettings(saved, { mode: 'about', title: 'New', about: 'A' })).toEqual([
      ['home_mode', 'about'],
      ['home_title', 'New'],
    ]);
  });
  it('clamps the preview shape of a rejected image to 2:1 to 4:1', () => {
    expect(previewHeroSize(600, 600)).toEqual({ width: 600, height: 300 });
    expect(previewHeroSize(4000, 400)).toEqual({ width: 4000, height: 1000 });
    expect(previewHeroSize(2400, 800)).toEqual({ width: 2400, height: 800 });
    expect(previewHeroSize(0, 0)).toEqual({ width: 2400, height: 800 });
  });
  it('builds seven sample days ending today', () => {
    const a = sampleActivity(new Date(2026, 10, 19));
    expect(a.timeseries).toHaveLength(7);
    expect(a.window.to).toBe('2026-11-19');
    expect(a.window.from).toBe('2026-11-13');
    expect(a.top_links).toHaveLength(3);
  });
});

describe('PublicHome', () => {
  const activity = {
    window: { from: '2026-11-13', to: '2026-11-19', tz: 'UTC', partial_last_day: true },
    total_clicks: 7,
    timeseries: ['13', '14', '15', '16', '17', '18', '19'].map((d) => ({
      date: `2026-11-${d}`,
      clicks: 1,
    })),
    top_links: [{ title: 'A <b>guide</b>', short_url: 'https://go.test/u/abc', clicks: 4 }],
  };

  it('shows Register only when registrations are enabled', () => {
    render(PublicHome, { info: { ...base, mode: 'about', registrations_enabled: false }, host: 'h' });
    expect(screen.getByRole('link', { name: 'Sign in' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Register' })).toBeNull();
    cleanup();
    render(PublicHome, { info: { ...base, mode: 'about', registrations_enabled: true }, host: 'h' });
    expect(screen.getByRole('link', { name: 'Register' })).toBeTruthy();
  });

  it('prints title and about as text, never as markup', () => {
    const { container } = render(PublicHome, {
      info: {
        ...base,
        mode: 'about',
        title: '<img src=x onerror=alert(1)>',
        about: '<script>alert(1)</script>\n\nhttps://example.com/ok. javascript:alert(1)',
      },
      host: 'h',
    });
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('h1 img')).toBeNull();
    expect(container.querySelector('h1')?.textContent).toBe('<img src=x onerror=alert(1)>');
    const anchors = [...container.querySelectorAll('.about a')];
    expect(anchors.map((a) => a.getAttribute('href'))).toEqual(['https://example.com/ok']);
  });

  it('falls back to the host when there is no title', () => {
    render(PublicHome, { info: { ...base, mode: 'about' }, host: 'go.example.com' });
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('go.example.com');
  });

  it('renders the hero with its alt text and reserved aspect ratio', () => {
    const { container } = render(PublicHome, {
      info: {
        ...base,
        mode: 'about',
        hero: { url: '/public/hero?v=1', alt: 'A wide view', width: 2400, height: 800 },
      },
      host: 'h',
    });
    const img = screen.getByAltText('A wide view') as HTMLImageElement;
    expect(img.getAttribute('width')).toBe('2400');
    expect(img.getAttribute('height')).toBe('800');
    expect(
      (container.querySelector('figure') as HTMLElement).style.getPropertyValue('--hero-ratio'),
    ).toBe('2400 / 800');
  });

  it('activity mode shows the total, the chart and the top links as text', () => {
    const { container } = render(PublicHome, { info: { ...base, mode: 'activity', activity }, host: 'h' });
    expect(screen.getByText('7 clicks this week')).toBeTruthy();
    expect(screen.getByText('Popular this week')).toBeTruthy();
    expect(container.querySelector('.card b')).toBeNull();
    const card = container.querySelector('a.card') as HTMLAnchorElement;
    expect(card.getAttribute('href')).toBe('https://go.test/u/abc');
    expect(card.textContent).toContain('A <b>guide</b>');
    expect(container.querySelector('svg')).toBeTruthy();
  });

  it('activity mode with no clicks shows the empty state', () => {
    render(PublicHome, {
      info: { ...base, mode: 'activity', activity: { ...activity, total_clicks: 0, top_links: [] } },
      host: 'h',
    });
    expect(screen.getByText('No clicks yet this week.')).toBeTruthy();
  });

  it('preview renders a div and an h2, not main and h1', () => {
    const { container } = render(PublicHome, { info: { ...base, mode: 'about', title: 'T' }, host: 'h', preview: true });
    expect(container.querySelector('main')).toBeNull();
    expect(container.querySelector('h1')).toBeNull();
    expect(container.querySelector('h2')?.textContent).toBe('T');
  });

  it('a failed activity load says so instead of "No clicks yet"', () => {
    render(PublicHome, { info: { ...base, mode: 'activity' }, host: 'h' });
    expect(screen.getByText("Activity isn't available right now.")).toBeTruthy();
    expect(screen.queryByText('No clicks yet this week.')).toBeNull();
  });

  it('about mode ignores any activity it is given', () => {
    render(PublicHome, { info: { ...base, mode: 'about', activity }, host: 'h' });
    expect(screen.queryByText('Popular this week')).toBeNull();
  });
});

describe('AdminHome draft', () => {
  const settings = [
    { key: 'home_mode', value: 'about' },
    { key: 'home_title', value: 'Saved title' },
    { key: 'home_about', value: '' },
    { key: 'registrations_enabled', value: 'false' },
  ];
  const props = { onSettings: () => {}, onAuthError: () => false };

  it('keeps unsaved edits when unrelated settings change', async () => {
    const { rerender } = render(AdminHome, { settings, ...props });
    const title = (await screen.findByLabelText('Title')) as HTMLInputElement;
    await waitFor(() => expect(title.value).toBe('Saved title'));
    await fireEvent.input(title, { target: { value: 'Unsaved edit' } });
    await rerender({
      settings: settings.map((s) => (s.key === 'registrations_enabled' ? { ...s, value: 'true' } : s)),
      ...props,
    });
    expect((screen.getByLabelText('Title') as HTMLInputElement).value).toBe('Unsaved edit');
  });

  it('disables alt text until an image is chosen', async () => {
    render(AdminHome, { settings, ...props });
    expect(((await screen.findByLabelText('Alt text')) as HTMLInputElement).disabled).toBe(true);
  });
});
