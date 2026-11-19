import { describe, it, expect } from 'vitest';
import {
  allState,
  barPercent,
  columnMax,
  nextSort,
  sortRows,
  toggleAll,
  toggleKey,
  type Column,
} from './dataTable';
import { nextMenuIndex } from './rowMenu';

interface R { id: string; name: string | null; n: number }
const cols: Column<R>[] = [
  { key: 'name', label: 'Name', sortable: true, value: (r) => r.name },
  { key: 'n', label: 'N', sortable: true, value: (r) => r.n },
  { key: 'plain', label: 'Plain' },
];
const rows: R[] = [
  { id: 'a', name: 'beta', n: 5 },
  { id: 'b', name: 'Alpha', n: 20 },
  { id: 'c', name: null, n: 1 },
  { id: 'd', name: 'item10', n: 5 },
  { id: 'e', name: 'item2', n: 5 },
];
const ids = (rs: R[]) => rs.map((r) => r.id).join('');

describe('nextSort', () => {
  it('starts a new column in firstDir and toggles the same column', () => {
    expect(nextSort(null, 'n')).toEqual({ key: 'n', dir: 'asc' });
    expect(nextSort(null, 'n', 'desc')).toEqual({ key: 'n', dir: 'desc' });
    expect(nextSort({ key: 'n', dir: 'asc' }, 'n')).toEqual({ key: 'n', dir: 'desc' });
    expect(nextSort({ key: 'n', dir: 'desc' }, 'n')).toEqual({ key: 'n', dir: 'asc' });
    expect(nextSort({ key: 'n', dir: 'desc' }, 'name')).toEqual({ key: 'name', dir: 'asc' });
  });
});

describe('sortRows', () => {
  it('sorts numbers both ways and is stable on ties', () => {
    expect(ids(sortRows(rows, cols, { key: 'n', dir: 'asc' }))).toBe('cadeb');
    expect(ids(sortRows(rows, cols, { key: 'n', dir: 'desc' }))).toBe('badec');
  });
  it('sorts strings case-insensitively with natural numbers, nulls last both ways', () => {
    expect(ids(sortRows(rows, cols, { key: 'name', dir: 'asc' }))).toBe('baedc');
    expect(ids(sortRows(rows, cols, { key: 'name', dir: 'desc' }))).toBe('deabc');
  });
  it('does not mutate and ignores unsortable / unknown / null sort', () => {
    const copy = [...rows];
    sortRows(rows, cols, { key: 'n', dir: 'asc' });
    expect(rows).toEqual(copy);
    expect(sortRows(rows, cols, { key: 'plain', dir: 'asc' })).toBe(rows);
    expect(sortRows(rows, cols, { key: 'zzz', dir: 'asc' })).toBe(rows);
    expect(sortRows(rows, cols, null)).toBe(rows);
  });
});

describe('selection', () => {
  const keys = ['a', 'b', 'c'];
  it('toggleKey is immutable add/remove', () => {
    const s = new Set<string>();
    const s1 = toggleKey(s, 'a');
    expect([...s1]).toEqual(['a']);
    expect(s.size).toBe(0);
    expect(toggleKey(s1, 'a').size).toBe(0);
  });
  it('allState reports none / some / all', () => {
    expect(allState(new Set(), keys)).toBe('none');
    expect(allState(new Set(['a']), keys)).toBe('some');
    expect(allState(new Set(keys), keys)).toBe('all');
    expect(allState(new Set(['a']), [])).toBe('none');
  });
  it('toggleAll selects everything unless already all, then clears', () => {
    expect([...toggleAll(new Set(), keys)]).toEqual(keys);
    expect([...toggleAll(new Set(['a']), keys)]).toEqual(keys);
    expect(toggleAll(new Set(keys), keys).size).toBe(0);
  });
});

describe('bars', () => {
  it('scales to the column maximum', () => {
    expect(columnMax(rows, (r) => r.n)).toBe(20);
    expect(columnMax([], (r: R) => r.n)).toBe(0);
    expect(barPercent(10, 20)).toBe(50);
    expect(barPercent(20, 20)).toBe(100);
    expect(barPercent(0, 20)).toBe(0);
    expect(barPercent(5, 0)).toBe(0);
  });
});

describe('nextMenuIndex', () => {
  it('wraps with arrows and jumps with Home/End', () => {
    expect(nextMenuIndex('ArrowDown', 0, 3)).toBe(1);
    expect(nextMenuIndex('ArrowDown', 2, 3)).toBe(0);
    expect(nextMenuIndex('ArrowUp', 0, 3)).toBe(2);
    expect(nextMenuIndex('ArrowUp', -1, 3)).toBe(2);
    expect(nextMenuIndex('Home', 2, 3)).toBe(0);
    expect(nextMenuIndex('End', 0, 3)).toBe(2);
    expect(nextMenuIndex('x', 0, 3)).toBeNull();
    expect(nextMenuIndex('ArrowDown', 0, 0)).toBeNull();
  });
});
