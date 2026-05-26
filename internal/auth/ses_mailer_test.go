package auth

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// newTestMailer returns a SESMailer wired with the given recording transport.
func newTestMailer(send sendMailFunc) *SESMailer {
	return &SESMailer{
		host:     "smtp.example.com",
		port:     587,
		username: "AKIAEXAMPLE",
		password: "smtp-secret",
		from:     "ShortLinks <noreply@sstools.co>",
		baseURL:  "https://go.sstools.co",
		sendMail: send,
	}
}

// recorder captures the arguments passed to the transport.
type recorder struct {
	addr string
	from string
	to   []string
	msg  []byte
}

func (r *recorder) capture(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
	r.addr = addr
	r.from = from
	r.to = to
	r.msg = msg
	return nil
}

// TestSESMailer_SendVerification_InjectedTransport asserts the envelope and the
// composed RFC 5322 message handed to the transport for a verification email.
func TestSESMailer_SendVerification_InjectedTransport(t *testing.T) {
	var rec recorder
	m := newTestMailer(rec.capture)

	if err := m.SendVerification(context.Background(), "alice@example.com", "tok-123"); err != nil {
		t.Fatalf("SendVerification: %v", err)
	}

	if rec.addr != "smtp.example.com:587" {
		t.Errorf("addr = %q, want %q", rec.addr, "smtp.example.com:587")
	}
	// Envelope sender must be the bare address, stripped of the display name.
	if rec.from != "noreply@sstools.co" {
		t.Errorf("envelope from = %q, want %q", rec.from, "noreply@sstools.co")
	}
	if len(rec.to) != 1 || rec.to[0] != "alice@example.com" {
		t.Errorf("envelope to = %v, want [alice@example.com]", rec.to)
	}

	msg := string(rec.msg)
	wantHeaders := []string{
		"From: ShortLinks <noreply@sstools.co>\r\n",
		"To: alice@example.com\r\n",
		"Subject: Verify your ShortLinks account\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=\"UTF-8\"\r\n",
	}
	for _, h := range wantHeaders {
		if !strings.Contains(msg, h) {
			t.Errorf("message missing header %q\nfull message:\n%s", h, msg)
		}
	}
	wantLink := "https://go.sstools.co/auth/register/verify?token=tok-123"
	if !strings.Contains(msg, wantLink) {
		t.Errorf("message missing verification link %q\nfull message:\n%s", wantLink, msg)
	}
	// Headers and body must be separated by a blank line.
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Errorf("message missing header/body separator (blank line)")
	}
}

// TestSESMailer_SendRecovery_InjectedTransport asserts the recovery email
// envelope and link.
func TestSESMailer_SendRecovery_InjectedTransport(t *testing.T) {
	var rec recorder
	m := newTestMailer(rec.capture)

	if err := m.SendRecovery(context.Background(), "bob@example.com", "rec-789"); err != nil {
		t.Fatalf("SendRecovery: %v", err)
	}

	if len(rec.to) != 1 || rec.to[0] != "bob@example.com" {
		t.Errorf("envelope to = %v, want [bob@example.com]", rec.to)
	}
	msg := string(rec.msg)
	if !strings.Contains(msg, "Subject: Recover your ShortLinks account\r\n") {
		t.Errorf("message missing recovery subject\nfull message:\n%s", msg)
	}
	wantLink := "https://go.sstools.co/auth/recover/verify?token=rec-789"
	if !strings.Contains(msg, wantLink) {
		t.Errorf("message missing recovery link %q\nfull message:\n%s", wantLink, msg)
	}
}

