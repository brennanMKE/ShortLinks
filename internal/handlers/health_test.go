package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakePinger is an in-memory Pinger for tests. A non-nil err simulates a
// database that is unreachable.
type fakePinger struct {
	err error
}

func (p *fakePinger) Ping(_ context.Context) error { return p.err }

func serveHealth(p Pinger) *httptest.ResponseRecorder {
	h := NewHealthHandler(p)
	mux := http.NewServeMux()
	mux.Handle("GET /health", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	return rec
}

func TestHealthDBUpReturns200(t *testing.T) {
	rec := serveHealth(&fakePinger{})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" || body.DB != "ok" {
		t.Errorf("body = %+v, want {status:ok db:ok}", body)
	}
	if body.Error != "" {
		t.Errorf("error field = %q, want empty on healthy response", body.Error)
	}
}

func TestHealthDBDownReturns503(t *testing.T) {
	rec := serveHealth(&fakePinger{err: errors.New("connection refused")})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "degraded" || body.DB != "error" {
		t.Errorf("body = %+v, want {status:degraded db:error}", body)
	}
	if body.Error != "connection refused" {
		t.Errorf("error field = %q, want %q", body.Error, "connection refused")
	}
}
