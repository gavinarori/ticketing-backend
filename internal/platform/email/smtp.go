package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// SMTPSender sends real email over SMTP with PLAIN auth — the lowest-
// common-denominator transport that works against a real mail provider
// (SendGrid, SES's SMTP interface, Mailgun, etc.) or a local dev SMTP
// server, without pulling in a provider-specific SDK. Swap to a
// provider's HTTP API implementation later if template management,
// delivery webhooks, or bounce handling become requirements this can't
// meet — this exists to make the outbox dispatch loop functionally
// complete today, not to be the final word on email infrastructure.
type SMTPSender struct {
	host, port   string
	username     string
	password     string
	fromAddress  string
	sendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

func NewSMTPSender(host, port, username, password, fromAddress string) *SMTPSender {
	return &SMTPSender{
		host: host, port: port, username: username, password: password, fromAddress: fromAddress,
		sendMailFunc: smtp.SendMail,
	}
}

var _ domain.EmailSender = (*SMTPSender)(nil)

func (s *SMTPSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	addr := s.host + ":" + s.port
	auth := smtp.PlainAuth("", s.username, s.password, s.host)

	// A minimal RFC 5322 message: headers, blank line, body. No HTML
	// multipart support — plain text is sufficient for a transactional
	// order confirmation and keeps this implementation small.
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.fromAddress)
	fmt.Fprintf(&b, "To: %s\r\n", msg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(msg.Body)

	// net/smtp has no context support natively; SendMail's own dial/write
	// timeouts are what actually bound how long this call can block. A
	// context-aware transport is a reasonable future improvement if this
	// ever needs to respect caller-supplied deadlines mid-send.
	if err := s.sendMailFunc(addr, auth, s.fromAddress, []string{msg.To}, []byte(b.String())); err != nil {
		return fmt.Errorf("email: smtp send: %w", err)
	}
	return nil
}
