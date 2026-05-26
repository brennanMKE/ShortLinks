// SSE client for the Dashboard's real-time link list (#0034). The server's
// /api/events endpoint (#0026) pushes one `event: link.created\ndata: <JSON
// link row>\n\n` frame per newly created or reactivated link, scoped to the
// authenticated user. This module opens that stream, parses each frame into a
// Link, and hands it to a callback. The store-mutation policy (prepend, but
// dedupe by key) is kept as a pure function so it is unit-testable without a
// DOM or a live EventSource — see events.test.ts.

import type { Link } from './types';

/** The SSE event name the server uses for a created/reactivated link (#0026). */
export const LINK_CREATED_EVENT = 'link.created';

/** The endpoint the dashboard subscribes to for live link events (#0026). */
export const EVENTS_URL = '/api/events';

/**
 * Prepend `link` to `list`, deduping by `key`. If a link with the same key is
 * already present (e.g. the user's own create already prepended it optimistically
 * in #0033, and the same link now also arrives over SSE), the incoming link
 * REPLACES the existing entry in place at the FRONT — it is never inserted twice.
 *
 * Pure and immutable: returns a new array and never mutates the input. This is
 * the single source of truth for "a link.created arrived" so the Svelte store
 * update and its tests share identical behavior.
 */
export function prependUniqueByKey(list: readonly Link[], link: Link): Link[] {
  return [link, ...list.filter((l) => l.key !== link.key)];
}

/**
 * The minimal EventSource surface this module uses. Declaring it lets tests pass
 * a hand-rolled fake (the real DOM EventSource satisfies it) without depending on
 * a jsdom environment.
 */
export interface EventSourceLike {
  addEventListener(type: string, listener: (event: { data: string }) => void): void;
  close(): void;
}

/** A factory for an EventSource, injectable so tests can supply a fake. */
export type EventSourceFactory = (url: string) => EventSourceLike;

const defaultFactory: EventSourceFactory = (url) =>
  new EventSource(url) as unknown as EventSourceLike;

/**
 * Open the SSE stream and invoke `onCreated` with the parsed Link for every
 * `link.created` frame. Returns a cleanup function that closes the connection;
 * call it on Dashboard unmount so the stream is torn down.
 *
 * Reconnection is intentionally NOT custom: the browser's built-in EventSource
 * reconnects automatically after a drop (per the issue AC), so this stays simple.
 *
 * A malformed frame (non-JSON `data`) is swallowed defensively: it is ignored so
 * one bad event cannot crash the dashboard or kill the live stream.
 *
 * @param onCreated called with each parsed Link from a link.created event.
 * @param factory   optional EventSource factory (defaults to the global), for tests.
 */
export function subscribeLinks(
  onCreated: (link: Link) => void,
  factory: EventSourceFactory = defaultFactory,
): () => void {
  const es = factory(EVENTS_URL);

  es.addEventListener(LINK_CREATED_EVENT, (event) => {
    let link: Link;
    try {
      link = JSON.parse(event.data) as Link;
    } catch {
      // A bad/partial frame must not crash the dashboard or end the stream.
      return;
    }
    onCreated(link);
  });

  return () => es.close();
}
