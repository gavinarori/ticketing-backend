package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/domain"
)

// InventoryRepo implements domain.InventoryRepository against the
// event_seat_inventory table described in
// migrations/000006_inventory.up.sql. Every state-changing method here is
// a single conditional UPDATE — see that migration's header comment for
// why that, on its own, is sufficient to guarantee no seat is ever sold
// twice, independent of anything happening in Redis above it.
type InventoryRepo struct {
	pool *pgxpool.Pool
}

func NewInventoryRepo(pool *pgxpool.Pool) *InventoryRepo {
	return &InventoryRepo{pool: pool}
}

// Compile-time check that InventoryRepo satisfies the domain interface.
var _ domain.InventoryRepository = (*InventoryRepo)(nil)

const inventoryColumns = `id, tenant_id, event_id, event_ticket_category_id, seat_id, status, hold_token, held_by_user_id, hold_expires_at, created_at, updated_at`

func scanInventory(row pgx.Row) (*domain.EventSeatInventory, error) {
	var inv domain.EventSeatInventory
	var status string
	if err := row.Scan(
		&inv.ID, &inv.TenantID, &inv.EventID, &inv.EventTicketCategoryID, &inv.SeatID,
		&status, &inv.HoldToken, &inv.HeldByUserID, &inv.HoldExpiresAt,
		&inv.CreatedAt, &inv.UpdatedAt,
	); err != nil {
		return nil, err
	}
	inv.Status = domain.InventoryStatus(status)
	return &inv, nil
}

// BulkCreate inserts the full set of sellable units for an event via
// pgx's pipelined batch support rather than one INSERT per row — an event
// can have tens of thousands of GA/seated units generated at once, and
// paying a network round trip per row would make "publish this event"
// unacceptably slow.
func (r *InventoryRepo) BulkCreate(ctx context.Context, rows []*domain.EventSeatInventory) error {
	if len(rows) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, row := range rows {
		id := row.ID
		if id == uuid.Nil {
			id = uuid.New()
		}
		batch.Queue(
			`INSERT INTO event_seat_inventory (id, tenant_id, event_id, event_ticket_category_id, seat_id, status)
			 VALUES ($1, $2, $3, $4, $5, 'available')`,
			id, row.TenantID, row.EventID, row.EventTicketCategoryID, row.SeatID,
		)
	}

	br := db(ctx, r.pool).SendBatch(ctx, batch)
	defer br.Close()

	for range rows {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("postgres: bulk create inventory: %w", err)
		}
	}
	return nil
}

