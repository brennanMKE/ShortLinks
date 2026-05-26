package handlers

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeAuthenticator is an in-memory login service for handler tests.
type fakeAuthenticator struct {
	startResp *protocol.CredentialAssertion
	startErr  error
	startGot  string

	finishResult auth.LoginResult
	finishErr    error

	logoutGot string
	logoutErr error
}

func (f *fakeAuthenticator) StartLogin(_ context.Context, email string) (*protocol.CredentialAssertion, error) {
	f.startGot = email
	return f.startResp, f.startErr
}

func (f *fakeAuthenticator) FinishLogin(_ context.Context, _ *http.Request) (auth.LoginResult, error) {
	return f.finishResult, f.finishErr
}

func (f *fakeAuthenticator) Logout(_ context.Context, token string) error {
	f.logoutGot = token
	return f.logoutErr
}

// fakeRecoverer is an in-memory recovery service for handler tests.
type fakeRecoverer struct {
	startErr error
	startGot string

	verifyResp *protocol.CredentialCreation
	verifyErr  error
	verifyGot  string

	finishResult auth.RecoveryResult
	finishErr    error
	finishToken  string
	finishDevice string
}

func (f *fakeRecoverer) StartRecovery(_ context.Context, email string) error {
	f.startGot = email
	return f.startErr
}

func (f *fakeRecoverer) VerifyRecovery(_ context.Context, token string) (*protocol.CredentialCreation, error) {
	f.verifyGot = token
	return f.verifyResp, f.verifyErr
}

func (f *fakeRecoverer) FinishRecovery(_ context.Context, token, deviceName string, _ *http.Request) (auth.RecoveryResult, error) {
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(&fakeRegistrar{}, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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
	h := NewAuthHandler(&fakeRegistrar{}, nil, nil)
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
	h := NewAuthHandler(f, nil, nil)
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

// TestLoginStart_PassesEmailAndReturnsOptions asserts the email query param
// reaches the service and the assertion options are returned as JSON.
func TestLoginStart_PassesEmailAndReturnsOptions(t *testing.T) {
	assertion := &protocol.CredentialAssertion{}
	assertion.Response.Challenge = protocol.URLEncodedBase64("challenge-bytes")
	a := &fakeAuthenticator{startResp: assertion}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/login/start?email=alice@example.com", nil)

	h.LoginStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if a.startGot != "alice@example.com" {
		t.Errorf("service received email %q", a.startGot)
	}
	if _, ok := decodeBody(t, rr)["publicKey"]; !ok {
		t.Errorf("response missing publicKey object: %s", rr.Body.String())
	}
}

// TestLoginStart_NoEmailDiscoverable asserts a start with no email still returns
// generic options (conditional UI / discoverable login).
func TestLoginStart_NoEmailDiscoverable(t *testing.T) {
	a := &fakeAuthenticator{startResp: &protocol.CredentialAssertion{}}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/login/start", nil)

	h.LoginStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if a.startGot != "" {
		t.Errorf("service received email %q, want empty", a.startGot)
	}
}

// TestLoginFinish_Success asserts 200, the session cookie attributes, and the
// user_id JSON.
func TestLoginFinish_Success(t *testing.T) {
	a := &fakeAuthenticator{finishResult: auth.LoginResult{
		UserID:         42,
		SessionToken:   "sess-token",
		SessionExpires: time.Now().Add(30 * 24 * time.Hour),
	}}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		strings.NewReader(`{"id":"x","response":{}}`))

	h.LoginFinish(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != auth.SessionCookieName || c.Value != "sess-token" {
		t.Errorf("cookie = %s=%s, want %s=sess-token", c.Name, c.Value, auth.SessionCookieName)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie security attrs wrong: HttpOnly=%v Secure=%v SameSite=%v", c.HttpOnly, c.Secure, c.SameSite)
	}
	if body := decodeBody(t, rr); body["user_id"] != float64(42) {
		t.Errorf("body user_id = %v, want 42", body["user_id"])
	}
}

// TestLoginFinish_Deactivated asserts a deactivated account yields 403 with the
// PRD message and no session cookie.
func TestLoginFinish_Deactivated(t *testing.T) {
	a := &fakeAuthenticator{finishErr: auth.ErrAccountDeactivated}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		strings.NewReader(`{}`))

	h.LoginFinish(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if msg := decodeBody(t, rr)["error"]; msg != "Account deactivated" {
		t.Errorf("error = %v, want %q", msg, "Account deactivated")
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("no session cookie should be set for a deactivated account")
	}
}

// TestLoginFinish_Failure asserts a generic verification failure yields 401 and
// no cookie.
func TestLoginFinish_Failure(t *testing.T) {
	a := &fakeAuthenticator{finishErr: auth.ErrLoginFailed}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login/finish",
		strings.NewReader(`{}`))

	h.LoginFinish(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("no session cookie should be set on a failed login")
	}
}

// TestLogout_DeletesAndClears asserts the cookie token reaches the service and
// an expiring cookie is set.
func TestLogout_DeletesAndClears(t *testing.T) {
	a := &fakeAuthenticator{}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok-to-delete"})

	h.Logout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if a.logoutGot != "tok-to-delete" {
		t.Errorf("service received token %q, want tok-to-delete", a.logoutGot)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 (clearing) cookie, got %d", len(cookies))
	}
	if c := cookies[0]; c.Name != auth.SessionCookieName || c.MaxAge != -1 {
		t.Errorf("clearing cookie = %s MaxAge=%d, want %s MaxAge=-1", c.Name, c.MaxAge, auth.SessionCookieName)
	}
}

// TestLogout_NoCookie asserts logout without a session cookie still returns 200
// and does not call the service.
func TestLogout_NoCookie(t *testing.T) {
	a := &fakeAuthenticator{}
	h := NewAuthHandler(&fakeRegistrar{}, a, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)

	h.Logout(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if a.logoutGot != "" {
		t.Errorf("service should not be called without a cookie; got %q", a.logoutGot)
	}
}

// TestRecoverStart_GenericSuccess asserts a 200 with the generic enumeration-safe
// message and that the email reaches the service.
func TestRecoverStart_GenericSuccess(t *testing.T) {
	f := &fakeRecoverer{}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover",
		strings.NewReader(`{"email":"alice@example.com"}`))

	h.RecoverStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if msg := decodeBody(t, rr)["message"]; msg != recoverGenericMessage {
		t.Errorf("message = %v, want %q", msg, recoverGenericMessage)
	}
	if f.startGot != "alice@example.com" {
		t.Errorf("service received email %q", f.startGot)
	}
}

// TestRecoverStart_UnknownLooksLikeSuccess asserts that even when the service
// swallows an unknown email (returning nil), the same generic 200 is returned —
// no account-existence leak.
func TestRecoverStart_UnknownLooksLikeSuccess(t *testing.T) {
	f := &fakeRecoverer{startErr: nil}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover",
		strings.NewReader(`{"email":"nobody@example.com"}`))

	h.RecoverStart(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no leak); body=%s", rr.Code, rr.Body.String())
	}
	if msg := decodeBody(t, rr)["message"]; msg != recoverGenericMessage {
		t.Errorf("message = %v, want generic", msg)
	}
}

