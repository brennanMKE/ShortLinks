package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

type fakeHomeReader struct {
	enabled    bool
	enabledErr error
	url        string
	urlErr     error
}

func (f fakeHomeReader) RegistrationsEnabled(context.Context) (bool, error) {
	return f.enabled, f.enabledErr
}
func (f fakeHomeReader) GetSetting(context.Context, string) (string, error) {
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

func TestHomeHandler_TwoKeysNoStoreNoAuth(t *testing.T) {
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
	if len(m) != 2 {
		t.Errorf("keys = %v, want exactly 2", m)
	}
	if m["redirect_url"] != "https://www.example.com/" || m["registrations_enabled"] != false {
		t.Errorf("body = %v", m)
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
