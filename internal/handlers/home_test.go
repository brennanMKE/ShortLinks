package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

type fakeHomeReader struct {
	enabled    bool
	enabledErr error
	url        string
	urlErr     error
	// extra overrides per-key values for the #0174 settings; a key absent from
	// the map falls through to url/urlErr (the behaviour #0125 tested).
	extra map[string]string
}

func (f fakeHomeReader) RegistrationsEnabled(context.Context) (bool, error) {
	return f.enabled, f.enabledErr
}
func (f fakeHomeReader) GetSetting(_ context.Context, key string) (string, error) {
	if key != settingHomeRedirectURL {
		if v, ok := f.extra[key]; ok {
			return v, nil
		}
		if f.urlErr != nil && !errors.Is(f.urlErr, auth.ErrSettingNotFound) {
			return "", f.urlErr
		}
		return "", auth.ErrSettingNotFound
	}
	return f.url, f.urlErr
}

// serveHome mounts the handler on a bare mux (no auth middleware, no cookie),
// the way main.go does.
func serveHome(f fakeHomeReader) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	mux.Handle("GET /api/public/home", NewHomeHandler(f))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/home", nil))
	return rec
}

func TestHomeHandler_ExactKeysNoStoreNoAuth(t *testing.T) {
	rec := serveHome(fakeHomeReader{enabled: false, url: "https://www.example.com/"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The key set is pinned: nothing else from settings may leak. No activity
	// key outside activity mode (#0175).
	want := []string{"about", "hero", "mode", "redirect_url", "registrations_enabled", "title"}
	var got []string
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if m["redirect_url"] != "https://www.example.com/" || m["registrations_enabled"] != false {
		t.Errorf("body = %v", m)
	}
	// Unset home settings: default mode, empty text, no hero.
	if m["mode"] != "default" || m["title"] != "" || m["about"] != "" {
		t.Errorf("home content = %v, want default/empty/empty", m)
	}
	if v, ok := m["hero"]; !ok || v != nil {
		t.Errorf("hero = %v (present=%v), want null", v, ok)
	}
}

func TestHomeHandler_ReturnsConfiguredContent(t *testing.T) {
	rec := serveHome(fakeHomeReader{enabled: true, extra: map[string]string{
		"home_mode": "activity", "home_title": "Maker links", "home_about": "Hello\n\nhttps://example.org",
	}})
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["mode"] != "activity" || m["title"] != "Maker links" || m["about"] != "Hello\n\nhttps://example.org" {
		t.Errorf("body = %v", m)
	}
}

func TestHomeHandler_UnknownStoredModeFallsBackToDefault(t *testing.T) {
	rec := serveHome(fakeHomeReader{extra: map[string]string{"home_mode": "bogus"}})
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["mode"] != "default" {
		t.Errorf("mode = %v, want default", m["mode"])
	}
}

func TestHomeHandler_SettingNotFoundIsEmpty(t *testing.T) {
	rec := serveHome(fakeHomeReader{enabled: true, urlErr: auth.ErrSettingNotFound})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["redirect_url"] != "" || m["registrations_enabled"] != true {
		t.Errorf("body = %v, want empty url and enabled", m)
	}
}

func TestHomeHandler_ErrorsAre500(t *testing.T) {
	if rec := serveHome(fakeHomeReader{enabledErr: errors.New("boom")}); rec.Code != http.StatusInternalServerError {
		t.Errorf("registrations error status = %d, want 500", rec.Code)
	}
	if rec := serveHome(fakeHomeReader{urlErr: errors.New("boom")}); rec.Code != http.StatusInternalServerError {
		t.Errorf("setting error status = %d, want 500", rec.Code)
	}
}