// TestRecoverStart_BadBody asserts malformed JSON yields 400.
func TestRecoverStart_BadBody(t *testing.T) {
	h := NewAuthHandler(&fakeRegistrar{}, nil, &fakeRecoverer{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover",
		strings.NewReader(`{not json`))

	h.RecoverStart(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRecoverStart_InternalError asserts a genuine infrastructure failure yields
// 500 (the service only returns an error for real failures, never enumeration).
func TestRecoverStart_InternalError(t *testing.T) {
	f := &fakeRecoverer{startErr: errors.New("smtp down")}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover",
		strings.NewReader(`{"email":"alice@example.com"}`))

	h.RecoverStart(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// TestRecoverVerify_Success asserts the WebAuthn options are returned as JSON.
func TestRecoverVerify_Success(t *testing.T) {
	creation := &protocol.CredentialCreation{}
	creation.Response.Challenge = protocol.URLEncodedBase64("challenge-bytes")
	f := &fakeRecoverer{verifyResp: creation}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/recover/verify?token=rtok123", nil)

	h.RecoverVerify(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if f.verifyGot != "rtok123" {
		t.Errorf("service received token %q", f.verifyGot)
	}
	if _, ok := decodeBody(t, rr)["publicKey"]; !ok {
		t.Errorf("response missing publicKey object: %s", rr.Body.String())
	}
}

// TestRecoverVerify_MissingToken asserts 400 when no token is supplied.
func TestRecoverVerify_MissingToken(t *testing.T) {
	h := NewAuthHandler(&fakeRegistrar{}, nil, &fakeRecoverer{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/recover/verify", nil)

	h.RecoverVerify(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRecoverVerify_InvalidToken asserts 400 for an unknown/expired token.
func TestRecoverVerify_InvalidToken(t *testing.T) {
	f := &fakeRecoverer{verifyErr: auth.ErrTokenInvalid}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/recover/verify?token=bad", nil)

	h.RecoverVerify(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRecoverFinish_Success asserts 200, the session cookie attributes, the
// user_id JSON, and that token + device_name reach the service.
func TestRecoverFinish_Success(t *testing.T) {
	f := &fakeRecoverer{finishResult: auth.RecoveryResult{
		UserID:         9,
		SessionToken:   "sess-token",
		SessionExpires: time.Now().Add(30 * 24 * time.Hour),
	}}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/auth/recover/finish?token=rtok123&device_name=NewKey",
		strings.NewReader(`{"id":"x","response":{}}`))

	h.RecoverFinish(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if f.finishToken != "rtok123" || f.finishDevice != "NewKey" {
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
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie security attrs wrong: HttpOnly=%v Secure=%v SameSite=%v", c.HttpOnly, c.Secure, c.SameSite)
	}
	if body := decodeBody(t, rr); body["user_id"] != float64(9) {
		t.Errorf("body user_id = %v, want 9", body["user_id"])
	}
}

// TestRecoverFinish_MissingToken asserts 400 when no token is supplied.
func TestRecoverFinish_MissingToken(t *testing.T) {
	h := NewAuthHandler(&fakeRegistrar{}, nil, &fakeRecoverer{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover/finish",
		strings.NewReader(`{}`))

	h.RecoverFinish(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestRecoverFinish_InvalidToken asserts 400 when the challenge/token is gone,
// and that no session cookie is set on failure.
func TestRecoverFinish_InvalidToken(t *testing.T) {
	f := &fakeRecoverer{finishErr: auth.ErrTokenInvalid}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover/finish?token=bad",
		strings.NewReader(`{}`))

	h.RecoverFinish(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Error("no session cookie should be set on a failed recovery finish")
	}
}
