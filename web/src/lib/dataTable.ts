// Pure helpers behind DataTable (#0155): sorting, selection, bar scaling.

export type SortDir = 'asc' | 'desc';
export interface SortState {
  key: string;
  dir: SortDir;
}

export interface Column<T> {
  key: string;
  label: string;
  /** CSS width for the <colgroup>. Omit on the ONE flexible column. */
  width?: string;
  align?: 'left' | 'right';
  /** Header click toggles sorting; needs `value` for local sorting. */
  sortable?: boolean;
  /** Sort value. Required for local sorting. */
  value?: (row: T) => string | number | null | undefined;
  /** Phone cards (<900px): how this column renders. Default 'hide'. */
  mobile?: 'title' | 'meta' | 'metric' | 'actions' | 'hide';
  /** Visually hide the header label (actions column). */
  hideLabel?: boolean;
}

/** Header click: a new key starts at `firstDir`; the same key toggles. */
export function nextSort(current: SortState | null, key: string, firstDir: SortDir = 'asc'): SortState {
  if (current && current.key === key) {
    return { key, dir: current.dir === 'asc' ? 'desc' : 'asc' };
  }
  return { key, dir: firstDir };
}

function isNil(v: unknown): v is null | undefined {
  return v === null || v === undefined;
}

function cmp(a: string | number, b: string | number): number {
  if (typeof a === 'number' && typeof b === 'number') return a - b;
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}

/** Stable sort of a copy; missing values sort last in both directions. */
export function sortRows<T>(rows: T[], columns: Column<T>[], sort: SortState | null): T[] {
  if (!sort) return rows;
  const col = columns.find((c) => c.key === sort.key);
  if (!col || !col.value) return rows;
  const val = col.value;
  const sign = sort.dir === 'asc' ? 1 : -1;
  return rows
    .map((r, i) => ({ r, i, v: val(r) }))
    .sort((x, y) => {
      if (isNil(x.v) || isNil(y.v)) {
        if (isNil(x.v) && isNil(y.v)) return x.i - y.i;
        return isNil(x.v) ? 1 : -1;
      }
      return sign * cmp(x.v, y.v) || x.i - y.i;
    })
    .map((x) => x.r);
}

/** Toggle one key in an immutable selection set. */
export function toggleKey(selected: ReadonlySet<string>, key: string): Set<string> {
  const next = new Set(selected);
  if (next.has(key)) next.delete(key);
  else next.add(key);
  return next;
}

export type AllState = 'none' | 'some' | 'all';
export function allState(selected: ReadonlySet<string>, keys: string[]): AllState {
  if (keys.length === 0) return 'none';
  const n = keys.filter((k) => selected.has(k)).length;
  return n === 0 ? 'none' : n === keys.length ? 'all' : 'some';
}

/** Select-all header: anything but 'all' selects every row; 'all' clears. */
export function toggleAll(selected: ReadonlySet<string>, keys: string[]): Set<string> {
  return allState(selected, keys) === 'all' ? new Set() : new Set(keys);
}

/** Bar width percentage (0–100) relative to the column maximum. */
export function barPercent(value: number, max: number): number {
  if (!(max > 0) || !(value > 0)) return 0;
  return Math.min(100, (value / max) * 100);
}

/** Maximum of a numeric column (0 when empty). */
export function columnMax<T>(rows: T[], get: (r: T) => number): number {
  let m = 0;
  for (const r of rows) m = Math.max(m, get(r) || 0);
  return m;
}
