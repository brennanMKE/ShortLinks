package events

import "sync"

// subscriberBuffer is the per-channel buffer depth handed out by Subscribe. A
// small buffer lets Publish absorb a brief reader stall (e.g. an SSE goroutine
// between two flushes) without dropping the event, while the non-blocking send
// in Publish guarantees that a reader stalled longer than this never blocks the
// publisher or starves the other subscribers.
const subscriberBuffer = 8

// Event is a single message fanned out to a user's connected SSE clients. Name
// is the SSE event name (e.g. "link.created") and Payload is the already
// JSON-encoded event body (e.g. a link row) written verbatim into the SSE
// frame's data field.
type Event struct {
	Name    string // "link.created"
	Payload []byte // JSON-encoded link row
}

// Broker is the in-memory pub/sub hub between the link handler (publisher) and
// the SSE handlers (subscribers). It fans an Event out to every channel
// registered for a given user id. A single Broker is created in main and shared
// by both sides; it is safe for concurrent use by any number of goroutines.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[int64][]chan Event // keyed by userID
}

// NewBroker returns an empty, ready-to-use Broker.
func NewBroker() *Broker {
	return &Broker{subscribers: make(map[int64][]chan Event)}
}

// Subscribe registers a new subscription for userID and returns a freshly
// created buffered channel on which that user's events will be delivered. The
// caller (an SSE handler) reads from the returned channel until it disconnects,
// then passes the same channel back to Unsubscribe. The buffer (see
// subscriberBuffer) lets a brief reader stall ride through without dropping an
// event; the channel is owned by the broker and is closed by Unsubscribe — the
// caller must not close it.
func (b *Broker) Subscribe(userID int64) chan Event {
	ch := make(chan Event, subscriberBuffer)
	b.mu.Lock()
	b.subscribers[userID] = append(b.subscribers[userID], ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes ch from userID's subscriber set and closes it. It is
// idempotent: a channel that is not (or no longer) registered is ignored, so a
// double Unsubscribe never panics on a double close. Once removed the channel
// will receive no further Publish sends, so closing it here is safe.
func (b *Broker) Unsubscribe(userID int64, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	subs := b.subscribers[userID]
	for i, c := range subs {
		if c == ch {
			// Remove index i without preserving order.
			subs[i] = subs[len(subs)-1]
			subs[len(subs)-1] = nil
			b.subscribers[userID] = subs[:len(subs)-1]
			if len(b.subscribers[userID]) == 0 {
				delete(b.subscribers, userID)
			}
			close(ch)
			return
		}
	}
}

// Publish fans event out to every channel currently subscribed for userID.
// Delivery is best-effort and non-blocking: each send uses a select with a
// default branch, so a subscriber whose buffer is full (a slow or absent reader)
// is skipped rather than blocking the publisher or the other subscribers. Users
// with no subscribers are a no-op. Publish holds only a read lock, so concurrent
// Publishes to different users do not contend.
func (b *Broker) Publish(userID int64, event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers[userID] {
		select {
		case ch <- event:
		default:
			// Subscriber buffer full: drop for this subscriber so one stuck
			// reader cannot block Publish or the remaining subscribers.
		}
	}
}
