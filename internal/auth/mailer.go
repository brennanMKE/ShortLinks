package auth

import (
	"context"
	"log"
)

// Mailer sends transactional emails for the authentication flows: magic-link
// verification during registration and single-use recovery links.
//
// The interface is intentionally narrow so the transport can be swapped in
// tests (see NoOpMailer) and so callers in the auth package need not know
// whether email is delivered via SES SMTP, a fake, or stdout.
type Mailer interface {
	// SendVerification sends a registration magic-link email to toEmail. The
	// body contains the link {BASE_URL}/register/verify?token={token}, which is
	// an SPA browser path (not an /auth/* API endpoint) that loads the Svelte
	// app. The SPA then calls GET /auth/register/verify?token={token} to fetch
	// WebAuthn creation options.
	SendVerification(ctx context.Context, toEmail, token string) error

	// SendRecovery sends an account-recovery email to toEmail. The body
	// contains the link {BASE_URL}/recover/verify?token={token}, which is an
	// SPA browser path. The SPA calls GET /auth/recover/verify?token={token}
	// for WebAuthn creation options.
	SendRecovery(ctx context.Context, toEmail, token string) error
}

// NoOpMailer is a Mailer that does not send anything. It logs the would-be
// recipient and link to stdout, which makes local development and tests usable
// without any SES credentials or network access.
type NoOpMailer struct {
	// BaseURL is used to render the link in the log line so developers can copy
	// it into a browser. If empty, only the token is logged.
	BaseURL string
}

// SendVerification logs the verification link instead of sending it.
func (m NoOpMailer) SendVerification(_ context.Context, toEmail, token string) error {
	log.Printf("NoOpMailer: verification email to %s: %s", toEmail, verificationURL(m.BaseURL, token))
	return nil
}

// SendRecovery logs the recovery link instead of sending it.
func (m NoOpMailer) SendRecovery(_ context.Context, toEmail, token string) error {
	log.Printf("NoOpMailer: recovery email to %s: %s", toEmail, recoveryURL(m.BaseURL, token))
	return nil
}

// verificationURL builds the registration magic-link URL.
//
// The path /register/verify is an SPA route (not an /auth/* API path), so the
// Go mux catch-all "GET /" serves index.html and the Svelte app reads the token
// from the query string. The JSON creation-options endpoint remains at
// GET /auth/register/verify and is called by the SPA after landing.
func verificationURL(baseURL, token string) string {
	return baseURL + "/register/verify?token=" + token
}

// recoveryURL builds the account-recovery magic-link URL.
//
// Same scheme as verificationURL: /recover/verify is an SPA route that falls
// through to index.html; the JSON options are at GET /auth/recover/verify.
func recoveryURL(baseURL, token string) string {
	return baseURL + "/recover/verify?token=" + token
}
