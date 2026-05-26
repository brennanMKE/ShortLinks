package events

import (
	"sync"
	"testing"
	"time"
)

// recv reads one event from ch within a short timeout, failing the test if none
// arrives. Using a timeout select (rather than time.Sleep) keeps the test fast
// and non-flaky.
func recv(t *testing.T, ch chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed; wanted an event")
		}
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// assertNoEvent asserts ch delivers nothing within a brief window.
func assertNoEvent(t *testing.T, ch chan Event) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event %q on channel", ev.Name)
		}
		// closed channel is fine for the "no delivery" assertion.
	case <-time.After(100 * time.Millisecond):
	}
}

// TestSubscribeReturnsChannel asserts Subscribe hands back a usable, buffered
// channel registered for the user.
func TestSubscribeReturnsChannel(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe(1)
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}
	if cap(ch) == 0 {
		t.Errorf("channel cap = 0, want buffered (>0)")
	}
	b.Publish(1, Event{Name: "link.created", Payload: []byte(`{"k":1}`)})
	got := recv(t, ch)
	if got.Name != "link.created" || string(got.Payload) != `{"k":1}` {
		t.Errorf("got %q/%q, want link.created/{\"k\":1}", got.Name, got.Payload)
	}
}

// TestPublishFansOutToAllSubscribersSameUser asserts every channel registered
// for a user receives the published event.
func TestPublishFansOutToAllSubscribersSameUser(t *testing.T) {
	b := NewBroker()
	a := b.Subscribe(7)
	c := b.Subscribe(7)

	b.Publish(7, Event{Name: "link.created", Payload: []byte("x")})

	if ev := recv(t, a); ev.Name != "link.created" {
		t.Errorf("subscriber a: name = %q", ev.Name)
	}
	if ev := recv(t, c); ev.Name != "link.created" {
		t.Errorf("subscriber c: name = %q", ev.Name)
	}
}

// TestPublishDoesNotReachOtherUsers asserts events are scoped per user: a
// publish for user 1 never reaches user 2's subscriber.
func TestPublishDoesNotReachOtherUsers(t *testing.T) {
	b := NewBroker()
	one := b.Subscribe(1)
	two := b.Subscribe(2)

	b.Publish(1, Event{Name: "link.created", Payload: []byte("for-1")})

	if ev := recv(t, one); string(ev.Payload) != "for-1" {
		t.Errorf("user 1 payload = %q, want for-1", ev.Payload)
	}
	assertNoEvent(t, two) // user 2 must not see user 1's event.
}

// TestUnsubscribeRemovesAndCloses asserts Unsubscribe closes the channel and a
// subsequent Publish neither panics nor delivers to the removed channel.
func TestUnsubscribeRemovesAndCloses(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe(3)
	b.Unsubscribe(3, ch)

	// Channel must be closed.
	if _, ok := <-ch; ok {
		t.Error("channel not closed after Unsubscribe")
	}

	// Publish after Unsubscribe must not panic (no send on closed channel) and
	// must deliver nothing.
	b.Publish(3, Event{Name: "link.created", Payload: []byte("late")})
	if _, ok := <-ch; ok {
		t.Error("received event on an unsubscribed channel")
	}
}

// TestUnsubscribeOneOfMany asserts removing one subscriber leaves the others
// receiving events.
func TestUnsubscribeOneOfMany(t *testing.T) {
	b := NewBroker()
	keep := b.Subscribe(5)
	drop := b.Subscribe(5)
	b.Unsubscribe(5, drop)

	b.Publish(5, Event{Name: "link.created", Payload: []byte("y")})

	if ev := recv(t, keep); ev.Name != "link.created" {
		t.Errorf("remaining subscriber name = %q", ev.Name)
	}
	if _, ok := <-drop; ok {
		t.Error("dropped subscriber still received an event")
	}
}

// TestUnsubscribeUnknownChannelNoPanic asserts Unsubscribing a channel that was
// never registered (or already removed) is a safe no-op.
func TestUnsubscribeUnknownChannelNoPanic(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe(9)
	b.Unsubscribe(9, ch)
	// Second Unsubscribe of the same (now-removed) channel must not double-close.
	b.Unsubscribe(9, ch)
	// Unsubscribe of a never-registered channel must not panic.
	b.Unsubscribe(9, make(chan Event))
}

// TestPublishNonBlockingOnFullBuffer asserts a full/slow subscriber buffer does
// NOT block Publish: the publisher returns promptly and other subscribers still
// receive. The slow subscriber's buffer is filled past capacity; Publish must
// drop for it rather than block.
func TestPublishNonBlockingOnFullBuffer(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe(11) // never read from → buffer fills.
	fast := b.Subscribe(11)

	// Fill the slow subscriber's buffer beyond capacity. None of these may block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*4; i++ {
			b.Publish(11, Event{Name: "link.created", Payload: []byte("flood")})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}

	// The fast subscriber should still have received events (its buffer absorbed
	// at least the first sends), and the slow one must not have wedged the broker.
	if ev := recv(t, fast); ev.Name != "link.created" {
		t.Errorf("fast subscriber name = %q", ev.Name)
	}
	_ = slow
}

// TestConcurrentSubscribePublishUnsubscribe hammers the broker from many
// goroutines to shake out data races under -race. It asserts no panic/deadlock;
// correctness of individual deliveries is covered by the focused tests above.
func TestConcurrentSubscribePublishUnsubscribe(t *testing.T) {
	b := NewBroker()
	var wg sync.WaitGroup
	for u := int64(0); u < 8; u++ {
		for r := 0; r < 8; r++ {
			wg.Add(1)
			go func(userID int64) {
				defer wg.Done()
				ch := b.Subscribe(userID)
				// Drain in the background so Publish has a live reader.
				drained := make(chan struct{})
				go func() {
					for range ch {
					}
					close(drained)
				}()
				for i := 0; i < 50; i++ {
					b.Publish(userID, Event{Name: "link.created", Payload: []byte("z")})
				}
				b.Unsubscribe(userID, ch)
				<-drained
			}(u)
		}
	}
	wg.Wait()
}
