package auth

import (
	"fmt"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// userHandleLen is the length, in bytes, of the random WebAuthn user handle.
// The PRD fixes this at 16 bytes ("user.id = random 16 bytes"). It is the
// opaque value the authenticator associates with the discoverable credential;
// it is intentionally not derived from the email.
const userHandleLen = 16

// NewWebAuthn constructs the relying-party WebAuthn instance from configuration.
// The RP ID and origin come from WEBAUTHN_RP_ID / WEBAUTHN_RP_ORIGIN so the
// service is portable to any domain without a code change.
//
// This is the single construction point reused by the registration,
// authentication (#0016), and recovery (#0017) ceremonies; build it once at
// startup and inject it into the auth service.
func NewWebAuthn(cfg *config.Config) (*webauthn.WebAuthn, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.WebAuthnRPID,
		RPDisplayName: "ShortLinks",
		RPOrigins:     []string{cfg.WebAuthnRPOrigin},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: configuring webauthn: %w", err)
	}
	return wa, nil
}

// RegistrationUser is the webauthn.User implementation used during the
// registration ceremony, before any users row exists. The handle is a freshly
// generated random 16-byte value and the credential list is empty (a brand-new
// account has no passkeys yet).
//
// A separate type backed by stored credentials is introduced for the
// authentication ceremony in #0016; both satisfy webauthn.User so they can be
// passed interchangeably to the go-webauthn Begin*/Finish* calls.
type RegistrationUser struct {
	handle []byte
	email  string
}

// NewRegistrationUser builds a registration user with a fresh random handle for
// the given email.
func NewRegistrationUser(email string) (*RegistrationUser, error) {
	handle, err := randomBytes(userHandleLen)
	if err != nil {
		return nil, err
	}
	return &RegistrationUser{handle: handle, email: email}, nil
}

// registrationUserFromHandle reconstructs a registration user from a previously
// generated handle. It is used on the finish leg, where the handle issued at
// the verify step must be replayed unchanged for FinishRegistration to verify
// the attestation against the same user.id.
func registrationUserFromHandle(handle []byte, email string) *RegistrationUser {
	return &RegistrationUser{handle: handle, email: email}
}

// WebAuthnID returns the random 16-byte user handle (not derived from email).
func (u *RegistrationUser) WebAuthnID() []byte { return u.handle }

// WebAuthnName returns the email, used as the human-palatable account name.
func (u *RegistrationUser) WebAuthnName() string { return u.email }

// WebAuthnDisplayName returns the email, used as the display name.
func (u *RegistrationUser) WebAuthnDisplayName() string { return u.email }

// WebAuthnCredentials returns no credentials: a new registration starts with an
// empty credential set.
func (u *RegistrationUser) WebAuthnCredentials() []webauthn.Credential { return nil }

// WebAuthnIcon returns an empty icon URL, per the PRD.
func (u *RegistrationUser) WebAuthnIcon() string { return "" }

// registrationOptions returns the RegistrationOptions enforcing the PRD's
// passkey policy:
//   - residentKey "required" + userVerification "required" → a true
//     discoverable passkey,
//   - authenticatorAttachment intentionally omitted so the platform (e.g.
//     iCloud Keychain) chooses the authenticator,
//   - pubKeyCredParams ES256 (-7) preferred, RS256 (-257) accepted.
//
// It is shared so the verify handler and any test produce identical options.
func registrationOptions() []webauthn.RegistrationOption {
	return []webauthn.RegistrationOption{
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			// AuthenticatorAttachment deliberately left at its zero value so the
			// field is omitted from the JSON sent to the browser.
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithCredentialParameters([]protocol.CredentialParameter{
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgES256},
			{Type: protocol.PublicKeyCredentialType, Algorithm: webauthncose.AlgRS256},
		}),
	}
}
