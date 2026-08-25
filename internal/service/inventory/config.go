package inventory

import "time"

// Config holds the tunables for the inventory locking service. Callers
// building the service for production should override at least
// HoldDuration and the rate limit based on real event characteristics —
// DefaultConfig exists for local development and tests.
type Config struct {
	// HoldDuration is how long a successful Hold reserves a seat before
	// it becomes eligible for the expiry sweep to reclaim it.
	HoldDuration time.Duration

	// LockTTL is how long the Redis per-row lock guarding a single Hold
	// attempt is held. Short and strict: if a Hold attempt somehow takes
	// longer than this (e.g. an unusually slow Postgres round trip), the
	// lock expires and a subsequent attempt is allowed to proceed rather
	// than being wedged behind a stuck lock indefinitely.
	LockTTL time.Duration

	// AdmissionTTL is how long a fan admitted from the waiting room has
	// to complete a hold before their admission window closes.
	AdmissionTTL time.Duration

	// HoldRateLimit and HoldRateLimitWindow bound how many hold attempts
	// a single user may make per event within the window, to blunt
	// scripted/bot hammering of the hold endpoint.
	HoldRateLimit       int
	HoldRateLimitWindow time.Duration
}

// DefaultConfig returns reasonable defaults for a football ticket drop.
func DefaultConfig() Config {
	return Config{
		HoldDuration:        5 * time.Minute,
		LockTTL:             3 * time.Second,
		AdmissionTTL:        10 * time.Minute,
		HoldRateLimit:       10,
		HoldRateLimitWindow: time.Minute,
	}
}
