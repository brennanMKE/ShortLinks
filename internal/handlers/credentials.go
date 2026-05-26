package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// credentialStore is the behavior the credentials handler needs from the data
// layer. Depending on an interface (rather than the concrete *auth.Store) keeps
// the handler unit-testable with a fake, mirroring the AuthHandler pattern.
//
// *auth.Store satisfies it via ListCredentialsForUser, RenameCredential, and
// RevokeCredential. Every method takes the authenticated user id so the store
// can scope each query to the caller's own rows — the handler never trusts a
// client-supplied owner.
type credentialStore interface {
	ListCredentialsForUser(ctx context.Context, userID int64) ([]auth.ManagedCredential, error)
	RenameCredential(ctx context.Context, userID, id int64, deviceName string) error
	RevokeCredential(ctx context.Context, userID, id int64) error
}

// CredentialsHandler serves the authenticated passkey-management routes:
//
//	GET    /account/credentials       — list the caller's registered passkeys
//	PATCH  /account/credentials/{id}  — rename one of the caller's passkeys
//	DELETE /account/credentials/{id}  — revoke one of the caller's passkeys
//
// All three routes MUST be mounted behind middleware.RequireSession; each
// handler reads the authenticated user from the request context and scopes
// every store query to that user, so a request can only ever see or mutate its
// own credentials.
type CredentialsHandler struct {
	store credentialStore
}

// NewCredentialsHandler constructs a CredentialsHandler over the data layer.
func NewCredentialsHandler(store credentialStore) *CredentialsHandler {
	return &CredentialsHandler{store: store}
}

// credentialView is the JSON shape returned for a single passkey. It carries
// only display metadata — never the public_key or raw credential_id bytes. The
// device_hint is derived from the AAGUID via a small known-authenticator map.
type credentialView struct {
	ID         int64      `json:"id"`
	DeviceName string     `json:"device_name"`
	AAGUID     string     `json:"aaguid"`
	DeviceHint string     `json:"device_hint"`
	SignCount  int64      `json:"sign_count"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// List handles GET /account/credentials. It returns the authenticated user's
// passkeys as a JSON array of credentialView. The list is scoped to the caller
// inside the store (WHERE user_id = <ctx user>), so it can never include another
// account's credentials.
func (h *CredentialsHandler) List(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	creds, err := h.store.ListCredentialsForUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Always emit an array (never null) so the client gets a stable shape even
	// for an account with zero credentials.
	views := make([]credentialView, 0, len(creds))
	for _, c := range creds {
		views = append(views, credentialView{
			ID:         c.ID,
			DeviceName: c.DeviceName,
			AAGUID:     c.AAGUID,
			DeviceHint: deviceHint(c.AAGUID),
			SignCount:  c.SignCount,
			CreatedAt:  c.CreatedAt,
			LastUsedAt: c.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, views)
}

// renameRequest is the PATCH /account/credentials/{id} body. The PRD/issue label
// the field device_name; name is accepted as a convenience alias so the client
// may send either.
type renameRequest struct {
	DeviceName string `json:"device_name"`
	Name       string `json:"name"`
}

// Rename handles PATCH /account/credentials/{id}. It updates device_name for one
// of the caller's own credentials and returns the updated credential as JSON.
// A credential that does not belong to the caller (or does not exist) yields
// 404 — the same response in both cases so the endpoint never reveals that
// someone else's credential id is valid.
func (h *CredentialsHandler) Rename(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	id, ok := parseCredentialID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	var req renameRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := req.DeviceName
	if name == "" {
		name = req.Name
	}
	name = strings.TrimSpace(name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "device_name is required")
		return
	}

	err := h.store.RenameCredential(r.Context(), u.ID, id, name)
	switch {
	case err == nil:
		// fall through to return the updated record.
	case errors.Is(err, auth.ErrCredentialNotFound):
		writeError(w, http.StatusNotFound, "credential not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Re-read the caller's credentials and return the renamed one so the client
	// gets the authoritative updated record (with the device hint resolved).
	creds, err := h.store.ListCredentialsForUser(r.Context(), u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	for _, c := range creds {
		if c.ID == id {
			writeJSON(w, http.StatusOK, credentialView{
				ID:         c.ID,
				DeviceName: c.DeviceName,
				AAGUID:     c.AAGUID,
				DeviceHint: deviceHint(c.AAGUID),
				SignCount:  c.SignCount,
				CreatedAt:  c.CreatedAt,
				LastUsedAt: c.LastUsedAt,
			})
			return
		}
	}
	// The row was renamed but vanished from the re-read (concurrent revoke):
	// treat as not found rather than returning a stale view.
	writeError(w, http.StatusNotFound, "credential not found")
}

// Revoke handles DELETE /account/credentials/{id}. It deletes one of the
// caller's own credentials. A credential that does not belong to the caller (or
// does not exist) yields 404. If the credential is the account's only remaining
// passkey the request is refused with 409 {"error":"cannot_revoke_last_credential"}
// — the user must register a replacement first, per the PRD.
//
// NOTE: the credential.revoked audit-log entry is #0025 and is not written here
// yet (no-op until that issue lands).
func (h *CredentialsHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}

	id, ok := parseCredentialID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	err := h.store.RevokeCredential(r.Context(), u.ID, id)
	switch {
	case err == nil:
		// TODO(#0025): write credential.revoked audit-log entry here.
		writeJSON(w, http.StatusOK, map[string]string{"message": "Credential revoked"})
	case errors.Is(err, auth.ErrCredentialNotFound):
		writeError(w, http.StatusNotFound, "credential not found")
	case errors.Is(err, auth.ErrLastCredential):
		writeError(w, http.StatusConflict, "cannot_revoke_last_credential")
	default:
		writeError(w, http.StatusInternalServerError, "internal server error")
	}
}

// parseCredentialID reads the {id} path value (Go 1.22 routing) and parses it as
// a positive int64. It reports ok=false for a missing, non-numeric, or
// non-positive id.
func parseCredentialID(r *http.Request) (int64, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// knownAuthenticators maps a handful of well-known AAGUIDs to a friendly device
// hint. This is intentionally a small, best-effort lookup — not an exhaustive
// registry. Unknown or absent AAGUIDs fall back to a generic label in
// deviceHint.
var knownAuthenticators = map[string]string{
	// Apple platform authenticators (iCloud Keychain).
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"00000000-0000-0000-0000-000000000000": "Platform authenticator",
	// Google Password Manager / Android.
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	// Windows Hello.
	"08987058-cadc-4b81-b6e1-30de50dcbe96": "Windows Hello",
	"9ddd1817-af5a-4672-a2b9-3e3dd95000a9": "Windows Hello",
	// YubiKey family.
	"ee882879-721c-4913-9775-3dfcce97072a": "YubiKey 5",
	"fa2b99dc-9e39-4257-8f92-4a30d23c4118": "YubiKey 5 (NFC)",
	"2fc0579f-8113-47ea-b116-bb5a8db9202a": "YubiKey 5 (NFC)",
	"cb69481e-8ff7-4039-93ec-0a2729a154a8": "YubiKey 5",
}

// deviceHint maps an AAGUID string to a human-readable authenticator label,
// falling back to "Unknown" when the AAGUID is absent/all-zero-equivalent and to
// the raw AAGUID string when it is present but unrecognized. A simple mapping is
// sufficient here; the goal is a useful hint, not an exhaustive registry.
func deviceHint(aaguid string) string {
	if aaguid == "" {
		return "Unknown"
	}
	if hint, ok := knownAuthenticators[aaguid]; ok {
		return hint
	}
	return aaguid
}
