# Server-Sent Events (SSE) — real-time link updates

ShortLinks uses Server-Sent Events to push live `link.created` notifications
from the Go service to every authenticated dashboard client, so a newly created
or reactivated short link appears in the list immediately — without polling.

---

## Architecture overview

```
POST /api/links
   └─ LinksHandler.Create
        └─ broker.Publish(userID, Event{Name:"link.created", Payload:<JSON>})
                  │
                  │  fan-out (one goroutine per connected tab)
                  ▼
      ┌──────────────────────────┐
      │  events.Broker           │
      │  subscribers[userID]     │
      │   [chan Event, chan Event,│
      │    ...]                  │
      └──────────┬───────────────┘
                 │  buffered channel delivery
                 ▼
      EventsHandler.Stream  (GET /api/events)
        writes SSE frames → Apache (flushpackets=on) → browser EventSource
```

The single `*events.Broker` singleton is created in `main` and injected into
both the links handler (publisher) and the events handler (subscriber side).

---

## Broker design (`internal/events/`)

Source: `internal/events/broker.go`

### Data structures

```go
type Event struct {
    Name    string // SSE event name, e.g. "link.created"
    Payload []byte // pre-marshaled JSON written verbatim into the SSE data field
}

type Broker struct {
    mu          sync.RWMutex
    subscribers map[int64][]chan Event // keyed by userID
}
```

Each user can have multiple concurrent subscribers — one per open browser tab.

### Subscribe

```go
func (b *Broker) Subscribe(userID int64) chan Event
```

Creates a buffered channel (depth `subscriberBuffer = 8`) and registers it
under `userID`. The caller (the SSE handler) owns the read end; the broker owns
the write end and closes it on `Unsubscribe`. The buffer lets a briefly stalled
HTTP flush absorb up to 8 events without dropping; sends beyond that depth are
dropped rather than blocking the publisher.

### Publish

```go
func (b *Broker) Publish(userID int64, event Event)
```

Fans the event out to every channel registered for `userID` using a
non-blocking `select { case ch <- event: default: }` — a slow or absent
subscriber is silently skipped. Holds only a read lock, so concurrent publishes
to different users do not contend. Publishing to a user with no subscribers is
a no-op.

### Unsubscribe

```go
func (b *Broker) Unsubscribe(userID int64, ch chan Event)
```

Removes the channel from the user's slice (order-preserving swap-and-truncate),
then closes it. Idempotent: a channel not currently registered is ignored, so a
double `Unsubscribe` never panics on a double-close. When the last subscriber
for a user is removed the user's map entry is deleted.

### Concurrency guarantees

- `Subscribe` and `Unsubscribe` take a **write lock**.
- `Publish` takes a **read lock**, so concurrent publishes to different users
  proceed without contention.
- Safe for use from any number of goroutines simultaneously.

---

## The `/api/events` endpoint (`internal/handlers/events.go`)

### Route and middleware

```
GET /api/events
```

Mounted behind `middleware.RequireSession` — unauthenticated requests are
rejected before the handler is reached. The handler also does a direct
`middleware.UserFromContext` check and returns `401` if no user is present
(defense in depth).

### Handler lifecycle

1. **Flusher check.** The handler asserts the `http.ResponseWriter` implements
   `http.Flusher`. If not, it returns `500 streaming unsupported` before writing
   any headers, so the client never sees a partial SSE response.

2. **Headers.** Written before the first byte:

   | Header | Value | Purpose |
   |---|---|---|
   | `Content-Type` | `text/event-stream` | SSE MIME type |
   | `Cache-Control` | `no-cache` | prevents intermediaries from caching the stream |
   | `Connection` | `keep-alive` | signals a long-lived connection |
   | `X-Accel-Buffering` | `no` | disables nginx proxy buffering (if nginx is ever used) |

3. **Initial comment frame.** Immediately after headers:

   ```
   : connected\n\n
   ```

   This comment flushes the response headers so the browser's `EventSource`
   fires its `open` event promptly, even before the first named event arrives.

4. **Subscribe.** Calls `broker.Subscribe(u.ID)` and defers
   `broker.Unsubscribe(u.ID, ch)` so cleanup is guaranteed on any return path.

5. **Event loop.** Blocks on a `select`:
   - `ctx.Done()` — client disconnected; the deferred `Unsubscribe` runs and
     the goroutine exits cleanly.
   - `event, open := <-ch` — writes one SSE frame and flushes:

     ```
     event: <Name>\n
     data: <Payload>\n
     \n
     ```

   The flush is synchronous; control returns to the select immediately after.

### Wire format example

```
event: link.created
data: {"id":42,"key":"abc123","destination_url":"https://example.com","title":"Example","active":true,"denied_reason":0,"created_at":"2026-05-25T12:00:00Z","expires_at":null,"click_count":0,"duplicate":true}

```

(The blank line terminates each SSE frame per the spec.)

---

## Apache `flushpackets=on` requirement

Source: `deploy/apache/go.sstools.co.conf`

