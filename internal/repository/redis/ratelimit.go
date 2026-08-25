package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter implements a fixed-window counter: INCR a per-window key,
// EXPIRE it on first increment, compare against limit.
//
// Trade-off, stated plainly: fixed windows allow up to ~2x the stated
// limit right at a window boundary (e.g. a burst just before the window
// resets and another just after). A sliding-window-log or token-bucket
// algorithm avoids that at the cost of more Redis operations per check.
// That precision isn't needed here — this limiter exists to stop a single
// client from hammering the hold endpoint, not to enforce an exact
// business quota — and Postgres's own compare-and-swap remains the actual
// correctness guarantee regardless of how generous the limiter is at the
// edges.
type RateLimiter struct {
	client *redis.Client
}

func NewRateLimiter(client *redis.Client) *RateLimiter {
	return &RateLimiter{client: client}
}

// Allow increments the counter for key and reports whether the caller is
// still within limit for the current window. window is applied as the
// key's TTL only on the first increment of each window, so the window
// slides forward from whenever the key was first touched rather than
// from a fixed clock boundary.
func (l *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	count, err := l.client.Incr(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("redis: rate limit incr: %w", err)
	}
	if count == 1 {
		if err := l.client.Expire(ctx, key, window).Err(); err != nil {
			return false, fmt.Errorf("redis: rate limit expire: %w", err)
		}
	}
	return count <= int64(limit), nil
}
