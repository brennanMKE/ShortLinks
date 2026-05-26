package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/cache"
	"github.com/brennanMKE/ShortLinks/internal/events"
	"github.com/brennanMKE/ShortLinks/internal/filters"
	"github.com/brennanMKE/ShortLinks/internal/links"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// countingBroker wraps a real *events.Broker to record Subscribe/Unsubscribe so
// a test can assert the handler tore down its subscription on disconnect, and to
// signal when a subscription has been registered.
type countingBroker struct {
	inner      *events.Broker
	mu         sync.Mutex
	subs       int
	unsubs     int
	subscribed chan struct{} // receives once per Subscribe
}

func newCountingBroker() *countingBroker {
	return &countingBroker{inner: events.NewBroker(), subscribed: make(chan struct{}, 8)}
}

func (b *countingBroker) Subscribe(userID int64) chan events.Event {
	ch := b.inner.Subscribe(userID)
	b.mu.Lock()
	b.subs++
	b.mu.Unlock()
	b.subscribed <- struct{}{}
	return ch
}

func (b *countingBroker) Unsubscribe(userID int64, ch chan events.Event) {
	b.inner.Unsubscribe(userID, ch)
	b.mu.Lock()
	b.unsubs++
	b.mu.Unlock()
}

func (b *countingBroker) counts() (subs, unsubs int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subs, b.unsubs
}

// TestEventsStream_DeliversFrameAndUnsubscribesOnCancel drives the SSE handler
// through the REAL RequireSession (live DB session): it subscribes, writes a
// well-formed link.created frame for a published event, and—when the client
// disconnects (request context canceled)—unsubscribes and returns.
func TestEventsStream_DeliversFrameAndUnsubscribesOnCancel(t *testing.T) {
	pool := credsTestPool(t) // skips when TEST_DATABASE_URL unset.
	authStore := auth.NewStore(pool)

	broker := newCountingBroker()
	h := NewEventsHandler(broker)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("GET /api/events", requireSession(http.HandlerFunc(h.Stream)))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	uid := seedUser(t, pool, "sse@example.com")
	seedSession(t, pool, uid, "sse-token")

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := srv.Client().Do(withCookie(req, "sse-token"))
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if ab := resp.Header.Get("X-Accel-Buffering"); ab != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", ab)
	}

	reader := bufio.NewReader(resp.Body)

	// Initial ": connected" comment frame.
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, ":") {
		t.Errorf("first line = %q, want a comment frame", line)
	}

	// Wait until the handler has subscribed before publishing, so the event
	// cannot race ahead of the subscription.
	select {
	case <-broker.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never subscribed")
	}

	broker.inner.Publish(uid, events.Event{Name: "link.created", Payload: []byte(`{"id":1,"key":"abc123"}`)})

	frame := readFrame(t, reader)
	if !strings.Contains(frame, "event: link.created\n") {
		t.Errorf("frame missing event line:\n%q", frame)
	}
	if !strings.Contains(frame, `data: {"id":1,"key":"abc123"}`) {
		t.Errorf("frame missing/incorrect data line:\n%q", frame)
	}

	// Disconnect: cancel the request context; the handler must Unsubscribe.
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		if _, unsubs := broker.counts(); unsubs == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("handler did not Unsubscribe after client disconnect")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if subs, unsubs := broker.counts(); subs != 1 || unsubs != 1 {
		t.Errorf("subs=%d unsubs=%d, want 1/1", subs, unsubs)
	}
}

// readFrame reads lines until it has collected a `data:` line followed by a
// blank line, returning the accumulated `event:`/`data:` text.
func readFrame(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var b strings.Builder
	sawData := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read frame: %v (so far: %q)", err, b.String())
		}
		if strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "data:") {
			b.WriteString(line)
			if strings.HasPrefix(line, "data:") {
				sawData = true
			}
			continue
		}
		if line == "\n" && sawData {
			return b.String()
		}
	}
	t.Fatalf("timed out reading frame (so far: %q)", b.String())
	return ""
}

