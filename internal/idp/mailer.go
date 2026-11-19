package idp

import (
	"context"
	"fmt"
	"time"

	"github.com/Kolonnade/Portiko/provider"
)

// TextSender is the part of auth.SESMailer the adapter needs.
type TextSender interface {
	SendText(ctx context.Context, toEmail, subject, textBody string) error
}

// Mailer adapts a TextSender (auth.SESMailer) to Portiko's provider.Mailer: the
// one-time code, the new-passkey notice and the address-change notice.
type Mailer struct {
	send        TextSender
	serviceName string
}

// NewMailer returns a provider.Mailer that sends through s.
func NewMailer(s TextSender, serviceName string) *Mailer {
	return &Mailer{send: s, serviceName: serviceName}
}

// sendTimeout bounds one message: provider.Mailer methods carry no context, and
// an SMTP server that stops answering must not hold a sign-in request open.
const sendTimeout = 30 * time.Second

func (m *Mailer) sendText(to, subject, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	return m.send.SendText(ctx, to, subject, body)
}

var _ provider.Mailer = (*Mailer)(nil)

// SendCode sends the sign-in code.
func (m *Mailer) SendCode(to, code string) error {
	return m.sendText(to,
		fmt.Sprintf("Your %s sign-in code", m.serviceName),
		fmt.Sprintf("Your sign-in code is %s\r\n\r\n"+
			"It expires in 10 minutes. If you didn't ask for it, you can ignore this.\r\n", code))
}

// SendPasskeyAdded tells the holder a passkey was added. It asks for no click
// to confirm: a security notice with an action link is a phishing template.
func (m *Mailer) SendPasskeyAdded(to string, n provider.PasskeyNotice) error {
	what := "A new passkey"
	if n.Authenticator != "" {
		what = "A new passkey in " + n.Authenticator
	}
	body := fmt.Sprintf("%s was added to your %s account on %s.\r\n\r\n"+
		"It can sign in to your account from now on. If you added it, there is nothing to do.\r\n\r\n"+
		"If you did not, review the passkeys on your account and remove the one you do not recognise:\r\n\r\n%s\r\n",
		what, m.serviceName, n.When.UTC().Format(time.RFC1123Z), n.ReviewURL)
	return m.sendText(to, fmt.Sprintf("A passkey was added to your %s account", m.serviceName), body)
}

// SendEmailChanged tells the holder an address on the account changed.
func (m *Mailer) SendEmailChanged(to string, n provider.EmailNotice) error {
	var what string
	switch n.Change {
	case provider.EmailAdded:
		what = n.Address + " was added to your account."
	case provider.EmailRemoved:
		what = n.Address + " was removed from your account."
	case provider.EmailPrimaryChanged:
		what = fmt.Sprintf("%s is now your account's primary address (it was %s).", n.Address, n.Previous)
	default:
		what = "An address on your account changed."
	}
	body := fmt.Sprintf("%s\r\n\r\nThis happened on %s. If it was you, there is nothing to do.\r\n\r\n"+
		"If it was not, review your account's addresses and passkeys:\r\n\r\n%s\r\n",
		what, n.When.UTC().Format(time.RFC1123Z), n.ReviewURL)
	return m.sendText(to, fmt.Sprintf("Your %s account's addresses changed", m.serviceName), body)
}
