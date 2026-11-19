import { describe, it, expect } from 'vitest';
import {
  isGridPaste,
  parseTsv,
  applyPaste,
  stripHeaderRow,
  loadChannelPreset,
  saveChannelPreset,
  rowsFromPreset,
  keyForPayload,
  newIdempotencyKey,
  PRESET_KEY,
} from './batchGrid';
import { emptyBatchChannelRow, buildBatchCreateRows, type BatchChannelRow } from './campaigns';

function memStorage(init: Record<string, string> = {}): Storage {
  const m = new Map(Object.entries(init));
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => void m.set(k, v),
    removeItem: (k: string) => void m.delete(k),
    clear: () => m.clear(),
    key: () => null,
    get length() {
      return m.size;
    },
  };
}
const row = (o: Partial<BatchChannelRow> = {}): BatchChannelRow => ({ ...emptyBatchChannelRow(), ...o });

describe('isGridPaste', () => {
  it('is false for a plain single value', () => expect(isGridPaste('mastodon')).toBe(false));
  it('is false for one value with a trailing newline', () => expect(isGridPaste('mastodon\n')).toBe(false));
  it('is true with a tab', () => expect(isGridPaste('a\tb')).toBe(true));
  it('is true for several lines', () => expect(isGridPaste('a\nb')).toBe(true));
});

describe('parseTsv', () => {
  it('splits rows and cells and trims', () => {
    expect(parseTsv('mastodon\tsocial\t guide post 84 \r\nbluesky\tsocial\tguide post 84')).toEqual([
      ['mastodon', 'social', 'guide post 84'],
      ['bluesky', 'social', 'guide post 84'],
    ]);
  });
  it('ignores trailing blank lines but keeps interior ones', () => {
    expect(parseTsv('a\tb\n\nc\td\n\n')).toEqual([['a', 'b'], [''], ['c', 'd']]);
  });
  it('keeps empty cells in place', () => {
    expect(parseTsv('a\t\tc')).toEqual([['a', '', 'c']]);
  });
  it('returns no rows for empty text', () => expect(parseTsv('')).toEqual([]));
});

describe('stripHeaderRow', () => {
  it('drops a heading row', () => {
    expect(
      stripHeaderRow([
        ['Source', 'Medium', 'Label'],
        ['a', 'b', 'c'],
      ]),
    ).toEqual([['a', 'b', 'c']]);
  });
  it('keeps data rows and single rows', () => {
    expect(stripHeaderRow([['source']])).toEqual([['source']]);
    expect(
      stripHeaderRow([
        ['mastodon', 'social'],
        ['x', 'y'],
      ]),
    ).toHaveLength(2);
  });
});

describe('applyPaste', () => {
  it('fills from the top-left cell and appends rows', () => {
    const out = applyPaste(
      [row()],
      0,
      0,
      [
        ['mastodon', 'social', 'post 84'],
        ['poster', 'print', 'qr', '18th'],
      ],
      50,
    );
    expect(out).toHaveLength(2);
    expect(out[0]).toMatchObject({ utm_source: 'mastodon', utm_medium: 'social', utm_content: 'post 84' });
    expect(out[1]).toMatchObject({ utm_source: 'poster', utm_content: 'qr', placement: '18th' });
  });
  it('starts at the focused column and drops overflow cells', () => {
    const out = applyPaste([row({ utm_source: 'keep' })], 0, 2, [['label', 'place', 'extra']], 50);
    expect(out[0]).toMatchObject({ utm_source: 'keep', utm_content: 'label', placement: 'place' });
  });
  it('respects the row cap and does not mutate the input', () => {
    const input = [row()];
    const out = applyPaste(input, 0, 0, [['a'], ['b'], ['c']], 2);
    expect(out).toHaveLength(2);
    expect(input[0].utm_source).toBe('');
  });
});

describe('channel presets', () => {
  it('round-trips distinct source+medium pairs', () => {
    const s = memStorage();
    saveChannelPreset(
      [
        row({ utm_source: 'mastodon', utm_medium: 'social', utm_content: 'x' }),
        row({ utm_source: 'mastodon', utm_medium: 'social', utm_content: 'y' }),
        row({ utm_source: 'poster', utm_medium: 'print' }),
      ],
      s,
    );
    const p = loadChannelPreset(s);
    expect(p).toEqual([
      { utm_source: 'mastodon', utm_medium: 'social' },
      { utm_source: 'poster', utm_medium: 'print' },
    ]);
    expect(rowsFromPreset(p)[1]).toMatchObject({ utm_source: 'poster', utm_content: '' });
  });
  it('does not overwrite with an empty set', () => {
    const s = memStorage();
    saveChannelPreset([row({ utm_source: 'a' })], s);
    saveChannelPreset([row()], s);
    expect(loadChannelPreset(s)).toHaveLength(1);
  });
  it('survives corrupt data and throwing storage', () => {
    expect(loadChannelPreset(memStorage({ [PRESET_KEY]: '{nope' }))).toEqual([]);
    expect(loadChannelPreset(memStorage({ [PRESET_KEY]: '{"a":1}' }))).toEqual([]);
    const bad = {
      getItem() {
        throw new Error('blocked');
      },
      setItem() {
        throw new Error('blocked');
      },
    } as unknown as Storage;
    expect(loadChannelPreset(bad)).toEqual([]);
    expect(() => saveChannelPreset([row({ utm_source: 'a' })], bad)).not.toThrow();
  });
});

describe('idempotency key', () => {
  it('is printable ASCII within 255 chars', () => {
    const k = newIdempotencyKey();
    expect(k).toMatch(/^[\x20-\x7e]{1,255}$/);
    expect(newIdempotencyKey()).not.toBe(k);
  });
  it('is reused for an identical payload and renewed for an edited one', () => {
    let n = 0;
    const make = () => `k${++n}`;
    const a = keyForPayload(null, 'body1', make);
    expect(keyForPayload(a, 'body1', make)).toBe(a);
    expect(keyForPayload(a, 'body2', make).key).toBe('k2');
  });
});

describe('shared title in the batch payload', () => {
  it('puts one title on every row and the label in utm_content', () => {
    const rows = [
      row({ utm_source: 'mastodon', utm_medium: 'social', utm_content: 'guide post 84' }),
      row({ utm_source: 'bluesky', utm_medium: 'social', utm_content: 'guide post 84' }),
    ];
    const out = buildBatchCreateRows('https://example.com/g', rows, 'guides', '', ' Measuring current ');
    expect(out.map((r) => r.title)).toEqual(['Measuring current', 'Measuring current']);
    expect(out[0].utm_content).toBe('guide post 84');
    expect(out[0].destination_url).toContain('utm_content=guide');
  });
});
