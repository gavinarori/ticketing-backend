package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// WaitingRoom implements a per-event virtual queue: fans join a FIFO
// line, and a controlled number are admitted at a time, so the purchase
// path — and Postgres — only ever has to handle as much concurrency as it
// was sized for, instead of every fan for a 50,000-seat stadium hitting
// the hold endpoint in the same second.
//
// Backed by two Redis structures per event:
//   - a sorted set (queueKey) whose score is a strictly increasing
//     sequence number — not a timestamp, see Join for why — giving exact
//     FIFO ordering even across multiple app instances with imperfect
//     clock sync;
//   - a plain key with a TTL per admitted fan (admittedKey), present only
//     while they're inside their admission window.
type WaitingRoom struct {
	client *redis.Client
}

func NewWaitingRoom(client *redis.Client) *WaitingRoom {
	return &WaitingRoom{client: client}
}

func queueKey(eventID uuid.UUID) string { return fmt.Sprintf("waitingroom:queue:%s", eventID) }
func seqKey(eventID uuid.UUID) string   { return fmt.Sprintf("waitingroom:seq:%s", eventID) }
func admittedKey(eventID, userID uuid.UUID) string {
	return fmt.Sprintf("waitingroom:admitted:%s:%s", eventID, userID)
}

// Join adds userID to the back of the queue for eventID, idempotently:
// calling Join again for a user already queued does not move them (ZADD
// NX leaves an existing member's score untouched), and simply returns
// their current position.
//
// The score comes from an INCR'd per-event counter rather than a
// timestamp deliberately: wall-clock time can skew slightly between app
// instances under load, which could let a later joiner land a numerically
// earlier — and therefore unfair — score. A shared Redis counter can't
// drift that way.
func (w *WaitingRoom) Join(ctx context.Context, eventID, userID uuid.UUID) (position int64, err error) {
	seq, err := w.client.Incr(ctx, seqKey(eventID)).Result()
	if err != nil {
		return 0, fmt.Errorf("redis: waiting room join (seq): %w", err)
	}

	if err := w.client.ZAddNX(ctx, queueKey(eventID), redis.Z{Score: float64(seq), Member: userID.String()}).Err(); err != nil {
		return 0, fmt.Errorf("redis: waiting room join (zadd): %w", err)
	}

	return w.Position(ctx, eventID, userID)
}

// Position returns the caller's 1-based place in line, or ErrNotQueued if
// they haven't joined (or have already been admitted and removed from
// the queue).
func (w *WaitingRoom) Position(ctx context.Context, eventID, userID uuid.UUID) (int64, error) {
	rank, err := w.client.ZRank(ctx, queueKey(eventID), userID.String()).Result()
	if err == redis.Nil {
		return 0, ErrNotQueued
	}
	if err != nil {
		return 0, fmt.Errorf("redis: waiting room position: %w", err)
	}
	return rank + 1, nil
}

// QueueLength reports how many fans are currently waiting for eventID.
func (w *WaitingRoom) QueueLength(ctx context.Context, eventID uuid.UUID) (int64, error) {
	n, err := w.client.ZCard(ctx, queueKey(eventID)).Result()
	if err != nil {
		return 0, fmt.Errorf("redis: waiting room length: %w", err)
	}
	return n, nil
}

// Admit pops up to count fans off the front of the queue — lowest scores
// first; ZPOPMIN is atomic, so concurrent Admit calls from multiple
// worker instances can never double-admit the same fan — and marks each
// as admitted for admissionTTL. Returns the admitted user IDs.
//
// admissionTTL is a deliberate simplification worth knowing: a fan who
// doesn't complete a hold within that window is simply evicted, not
// automatically requeued. A production system would likely re-add them
// near the front of the queue on expiry; that's flagged here rather than
// silently assumed away.
func (w *WaitingRoom) Admit(ctx context.Context, eventID uuid.UUID, count int64, admissionTTL time.Duration) ([]uuid.UUID, error) {
	if count <= 0 {
		return nil, nil
	}

	popped, err := w.client.ZPopMin(ctx, queueKey(eventID), count).Result()
	if err != nil {
		return nil, fmt.Errorf("redis: waiting room admit (pop): %w", err)
	}
	if len(popped) == 0 {
		return nil, nil
	}

	admitted := make([]uuid.UUID, 0, len(popped))
	pipe := w.client.Pipeline()
	for _, z := range popped {
		id, err := uuid.Parse(fmt.Sprint(z.Member))
		if err != nil {
			// Skip a malformed member rather than failing the whole
			// batch. This should never happen — we only ever write
			// uuid.String() values — but a corrupt entry shouldn't be
			// able to wedge admission for everyone behind it.
			continue
		}
		pipe.Set(ctx, admittedKey(eventID, id), "1", admissionTTL)
		admitted = append(admitted, id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("redis: waiting room admit (mark): %w", err)
	}

	return admitted, nil
}

// IsAdmitted reports whether userID currently holds an active admission
// window for eventID. The inventory service checks this before ever
// attempting a hold.
func (w *WaitingRoom) IsAdmitted(ctx context.Context, eventID, userID uuid.UUID) (bool, error) {
	n, err := w.client.Exists(ctx, admittedKey(eventID, userID)).Result()
	if err != nil {
		return false, fmt.Errorf("redis: waiting room check admission: %w", err)
	}
	return n == 1, nil
}
