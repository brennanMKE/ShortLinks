package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/brennanMKE/ShortLinks/internal/config"
)

// sendMailFunc is the seam that decouples SESMailer from the network. It has
// the same signature as smtp.SendMail, so the production default is
// smtp.SendMail and tests can inject a recorder or point it at an in-process
// listener without performing real TLS or DNS.
type sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// SESMailer is a Mailer that delivers email through an SMTP server — in
// production, an AWS SES SMTP endpoint on port 587 using STARTTLS and PLAIN
// auth with IAM-derived SMTP credentials.
type SESMailer struct {
	host     string
	port     int
	username string
	password string
	from     string // RFC 5322 From header value, e.g. "ShortLinks <noreply@sstools.co>"
	baseURL  string

	// sendMail is the transport. Defaults to starttlsSendMail; tests override
	// it to capture the envelope and message without touching the network.
	sendMail sendMailFunc
}

// NewSESMailer constructs a SESMailer from configuration. The default transport
// performs STARTTLS + PLAIN auth, matching the SES SMTP endpoint on port 587.
func NewSESMailer(cfg *config.Config) *SESMailer {
	return &SESMailer{
		host:     cfg.SESSmtpHost,
		port:     cfg.SESSmtpPort,
		username: cfg.SESSmtpUsername,
		password: cfg.SESSmtpPassword,
		from:     cfg.EmailFrom,
		baseURL:  cfg.BaseURL,
		sendMail: starttlsSendMail,
	}
}

// SendVerification sends the registration magic-link email.
func (m *SESMailer) SendVerification(ctx context.Context, toEmail, token string) error {
	link := verificationURL(m.baseURL, token)
	subject := "Verify your ShortLinks account"
	text := "Welcome to ShortLinks.\r\n\r\n" +
		"Click the link below to verify your email and add a passkey. " +
		"This link expires in 5 minutes.\r\n\r\n" +
		link + "\r\n\r\n" +
		"If you did not request this, you can ignore this email.\r\n"
	return m.send(ctx, toEmail, subject, text)
}

// SendRecovery sends the single-use account-recovery email.
func (m *SESMailer) SendRecovery(ctx context.Context, toEmail, token string) error {
	link := recoveryURL(m.baseURL, token)
	subject := "Recover your ShortLinks account"
	text := "A recovery link was requested for your ShortLinks account.\r\n\r\n" +
		"Click the link below to register a new passkey. " +
		"This link expires in 15 minutes.\r\n\r\n" +
		link + "\r\n\r\n" +
		"If you did not request this, you can ignore this email; " +
		"your existing passkeys remain valid.\r\n"
	return m.send(ctx, toEmail, subject, text)
}

// send composes an RFC 5322 message and hands it to the transport. The context
// is honored before dispatch; the default transport does not itself accept a
// context, so cancellation is checked up front.
func (m *SESMailer) send(ctx context.Context, toEmail, subject, textBody string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.from == "" {
		return fmt.Errorf("auth: SESMailer has no From address (EMAIL_FROM unset)")
	}

	msg := buildMessage(m.from, toEmail, subject, textBody)
	addr := net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))
	auth := smtp.PlainAuth("", m.username, m.password, m.host)

	send := m.sendMail
	if send == nil {
		send = starttlsSendMail
	}
	if err := send(addr, auth, fromAddress(m.from), []string{toEmail}, msg); err != nil {
		return fmt.Errorf("auth: sending email to %s: %w", toEmail, err)
	}
	return nil
}

// buildMessage composes a single-part text/plain RFC 5322 message with CRLF
// line endings. The From header carries the configured display-name form
// (e.g. "ShortLinks <noreply@sstools.co>"), while the SMTP envelope sender is
// the bare address (see fromAddress).
func buildMessage(from, to, subject, textBody string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(textBody)
	return []byte(b.String())
}

// fromAddress extracts the bare email address from a possibly display-name
// formatted From value. "ShortLinks <noreply@sstools.co>" -> "noreply@sstools.co".
// A plain address is returned unchanged.
func fromAddress(from string) string {
	if i := strings.LastIndex(from, "<"); i >= 0 {
		if j := strings.LastIndex(from, ">"); j > i {
			return strings.TrimSpace(from[i+1 : j])
		}
	}
	return strings.TrimSpace(from)
}

// starttlsSendMail dials addr, upgrades the connection with STARTTLS, performs
// PLAIN auth, and sends msg. It mirrors smtp.SendMail's signature but forces a
// TLS upgrade, which the SES SMTP endpoint on port 587 requires. It is the
// production default transport for SESMailer.
func starttlsSendMail(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}

	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if a != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(a); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, addr := range to {
		if err := c.Rcpt(addr); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Ensure the concrete types satisfy the interface at compile time.
var (
	_ Mailer = (*SESMailer)(nil)
	_ Mailer = NoOpMailer{}
)
