//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gavinarori/ticketing-backend/internal/domain"
	ordersvc "github.com/gavinarori/ticketing-backend/internal/service/order"
)

// TestOrderFlow_HoldToPaid exercises the full happy path end to end:
// join the waiting room, get admitted, hold a seat, create an order from
// that hold, authorize payment with the mock gateway, confirm payment —
// and checks the state of every table along the way, not just the
// service call's return values.
func TestOrderFlow_HoldToPaid(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

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

	order, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, "idem-"+uuid.NewString(), []ordersvc.HeldItem{
		{InventoryID: inventoryID, HoldToken: hold.HoldToken},
	})
	if err != nil {
		t.Fatalf("create order failed: %v", err)
	}
	if order.Total.Cents != 250000 {
		t.Errorf("expected order total 250000 cents (matching seeded price), got %d", order.Total.Cents)
	}
	if order.Status != domain.OrderStatusPending {
		t.Errorf("expected order status 'pending', got %q", order.Status)
	}

	// The seat must still be 'held', NOT 'sold', at this point — that's
	// the whole point of deferring ConfirmSale until after payment.
	invRow, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if invRow.Status != domain.InventoryStatusHeld {
		t.Fatalf("expected inventory to remain 'held' after order creation (before payment), got %q", invRow.Status)
	}

	clientSecret, err := env.orderSvc.AuthorizePayment(ctx, tenantID, order.ID)
	if err != nil {
		t.Fatalf("authorize payment failed: %v", err)
	}
	if clientSecret == "" {
		t.Error("expected a non-empty client secret from the mock gateway")
	}

	orderAfterAuth, err := env.orderRepo.GetByID(ctx, tenantID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orderAfterAuth.Status != domain.OrderStatusAwaitingPayment {
		t.Errorf("expected order status 'awaiting_payment' after authorization, got %q", orderAfterAuth.Status)
	}

	payments, err := env.pool.Query(ctx, `SELECT id FROM payments WHERE order_id = $1`, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	var paymentID uuid.UUID
	if !payments.Next() {
		t.Fatal("expected exactly one payment row after AuthorizePayment")
	}
	if err := payments.Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	payments.Close()

	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID); err != nil {
		t.Fatalf("confirm payment failed: %v", err)
	}

	finalOrder, err := env.orderRepo.GetByID(ctx, tenantID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalOrder.Status != domain.OrderStatusPaid {
		t.Errorf("expected final order status 'paid', got %q", finalOrder.Status)
	}

	finalPayment, err := env.paymentRepo.GetByID(ctx, tenantID, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if finalPayment.Status != domain.PaymentStatusCaptured {
		t.Errorf("expected final payment status 'captured', got %q", finalPayment.Status)
	}

	finalInv, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if finalInv.Status != domain.InventoryStatusSold {
		t.Errorf("expected final inventory status 'sold', got %q", finalInv.Status)
	}

	// TestOrderFlow_HoldToPaid_ReplayedWebhookIsIdempotent (see below)
	// covers what happens if ConfirmPayment is called again for this same
	// already-paid order — a real scenario (payment providers deliver
	// webhooks at-least-once), not a hypothetical one.
	if err := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID); err != nil {
		t.Fatalf("expected a replayed ConfirmPayment call on an already-paid order to be a no-op, got error: %v", err)
	}

	reconfirmedOrder, err := env.orderRepo.GetByID(ctx, tenantID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconfirmedOrder.Status != domain.OrderStatusPaid {
		t.Errorf("expected order to remain 'paid' after a replayed confirmation, got %q", reconfirmedOrder.Status)
	}
	reconfirmedInv, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if reconfirmedInv.Status != domain.InventoryStatusSold {
		t.Errorf("expected inventory to remain 'sold' after a replayed confirmation, got %q", reconfirmedInv.Status)
	}
	reconfirmedPayment, err := env.paymentRepo.GetByID(ctx, tenantID, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if reconfirmedPayment.Status != domain.PaymentStatusCaptured {
		t.Errorf("expected payment to remain 'captured' (NOT refunded) after a replayed confirmation, got %q", reconfirmedPayment.Status)
	}
}

// TestCreateOrder_Idempotent proves a retried CreateOrder call with the
// same idempotency key returns the original order rather than creating a
// second one — critical for a client retrying after a network timeout
// during checkout.
func TestCreateOrder_Idempotent(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

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

	idemKey := "idem-" + uuid.NewString()
	items := []ordersvc.HeldItem{{InventoryID: inventoryID, HoldToken: hold.HoldToken}}

	first, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, idemKey, items)
	if err != nil {
		t.Fatalf("first create order failed: %v", err)
	}
	second, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, idemKey, items)
	if err != nil {
		t.Fatalf("second create order failed: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("expected the same order ID on retry, got %s and %s", first.ID, second.ID)
	}

	var count int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, idemKey).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 order row for this idempotency key, got %d", count)
	}
}

