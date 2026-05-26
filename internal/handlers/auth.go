package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/brennanMKE/ShortLinks/internal/auth"
)

// maxAuthBodyBytes caps request bodies for the auth endpoints. The attestation
// on finish is the largest payload and is comfortably under this limit.
const maxAuthBodyBytes = 1 << 20 // 1 MiB

// registrar is the behavior the auth handler needs from the registration
// service. Depending on the interface (rather than the concrete
// *auth.RegistrationService) keeps the handler unit-testable with a fake.
type registrar interface {
	StartRegistration(ctx context.Context, email string) error
	VerifyRegistration(ctx context.Context, token string) (*protocol.CredentialCreation, error)
	FinishRegistration(ctx context.Context, token, deviceName string, r *http.Request) (auth.FinishResult, error)
}

// authenticator is the behavior the auth handler needs from the login service.
// As with registrar, depending on an interface keeps the handler unit-testable
// with a fake.
type authenticator interface {
	StartLogin(ctx context.Context, email string) (*protocol.CredentialAssertion, error)
	FinishLogin(ctx context.Context, r *http.Request) (auth.LoginResult, error)
	Logout(ctx context.Context, token string) error
}

// recoverer is the behavior the auth handler needs from the recovery service.
// As with registrar/authenticator, depending on an interface keeps the handler
// unit-testable with a fake.
type recoverer interface {
	StartRecovery(ctx context.Context, email string) error
	VerifyRecovery(ctx context.Context, token string) (*protocol.CredentialCreation, error)
	FinishRecovery(ctx context.Context, token, deviceName string, r *http.Request) (auth.RecoveryResult, error)
}

// AuthHandler serves the passkey registration, login, and recovery ceremony
// routes:
//
//	POST /auth/register/start   — submit email, send magic link
//	GET  /auth/register/verify  — validate token, return WebAuthn options
//	POST /auth/register/finish  — submit attestation, create account + session
//	GET  /auth/login/start      — issue an assertion challenge (optional ?email=)
//	POST /auth/login/finish     — submit assertion, verify, create session
//	POST /auth/logout           — delete the session, clear the cookie
//	POST /auth/recover          — submit email, send recovery link (generic 200)
//	GET  /auth/recover/verify   — validate recovery token, return WebAuthn options
//	POST /auth/recover/finish   — submit attestation, add credential + session
type AuthHandler struct {
	reg      registrar
	login    authenticator
	recovery recoverer
}

// NewAuthHandler constructs an AuthHandler over the registration, login, and
// recovery services. Any dependency may be nil where only a subset of routes is
// exercised (e.g. existing registration handler tests pass nil for login and
// recovery).
func NewAuthHandler(reg registrar, login authenticator, recovery recoverer) *AuthHandler {
	return &AuthHandler{reg: reg, login: login, recovery: recovery}
}

// startRequest is the POST /auth/register/start body.
type startRequest struct {
	Email string `json:"email"`
}

// RegisterStart handles POST /auth/register/start.
func (h *AuthHandler) RegisterStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	err := h.reg.StartRegistration(r.Context(), req.Email)
	switch {
	case err == nil:
		// Success — always the same generic message.
	case errors.Is(err, auth.ErrRegistrationsDisabled):
		writeError(w, http.StatusForbidden, "Registration closed")
		return
	case errors.Is(err, auth.ErrInvalidEmail):
		writeError(w, http.StatusBadRequest, "invalid email")
		return
	case errors.Is(err, auth.ErrEmailRegistered):
		// Do not reveal whether the email is already registered: respond as if
		// the email was sent. This avoids leaking account existence.
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Check your email"})
}

// RegisterVerify handles GET /auth/register/verify?token=...
func (h *AuthHandler) RegisterVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing token")
		return
	}

	creation, err := h.reg.VerifyRegistration(r.Context(), token)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, creation)
	case errors.Is(err, auth.ErrTokenInvalid):
		writeError(w, http.StatusBadRequest, "token invalid or expired")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// RegisterFinish handles POST /auth/register/finish. The body is the WebAuthn
// attestation; the magic-link token is taken from the query string so the
// attestation JSON is passed to FinishRegistration untouched. An optional
// device_name query parameter labels the credential.
func (h *AuthHandler) RegisterFinish(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing token")
		return
	}
	deviceName := r.URL.Query().Get("device_name")

	// Cap and buffer the body so FinishRegistration can read the attestation
	// from a fresh reader (the service re-parses r.Body internally).
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	result, err := h.reg.FinishRegistration(r.Context(), token, deviceName, r)
	switch {
	case err == nil:
		auth.SetSessionCookie(w, result.SessionToken, result.SessionExpires)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":       result.User.ID,
			"email":    result.User.Email,
			"is_admin": result.User.IsAdmin,
		})
	case errors.Is(err, auth.ErrTokenInvalid):
		writeError(w, http.StatusBadRequest, "token invalid or expired")
	default:
		// A failed attestation verification or any other error: do not leak
		// detail to the client.
		writeError(w, http.StatusBadRequest, "registration failed")
	}
}

