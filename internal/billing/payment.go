// Package billing creates Stripe payments and processes Stripe webhooks.
//
// Two failure modes drive the design:
//   - Outgoing: a create call can time out after Stripe has already charged
//     the card. Every create carries an idempotency key, so a retry replays
//     the original result instead of charging again.
//   - Incoming: Stripe delivers webhooks at least once and in no particular
//     order. Events are deduplicated by ID inside the same transaction as
//     their side effects, and stale events are ignored.
package billing

import (
	"context"
	"time"
)

// Payment statuses. Apart from "pending" (row created, Stripe not yet
// called) they mirror Stripe's PaymentIntent statuses.
const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusCanceled  = "canceled"
)

type Payment struct {
	ID              int64      `json:"id"`
	UserID          int64      `json:"user_id"`
	PaymentIntentID *string    `json:"payment_intent_id"`
	AmountCents     int64      `json:"amount_cents"`
	Currency        string     `json:"currency"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	IdempotencyKey  string     `json:"-"`
	LastEventAt     *time.Time `json:"-"`
}

type NewPayment struct {
	UserID         int64
	IdempotencyKey string
	AmountCents    int64
	Currency       string
}

// Store is the persistence port for billing. Not-found and unique-violation
// errors wrap service.ErrNotFound and service.ErrConflict.
type Store interface {
	CreatePayment(ctx context.Context, p NewPayment) (Payment, error)
	GetPayment(ctx context.Context, id int64) (Payment, error)
	GetPaymentByIdempotencyKey(ctx context.Context, key string) (Payment, error)
	// ListPayments returns one user's payments, or everyone's when userID is nil.
	ListPayments(ctx context.Context, userID *int64, limit, offset int) ([]Payment, error)
	CountPayments(ctx context.Context, userID *int64) (int64, error)

	ListPaymentsSince(ctx context.Context, since time.Time) ([]Payment, error)
	// SucceededJobCounts maps payment ID to its number of payment_succeeded
	// notification jobs, for payments created since the given time.
	SucceededJobCounts(ctx context.Context, since time.Time) (map[int64]int, error)

	// InTx runs fn in one database transaction.
	InTx(ctx context.Context, fn func(Tx) error) error
}

// Tx is what status changes do inside their transaction.
type Tx interface {
	// RecordEvent inserts the event ID and reports whether it was new.
	RecordEvent(ctx context.Context, id, eventType string) (bool, error)
	GetPaymentForUpdate(ctx context.Context, id int64) (Payment, error)
	GetPaymentByIntentForUpdate(ctx context.Context, intentID string) (Payment, error)
	// ApplyPaymentStatus sets status and (if unset) the intent ID. A nil
	// eventAt keeps last_event_at unchanged.
	ApplyPaymentStatus(ctx context.Context, id int64, intentID, status string, eventAt *time.Time) error
	EnqueueNotification(ctx context.Context, kind string, payload []byte) error
}

type PaymentPage struct {
	Items  []Payment `json:"items"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
	Total  int64     `json:"total"`
}