func (r *InventoryRepo) GetByID(ctx context.Context, tenantID, id uuid.UUID) (*domain.EventSeatInventory, error) {
	row := db(ctx, r.pool).QueryRow(ctx,
		`SELECT `+inventoryColumns+` FROM event_seat_inventory WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	inv, err := scanInventory(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NewError("postgres.InventoryRepo.GetByID", domain.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get inventory by id: %w", err)
	}
	return inv, nil
}

// Hold implements the atomic compare-and-swap described in
// migrations/000006_inventory.up.sql: the UPDATE's WHERE clause IS the
// entire concurrency control mechanism. RowsAffected() == 0 means someone
// else's Hold/ConfirmSale already changed this row's status away from
// 'available' between when the caller looked at it and when this ran —
// an ordinary, frequent outcome during a drop, surfaced as (false, nil),
// never as an error.
func (r *InventoryRepo) Hold(ctx context.Context, tenantID, id uuid.UUID, holdToken, userID uuid.UUID, expiresAt time.Time) (bool, error) {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE event_seat_inventory
		 SET status = 'held', hold_token = $1, held_by_user_id = $2, hold_expires_at = $3
		 WHERE id = $4 AND tenant_id = $5 AND status = 'available'`,
		holdToken, userID, expiresAt, id, tenantID,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: hold inventory: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Release is gated on hold_token matching, not just status = 'held' — see
// domain.InventoryRepository.Release for why: it stops a stale or expired
// release request from clobbering a seat that a *different* hold has
// since claimed. Note held_by_user_id is cleared here too: the CHECK
// constraint on event_seat_inventory requires it whenever status isn't
// 'held', and "who currently holds this seat" isn't information worth
// keeping once the hold is gone (who *bought* it lives in orders, not
// here).
func (r *InventoryRepo) Release(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error) {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE event_seat_inventory
		 SET status = 'available', hold_token = NULL, held_by_user_id = NULL, hold_expires_at = NULL
		 WHERE id = $1 AND tenant_id = $2 AND status = 'held' AND hold_token = $3`,
		id, tenantID, holdToken,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: release inventory: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ConfirmSale atomically transitions 'held' -> 'sold', gated on
// hold_token matching for the same reason as Release. It is intended to
// be called with a ctx carrying a WithTx transaction shared with
// order/order_item creation (a later task), so a payment can never be
// recorded as successful against a seat whose hold expired moments
// earlier.
func (r *InventoryRepo) ConfirmSale(ctx context.Context, tenantID, id uuid.UUID, holdToken uuid.UUID) (bool, error) {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE event_seat_inventory
		 SET status = 'sold', hold_token = NULL, held_by_user_id = NULL, hold_expires_at = NULL
		 WHERE id = $1 AND tenant_id = $2 AND status = 'held' AND hold_token = $3`,
		id, tenantID, holdToken,
	)
	if err != nil {
		return false, fmt.Errorf("postgres: confirm sale: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Void permanently marks a row unsellable (e.g. a venue safety issue
// removes a seat from sale). An administrative action outside the
// purchase race, so — unlike Hold/Release/ConfirmSale — it returns a
// plain error rather than a (bool, error): there's no "someone else got
// there first" outcome to distinguish here, only success or genuine
// failure (including "no such row").
func (r *InventoryRepo) Void(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := db(ctx, r.pool).Exec(ctx,
		`UPDATE event_seat_inventory
		 SET status = 'void', hold_token = NULL, held_by_user_id = NULL, hold_expires_at = NULL
		 WHERE id = $1 AND tenant_id = $2`,
		id, tenantID,
	)
	if err != nil {
		return fmt.Errorf("postgres: void inventory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError("postgres.InventoryRepo.Void", domain.ErrNotFound)
	}
	return nil
}

// ListExpiredHolds powers the worker's sweep loop — see
// idx_esi_hold_expiry in migrations/000006_inventory.up.sql, the partial
// index that keeps this query cheap regardless of how large the table
// grows, since only 'held' rows are ever indexed there.
func (r *InventoryRepo) ListExpiredHolds(ctx context.Context, before time.Time, limit int) ([]*domain.EventSeatInventory, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT `+inventoryColumns+`
		 FROM event_seat_inventory
		 WHERE status = 'held' AND hold_expires_at < $1
		 ORDER BY hold_expires_at
		 LIMIT $2`,
		before, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: list expired holds: %w", err)
	}
	defer rows.Close()

	var result []*domain.EventSeatInventory
	for rows.Next() {
		inv, err := scanInventory(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan expired hold: %w", err)
		}
		result = append(result, inv)
	}
	return result, rows.Err()
}

// CountByStatus powers "N of M remaining" displays without loading every
// inventory row for an event.
func (r *InventoryRepo) CountByStatus(ctx context.Context, tenantID, eventID uuid.UUID) (map[uuid.UUID]domain.InventoryCounts, error) {
	rows, err := db(ctx, r.pool).Query(ctx,
		`SELECT event_ticket_category_id, status, count(*)
		 FROM event_seat_inventory
		 WHERE tenant_id = $1 AND event_id = $2
		 GROUP BY event_ticket_category_id, status`,
		tenantID, eventID,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: count inventory by status: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID]domain.InventoryCounts)
	for rows.Next() {
		var categoryID uuid.UUID
		var status string
		var count int
		if err := rows.Scan(&categoryID, &status, &count); err != nil {
			return nil, fmt.Errorf("postgres: scan inventory count: %w", err)
		}
		counts := result[categoryID]
		switch domain.InventoryStatus(status) {
		case domain.InventoryStatusAvailable:
			counts.Available = count
		case domain.InventoryStatusHeld:
			counts.Held = count
		case domain.InventoryStatusSold:
			counts.Sold = count
		case domain.InventoryStatusVoid:
			counts.Void = count
		}
		result[categoryID] = counts
	}
	return result, rows.Err()
}
