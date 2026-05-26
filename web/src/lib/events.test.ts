// Unit tests for the SSE client (#0034): the pure dedupe-prepend store policy
// and the subscribe/parse/cleanup wiring. There is no real EventSource or DOM
// here — the subscribe path is driven through a hand-rolled fake EventSource so
// we can assert frame parsing, defensive handling of bad frames, the callback
// firing, and that cleanup closes the connection. The pure helper needs nothing.

import { describe, it, expect, vi } from 'vitest';
import type { Link } from './types';
import {
  prependUniqueByKey,
  subscribeLinks,
  EVENTS_URL,
  LINK_CREATED_EVENT,
  type EventSourceLike,
} from './events';

function link(overrides: Partial<Link> = {}): Link {
  return {
    id: 1,
    key: '8d0d93',
    destination_url: 'https://www.example.com/page',
    title: '',
    active: true,
    denied_reason: 0,
    created_at: '2026-05-25T12:00:00Z',
    expires_at: null,
    click_count: 0,
    ...overrides,
  };
}

describe('prependUniqueByKey', () => {
  it('prepends a brand-new link to the front', () => {
    const a = link({ key: 'aaa' });
    const b = link({ key: 'bbb', id: 2 });
    const result = prependUniqueByKey([a], b);
    expect(result.map((l) => l.key)).toEqual(['bbb', 'aaa']);
  });

  it('does NOT double-insert a duplicate key (optimistic-create then SSE)', () => {
    const optimistic = link({ key: 'dup', title: 'optimistic' });
    const fromSse = link({ key: 'dup', title: 'from-sse', click_count: 5 });
    const result = prependUniqueByKey([optimistic], fromSse);
    expect(result).toHaveLength(1);
    expect(result[0].key).toBe('dup');
  });

  it('replaces the duplicate in place with the incoming link, at the front', () => {
    const x = link({ key: 'x' });
    const dupOld = link({ key: 'dup', title: 'old' });
    const y = link({ key: 'y', id: 9 });
    const dupNew = link({ key: 'dup', title: 'new' });
    const result = prependUniqueByKey([x, dupOld, y], dupNew);
    // dup moves to front, carries the NEW data, and is not duplicated.
    expect(result.map((l) => l.key)).toEqual(['dup', 'x', 'y']);
    expect(result[0].title).toBe('new');
    expect(result.filter((l) => l.key === 'dup')).toHaveLength(1);
  });

  it('does not mutate the input list', () => {
    const input = [link({ key: 'a' })];
    const snapshot = [...input];
    prependUniqueByKey(input, link({ key: 'b' }));
    expect(input).toEqual(snapshot);
  });
});

/** A minimal fake EventSource that records listeners and lets tests emit frames. */
class FakeEventSource implements EventSourceLike {
  url: string;
  closed = false;
  private listeners = new Map<string, ((event: { data: string }) => void)[]>();

  constructor(url: string) {
    this.url = url;
  }

  addEventListener(type: string, listener: (event: { data: string }) => void): void {
    const arr = this.listeners.get(type) ?? [];
    arr.push(listener);
    this.listeners.set(type, arr);
  }

  /** Test helper: deliver a frame to every listener registered for `type`. */
  emit(type: string, data: string): void {
    for (const l of this.listeners.get(type) ?? []) l({ data });
  }

  close(): void {
    this.closed = true;
  }
}

describe('subscribeLinks', () => {
  it('opens the /api/events stream', () => {
    let captured: FakeEventSource | null = null;
    subscribeLinks(() => {}, (url) => (captured = new FakeEventSource(url)));
    expect(captured).not.toBeNull();
    expect(captured!.url).toBe(EVENTS_URL);
  });

  it('parses a link.created frame and fires the callback with the Link', () => {
    let es!: FakeEventSource;
    const onCreated = vi.fn();
    subscribeLinks(onCreated, (url) => (es = new FakeEventSource(url)));

    es.emit(LINK_CREATED_EVENT, JSON.stringify(link({ key: 'live1', click_count: 3 })));

    expect(onCreated).toHaveBeenCalledTimes(1);
    const arg = onCreated.mock.calls[0][0] as Link;
    expect(arg.key).toBe('live1');
    expect(arg.click_count).toBe(3);
  });

  it('ignores a malformed frame without crashing or firing the callback', () => {
    let es!: FakeEventSource;
    const onCreated = vi.fn();
    subscribeLinks(onCreated, (url) => (es = new FakeEventSource(url)));

    expect(() => es.emit(LINK_CREATED_EVENT, 'not json{')).not.toThrow();
    expect(onCreated).not.toHaveBeenCalled();

    // A subsequent good frame still works — one bad frame doesn't kill the stream.
    es.emit(LINK_CREATED_EVENT, JSON.stringify(link({ key: 'ok' })));
    expect(onCreated).toHaveBeenCalledTimes(1);
  });

  it('cleanup closes the EventSource', () => {
    let es!: FakeEventSource;
    const cleanup = subscribeLinks(() => {}, (url) => (es = new FakeEventSource(url)));
    expect(es.closed).toBe(false);
    cleanup();
    expect(es.closed).toBe(true);
  });
});
