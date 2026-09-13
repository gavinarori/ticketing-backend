// Package email implements domain.EmailSender per transport.
package email

import (
	"context"
	"sync"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// ConsoleSender logs every email instead of actually sending it — the
// email-package equivalent of internal/platform/payment.MockGateway.
// Used for local dev (no SMTP credentials required to see the system
// work end to end) and for tests that want to assert on what *would*
// have been sent without any real transport involved.
type ConsoleSender struct {
	mu   sync.Mutex
	sent []domain.EmailMessage
	log  *zap.Logger
}

func NewConsoleSender(log *zap.Logger) *ConsoleSender {
	return &ConsoleSender{log: log}
}

var _ domain.EmailSender = (*ConsoleSender)(nil)

func (s *ConsoleSender) Send(ctx context.Context, msg domain.EmailMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, msg)
	s.log.Info("email_sent_console",
		zap.String("to", msg.To), zap.String("subject", msg.Subject), zap.Int("body_len", len(msg.Body)))
	return nil
}

// Sent returns every message handed to Send so far, for test assertions.
func (s *ConsoleSender) Sent() []domain.EmailMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.EmailMessage, len(s.sent))
	copy(out, s.sent)
	return out
}
