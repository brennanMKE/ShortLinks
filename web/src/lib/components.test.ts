// @vitest-environment jsdom
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent, cleanup, within } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import DataTable from './DataTable.svelte';
import RowMenu from './RowMenu.svelte';
import RelativeDate from './RelativeDate.svelte';
import InlineBar from './InlineBar.svelte';
import TwoLineCell from './TwoLineCell.svelte';
import type { Column } from './dataTable';

afterEach(cleanup);

interface R { id: string; name: string; n: number }
const columns: Column<R>[] = [
  { key: 'name', label: 'Name', sortable: true, value: (r) => r.name },
  { key: 'n', label: 'Count', width: '6rem', align: 'right', sortable: true, value: (r) => r.n },
];
const rows: R[] = [
  { id: '1', name: 'Bravo', n: 5 },
  { id: '2', name: 'Alpha', n: 20 },
  { id: '3', name: 'Charlie', n: 1 },
];
const cell = createRawSnippet((row: () => R, col: () => Column<R>) => ({
  render: () => `<span>${col().key === 'name' ? row().name : row().n}</span>`,
}));
const bulk = createRawSnippet((keys: () => string[]) => ({
  render: () => `<button type="button">Bulk ${keys().length}</button>`,
}));

function bodyNames(): string[] {
  return screen.getAllByRole('row').slice(1).map((r) => r.querySelector('td:not(.sel) span')?.textContent ?? '');
}

describe('DataTable', () => {
  it('renders a colgroup with fixed widths and table-layout fixed', () => {
    const { container } = render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T' });
    const cols = container.querySelectorAll('colgroup col');
    expect(cols).toHaveLength(2);
    expect((cols[1] as HTMLElement).style.width).toBe('6rem');
    expect((cols[0] as HTMLElement).style.width).toBe('');
  });

  it('sorts when a header is clicked, toggles direction, and sets aria-sort', async () => {
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T' });
    expect(bodyNames()).toEqual(['Bravo', 'Alpha', 'Charlie']);
    await fireEvent.click(screen.getByRole('button', { name: /Name/ }));
    expect(bodyNames()).toEqual(['Alpha', 'Bravo', 'Charlie']);
    expect(screen.getByRole('columnheader', { name: /Name/ }).getAttribute('aria-sort')).toBe('ascending');
    await fireEvent.click(screen.getByRole('button', { name: /Name/ }));
    expect(bodyNames()).toEqual(['Charlie', 'Bravo', 'Alpha']);
    expect(screen.getByRole('columnheader', { name: /Name/ }).getAttribute('aria-sort')).toBe('descending');
  });

  it('numeric columns start descending and report onsort', async () => {
    const onsort = vi.fn();
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', onsort });
    await fireEvent.click(screen.getByRole('button', { name: /Count/ }));
    expect(bodyNames()).toEqual(['Alpha', 'Bravo', 'Charlie']);
    expect(onsort).toHaveBeenCalledWith({ key: 'n', dir: 'desc' });
  });

  it('serverSort leaves row order to the parent', async () => {
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', serverSort: true });
    await fireEvent.click(screen.getByRole('button', { name: /Name/ }));
    expect(bodyNames()).toEqual(['Bravo', 'Alpha', 'Charlie']);
  });

  it('selection: row checkboxes, select-all, indeterminate, bulk bar', async () => {
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', selectable: true, bulk });
    expect(screen.queryByRole('region', { name: 'Bulk actions' })).toBeNull();
    const boxes = screen.getAllByRole('checkbox', { name: 'Select row' }) as HTMLInputElement[];
    await fireEvent.click(boxes[0]);
    const bar = screen.getByRole('region', { name: 'Bulk actions' });
    expect(within(bar).getByText('1 selected')).toBeTruthy();
    expect(within(bar).getByRole('button', { name: 'Bulk 1' })).toBeTruthy();
    const head = screen.getByRole('checkbox', { name: 'Select all rows' }) as HTMLInputElement;
    expect(head.indeterminate).toBe(true);
    await fireEvent.click(head);
    expect(within(screen.getByRole('region', { name: 'Bulk actions' })).getByText('3 selected')).toBeTruthy();
    expect(head.indeterminate).toBe(false);
    await fireEvent.click(head);
    expect(screen.queryByRole('region', { name: 'Bulk actions' })).toBeNull();
  });

  it('rowLabel names each checkbox', () => {
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', selectable: true, rowLabel: (r: R) => r.name });
    expect(screen.getByRole('checkbox', { name: 'Select Alpha' })).toBeTruthy();
  });

  it('prunes selected keys for rows that disappear', async () => {
    const { rerender } = render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', selectable: true, bulk, selected: new Set(['1', '2']) });
    expect(screen.getByText('2 selected')).toBeTruthy();
    await rerender({ rows: rows.slice(1) });
    expect(await screen.findByText('1 selected')).toBeTruthy();
  });

  it('Clear in the bulk bar deselects', async () => {
    render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', selectable: true, bulk });
    await fireEvent.click(screen.getAllByRole('checkbox', { name: 'Select row' })[1]);
    await fireEvent.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.queryByRole('region', { name: 'Bulk actions' })).toBeNull();
  });

  it('shows skeleton rows while loading and an empty state with no rows', () => {
    const { container, rerender } = render(DataTable<R>, { columns, rows, rowKey: (r: R) => r.id, cell, caption: 'T', loading: true, skeletonRows: 3 });
    expect(container.querySelectorAll('tr.skeleton')).toHaveLength(3);
    expect(container.querySelector('table')!.getAttribute('aria-busy')).toBe('true');
    rerender({ loading: false, rows: [] });
    expect(container.querySelectorAll('tr.skeleton')).toHaveLength(0);
    expect(screen.getByText('Nothing to show.')).toBeTruthy();
  });
});

