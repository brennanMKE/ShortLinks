package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/audit"
	"github.com/brennanMKE/ShortLinks/internal/reports"
	"github.com/brennanMKE/ShortLinks/internal/siteassets"
)

// homeSettingsReader is what the public home-info handler needs from the data
// layer. *auth.Store and *devstore.Store satisfy it. GetSettings reads every
// requested key in ONE query; absent keys are simply missing from the map.
type homeSettingsReader interface {
	GetSettings(ctx context.Context, keys ...string) (map[string]string, error)
}

// ActivityFunc computes the site-wide public activity summary (#0175).
type ActivityFunc func(ctx context.Context) (*reports.PublicActivity, error)

// homeActivityTTL is how long a computed activity summary is reused, so
// anonymous traffic does not run the click queries on every request.
const homeActivityTTL = 60 * time.Second

// homeActivityTopLinks is how many top links the public page shows.
const homeActivityTopLinks = 3

// HomeHandler serves GET /api/public/home (#0125). It is UNAUTHENTICATED by
// design: the SPA needs it before sign-in. It exposes a fixed set of keys and no
// other setting.
type HomeHandler struct {
	store    homeSettingsReader
	hero     *HeroHandler
	activity ActivityFunc
	now      func() time.Time
	portiko  bool

	mu        sync.Mutex
	cached    *reports.PublicActivity
	cacheAt   time.Time
	failUntil time.Time
	flight    *activityFlight
}

// NewHomeHandler constructs a HomeHandler.
func NewHomeHandler(store homeSettingsReader) *HomeHandler {
	return &HomeHandler{store: store, now: time.Now}
}

// WithPortiko records whether this instance signs in through Portiko
// (PORTIKO_ENABLED, #0132) so the SPA can pick the redirect flow or the passkey
// screens at runtime (#0133). It is a fixed boolean for the life of the process.
func (h *HomeHandler) WithPortiko(on bool) *HomeHandler {
	h.portiko = on
	return h
}

// WithActivity wires the activity source used in activity mode. Without it
// (dev mode) the activity key is simply absent.
func (h *HomeHandler) WithActivity(fn ActivityFunc) *HomeHandler {
	h.activity = fn
	return h
}

// PublicActivityFunc returns an ActivityFunc over the reports database for the
// given time zone and base URL.
func PublicActivityFunc(q reports.Querier, tz, baseURL string) ActivityFunc {
	return func(ctx context.Context) (*reports.PublicActivity, error) {
		return reports.PublicSummary(ctx, q, time.Now(), tz, baseURL, homeActivityTopLinks)
	}
}

// WithHero attaches the hero image store (#0176) so the response can describe
// the image. Without it hero stays null. It also builds the HeroHandler that
// serves /public/hero and the admin upload routes; see Hero.
func (h *HomeHandler) WithHero(store heroStore, auditor *audit.Logger) *HomeHandler {
	h.hero = NewHeroHandler(store, auditor)
	return h
}

// Hero returns the hero handler set by WithHero, or nil.
func (h *HomeHandler) Hero() *HeroHandler {
	if h == nil {
		return nil
	}
	return h.hero
}

// homeResponse is the public body. The key set is pinned by a test so nothing
// else from settings can leak. Hero is filled from the hero store (#0176) and Activity by #0175
// (present only in activity mode); hero is null when unset and activity is absent until #0175.
type homeResponse struct {
	RedirectURL          string    `json:"redirect_url"`
	RegistrationsEnabled bool      `json:"registrations_enabled"`
	Mode                 string    `json:"mode"`
	Title                string    `json:"title"`
	About                string    `json:"about"`
	Hero                 *heroInfo `json:"hero"`
	// Portiko is true when sign-in goes through Portiko's redirect flow (#0133).
	Portiko bool `json:"portiko"`
	// Activity is present only when Mode is "activity" (#0175).
	Activity *reports.PublicActivity `json:"activity,omitempty"`
}

