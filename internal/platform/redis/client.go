// Package redis wires up the Redis client used for distributed seat locks,
// the virtual waiting room, and rate limiting. These are on the hottest
// path in the system (every ticket purchase attempt touches Redis before
// it ever touches Postgres), so the client is configured with a sizeable
// pool and short, explicit timeouts rather than library defaults.
package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gavinarori/ticketing-backend/internal/config"
)

// NewClient creates and validates a Redis client from the given config.
// Like the Postgres pool, it pings before returning so a broken Redis
// connection surfaces at startup, not on the first fan trying to buy a
// ticket.
func NewClient(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}

	return client, nil
}