// TestCreateOrder_ExpiredHold_Rejected confirms CreateOrder refuses to
// build an order around a hold that's already expired, rather than
// silently accepting stale hold data from the client.
func TestCreateOrder_ExpiredHold_Rejected(t *testing.T) {
	env := setup(t)
	tenantID, _, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

	userID := uuid.New()
	seedUser(t, env, userID)

	holdToken := uuid.New()
	ok, err := env.repo.Hold(ctx, tenantID, inventoryID, holdToken, userID, time.Now().Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("seed hold failed: ok=%v err=%v", ok, err)
	}

	_, err = env.orderSvc.CreateOrder(ctx, tenantID, userID, "idem-"+uuid.NewString(), []ordersvc.HeldItem{
		{InventoryID: inventoryID, HoldToken: holdToken},
	})
	if !errors.Is(err, domain.ErrExpired) {
		t.Fatalf("expected domain.ErrExpired, got %v", err)
	}

	// The inventory row must be untouched — CreateOrder validates before
	// writing anything.
	row, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != domain.InventoryStatusHeld {
		t.Errorf("expected inventory to remain 'held' (untouched) after a rejected order, got %q", row.Status)
	}
}

// TestConfirmPayment_HoldExpiredMeanwhile_RefundsAndFails is the edge
// case the package doc in internal/service/order calls out explicitly:
// payment captures successfully, but by the time ConfirmPayment runs, the
// hold backing the order has been released (simulating the sweep
// reclaiming an expired hold). The order must NOT end up 'paid', the
// seat must NOT end up 'sold', and the mock gateway must have recorded a
// refund.
func TestConfirmPayment_HoldExpiredMeanwhile_RefundsAndFails(t *testing.T) {
	env := setup(t)
	tenantID, eventID, inventoryID := seedInventoryRow(t, env)
	ctx := context.Background()

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

	order, err := env.orderSvc.CreateOrder(ctx, tenantID, userID, "idem-"+uuid.NewString(), []ordersvc.HeldItem{
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

	// Simulate the sweep reclaiming this hold in the gap between
	// authorization and confirmation — exactly what SweepExpiredHolds
	// would do to a hold that outlasted HoldDuration.
	if ok, err := env.repo.Release(ctx, tenantID, inventoryID, hold.HoldToken); err != nil || !ok {
		t.Fatalf("simulated release failed: ok=%v err=%v", ok, err)
	}

	confirmErr := env.orderSvc.ConfirmPayment(ctx, tenantID, order.ID, paymentID)
	if confirmErr == nil {
		t.Fatal("expected ConfirmPayment to fail when the hold expired meanwhile")
	}

	finalOrder, err := env.orderRepo.GetByID(ctx, tenantID, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finalOrder.Status != domain.OrderStatusExpired {
		t.Errorf("expected order status 'expired' after a failed confirmation, got %q", finalOrder.Status)
	}

	finalPayment, err := env.paymentRepo.GetByID(ctx, tenantID, paymentID)
	if err != nil {
		t.Fatal(err)
	}
	if finalPayment.Status != domain.PaymentStatusRefunded {
		t.Errorf("expected payment status 'refunded', got %q", finalPayment.Status)
	}

	finalInv, err := env.repo.GetByID(ctx, tenantID, inventoryID)
	if err != nil {
		t.Fatal(err)
	}
	if finalInv.Status == domain.InventoryStatusSold {
		t.Error("the seat must never end up 'sold' when payment could not be confirmed against a valid hold")
	}
}