// heroInfo describes the hero image (#0176).
type heroInfo struct {
	URL    string `json:"url"`
	Alt    string `json:"alt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// activityFailBackoff is how long after a failed computation requests are
// served without activity instead of retrying.
const activityFailBackoff = 10 * time.Second

// activityTimeout bounds one computation. It runs on a detached context so one
// visitor disconnecting cannot cancel the work other requests are waiting on.
const activityTimeout = 5 * time.Second

// activityFlight is one in-progress computation shared by every waiter.
type activityFlight struct {
	done chan struct{}
	val  *reports.PublicActivity
	err  error
}

// activitySummary returns the cached summary, recomputing it once it is older
// than homeActivityTTL. Concurrent requests share one computation
// (single-flight); the mutex is held only to check and set state, never across
// the query. After a failure it returns an error immediately for
// activityFailBackoff, so the page is served without activity.
func (h *HomeHandler) activitySummary(ctx context.Context) (*reports.PublicActivity, error) {
	h.mu.Lock()
	now := h.now()
	if h.cached != nil && now.Sub(h.cacheAt) < homeActivityTTL {
		a := h.cached
		h.mu.Unlock()
		return a, nil
	}
	if now.Before(h.failUntil) {
		h.mu.Unlock()
		return nil, errActivityBackoff
	}
	f := h.flight
	if f == nil {
		f = &activityFlight{done: make(chan struct{})}
		h.flight = f
		go h.computeActivity(f)
	}
	h.mu.Unlock()

	select {
	case <-f.done:
		return f.val, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errActivityBackoff = errors.New("public home: activity unavailable, backing off")

func (h *HomeHandler) computeActivity(f *activityFlight) {
	cctx, cancel := context.WithTimeout(context.Background(), activityTimeout)
	defer cancel()
	f.val, f.err = h.activity(cctx)
	h.mu.Lock()
	if f.err != nil {
		h.failUntil = h.now().Add(activityFailBackoff)
	} else {
		h.cached, h.cacheAt = f.val, h.now()
	}
	h.flight = nil
	h.mu.Unlock()
	close(f.done)
}

// InvalidateActivity drops the cached activity summary (and any failure
// backoff) so the next request recomputes it. A purged link must not keep
// showing in the public activity for the rest of the cache window (#0183).
func (h *HomeHandler) InvalidateActivity() {
	h.mu.Lock()
	h.cached = nil
	h.failUntil = time.Time{}
	h.mu.Unlock()
}

// ServeHTTP reads the settings fresh in a single query (no cache); only the
// activity summary is cached. Missing rows are treated as unset, not an error;
// an unknown stored mode falls back to default.
func (h *HomeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vals, err := h.store.GetSettings(r.Context(),
		settingRegistrationsEnabled, settingHomeRedirectURL, settingHomeMode, settingHomeTitle, settingHomeAbout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	resp := homeResponse{
		RegistrationsEnabled: vals[settingRegistrationsEnabled] == "true",
		RedirectURL:          vals[settingHomeRedirectURL],
		Mode:                 vals[settingHomeMode],
		Title:                vals[settingHomeTitle],
		About:                vals[settingHomeAbout],
		Portiko:              h.portiko,
	}
	if validHomeMode(resp.Mode) != nil {
		resp.Mode = homeModeDefault
	}
	if resp.Mode == homeModeActivity && h.activity != nil {
		a, err := h.activitySummary(r.Context())
		if err != nil {
			if !errors.Is(err, errActivityBackoff) && !errors.Is(err, context.Canceled) {
				slog.Error("public home: activity summary failed", "error", err)
			}
		} else {
			resp.Activity = a
		}
	}
	if h.hero != nil {
		a, err := h.hero.store.HeroMeta(r.Context())
		switch {
		case err == nil:
			resp.Hero = heroInfoFor(a)
		case errors.Is(err, siteassets.ErrNotFound):
		default:
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, resp)
}
