// Package redis implements the Redis-backed pieces of the purchase path
// that sit ahead of Postgres: a short-lived distributed lock that cheaply
// rejects thundering-herd contention on a single hot seat before it ever
// reaches the database, a virtual waiting room, and per-user rate
// limiting.
//
// None of this package is load-bearing for correctness. The Postgres
// compare-and-swap in internal/repository/postgres/inventory.go is what
// actually guarantees a seat is never sold twice, with or without Redis
// in front of it. Everything here exists purely to make contention cheap
// and fast, and to keep hopeless requests — rate-limited, or not yet
// admitted from the waiting room — from reaching Postgres at all.
package redis

import "errors"

// ErrNotQueued is returned by WaitingRoom.Position when the caller has
// never joined the queue for that event, or has already been admitted and
// removed from it.
var ErrNotQueued = errors.New("redis: not in queue")

// ErrLockNotAcquired is available for callers that want to treat a failed
// Locker.Acquire as an error rather than branching on its bool return —
// kept here so any such callers share one sentinel instead of each
// inventing their own.
var ErrLockNotAcquired = errors.New("redis: lock not acquired")