By default Apache's `mod_proxy` buffers the proxied response body before
forwarding it to the client. For SSE, that buffering swallows every frame until
the buffer fills, breaking real-time delivery.

The vhost explicitly disables this for the SSE path:

```apache
# SSE requires response buffering disabled so events flush immediately
ProxyPass /api/events http://127.0.0.1:8080/api/events flushpackets=on
ProxyPassReverse /api/events http://127.0.0.1:8080/api/events

ProxyPass / http://127.0.0.1:8080/
ProxyPassReverse / http://127.0.0.1:8080/
```

The `/api/events` rule **must appear before** the wildcard `ProxyPass /` line.
Apache evaluates `ProxyPass` directives top-to-bottom; if the wildcard matched
first the `flushpackets=on` flag would never take effect and events would be
silently buffered.

See also: `DEPLOYMENT.md` section **8. Apache** for the full install procedure,
and `deploy/apache/README.md` for a note on ordering.

---

## Event types and payloads

Currently exactly one named event type is published.

### `link.created`

**When published:** `LinksHandler.Create` (`internal/handlers/links.go`) publishes
this event after a successful database write, when `broadcast == true`. The
`broadcast` flag is set based on the store outcome:

| Store outcome | `broadcast` | Explanation |
|---|---|---|
| `OutcomeInserted` | `true` | New link created — publish |
| `OutcomeReactivated` | `true` | Inactive link reactivated — publish |
| `OutcomeActiveDuplicate` | `false` | Link already exists and is active — no publish |

A filter-denied create returns `422` before the broadcast code is reached and
never publishes.

**Payload:** the same `linkView` JSON object returned in the `POST /api/links`
response body.

```jsonc
{
  "id": 42,
  "key": "abc123",
  "destination_url": "https://www.example.com/page",
  "title": "My Link",
  "active": true,
  "denied_reason": 0,
  "created_at": "2026-05-25T12:00:00Z",
  "expires_at": null,
  "click_count": 0,
  "duplicate": true   // omitted when false (omitempty)
}
```

Go source (`internal/handlers/links.go`):

```go
if h.broker != nil && broadcast {
    if payload, err := json.Marshal(view); err == nil {
        h.broker.Publish(u.ID, events.Event{Name: "link.created", Payload: payload})
    }
}
```

---

## SPA subscription (`web/src/lib/events.ts`)

### Constants

```ts
export const LINK_CREATED_EVENT = 'link.created';
export const EVENTS_URL = '/api/events';
```

### `subscribeLinks(onCreated, factory?)`

Opens a browser `EventSource` on `/api/events` and registers a listener for
`link.created` frames. For each frame:

1. Parses `event.data` as JSON into a `Link`.
2. Calls `onCreated(link)`.
3. A malformed frame (non-JSON `data`) is swallowed with a `try/catch` — it
   never crashes the dashboard or kills the stream.

Returns a cleanup function (`() => es.close()`) for use in Svelte's `onMount`
return value.

Reconnection is left entirely to the browser's built-in `EventSource` behavior,
which automatically reconnects after a drop with exponential back-off.

### `prependUniqueByKey(list, link)`

Pure, immutable helper used by the Dashboard store mutation. Prepends `link` to
`list`, deduping by `key`: if a link with the same key already exists (e.g.
from an optimistic create), it is replaced at the front rather than inserted
again. Returns a new array; never mutates the input.

### Dashboard integration (`web/src/views/Dashboard.svelte`)

Inside `onMount`, the Dashboard opens the SSE stream and wires each incoming
`link.created` event directly into the shared `links` Svelte store:

```ts
onMount(() => {
    loadPage(1);

    const unsubscribe = subscribeLinks((link) => {
        links.update((cur) => prependUniqueByKey(cur, link));
    });

    return unsubscribe; // Svelte calls this on component destroy
});
```

Returning `unsubscribe` from `onMount` is the Svelte lifecycle idiom for
teardown: when the Dashboard is unmounted (e.g. user navigates to Account),
Svelte calls the returned function, which calls `es.close()`, which terminates
the long-lived SSE connection and triggers the server-side `ctx.Done()` path,
causing the handler goroutine to unsubscribe and exit.

---

## Testing

| File | What it covers |
|---|---|
| `internal/events/broker_test.go` | Subscribe/Publish/Unsubscribe correctness, fan-out to multiple subscribers, user isolation, non-blocking on full buffers, concurrent safety under `-race` |
| `internal/handlers/events_test.go` | Full SSE frame delivery end-to-end (live DB), header assertions, disconnect/unsubscribe verification, unauthenticated 401 |
| `internal/handlers/events_test.go` (`TestLinksCreate_BroadcastsOnInsertNotOnDuplicateOrDenied`) | Proves publish happens on insert/reactivation but NOT on active-duplicate or filter-denied create |
| `web/src/lib/events.test.ts` | `prependUniqueByKey` edge cases; `subscribeLinks` frame parsing, malformed-frame resilience, cleanup |
