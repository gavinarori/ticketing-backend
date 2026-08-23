// Package database wires up the Postgres connection pool. Postgres is the
// system of record for this platform — Redis locks make purchases fast and
// safe under contention, but Postgres is what we trust when the two ever
// disagree (see docs/inventory-locking.md, added alongside the inventory
// service).
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gavinarori/ticketing-backend/internal/config"
)

// NewPool creates and validates a pgx connection pool from the given config.
// It pings the database before returning so that startup fails fast if
// Postgres is unreachable — we never want the API to report itself healthy
// while unable to reach the source of truth for seat inventory.
func NewPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("database: parse dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxOpenConns
	poolCfg.MinConns = cfg.MaxIdleConns
	poolCfg.MaxConnLifetime = cfg.ConnMaxLifetime
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	poolCfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("database: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}

	return pool, nil
}