// TestEventsStream_Unauthenticated asserts the handler 401s when no user is in
// context (defense in depth; in production RequireSession answers first). This
// path needs no DB.
func TestEventsStream_Unauthenticated(t *testing.T) {
	h := NewEventsHandler(newCountingBroker())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	h.Stream(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// broadcastLinksMux wires the POST /api/links route through the real
// RequireSession, the real links + filters stores, and the given broker as the
// #0026 publisher, so a create end-to-end fans out (or doesn't) over the broker.
func broadcastLinksMux(t *testing.T, pool *pgxpool.Pool, broker eventPublisher, ruleCache *cache.RuleCache) http.Handler {
	t.Helper()
	authStore := auth.NewStore(pool)
	var rules ruleProvider
	if ruleCache != nil {
		rules = ruleCache
	}
	h := NewLinksHandler(links.NewStore(pool), nil, rules, nil, broker, nil)
	requireSession := middleware.RequireSession(authStore)
	mux := http.NewServeMux()
	mux.Handle("POST /api/links", requireSession(http.HandlerFunc(h.Create)))
	return mux
}

// postCreate POSTs body as token and returns the HTTP status (the body is
// drained/closed). It mirrors postLink but returns only the status, which is all
// the broadcast assertions need.
func postCreate(t *testing.T, srv *httptest.Server, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/links", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(withCookie(req, token))
	if err != nil {
		t.Fatalf("POST /api/links: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// recvEvent reads one event from ch within a timeout; fails if none arrives.
func recvEvent(t *testing.T, ch chan events.Event) events.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broker event")
		return events.Event{}
	}
}

// assertNoEvent asserts ch delivers nothing within a brief window.
func assertNoBrokerEvent(t *testing.T, ch chan events.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected broker event %q", ev.Name)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestLinksCreate_BroadcastsOnInsertNotOnDuplicateOrDenied is the #0026
// end-to-end proof against the live DB: a fresh insert publishes link.created to
// the user's subscriber with the created link JSON; an active-DUPLICATE create
// does NOT publish; and a filter-DENIED create does NOT publish.
func TestLinksCreate_BroadcastsOnInsertNotOnDuplicateOrDenied(t *testing.T) {
	pool := filterTestPool(t) // skips when TEST_DATABASE_URL unset; clears auth + url_filter_rules.

	broker := events.NewBroker()
	ruleCache := newFilterRuleCache(pool)
	srv := httptest.NewServer(broadcastLinksMux(t, pool, broker, ruleCache))
	defer srv.Close()

	alice := seedUser(t, pool, "alice@example.com")
	seedSession(t, pool, alice, "alice-token")

	// A rule that denies anything containing "evil.com".
	seedFilterRule(t, pool, `evil\.com`, int16(filters.ReasonMalware))

	// Subscribe BEFORE any create so no event is missed.
	sub := broker.Subscribe(alice)
	defer broker.Unsubscribe(alice, sub)

	const dest = "https://www.example.org/sse"

	// 1) Fresh insert → MUST publish link.created with the created link JSON.
	if status := postCreate(t, srv, "alice-token", `{"destination_url":"`+dest+`","title":"SSE"}`); status != http.StatusCreated {
		t.Fatalf("insert status = %d, want 201", status)
	}
	ev := recvEvent(t, sub)
	if ev.Name != "link.created" {
		t.Errorf("event name = %q, want link.created", ev.Name)
	}
	var got linkView
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	if got.DestinationURL != dest {
		t.Errorf("event payload destination_url = %q, want %q", got.DestinationURL, dest)
	}
	if got.Key == "" || !got.Active {
		t.Errorf("event payload key=%q active=%v, want non-empty key, active", got.Key, got.Active)
	}

	// 2) Active DUPLICATE (same URL, same user) → MUST NOT publish.
	if status := postCreate(t, srv, "alice-token", `{"destination_url":"`+dest+`","title":"again"}`); status != http.StatusCreated {
		t.Fatalf("duplicate status = %d, want 201", status)
	}
	assertNoBrokerEvent(t, sub)

	// 3) Filter DENIED → MUST NOT publish.
	if status := postCreate(t, srv, "alice-token", `{"destination_url":"https://evil.com/x","title":"bad"}`); status != http.StatusUnprocessableEntity {
		t.Fatalf("denied status = %d, want 422", status)
	}
	assertNoBrokerEvent(t, sub)
}
