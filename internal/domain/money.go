package domain

import "fmt"

// Money represents an amount as an integer count of the smallest currency
// unit (cents) plus an ISO 4217 currency code. float64 is never used for
// money anywhere in this codebase — binary floating-point rounding errors
// are unacceptable once you're charging real fans real money at scale.
type Money struct {
	Cents    int64
	Currency string // ISO 4217, e.g. "KES", "USD"
}

// NewMoney validates and constructs a Money value.
func NewMoney(cents int64, currency string) (Money, error) {
	if cents < 0 {
		return Money{}, NewError("domain.NewMoney", fmt.Errorf("%w: cents must be >= 0, got %d", ErrInvalidInput, cents))
	}
	if len(currency) != 3 {
		return Money{}, NewError("domain.NewMoney", fmt.Errorf("%w: currency must be a 3-letter ISO 4217 code, got %q", ErrInvalidInput, currency))
	}
	return Money{Cents: cents, Currency: currency}, nil
}

// Add returns the sum of two Money values. Both must share a currency —
// summing KES cents and USD cents directly would silently produce a
// meaningless number, so this fails loudly instead of guessing.
func (m Money) Add(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, NewError("Money.Add", fmt.Errorf("%w: currency mismatch %q vs %q", ErrInvalidInput, m.Currency, other.Currency))
	}
	return Money{Cents: m.Cents + other.Cents, Currency: m.Currency}, nil
}

// String renders a human-readable amount for logs/debugging only, e.g.
// "250.00 KES". API responses should send Cents and Currency as separate
// fields and let the client format for its own locale.
func (m Money) String() string {
	return fmt.Sprintf("%d.%02d %s", m.Cents/100, m.Cents%100, m.Currency)
}
