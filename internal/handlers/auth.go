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

// AuthHandler serves the passkey registration ceremony routes:
//
//	POST /auth/register/start   — submit email, send magic link
//	GET  /auth/register/verify  — validate token, return WebAuthn options
//	POST /auth/register/finish  — submit attestation, create account + session
//
// Login (#0016) and recovery (#0017) routes will be added to this handler (or a
// sibling) reusing the same service and JSON helpers.
type AuthHandler struct {
	reg registrar
}

// NewAuthHandler constructs an AuthHandler over the registration service.
func NewAuthHandler(reg registrar) *AuthHandler {
	return &AuthHandler{reg: reg}
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
