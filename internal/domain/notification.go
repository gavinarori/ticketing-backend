package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type NotificationType string

const (
	NotificationTypeOrderConfirmation NotificationType = "order_confirmation"
)

type NotificationStatus string

const (
	NotificationStatusPending NotificationStatus = "pending"
	NotificationStatusSent    NotificationStatus = "sent"
	NotificationStatusFailed  NotificationStatus = "failed"
)

// Notification is one row in the transactional outbox — see
// migrations/000014_notifications.up.sql for why this table exists and
// what guarantee it provides. Payload is intentionally a loose map
// rather than a typed struct per notification Type: new notification
// types will want different fields, and this table's job is durable
// storage of "something needs sending," not schema validation of every
// possible payload shape — that validation belongs in
// internal/service/notification's rendering logic, which knows what each
// Type actually requires.
type Notification struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	UserID    uuid.UUID
	Type      NotificationType
	Payload   map[string]any
	Status    NotificationStatus
	Attempts  int
	LastError string
	CreatedAt time.Time
	SentAt    *time.Time
}

// NotificationRepository. Create is the half of this interface that
// matters most for correctness: it must be callable with a ctx carrying
// an existing postgres.WithTx transaction (see
// internal/repository/postgres's db(ctx) pattern, used identically by
// every other repository in this codebase) so a caller like
// order.Service.ConfirmPayment can enqueue a notification atomically
// alongside the business state change it describes.
type NotificationRepository interface {
	Create(ctx context.Context, n *Notification) error
	// ListPending returns up to limit 'pending' rows, oldest first — what
	// the dispatch loop (internal/service/notification) polls.
	ListPending(ctx context.Context, limit int) ([]*Notification, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
}

// EmailMessage is a fully-rendered email ready to send — subject and
// body are already filled in by the time an EmailSender sees this; the
// sender's only job is transport.
type EmailMessage struct {
	To      string
	Subject string
	Body    string
}

// EmailSender abstracts over the actual email transport (SMTP, a
// provider API, or — for local dev — just logging), the same way
// PaymentGateway abstracts over Stripe/Adyen: service code depends on
// this interface, never a concrete provider type.
type EmailSender interface {
	Send(ctx context.Context, msg EmailMessage) error
}
