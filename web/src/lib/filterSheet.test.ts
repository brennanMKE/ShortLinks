// @vitest-environment jsdom
// #0173: below 900px the Overview FilterBar folds into one "Filters (N)" button
// and a bottom sheet; at desktop width it stays a row of dropdowns.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent, cleanup, within } from '@testing-library/svelte';
import FilterBar from './FilterBar.svelte';
import { emptyFilters, type UtmFilters } from './viewQuery';

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function stubMatchMedia(matches: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({
    matches,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

const facets = {
  source: [{ value: 'mastodon', clicks: 9 }, { value: 'bluesky', clicks: 4 }],
  medium: [{ value: 'social', clicks: 13 }],
  utm_campaign: [],
  content: [],
  placement: [],
  term: [],
};

function props(filters: Partial<UtmFilters> = {}) {
  return {
    facets,
    filters: { ...emptyFilters(), ...filters },
    campaignSlugs: [],
    onchange: vi.fn(),
    onclear: vi.fn(),
  };
}

describe('FilterBar on a phone', () => {
  it('shows one Filters button with the active count and no dropdown triggers', () => {
    stubMatchMedia(true);
    render(FilterBar, props({ source: ['mastodon', 'bluesky'], medium: ['social'] }));
    expect(screen.getByRole('button', { name: 'Filters (3)' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^Source/ })).toBeNull();
  });

  it('opens a sheet with inline sections and reports changes', async () => {
    stubMatchMedia(true);
    const p = props();
    render(FilterBar, p);
    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    const sheet = screen.getByRole('dialog', { hidden: true });
    expect(within(sheet).getByText('Source')).toBeTruthy();
    await fireEvent.click(within(sheet).getByLabelText(/mastodon/));
    expect(p.onchange).toHaveBeenCalledWith('source', ['mastodon']);
    await fireEvent.click(within(sheet).getByRole('button', { name: 'Done' }));
    expect(screen.queryByRole('dialog', { hidden: true })).toBeNull();
  });

  it('locks page scroll while the sheet is open and restores it after', async () => {
    stubMatchMedia(true);
    render(FilterBar, props());
    expect(document.documentElement.style.overflow).toBe('');
    await fireEvent.click(screen.getByRole('button', { name: 'Filters' }));
    expect(document.documentElement.style.overflow).toBe('hidden');
    await fireEvent.click(within(screen.getByRole('dialog', { hidden: true })).getByRole('button', { name: 'Done' }));
    expect(document.documentElement.style.overflow).toBe('');
  });

  it('Clear all in the sheet clears every filter', async () => {
    stubMatchMedia(true);
    const p = props({ source: ['mastodon'] });
    render(FilterBar, p);
    await fireEvent.click(screen.getByRole('button', { name: 'Filters (1)' }));
    await fireEvent.click(within(screen.getByRole('dialog', { hidden: true })).getByRole('button', { name: 'Clear all' }));
    expect(p.onclear).toHaveBeenCalled();
  });
});

describe('FilterBar on desktop', () => {
  it('keeps the dropdown row and has no Filters button', () => {
    stubMatchMedia(false);
    render(FilterBar, props());
    expect(screen.getByRole('button', { name: /^Source/ })).toBeTruthy();
    expect(screen.queryByRole('button', { name: /^Filters/ })).toBeNull();
  });
});