// loginStartRequest is the optional POST /auth/login/start body. The email may
// also arrive as a ?email= query parameter; either is optional.
type loginStartRequest struct {
	Email string `json:"email"`
}

// LoginStart handles GET (or POST) /auth/login/start. An optional email narrows
// the prompt via allowCredentials; absent it, a discoverable (conditional-UI)
// challenge is issued. The response is the PublicKeyCredentialRequestOptions
// JSON and is identical regardless of whether the email is registered, so it
// never leaks account existence.
func (h *AuthHandler) LoginStart(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	// Accept an optional JSON body too, but only if one was actually sent.
	if email == "" && r.Body != nil && r.ContentLength != 0 {
		var req loginStartRequest
		if err := decodeJSON(w, r, &req); err == nil {
			email = req.Email
		}
	}

	assertion, err := h.login.StartLogin(r.Context(), email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, assertion)
}

// LoginFinish handles POST /auth/login/finish. The body is the WebAuthn
// assertion. On success it sets the session cookie and returns 200; a
// deactivated account yields 403; any verification failure yields a generic 401.
func (h *AuthHandler) LoginFinish(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)

	result, err := h.login.FinishLogin(r.Context(), r)
	switch {
	case err == nil:
		auth.SetSessionCookie(w, result.SessionToken, result.SessionExpires)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": result.UserID})
	case errors.Is(err, auth.ErrAccountDeactivated):
		writeError(w, http.StatusForbidden, "Account deactivated")
	default:
		// Unknown credential, bad signature, consumed/expired challenge, ...:
		// never reveal which case occurred.
		writeError(w, http.StatusUnauthorized, "authentication failed")
	}
}

// Logout handles POST /auth/logout. It deletes the session row for the cookie
// (if present) and clears the cookie. It is idempotent and always returns 200.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		if derr := h.login.Logout(r.Context(), c.Value); derr != nil {
			writeError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"message": "Signed out"})
}

// recoverRequest is the POST /auth/recover body.
type recoverRequest struct {
	Email string `json:"email"`
}

// recoverGenericMessage is the single response returned by RecoverStart in
// every case (account exists or not) so the endpoint never leaks which emails
// are registered.
const recoverGenericMessage = "If that email is registered, a recovery link has been sent"

// RecoverStart handles POST /auth/recover. It accepts an email and, only when
// the account exists and is active, sends a single-use recovery link. The
// response is always the same generic 200 to prevent account enumeration; only
// a genuine infrastructure error yields a 500.
func (h *AuthHandler) RecoverStart(w http.ResponseWriter, r *http.Request) {
	var req recoverRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.recovery.StartRecovery(r.Context(), req.Email); err != nil {
		// The service swallows unknown/inactive/invalid emails (returning nil);
		// any error here is a real failure (token creation or mail delivery).
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": recoverGenericMessage})
}

// RecoverVerify handles GET /auth/recover/verify?token=... It validates the
// recovery token and returns the WebAuthn options for adding a new credential
// to the existing account.
func (h *AuthHandler) RecoverVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing token")
		return
	}

	creation, err := h.recovery.VerifyRecovery(r.Context(), token)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, creation)
	case errors.Is(err, auth.ErrTokenInvalid):
		writeError(w, http.StatusBadRequest, "token invalid or expired")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// RecoverFinish handles POST /auth/recover/finish. The body is the WebAuthn
// attestation; the recovery token is taken from the query string so the
// attestation JSON is passed to FinishRecovery untouched. An optional
// device_name query parameter labels the new credential. On success it adds the
// credential to the existing account, sets the session cookie, and returns 200.
func (h *AuthHandler) RecoverFinish(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "missing token")
		return
	}
	deviceName := r.URL.Query().Get("device_name")

	// Cap and buffer the body so FinishRecovery can read the attestation from a
	// fresh reader (the service re-parses r.Body internally).
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	result, err := h.recovery.FinishRecovery(r.Context(), token, deviceName, r)
	switch {
	case err == nil:
		auth.SetSessionCookie(w, result.SessionToken, result.SessionExpires)
		writeJSON(w, http.StatusOK, map[string]any{"user_id": result.UserID})
	case errors.Is(err, auth.ErrTokenInvalid):
		writeError(w, http.StatusBadRequest, "token invalid or expired")
	default:
		// A failed attestation verification or any other error: do not leak detail.
		writeError(w, http.StatusBadRequest, "recovery failed")
	}
}

// clearSessionCookie expires the session cookie in the browser, mirroring the
// attributes set by auth.SetSessionCookie so the deletion is accepted.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// decodeJSON reads a JSON body into v with a size cap, rejecting unknown fields
// and trailing data so malformed requests fail cleanly.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}

// writeJSON writes v as a JSON response with the given status code. Encoding a
// fixed-shape value cannot meaningfully fail; the error is ignored so a second
// header write is never attempted.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope: {"error":"<message>"}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
