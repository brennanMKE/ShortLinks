package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/auth"
	"github.com/brennanMKE/ShortLinks/internal/config"
	"github.com/brennanMKE/ShortLinks/internal/middleware"
)

// wantInternalErrorBody is the exact, byte-for-byte response every one of the
// seven StatusInternalServerError branches in auth.go must keep producing
// (#0097 must not change what the client receives).
const wantInternalErrorBody = `{"error":"internal server error"}` + "\n"

// fixedSessionResolver is a middleware.SessionResolver stub that resolves any
// token to the same fixed user, letting LogoutAll be exercised through the
// real middleware.RequireSession guard without a database.
type fixedSessionResolver struct {
	user auth.SessionUser
	err  error
}

func (f fixedSessionResolver) ResolveSession(_ context.Context, _ string, _ time.Time) (auth.SessionUser, error) {
	return f.user, f.err
}

// countLogLines returns the number of non-empty lines in an slog text-handler
// buffer, i.e. the number of emitted log records.
func countLogLines(buf *bytes.Buffer) int {
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	n := 0
	for _, l := range lines {
		if l != "" {
			n++
		}
	}
	return n
}

// TestAuthHandler_InternalErrorBranches_LogOnceAndResponseUnchanged is the
// #0097 acceptance test: it drives every one of the seven
// StatusInternalServerError branches in internal/handlers/auth.go to a
// genuine service-layer failure and asserts, per branch:
//   - the HTTP response (status + body) is byte-identical to what the
//     handler produced before #0097 — this issue adds a server-side log
//     record only, and must not touch the client-facing contract;
//   - exactly one Error-level log line is emitted, carrying "err";
//   - the LogoutAll branch's line additionally carries "user_id" for the
//     authenticated caller (the one branch, alongside Logout, where a user is
//     in scope — Logout has none: it runs ahead of RequireSession and
//     authenticator.Logout's signature returns only an error on failure, no
//     owning user id).
//
// This is the harness internal/auth/logout_all_test.go's
// TestLogoutAll_MailerErrorDoesNotFailOperation established for asserting log
// output — an slog buffer handler — reused here at the handler layer since
// #0097's logging lives in AuthHandler rather than the services behind it
// (see the AuthHandler.log field doc comment in auth.go for why).
func TestAuthHandler_InternalErrorBranches_LogOnceAndResponseUnchanged(t *testing.T) {
	wantErr := errors.New("simulated infrastructure failure")

	cases := []struct {
		name string
		do   func(h *AuthHandler) *httptest.ResponseRecorder
		// wantAttrs are substrings that must each appear in the single log line.
		wantAttrs []string
	}{
		{
			name: "RegisterStart",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
					strings.NewReader(`{"email":"alice@example.com"}`))
				h.RegisterStart(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error()},
		},
		{
			name: "RegisterVerify",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/auth/register/verify?token=tok", nil)
				h.RegisterVerify(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error()},
		},
		{
			name: "LoginStart",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/auth/login/start", nil)
				h.LoginStart(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error()},
		},
		{
			name: "Logout",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "some-token"})
				h.Logout(rr, req)
				return rr
			},
			// "ip" is the only correlating identifier this line can carry (the
			// repo has no request-logging middleware at all) — see the code
			// comment at auth.go's Logout for why user_id is unavailable.
			wantAttrs: []string{"err=", wantErr.Error(), "ip="},
		},
		{
			name: "LogoutAll",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				resolver := fixedSessionResolver{user: auth.SessionUser{ID: 4242, Email: "caller@example.com"}}
				requireSession := middleware.RequireSession(resolver)
				mux := http.NewServeMux()
				mux.Handle("POST /auth/logout/all", requireSession(http.HandlerFunc(h.LogoutAll)))
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/logout/all", nil)
				req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "caller-session"})
				mux.ServeHTTP(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error(), "user_id=4242"},
		},
		{
			name: "RecoverStart",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/recover",
					strings.NewReader(`{"email":"alice@example.com"}`))
				h.RecoverStart(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error()},
		},
		{
			name: "RecoverVerify",
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/auth/recover/verify?token=tok", nil)
				h.RecoverVerify(rr, req)
				return rr
			},
			wantAttrs: []string{"err=", wantErr.Error()},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

			reg := &fakeRegistrar{startErr: wantErr, verifyErr: wantErr}
			login := &fakeAuthenticator{
				startErr:     wantErr,
				logoutErr:    wantErr,
				logoutAllErr: wantErr,
			}
			recovery := &fakeRecoverer{startErr: wantErr, verifyErr: wantErr}
			h := NewAuthHandler(reg, login, recovery, logger)

			rr := tc.do(h)

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusInternalServerError, rr.Body.String())
			}
			if got := rr.Body.String(); got != wantInternalErrorBody {
				t.Errorf("body = %q, want %q (byte-identical response is #0097's hard requirement)", got, wantInternalErrorBody)
			}

			if n := countLogLines(&logBuf); n != 1 {
				t.Fatalf("log lines emitted = %d, want exactly 1 (no double-logging); log=%s", n, logBuf.String())
			}
			logged := logBuf.String()
			if !strings.Contains(logged, "level=ERROR") {
				t.Errorf("log line not at Error level: %s", logged)
			}
			for _, want := range tc.wantAttrs {
				if !strings.Contains(logged, want) {
					t.Errorf("log line missing %q: %s", want, logged)
				}
			}
			// No secrets/tokens/session values/full email addresses in the log
			// line (acceptance criteria): the cookie/session tokens used by
			// Logout and LogoutAll above, and every email address any fixture
			// in this table touches (request bodies, the LogoutAll caller's
			// AuthUser.Email), must never appear. This list is exercised for
			// real by TestAuthHandler_MailerErrorDoesNotLeakEmail below, which
			// injects the actual *auth.SESMailer error text rather than a
			// synthetic one — see that test's doc comment for why a forbidden
			// list no case can trip proves nothing.
			for _, secret := range []string{"some-token", "caller-session", "alice@example.com", "caller@example.com"} {
				if strings.Contains(logged, secret) {
					t.Errorf("log line leaked %q: %s", secret, logged)
				}
			}
		})
	}
}

