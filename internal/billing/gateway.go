package billing

import (
	"context"
	"errors"
	"time"
)

// ErrTransient marks gateway errors where the outcome is unknown or the call
// may succeed if repeated: timeouts, network errors, 429 and 5xx responses.
var ErrTransient = errors.New("billing: transient payment gateway error")

// ErrGatewayUnavailable is returned when Stripe could not be reached after
// all retries. The payment row stays pending; retrying with the same
// Idempotency-Key is safe.
var ErrGatewayUnavailable = errors.New("payment provider unavailable")

// PaymentIntent is the subset of a Stripe PaymentIntent that billing uses.
type PaymentIntent struct {
	ID          string
	AmountCents int64
	Currency    string
	Status      string
	PaymentID   int64 // from metadata["payment_id"]; 0 if absent
	Created     time.Time
}

type CreateIntentParams struct {
	AmountCents int64
	Currency    string
	PaymentID   int64
	// IdempotencyKey, when set, makes Stripe return the original result for
	// a repeated request instead of creating a second PaymentIntent.
	IdempotencyKey string
}

// Gateway is the payment provider. StripeGateway talks to Stripe;
// FakeStripe is an in-memory stand-in for tests and the simulator.
type Gateway interface {
	CreatePaymentIntent(ctx context.Context, p CreateIntentParams) (PaymentIntent, error)
	ListPaymentIntents(ctx context.Context, since time.Time) ([]PaymentIntent, error)
}