describe('RowMenu keyboard', () => {
  const items = () => [
    { label: 'One', onselect: vi.fn() },
    { label: 'Two', onselect: vi.fn() },
    { label: 'Gone', danger: true, onselect: vi.fn() },
  ];

  it('opens with Enter, focuses the first item, arrows move and wrap', async () => {
    render(RowMenu, { items: items(), label: 'Actions for X' });
    const trigger = screen.getByRole('button', { name: 'Actions for X' });
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: 'Enter' });
    const menu = await screen.findByRole('menu');
    const mi = within(menu).getAllByRole('menuitem');
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(document.activeElement).toBe(mi[0]);
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(mi[1]);
    await fireEvent.keyDown(menu, { key: 'End' });
    expect(document.activeElement).toBe(mi[2]);
    await fireEvent.keyDown(menu, { key: 'ArrowDown' });
    expect(document.activeElement).toBe(mi[0]);
    await fireEvent.keyDown(menu, { key: 'ArrowUp' });
    expect(document.activeElement).toBe(mi[2]);
    await fireEvent.keyDown(menu, { key: 'Home' });
    expect(document.activeElement).toBe(mi[0]);
  });

  it('ArrowUp on the trigger opens on the last item', async () => {
    render(RowMenu, { items: items() });
    const trigger = screen.getByRole('button', { name: 'Row actions' });
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: 'ArrowUp' });
    const mi = within(await screen.findByRole('menu')).getAllByRole('menuitem');
    expect(document.activeElement).toBe(mi[2]);
  });

  it('Escape closes and returns focus to the trigger', async () => {
    render(RowMenu, { items: items() });
    const trigger = screen.getByRole('button', { name: 'Row actions' });
    trigger.focus();
    await fireEvent.keyDown(trigger, { key: 'ArrowDown' });
    const menu = await screen.findByRole('menu');
    await fireEvent.keyDown(menu, { key: 'Escape' });
    expect(screen.queryByRole('menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('closes on scroll', async () => {
    render(RowMenu, { items: items() });
    await fireEvent.click(screen.getByRole('button', { name: 'Row actions' }));
    await screen.findByRole('menu');
    await fireEvent.scroll(window);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('choosing an item runs it and closes the menu', async () => {
    const its = items();
    render(RowMenu, { items: its });
    await fireEvent.click(screen.getByRole('button', { name: 'Row actions' }));
    await fireEvent.click(await screen.findByRole('menuitem', { name: 'Two' }));
    expect(its[1].onselect).toHaveBeenCalledOnce();
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('the hover copy icon copies and reports', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const oncopy = vi.fn();
    render(RowMenu, { items: items(), copyText: 'https://go.example/u/abc', oncopy });
    await fireEvent.click(screen.getByRole('button', { name: 'Copy short URL' }));
    expect(writeText).toHaveBeenCalledWith('https://go.example/u/abc');
    expect(oncopy).toHaveBeenCalledWith('https://go.example/u/abc');
  });
});

describe('RelativeDate / InlineBar / TwoLineCell', () => {
  const now = new Date('2026-11-18T12:00:00Z');
  it('RelativeDate shows 3d ago under 7 days, absolute after, with the timestamp in title', () => {
    const { container } = render(RelativeDate, { date: '2026-11-15T12:00:00Z', now });
    const t = container.querySelector('time')!;
    expect(t.textContent).toBe('3d ago');
    expect(t.getAttribute('title')).toMatch(/Nov 15, 2026/);
    cleanup();
    const r2 = render(RelativeDate, { date: '2026-11-01T12:00:00Z', now });
    expect(r2.container.querySelector('time')!.textContent).toBe('Nov 1');
  });

  it('InlineBar scales the bar to the column max', () => {
    const { container } = render(InlineBar, { value: 50, max: 200 });
    expect((container.querySelector('.bar') as HTMLElement).style.width).toBe('25%');
    expect(container.querySelector('.num')!.textContent).toBe('50');
  });

  it('TwoLineCell puts the full text in title attributes', () => {
    const long = 'A very long title '.repeat(10);
    const { container } = render(TwoLineCell, { title: long, meta: '/u/abc · /guides/x' });
    expect(container.querySelector('.line1')!.getAttribute('title')).toBe(long);
    expect(container.querySelector('.line2')!.textContent).toBe('/u/abc · /guides/x');
  });
});
