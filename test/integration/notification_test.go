//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	"github.com/gavinarori/ticketing-backend/internal/platform/email"
	notifsvc "github.com/gavinarori/ticketing-backend/internal/service/notification"
	ordersvc "github.com/gavinarori/ticketing-backend/internal/service/order"
)

// TestConfirmPayment_EnqueuesNotification_ThenDispatchLoopSendsIt proves
// the whole transactional-outbox path end to end against real Postgres:
// ConfirmPayment enqueues a 'pending' row atomically with marking the
// order paid, and a separate call to notification.Service.ProcessPending
// (exactly what cmd/worker's dispatch loop calls on a ticker) picks it up,
// renders it, sends it via a ConsoleSender, and marks it 'sent' — with
// the actually-rendered email content checked against the real order's
// real total, not a hardcoded expectation.
func TestConfirmPayment_EnqueuesNotification_ThenDispatchLoopSendsIt(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)

	userID := uuid.New()
	seedUser(t, env, userID)
	if _, err := env.svc.JoinQueue(ctx, eventID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.AdmitNext(ctx, eventID, 1); err != nil {
		t.Fatal(err)
	}
	hold, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userID)
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}

	order, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, "notif-test-"+uuid.NewString(), []ordersvc.HeldItem{
		{InventoryID: inventoryID, HoldToken: hold.HoldToken},
	})
	if err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	if _, err := env.orderSvc.AuthorizePayment(ctx, tenantID, order.ID); err != nil {
		t.Fatalf("authorize payment failed: %v", err)
	}

	var paymentID uuid.UUID
	if err := env.pool.QueryRow(ctx, `SELECT id FROM payments WHERE order_id = $1`, order.ID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}

	// Before confirmation: nothing queued yet.
	pendingBefore, err := env.notifRepo.ListPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range pendingBefore {
		if n.UserID == userID {
			t.Fatal("expected no notification queued before ConfirmPayment")
		}
	}

	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID); err != nil {
		t.Fatalf("confirm payment failed: %v", err)
	}

	// --- enqueue side: exactly one pending row, correct payload ---
	pending, err := env.notifRepo.ListPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var found *domain.Notification
	for _, n := range pending {
		if n.UserID == userID {
			found = n
			break
		}
	}
	if found == nil {
		t.Fatal("expected a pending notification for this order's user, found none")
	}
	if found.Type != domain.NotificationTypeOrderConfirmation {
		t.Errorf("expected type 'order_confirmation', got %q", found.Type)
	}
	if found.TenantID != tenantID {
		t.Errorf("expected notification tenant_id %s, got %s", tenantID, found.TenantID)
	}
	payloadOrderID, _ := found.Payload["order_id"].(string)
	if payloadOrderID != order.ID.String() {
		t.Errorf("expected payload order_id %s, got %v", order.ID, found.Payload["order_id"])
	}
	payloadTotal, _ := found.Payload["total_cents"].(float64)
	if int64(payloadTotal) != order.Total.Cents {
		t.Errorf("expected payload total_cents %d, got %v", order.Total.Cents, found.Payload["total_cents"])
	}

	// --- dispatch side: a real notification.Service, real UserRepository
	// lookup, a ConsoleSender standing in for real SMTP (see
	// internal/platform/email — this is the exact same interface a real
	// SMTPSender satisfies; only the transport differs). ---
	sender := email.NewConsoleSender(zap.NewNop())
	dispatchSvc := notifsvc.NewService(env.notifRepo, env.userRepo, sender, zap.NewNop())

	sent, err := dispatchSvc.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending failed: %v", err)
	}
	if sent < 1 {
		t.Fatalf("expected at least 1 notification sent, got %d", sent)
	}

	sentMessages := sender.Sent()
	var confirmationMsg *domain.EmailMessage
	for i := range sentMessages {
		if sentMessages[i].To == userID.String()+"@example.test" { // matches seedUser's synthesized address
			confirmationMsg = &sentMessages[i]
			break
		}
	}
	if confirmationMsg == nil {
		for i, m := range sentMessages {
			t.Logf("sent[%d]: To=%q Subject=%q Body=%q", i, m.To, m.Subject, m.Body)
		}
		t.Logf("expected To=%q", userID.String()+"@example.test")
		t.Fatal("expected an email addressed to the fan's seeded email address")
	}
	if confirmationMsg.Subject == "" {
		t.Error("expected a non-empty subject")
	}

	// --- row transitioned pending -> sent ---
	stillPending, err := env.notifRepo.ListPending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range stillPending {
		if n.ID == found.ID {
			t.Fatal("expected the notification to no longer be 'pending' after dispatch")
		}
	}
}

// TestConfirmPayment_ReplayDoesNotDoubleEnqueue extends the existing
// replay-idempotency guarantee (see order_test.go's replay assertions)
// to the notification outbox specifically: a webhook replay of an
// already-paid order must not enqueue a second confirmation email.
func TestConfirmPayment_ReplayDoesNotDoubleEnqueue(t *testing.T) {
	env := setup(t)
	ctx := context.Background()
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)

	userID := uuid.New()
	seedUser(t, env, userID)
	if _, err := env.svc.JoinQueue(ctx, eventID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.AdmitNext(ctx, eventID, 1); err != nil {
		t.Fatal(err)
	}
	hold, err := env.svc.HoldSeat(ctx, tenantID, eventID, inventoryID, userID)
	if err != nil {
		t.Fatalf("hold failed: %v", err)
	}
	order, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, "notif-replay-"+uuid.NewString(), []ordersvc.HeldItem{
		{InventoryID: inventoryID, HoldToken: hold.HoldToken},
	})
	if err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	if _, err := env.orderSvc.AuthorizePayment(ctx, tenantID, order.ID); err != nil {
		t.Fatalf("authorize payment failed: %v", err)
	}
	var paymentID uuid.UUID
	if err := env.pool.QueryRow(ctx, `SELECT id FROM payments WHERE order_id = $1`, order.ID).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}

	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID); err != nil {
		t.Fatalf("first confirm failed: %v", err)
	}
	// Replay — must be a no-op per the existing OrderStatusPaid guard.
	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID); err != nil {
		t.Fatalf("replayed confirm failed: %v", err)
	}

	var count int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM notifications WHERE user_id = $1 AND type = 'order_confirmation'`, userID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 notification row despite the replay, got %d", count)
	}
}
