// Package postgres implements the domain repository interfaces against
// PostgreSQL via pgx, following the schema in migrations/.
package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Executor is the subset of pgx's query methods that both *pgxpool.Pool
// and pgx.Tx satisfy. Every repository method in this package accepts a
// context and resolves its Executor from it via db(ctx) below, instead of
// taking a *pgxpool.Pool directly. That's what lets, e.g., the
// inventory repository and a future order repository share one
// transaction when the order-creation flow needs ConfirmSale plus
// order/order_item inserts to commit or roll back together (see
// WithTx).
type Executor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

type ctxKey string

const txKey ctxKey = "pgx_tx"

// WithTx runs fn inside a new database transaction: committing if fn
// returns nil, rolling back otherwise — including on panic, where the
// deferred recover rolls back and then re-panics so the transaction never
// leaks half-open. Repository calls made through fn's context
// automatically participate in this transaction via db(ctx).
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context) error) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin tx: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		err = tx.Commit(ctx)
	}()

	err = fn(context.WithValue(ctx, txKey, tx))
	return err
}

// db resolves the Executor to use for a repository call: the transaction
// stashed in ctx by WithTx if present, otherwise the ambient pool. This
// lets every repository method be written once and work correctly
// whether it's called standalone or as part of a larger transaction.
func db(ctx context.Context, pool *pgxpool.Pool) Executor {
	if tx, ok := ctx.Value(txKey).(pgx.Tx); ok {
		return tx
	}
	return pool
}

// SetTenantContext issues `SELECT set_config('app.current_tenant_id', ...,
// true)` against the executor active in ctx — the transactional
// equivalent of `SET LOCAL app.current_tenant_id = ...`, but safely
// parameterized (SET itself doesn't accept placeholders, so building it
// via string interpolation would be a SQL-injection footgun; set_config
// avoids that entirely).
//
// This only has an effect inside a transaction (is_local=true scopes it
// to the current transaction) — call it inside WithTx, before any other
// query on that ctx. It's what makes the RLS policies from
// migrations/000011_row_level_security.up.sql actually apply for
// whichever database role the app connects as. See
// docs/database-schema.md for the important caveat that the default
// table-owner role used in local dev bypasses RLS regardless of this
// call.
func SetTenantContext(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) error {
	_, err := db(ctx, pool).Exec(ctx, `SELECT set_config('app.current_tenant_id', $1, true)`, tenantID.String())
	if err != nil {
		return fmt.Errorf("postgres: set tenant context: %w", err)
	}
	return nil
}
