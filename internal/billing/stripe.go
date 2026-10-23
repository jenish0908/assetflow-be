package billing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
)

// StripeGateway charges a saved payment method server-side (confirm=true).
// In test mode "pm_card_visa" always succeeds.
type StripeGateway struct {
	api           *client.API
	paymentMethod string
}

func NewStripeGateway(secretKey, paymentMethod string) *StripeGateway {
	// Retries are owned by Service.createIntent so every attempt is explicit.
	// (stripe-go's built-in retries would also be safe: it adds an
	// idempotency key to retried POSTs automatically.)
	cfg := &stripe.BackendConfig{MaxNetworkRetries: stripe.Int64(0)}
	backends := &stripe.Backends{
		API:     stripe.GetBackendWithConfig(stripe.APIBackend, cfg),
		Connect: stripe.GetBackendWithConfig(stripe.ConnectBackend, cfg),
		Uploads: stripe.GetBackendWithConfig(stripe.UploadsBackend, cfg),
	}
	return &StripeGateway{api: client.New(secretKey, backends), paymentMethod: paymentMethod}
}

func (g *StripeGateway) CreatePaymentIntent(ctx context.Context, p CreateIntentParams) (PaymentIntent, error) {
	params := &stripe.PaymentIntentParams{
		Amount:        stripe.Int64(p.AmountCents),
		Currency:      stripe.String(p.Currency),
		PaymentMethod: stripe.String(g.paymentMethod),
		Confirm:       stripe.Bool(true),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled:        stripe.Bool(true),
			AllowRedirects: stripe.String("never"),
		},
	}
	params.Context = ctx
	params.AddMetadata("payment_id", strconv.FormatInt(p.PaymentID, 10))
	if p.IdempotencyKey != "" {
		params.SetIdempotencyKey(p.IdempotencyKey)
	}

	pi, err := g.api.PaymentIntents.New(params)
	if err == nil {
		return fromStripe(pi), nil
	}

	var se *stripe.Error
	if !errors.As(err, &se) {
		// No response from Stripe: the request may or may not have been applied.
		return PaymentIntent{}, fmt.Errorf("%w: %w", ErrTransient, err)
	}
	switch {
	case se.Type == stripe.ErrorTypeCard && se.PaymentIntent != nil:
		// A decline still creates the intent, in a failed state.
		return fromStripe(se.PaymentIntent), nil
	case se.HTTPStatusCode == http.StatusTooManyRequests,
		se.HTTPStatusCode == http.StatusConflict, // concurrent request with the same idempotency key
		se.HTTPStatusCode >= 500:
		return PaymentIntent{}, fmt.Errorf("%w: %w", ErrTransient, err)
	}
	return PaymentIntent{}, fmt.Errorf("billing: stripe create payment intent: %w", err)
}

func (g *StripeGateway) ListPaymentIntents(ctx context.Context, since time.Time) ([]PaymentIntent, error) {
	params := &stripe.PaymentIntentListParams{
		CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: since.Unix()},
	}
	params.Context = ctx

	var out []PaymentIntent
	it := g.api.PaymentIntents.List(params) // the iterator fetches further pages as needed
	for it.Next() {
		out = append(out, fromStripe(it.PaymentIntent()))
	}
	if err := it.Err(); err != nil {
		return nil, fmt.Errorf("billing: list payment intents: %w", err)
	}
	return out, nil
}

func fromStripe(pi *stripe.PaymentIntent) PaymentIntent {
	id, _ := strconv.ParseInt(pi.Metadata["payment_id"], 10, 64)
	return PaymentIntent{
		ID:          pi.ID,
		AmountCents: pi.Amount,
		Currency:    string(pi.Currency),
		Status:      string(pi.Status),
		PaymentID:   id,
		Created:     time.Unix(pi.Created, 0).UTC(),
	}
}
