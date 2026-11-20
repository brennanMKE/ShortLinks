package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/reports"
)

// countingReader counts GetSettings calls so a test can prove the handler reads
// every setting in a single query.
type countingReader struct {
	fakeHomeReader
	calls int
}

func (c *countingReader) GetSettings(ctx context.Context, keys ...string) (map[string]string, error) {
	c.calls++
	return c.fakeHomeReader.GetSettings(ctx, keys...)
}

func getHome(h *HomeHandler) (*httptest.ResponseRecorder, map[string]any) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/home", nil))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

func sampleActivity() *reports.PublicActivity {
	return &reports.PublicActivity{
		Window:      reports.PublicWindow{From: "2026-11-12", To: "2026-11-18", TZ: "UTC", PartialLastDay: true},
		TotalClicks: 5,
		Timeseries:  []reports.DayCount{{Date: "2026-11-18", Clicks: 5}},
		TopLinks:    []reports.PublicLink{{Title: "T", ShortURL: "https://go.example.com/u/abc", Clicks: 5}},
	}
}

func TestHomeHandler_ActivityOnlyInActivityMode(t *testing.T) {
	for _, mode := range []string{"", "default", "about", "bogus"} {
		calls := 0
		h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": mode}}).
			WithActivity(func(context.Context) (*reports.PublicActivity, error) { calls++; return sampleActivity(), nil })
		_, m := getHome(h)
		if _, ok := m["activity"]; ok {
			t.Errorf("mode %q: activity present, want absent", mode)
		}
		if calls != 0 {
			t.Errorf("mode %q: activity query ran %d times, want 0", mode, calls)
		}
	}

	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(context.Context) (*reports.PublicActivity, error) { return sampleActivity(), nil })
	_, m := getHome(h)
	a, ok := m["activity"].(map[string]any)
	if !ok {
		t.Fatalf("activity missing in activity mode: %v", m)
	}
	if a["total_clicks"] != float64(5) {
		t.Errorf("activity = %v", a)
	}
}

func TestHomeHandler_ActivityCachedFor60s(t *testing.T) {
	calls := 0
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(context.Context) (*reports.PublicActivity, error) { calls++; return sampleActivity(), nil })
	h.now = func() time.Time { return now }

	getHome(h)
	now = now.Add(59 * time.Second)
	_, m := getHome(h)
	if calls != 1 {
		t.Fatalf("activity computed %d times within 60s, want 1", calls)
	}
	if m["activity"] == nil {
		t.Error("cached request lost activity")
	}
	now = now.Add(2 * time.Second)
	getHome(h)
	if calls != 2 {
		t.Errorf("activity computed %d times after expiry, want 2", calls)
	}
}

func TestHomeHandler_ActivityErrorOmitsActivityAndBacksOff(t *testing.T) {
	calls := 0
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(context.Context) (*reports.PublicActivity, error) { calls++; return nil, errors.New("boom") })
	h.now = func() time.Time { return now }
	rec, m := getHome(h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (page still renders)", rec.Code)
	}
	if _, ok := m["activity"]; ok {
		t.Error("activity present after error")
	}
	now = now.Add(9 * time.Second)
	getHome(h)
	if calls != 1 {
		t.Errorf("retried inside the backoff: calls = %d, want 1", calls)
	}
	now = now.Add(2 * time.Second)
	getHome(h)
	if calls != 2 {
		t.Errorf("no retry after the backoff: calls = %d, want 2", calls)
	}
}

// A slow store is computed once for a burst of requests, and a visitor who
// disconnects mid-flight neither cancels the work nor fails the others.
func TestHomeHandler_SlowActivitySharedAndDetachedFromClient(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	var ctxErr atomic.Value
	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(ctx context.Context) (*reports.PublicActivity, error) {
			calls.Add(1)
			<-release
			if err := ctx.Err(); err != nil {
				ctxErr.Store(err)
			}
			return sampleActivity(), nil
		})

	// First visitor disconnects while the computation is in flight.
	cctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/home", nil).WithContext(cctx))
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	var wg sync.WaitGroup
	bodies := make([]map[string]any, 5)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, bodies[i] = getHome(h)
		}()
	}
	time.Sleep(50 * time.Millisecond) // let them queue behind the flight
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("activity computed %d times, want 1 shared computation", n)
	}
	if v := ctxErr.Load(); v != nil {
		t.Errorf("computation context was cancelled by a client: %v", v)
	}
	for i, m := range bodies {
		if m["activity"] == nil {
			t.Errorf("request %d has no activity", i)
		}
	}
}

func TestHomeHandler_SettingsReadInOneQuery(t *testing.T) {
	r := &countingReader{fakeHomeReader: fakeHomeReader{enabled: true, url: "https://www.example.com/"}}
	getHome(NewHomeHandler(r))
	if r.calls != 1 {
		t.Errorf("GetSettings calls = %d, want 1", r.calls)
	}
}

func TestHomeHandler_NoDestinationInActivityJSON(t *testing.T) {
	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(context.Context) (*reports.PublicActivity, error) { return sampleActivity(), nil })
	rec, _ := getHome(h)
	for _, bad := range []string{"destination", "utm_", "campaign"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Errorf("body contains %q: %s", bad, rec.Body.String())
		}
	}
}

// A removed link must leave the public activity at once (#0183), not after the
// 60 second cache window.
func TestHomeHandler_InvalidateActivityRecomputes(t *testing.T) {
	calls := 0
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	h := NewHomeHandler(fakeHomeReader{extra: map[string]string{"home_mode": "activity"}}).
		WithActivity(func(context.Context) (*reports.PublicActivity, error) { calls++; return sampleActivity(), nil })
	h.now = func() time.Time { return now }

	getHome(h)
	getHome(h)
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", calls)
	}
	h.InvalidateActivity()
	getHome(h)
	if calls != 2 {
		t.Errorf("calls = %d after InvalidateActivity, want 2", calls)
	}
}
