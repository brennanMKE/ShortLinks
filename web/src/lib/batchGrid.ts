// Batch-create grid helpers (#0168): TSV paste parsing, last-used channel
// presets (per-browser localStorage) and the Idempotency-Key for a submit.
// Pure, with an injectable Storage so they unit-test without a DOM.

import { emptyBatchChannelRow, type BatchChannelRow } from './campaigns';

/** The grid's editable columns, in tab order. `title` is shared, not a column. */
export const GRID_COLUMNS = ['utm_source', 'utm_medium', 'utm_content', 'placement'] as const;
export type GridColumn = (typeof GRID_COLUMNS)[number];

/**
 * Whether pasted text should be treated as a block of cells rather than typed
 * into one input: it contains a tab or more than one non-empty line.
 */
export function isGridPaste(text: string): boolean {
  if (text.includes('\t')) return true;
  return text.split(/\r?\n/).filter((l) => l.trim() !== '').length > 1;
}

/**
 * Parses tab-separated text (a spreadsheet selection) into a matrix of cells.
 * Rows split on CR/LF; trailing blank lines (what spreadsheets append) do not
 * make empty last rows; interior blank lines are kept as blank rows so the
 * pasted block keeps its shape. Cells are trimmed.
 */
export function parseTsv(text: string): string[][] {
  const lines = text.replace(/\r\n?/g, '\n').split('\n');
  while (lines.length > 0 && lines[lines.length - 1].trim() === '') lines.pop();
  return lines.map((l) => l.split('\t').map((c) => c.trim()));
}

/**
 * Applies a pasted block to the rows with its top-left cell at (rowIndex,
 * colIndex). Cells beyond the last column are dropped; rows beyond the end of
 * the list are appended, up to `max`. Returns new rows and does not touch the
 * input. Cells outside the pasted block are left alone.
 */
export function applyPaste(
  rows: BatchChannelRow[],
  rowIndex: number,
  colIndex: number,
  matrix: string[][],
  max: number,
): BatchChannelRow[] {
  const out = rows.map((r) => ({ ...r }));
  matrix.forEach((cells, dr) => {
    const target = rowIndex + dr;
    if (target >= max) return;
    while (out.length <= target) out.push(emptyBatchChannelRow());
    cells.forEach((cell, dc) => {
      const col = GRID_COLUMNS[colIndex + dc];
      if (col) out[target][col] = cell;
    });
  });
  return out;
}

/**
 * Drops a leading header row ("source<TAB>medium<TAB>label...") from a pasted
 * block, so copying a sheet together with its headings just works. Only when
 * every non-empty cell of the first row is a known heading and more rows follow.
 */
export function stripHeaderRow(matrix: string[][]): string[][] {
  const first = matrix[0];
  if (!first || matrix.length < 2) return matrix;
  const norm = first.map((c) => c.toLowerCase());
  const known = ['source', 'medium', 'content', 'label', 'placement', 'utm_source', 'utm_medium', 'utm_content'];
  const isHeader = norm.some((c) => c !== '') && norm.every((c) => c === '' || known.includes(c));
  return isHeader ? matrix.slice(1) : matrix;
}

// ── Presets ──────────────────────────────────────────────────────────────

export const PRESET_KEY = 'shortlinks.batchChannels';

/** A remembered channel: source and medium only (label and placement are per-content). */
export interface ChannelPreset {
  utm_source: string;
  utm_medium: string;
}

function store(s?: Storage): Storage | null {
  try {
    return s ?? (typeof localStorage === 'undefined' ? null : localStorage);
  } catch {
    return null;
  }
}

/** The channels of the last batch this browser created; [] when none or unreadable. */
export function loadChannelPreset(s?: Storage): ChannelPreset[] {
  try {
    const raw = store(s)?.getItem(PRESET_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((p): p is Record<string, unknown> => typeof p === 'object' && p !== null)
      .map((p) => ({
        utm_source: typeof p.utm_source === 'string' ? p.utm_source : '',
        utm_medium: typeof p.utm_medium === 'string' ? p.utm_medium : '',
      }))
      .filter((p) => p.utm_source !== '' || p.utm_medium !== '');
  } catch {
    return [];
  }
}

/** Remembers the distinct source+medium pairs of the submitted rows. Never throws. */
export function saveChannelPreset(rows: BatchChannelRow[], s?: Storage): void {
  const seen = new Set<string>();
  const preset: ChannelPreset[] = [];
  for (const r of rows) {
    const p = { utm_source: r.utm_source.trim(), utm_medium: r.utm_medium.trim() };
    if (p.utm_source === '' && p.utm_medium === '') continue;
    const k = JSON.stringify([p.utm_source, p.utm_medium]);
    if (seen.has(k)) continue;
    seen.add(k);
    preset.push(p);
  }
  try {
    if (preset.length === 0) return;
    store(s)?.setItem(PRESET_KEY, JSON.stringify(preset));
  } catch {
    /* storage blocked or full: the preset is a convenience */
  }
}

/** Rows for a preset: one per channel, label and placement blank. */
export function rowsFromPreset(preset: ChannelPreset[]): BatchChannelRow[] {
  return preset.map((p) => ({ ...emptyBatchChannelRow(), ...p }));
}

// ── Idempotency ──────────────────────────────────────────────────────────

/** A fresh Idempotency-Key (#0147: 1-255 printable ASCII). */
export function newIdempotencyKey(): string {
  const c = typeof crypto !== 'undefined' ? crypto : undefined;
  if (c && typeof c.randomUUID === 'function') return `batch-${c.randomUUID()}`;
  return `batch-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

/**
 * Keeps one key per distinct payload: a retry of the SAME payload (a double
 * submit, or a resubmit after a timeout) reuses the key so the server replays
 * the first response; an edited payload gets a new key, since the server
 * answers 422 for the same key with a different body.
 */
export function keyForPayload(
  prev: { key: string; body: string } | null,
  body: string,
  make: () => string = newIdempotencyKey,
): { key: string; body: string } {
  return prev && prev.body === body ? prev : { key: make(), body };
}
