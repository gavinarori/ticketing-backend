package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

type NotificationRepo struct {
	pool *pgxpool.Pool
}

func NewNotificationRepo(pool *pgxpool.Pool) *NotificationRepo { return &NotificationRepo{pool: pool} }

var _ domain.NotificationRepository = (*NotificationRepo)(nil)

// Create inserts via db(ctx, r.pool) — the same executor-in-context
// pattern every repository in this package uses — so a caller that
// already has a postgres.WithTx transaction on ctx (see
// internal/service/order.Service.ConfirmPayment) gets this insert
// committed or rolled back atomically with everything else in that
// transaction, with zero special-casing here.
func (r *NotificationRepo) Create(ctx context.Context, n *domain.Notification) error {
	if n.ID == uuid.Nil {
		n.ID = uuid.New()
	}
	payload := n.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal notification payload: %w", err)
	}
	status := n.Status
	if status == "" {
		status = domain.NotificationStatusPending
	}

	_, err = db(ctx, r.pool).Exec(ctx,
		`INSERT INTO notifications (id, tenant_id, user_id, type, payload, status) VALUES ($1,$2,$3,$4,$5,$6)`,
		n.ID, n.TenantID, n.UserID, string(n.Type), payloadJSON, string(status),
	)
	if err != nil {
		return fmt.Errorf("postgres: create notification: %w", err)
	}
	return nil
}

func (r *NotificationRepo) ListPending(ctx context.Context, limit int) ([]*domain.Notification, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT id, tenant_id, user_id, type, payload, status, attempts, last_error, created_at, sent_at
		 FROM notifications WHERE status = 'pending' ORDER BY created_at LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list pending notifications: %w", err)
	}
	defer rows.Close()

	var out []*domain.Notification
	for rows.Next() {
		var n domain.Notification
		var typ, status string
		var payloadJSON []byte
		var lastError *string
		if err := rows.Scan(&n.ID, &n.TenantID, &n.UserID, &typ, &payloadJSON, &status, &n.Attempts, &lastError, &n.CreatedAt, &n.SentAt); err != nil {
			return nil, fmt.Errorf("postgres: scan notification: %w", err)
		}
		n.Type = domain.NotificationType(typ)
		n.Status = domain.NotificationStatus(status)
		if lastError != nil {
			n.LastError = *lastError
		}
		if len(payloadJSON) > 0 {
			if err := json.Unmarshal(payloadJSON, &n.Payload); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal notification payload: %w", err)
			}
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (r *NotificationRepo) MarkSent(ctx context.Context, id uuid.UUID) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE notifications SET status = 'sent', sent_at = now(), attempts = attempts + 1 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("postgres: mark notification sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.NotificationRepo.MarkSent", domain.ErrNotFound)
	}
	return nil
}

func (r *NotificationRepo) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE notifications SET status = 'failed', attempts = attempts + 1, last_error = $1 WHERE id = $2`, errMsg, id)
	if err != nil {
		return fmt.Errorf("postgres: mark notification failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.NotificationRepo.MarkFailed", domain.ErrNotFound)
	}
	return nil
}
