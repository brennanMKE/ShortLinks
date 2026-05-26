package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// fakeRegistrar is an in-memory registrar for handler tests. Each field lets a
// test stub the corresponding service method's behavior.
type fakeRegistrar struct {
	startErr error
	startGot string

	verifyResp *protocol.CredentialCreation
	verifyErr  error
	verifyGot  string

	finishResult auth.FinishResult
	finishErr    error
	finishToken  string
	finishDevice string
}

func (f *fakeRegistrar) StartRegistration(_ context.Context, email string) error {
	f.startGot = email
	return f.startErr
}

func (f *fakeRegistrar) VerifyRegistration(_ context.Context, token string) (*protocol.CredentialCreation, error) {
	f.verifyGot = token
	return f.verifyResp, f.verifyErr
}

func (f *fakeRegistrar) FinishRegistration(_ context.Context, token, deviceName string, _ *http.Request) (auth.FinishResult, error) {
	f.finishToken = token
	f.finishDevice = deviceName
	return f.finishResult, f.finishErr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode response body %q: %v", rr.Body.String(), err)
	}
	return m
}

// TestRegisterStart_Success asserts a 200 with the canonical message and that
// the email reaches the service.
func TestRegisterStart_Success(t *testing.T) {
	f := &fakeRegistrar{}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
		strings.NewReader(`{"email":"alice@example.com"}`))

	h.RegisterStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if msg := decodeBody(t, rr)["message"]; msg != "Check your email" {
		t.Errorf("message = %v, want %q", msg, "Check your email")
	}
	if f.startGot != "alice@example.com" {
		t.Errorf("service received email %q", f.startGot)
	}
}

// TestRegisterStart_Disabled asserts a 403 when registrations are closed.
func TestRegisterStart_Disabled(t *testing.T) {
	f := &fakeRegistrar{startErr: auth.ErrRegistrationsDisabled}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
		strings.NewReader(`{"email":"alice@example.com"}`))

	h.RegisterStart(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// TestRegisterStart_DuplicateLooksLikeSuccess asserts an already-registered
// email yields the same 200 message (no account-existence leak).
func TestRegisterStart_DuplicateLooksLikeSuccess(t *testing.T) {
	f := &fakeRegistrar{startErr: auth.ErrEmailRegistered}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
		strings.NewReader(`{"email":"taken@example.com"}`))

	h.RegisterStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no leak); body=%s", rr.Code, rr.Body.String())
	}
	if msg := decodeBody(t, rr)["message"]; msg != "Check your email" {
		t.Errorf("message = %v, want generic success", msg)
	}
}

// TestRegisterStart_BadBody asserts malformed JSON yields 400.
func TestRegisterStart_BadBody(t *testing.T) {
	f := &fakeRegistrar{}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
		strings.NewReader(`{not json`))

	h.RegisterStart(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRegisterVerify_Success asserts the WebAuthn options are returned as JSON.
func TestRegisterVerify_Success(t *testing.T) {
	creation := &protocol.CredentialCreation{}
	creation.Response.Challenge = protocol.URLEncodedBase64("challenge-bytes")
	f := &fakeRegistrar{verifyResp: creation}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/register/verify?token=tok123", nil)

	h.RegisterVerify(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if f.verifyGot != "tok123" {
		t.Errorf("service received token %q", f.verifyGot)
	}
	body := decodeBody(t, rr)
	if _, ok := body["publicKey"]; !ok {
		t.Errorf("response missing publicKey object: %s", rr.Body.String())
	}
}

// TestRegisterVerify_MissingToken asserts 400 when no token is supplied.
func TestRegisterVerify_MissingToken(t *testing.T) {
	h := NewAuthHandler(&fakeRegistrar{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/register/verify", nil)

	h.RegisterVerify(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRegisterVerify_InvalidToken asserts 400 for an unknown/expired token.
func TestRegisterVerify_InvalidToken(t *testing.T) {
	f := &fakeRegistrar{verifyErr: auth.ErrTokenInvalid}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/register/verify?token=bad", nil)

	h.RegisterVerify(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRegisterFinish_Success asserts 200, the session cookie attributes, the
// user JSON, and that token + device_name reach the service.
func TestRegisterFinish_Success(t *testing.T) {
	f := &fakeRegistrar{finishResult: auth.FinishResult{
		User:           auth.CreatedUser{ID: 7, Email: "alice@example.com", IsAdmin: true},
		SessionToken:   "sess-token",
		SessionExpires: time.Now().Add(30 * 24 * time.Hour),
	}}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/auth/register/finish?token=tok123&device_name=MacBook",
		strings.NewReader(`{"id":"x","response":{}}`))

	h.RegisterFinish(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if f.finishToken != "tok123" || f.finishDevice != "MacBook" {
		t.Errorf("service got token=%q device=%q", f.finishToken, f.finishDevice)
	}

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.SessionCookieName || c.Value != "sess-token" {
		t.Errorf("cookie = %s=%s, want %s=sess-token", c.Name, c.Value, auth.SessionCookieName)
	}
	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if !c.Secure {
		t.Error("session cookie must be Secure")
	}
	if c.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie SameSite = %v, want Strict", c.SameSite)
	}

	body := decodeBody(t, rr)
	if body["email"] != "alice@example.com" || body["is_admin"] != true {
		t.Errorf("body = %v, want email + is_admin", body)
	}
}

// TestRegisterFinish_MissingToken asserts 400 when no token is supplied.
func TestRegisterFinish_MissingToken(t *testing.T) {
	h := NewAuthHandler(&fakeRegistrar{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish",
		strings.NewReader(`{}`))

	h.RegisterFinish(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRegisterFinish_InvalidToken asserts 400 when the challenge/token is gone.
func TestRegisterFinish_InvalidToken(t *testing.T) {
	f := &fakeRegistrar{finishErr: auth.ErrTokenInvalid}
	h := NewAuthHandler(f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/register/finish?token=bad",
		strings.NewReader(`{}`))

	h.RegisterFinish(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	// No cookie should be set on failure.
	if len(rr.Result().Cookies()) != 0 {
		t.Error("no session cookie should be set on failed finish")
	}
}
