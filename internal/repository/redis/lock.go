package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Locker provides short-lived, single-Redis-node distributed locks.
//
// Trade-off, stated plainly: this is NOT the multi-node Redlock
// algorithm — a single Redis instance is a single point of failure for
// the lock itself. That's an acceptable trade-off here specifically
// because the lock is an optimization, not a correctness mechanism (see
// the package doc in errors.go): if Redis is down or a lock is lost,
// worst case is extra contention hitting Postgres's own row-level
// compare-and-swap — never an oversold seat. A real Redlock
// implementation would be justified if this lock ever became load-bearing
// for correctness; today it isn't.
type Locker struct {
	client *redis.Client
}

func NewLocker(client *redis.Client) *Locker {
	return &Locker{client: client}
}

// unlockScript performs a check-and-delete atomically: it only deletes
// the key if its value still matches the token this caller set, so
// releasing a lock this caller no longer holds — e.g. because it already
// expired and was re-acquired by someone else — is a safe no-op instead
// of deleting a lock that isn't ours.
var unlockScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
else
	return 0
end
`)

// Acquire attempts to set key to a random token, succeeding only if the
// key doesn't already exist (SET NX), with the lock auto-expiring after
// ttl even if this process crashes before calling Release. Returns
// ok=false with a nil error if the lock is already held elsewhere —
// ordinary contention, not a failure.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (token string, ok bool, err error) {
	token = uuid.NewString()
	ok, err = l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return "", false, fmt.Errorf("redis: acquire lock %q: %w", key, err)
	}
	return token, ok, nil
}

// Release safely releases a lock previously acquired with Acquire.
// Returns ok=false if this token no longer owns the lock (already
// expired, or never held it) — not treated as an error, since Release is
// typically called from a defer, and an already-expired lock at that
// point is a normal outcome, not a bug.
func (l *Locker) Release(ctx context.Context, key, token string) (ok bool, err error) {
	res, err := unlockScript.Run(ctx, l.client, []string{key}, token).Int64()
	if err != nil {
		return false, fmt.Errorf("redis: release lock %q: %w", key, err)
	}
	return res == 1, nil
}