// TestNewAuthHandler_NilLoggerDoesNotPanic asserts the constructor's documented
// nil-dependency tolerance extends to the new logger parameter: passing nil
// must not panic when a 500 branch actually logs.
func TestNewAuthHandler_NilLoggerDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil logger panicked: %v", r)
		}
	}()

	f := &fakeRecoverer{startErr: errors.New("boom")}
	h := NewAuthHandler(&fakeRegistrar{}, nil, f, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/recover",
		strings.NewReader(`{"email":"alice@example.com"}`))
	h.RecoverStart(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

// TestAuthHandler_MailerErrorDoesNotLeakEmail is the guard the reviewer asked
// for: a subtest whose injected error is not a synthetic string but the real
// error *auth.SESMailer.send produces on a failed delivery. Before #0097's
// review fixup, that error was built with fmt.Errorf("... sending email to
// %s: %w", toEmail, err) (internal/auth/ses_mailer.go), so a real SMTP outage
// on the unauthenticated /auth/register/start and /auth/recover routes wrote
// the recipient's full address into the server journal — a PII leak any
// caller could trigger at will. The forbidden-substring list in the table
// test above is synthetic (none of its fixture errors ever contained an
// email), so it could not have caught this; this test exercises the actual
// production code path that leaked, and fails if ses_mailer.go's fix is
// reverted.
//
// No database or network is needed: SESMailer.send dials out immediately, so
// pointing it at a closed local port (127.0.0.1:1) yields the real
// "dial tcp ...: connection refused" error synchronously.
//
// Routing that real error through the fake registrar/recoverer below still
// proves the production case: registration.go:111 and recovery.go:106 are
// bare `return s.mailer.SendVerification(...)` / `return
// s.mailer.SendRecovery(...)`, with no wrapping frame in between, so the
// error value AuthHandler receives in production is byte-identical to the
// one injected here — the same fact that made the original leak possible.
func TestAuthHandler_MailerErrorDoesNotLeakEmail(t *testing.T) {
	const victimEmail = "victim@example.com"

	mailer := auth.NewSESMailer(&config.Config{
		SESSmtpHost: "127.0.0.1",
		SESSmtpPort: 1, // nothing listens here; dial fails immediately.
		EmailFrom:   "ShortLinks <noreply@example.com>",
		BaseURL:     "https://example.com",
	})

	registerErr := mailer.SendVerification(context.Background(), victimEmail, "tok-register")
	if registerErr == nil {
		t.Fatal("SendVerification: expected a dial failure, got nil")
	}
	recoverErr := mailer.SendRecovery(context.Background(), victimEmail, "tok-recover")
	if recoverErr == nil {
		t.Fatal("SendRecovery: expected a dial failure, got nil")
	}

	cases := []struct {
		name string
		err  error
		do   func(h *AuthHandler) *httptest.ResponseRecorder
	}{
		{
			name: "RegisterStart",
			err:  registerErr,
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/register/start",
					strings.NewReader(`{"email":"`+victimEmail+`"}`))
				h.RegisterStart(rr, req)
				return rr
			},
		},
		{
			name: "RecoverStart",
			err:  recoverErr,
			do: func(h *AuthHandler) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/auth/recover",
					strings.NewReader(`{"email":"`+victimEmail+`"}`))
				h.RecoverStart(rr, req)
				return rr
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))
			h := NewAuthHandler(
				&fakeRegistrar{startErr: tc.err},
				nil,
				&fakeRecoverer{startErr: tc.err},
				logger,
			)

			rr := tc.do(h)

			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
			}
			if got := rr.Body.String(); got != wantInternalErrorBody {
				t.Errorf("body = %q, want %q", got, wantInternalErrorBody)
			}
			if n := countLogLines(&logBuf); n != 1 {
				t.Fatalf("log lines emitted = %d, want exactly 1; log=%s", n, logBuf.String())
			}

			logged := logBuf.String()
			if !strings.Contains(logged, "connection refused") {
				t.Errorf("log line missing the real dial-failure detail (test setup problem?): %s", logged)
			}
			if strings.Contains(logged, victimEmail) {
				t.Errorf("log line leaked the recipient email address %q: %s", victimEmail, logged)
			}
		})
	}
}
