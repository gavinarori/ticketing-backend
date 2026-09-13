// Package notification implements the SEND half of the transactional
// outbox described in migrations/000014_notifications.up.sql. The
// ENQUEUE half deliberately lives elsewhere — inside
// internal/service/order.Service.ConfirmPayment, using
// domain.NotificationRepository directly — because enqueuing must happen
// inside the same database transaction as the business event it
// describes, and this package has no reason to know about orders,
// payments, or any other domain concept beyond "a pending row exists and
// needs sending."
package notification

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type Service struct {
	notifications domain.NotificationRepository
	users         domain.UserRepository
	sender        domain.EmailSender
	log           *zap.Logger
}

func NewService(notifications domain.NotificationRepository, users domain.UserRepository, sender domain.EmailSender, log *zap.Logger) *Service {
	return &Service{notifications: notifications, users: users, sender: sender, log: log}
}

// ProcessPending sends up to batchSize pending notifications, oldest
// first. Intended to be called on a ticker from cmd/worker.
//
// A failure sending one notification (bad address, transport error, an
// unrenderable payload) is recorded via MarkFailed and does not stop the
// batch — the same "one bad row must not block everything behind it"
// principle used throughout this codebase's worker loops (see
// internal/service/admission.RunOnce and
// internal/service/inventory.Service.SweepExpiredHolds).
//
// Known limitation, stated plainly: a 'failed' row is currently
// terminal — there is no automatic retry or backoff. A real production
// setup would want at least a few retries with backoff before giving up;
// this round gets the pipeline working end to end first.
func (s *Service) ProcessPending(ctx context.Context, batchSize int) (sent int, err error) {
	pending, err := s.notifications.ListPending(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("notification: list pending: %w", err)
	}

	for _, n := range pending {
		if err := s.processOne(ctx, n); err != nil {
			s.log.Error("notification_send_failed",
				zap.String("notification_id", n.ID.String()), zap.String("type", string(n.Type)), zap.Error(err))
			if markErr := s.notifications.MarkFailed(ctx, n.ID, err.Error()); markErr != nil {
				s.log.Error("notification_mark_failed_failed", zap.String("notification_id", n.ID.String()), zap.Error(markErr))
			}
			continue
		}
		if err := s.notifications.MarkSent(ctx, n.ID); err != nil {
			s.log.Error("notification_mark_sent_failed", zap.String("notification_id", n.ID.String()), zap.Error(err))
			continue
		}
		sent++
	}

	return sent, nil
}

func (s *Service) processOne(ctx context.Context, n *domain.Notification) error {
	user, err := s.users.GetByID(ctx, n.UserID)
	if err != nil {
		return fmt.Errorf("look up recipient: %w", err)
	}

	msg, err := render(n, user.Email)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	if err := s.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

// render builds the actual email content for a notification. Kept as a
// plain function (not a template file) deliberately — one notification
// type, one small function; a templating system is worth introducing
// once there's a second or third type with real formatting needs, not
// preemptively for this one.
func render(n *domain.Notification, recipientEmail string) (domain.EmailMessage, error) {
	switch n.Type {
	case domain.NotificationTypeOrderConfirmation:
		orderID, _ := n.Payload["order_id"].(string)
		totalCents, _ := n.Payload["total_cents"].(float64) // JSON numbers decode as float64
		currency, _ := n.Payload["currency"].(string)

		body := fmt.Sprintf(
			"Thanks for your order!\n\nOrder: %s\nTotal: %.2f %s\n\nSee you at the match.",
			orderID, totalCents/100, currency,
		)
		return domain.EmailMessage{
			To:      recipientEmail,
			Subject: "Your order is confirmed",
			Body:    body,
		}, nil
	default:
		return domain.EmailMessage{}, fmt.Errorf("unknown notification type %q", n.Type)
	}
}