// TestSESMailer_ContextCancelled verifies the context is honored before any
// transport call is made.
func TestSESMailer_ContextCancelled(t *testing.T) {
	called := false
	m := newTestMailer(func(string, smtp.Auth, string, []string, []byte) error {
		called = true
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.SendVerification(ctx, "alice@example.com", "tok"); err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if called {
		t.Error("transport was called despite cancelled context")
	}
}

// TestNewSESMailer_FromConfig verifies construction maps config fields and that
// the default transport is installed.
func TestNewSESMailer_FromConfig(t *testing.T) {
	cfg := &config.Config{
		SESSmtpHost:     "email-smtp.us-east-1.amazonaws.com",
		SESSmtpPort:     587,
		SESSmtpUsername: "user",
		SESSmtpPassword: "pass",
		EmailFrom:       "ShortLinks <noreply@sstools.co>",
		BaseURL:         "https://go.sstools.co",
	}
	m := NewSESMailer(cfg)
	if m.host != cfg.SESSmtpHost || m.port != cfg.SESSmtpPort ||
		m.username != cfg.SESSmtpUsername || m.password != cfg.SESSmtpPassword ||
		m.from != cfg.EmailFrom || m.baseURL != cfg.BaseURL {
		t.Errorf("NewSESMailer did not map config fields: %+v", m)
	}
	if m.sendMail == nil {
		t.Error("NewSESMailer left sendMail nil; default transport not installed")
	}
}

// fakeSMTPServer is a minimal in-process SMTP server: it accepts one connection,
// speaks just enough of the protocol (no STARTTLS, no AUTH advertised), and
// captures the DATA payload and the MAIL FROM / RCPT TO arguments. It exercises
// the real starttlsSendMail transport over a loopback listener — no network,
// no TLS, no SES.
type fakeSMTPServer struct {
	ln   net.Listener
	mu   sync.Mutex
	from string
	to   []string
	data string
	done chan struct{}
}

func startFakeSMTPServer(t *testing.T) *fakeSMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTPServer{ln: ln, done: make(chan struct{})}
	go s.serve()
	return s
}

func (s *fakeSMTPServer) addr() string { return s.ln.Addr().String() }

func (s *fakeSMTPServer) serve() {
	defer close(s.done)
	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) {
		_, _ = w.WriteString(line + "\r\n")
		_ = w.Flush()
	}

	write("220 fake ESMTP ready")
	inData := false
	var body strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				inData = false
				s.mu.Lock()
				s.data = body.String()
				s.mu.Unlock()
				write("250 OK queued")
				continue
			}
			body.WriteString(line)
			continue
		}

		trimmed := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(trimmed)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			// Advertise no extensions (no STARTTLS, no AUTH) to keep the test
			// transport on the plaintext path.
			write("250 fake at your service")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.from = extractAngleAddr(trimmed[len("MAIL FROM:"):])
			s.mu.Unlock()
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.to = append(s.to, extractAngleAddr(trimmed[len("RCPT TO:"):]))
			s.mu.Unlock()
			write("250 OK")
		case upper == "DATA":
			inData = true
			write("354 start mail input; end with <CRLF>.<CRLF>")
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("250 OK")
		}
	}
}

// extractAngleAddr pulls the address out of "<addr>" SMTP arguments.
func extractAngleAddr(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return s
}

// TestSESMailer_RealTransport_AgainstFakeServer drives the real
// starttlsSendMail transport against an in-process SMTP server and asserts the
// captured envelope and DATA payload. This proves the production transport path
// (dial, MAIL/RCPT/DATA) actually writes the composed message correctly,
// without contacting SES.
func TestSESMailer_RealTransport_AgainstFakeServer(t *testing.T) {
	srv := startFakeSMTPServer(t)

	host, portStr, err := net.SplitHostPort(srv.addr())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	m := &SESMailer{
		host:     host,
		port:     port,
		username: "user",
		password: "pass",
		from:     "ShortLinks <noreply@sstools.co>",
		baseURL:  "https://go.sstools.co",
		sendMail: starttlsSendMail, // exercise the real transport
	}

	if err := m.SendVerification(context.Background(), "carol@example.com", "tok-real"); err != nil {
		t.Fatalf("SendVerification via real transport: %v", err)
	}

	<-srv.done

	srv.mu.Lock()
	defer srv.mu.Unlock()

	if srv.from != "noreply@sstools.co" {
		t.Errorf("server MAIL FROM = %q, want %q", srv.from, "noreply@sstools.co")
	}
	if len(srv.to) != 1 || srv.to[0] != "carol@example.com" {
		t.Errorf("server RCPT TO = %v, want [carol@example.com]", srv.to)
	}
	if !strings.Contains(srv.data, "Subject: Verify your ShortLinks account") {
		t.Errorf("DATA missing subject\nfull DATA:\n%s", srv.data)
	}
	if !strings.Contains(srv.data, "https://go.sstools.co/auth/register/verify?token=tok-real") {
		t.Errorf("DATA missing verification link\nfull DATA:\n%s", srv.data)
	}
}

// TestNoOpMailer verifies the stub satisfies Mailer and never errors.
func TestNoOpMailer(t *testing.T) {
	var m Mailer = NoOpMailer{BaseURL: "https://go.sstools.co"}
	if err := m.SendVerification(context.Background(), "a@example.com", "t"); err != nil {
		t.Errorf("NoOpMailer.SendVerification: %v", err)
	}
	if err := m.SendRecovery(context.Background(), "a@example.com", "t"); err != nil {
		t.Errorf("NoOpMailer.SendRecovery: %v", err)
	}
}
