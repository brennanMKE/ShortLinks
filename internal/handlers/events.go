package handlers

import (
	"fmt"
	"net/http"

	"github.com/brennanMKE/ShortLinks/internal/events"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// eventSubscriber is the slice of the events broker the SSE handler needs: the
// ability to register a per-user channel and tear it down on disconnect.
// *events.Broker satisfies it. Depending on the interface keeps the handler
// unit-testable with a fake broker and documents the exact contract.
type eventSubscriber interface {
	Subscribe(userID int64) chan events.Event
	Unsubscribe(userID int64, ch chan events.Event)
}

// EventsHandler serves GET /api/events: a long-lived Server-Sent Events stream
// that pushes link.* events to the authenticated user's connected dashboard
// clients. It is mounted behind middleware.RequireSession (#0017) and reads the
// authenticated user from the request context so each stream only receives that
// user's events.
type EventsHandler struct {
	broker eventSubscriber
}

// NewEventsHandler constructs an EventsHandler over the shared events broker
// (the same singleton injected into the links handler as publisher).
func NewEventsHandler(broker eventSubscriber) *EventsHandler {
	return &EventsHandler{broker: broker}
}

// Stream handles GET /api/events. It establishes an SSE stream for the
// authenticated user, then blocks reading the user's broker channel and writing
// one `event: <Name>\ndata: <Payload>\n\n` frame per event, flushing after each
// so the dashboard sees it immediately. When the client disconnects
// (r.Context().Done()) it unsubscribes and returns, ending the goroutine
// cleanly. A response writer that cannot flush (no http.Flusher) yields 500,
// since SSE cannot work without per-event flushing.
func (h *EventsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	// SSE requires per-event flushing; bail before sending any 200/headers if the
	// writer can't flush (e.g. a buffering middleware that strips Flusher).
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// SSE headers. X-Accel-Buffering disables proxy buffering (nginx); the Apache
	// ProxyPass uses flushpackets=on (PRD/#0003). Set these before the first write.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial comment frame so intermediaries flush the response headers and the
	// client's EventSource fires `open` promptly.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch := h.broker.Subscribe(u.ID)
	defer h.broker.Unsubscribe(u.ID, ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			// Client disconnected: deferred Unsubscribe removes + closes ch and the
			// goroutine exits.
			return
		case event, open := <-ch:
			if !open {
				// Channel closed out from under us (defensive): stop the stream.
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, event.Payload)
			flusher.Flush()
		}
	}
}
