package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/service"
)

// scriptedGateway returns the queued errors in order, then succeeds, and
// records the idempotency key of every call.
type scriptedGateway struct {
	errs []error
	keys []string
}

func (g *scriptedGateway) CreatePaymentIntent(_ context.Context, p CreateIntentParams) (PaymentIntent, error) {
	g.keys = append(g.keys, p.IdempotencyKey)
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		return PaymentIntent{}, err
	}
	return PaymentIntent{ID: "pi_1", Status: StatusSucceeded}, nil
}

func (g *scriptedGateway) ListPaymentIntents(context.Context, time.Time) ([]PaymentIntent, error) {
	return nil, nil
}

func TestCreateIntentRetries(t *testing.T) {
	transient := ErrTransient
	permanent := errors.New("card_error: invalid amount")

	tests := []struct {
		name        string
		idempotency bool
		errs        []error
		wantCalls   int
		wantErr     error
		wantKeys    string // "same" (all equal to the payment's key) or "none"
	}{
		{name: "first try", idempotency: true, wantCalls: 1, wantKeys: "same"},
		{name: "retry after lost response reuses key", idempotency: true, errs: []error{transient, transient}, wantCalls: 3, wantKeys: "same"},
		{name: "gives up after max attempts", idempotency: true, errs: []error{transient, transient, transient}, wantCalls: 3, wantErr: ErrGatewayUnavailable, wantKeys: "same"},
		{name: "permanent error is not retried", idempotency: true, errs: []error{permanent}, wantCalls: 1, wantErr: permanent, wantKeys: "same"},
		{name: "without idempotency no key is sent", idempotency: false, errs: []error{transient}, wantCalls: 2, wantKeys: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := &scriptedGateway{errs: tt.errs}
			opts := Options{Idempotency: tt.idempotency, MaxAttempts: 3}
			s := NewService(nil, gw, "", opts)

			_, err := s.createIntent(context.Background(), Payment{ID: 1, AmountCents: 500, Currency: "usd", IdempotencyKey: "user:1:abc"})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Len(t, gw.keys, tt.wantCalls)
			for _, k := range gw.keys {
				if tt.wantKeys == "same" {
					assert.Equal(t, "user:1:abc", k)
				} else {
					assert.Empty(t, k)
				}
			}
		})
	}
}

func TestShouldApply(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	at := func(s int) *time.Time { v := t0.Add(time.Duration(s) * time.Second); return &v }

	tests := []struct {
		name    string
		status  string
		last    *time.Time
		eventAt time.Time
		want    bool
	}{
		{"first event", StatusPending, nil, t0, true},
		{"newer event", "processing", at(0), *at(1), true},
		{"same second: later arrival wins", "processing", at(1), *at(1), true},
		{"stale event is ignored", "processing", at(2), *at(1), false},
		{"succeeded is terminal", StatusSucceeded, at(1), *at(5), false},
		{"canceled is terminal", StatusCanceled, at(1), *at(5), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldApply(Payment{Status: tt.status, LastEventAt: tt.last}, tt.eventAt))
		})
	}
}

func TestHandleWebhookRejectsBadSignature(t *testing.T) {
	s := NewService(nil, nil, "whsec_real", DefaultOptions())
	payload := EventPayload("evt_1", "payment_intent.succeeded", time.Now(), PaymentIntent{ID: "pi_1"}, StatusSucceeded)

	tests := []struct {
		name      string
		signature string
	}{
		{"missing header", ""},
		{"garbage header", "t=1,v1=deadbeef"},
		{"signed with another secret", signedHeader(payload, "whsec_attacker")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.HandleWebhook(context.Background(), payload, tt.signature)
			require.ErrorIs(t, err, service.ErrInvalid)
		})
	}
}

func TestCreatePaymentValidation(t *testing.T) {
	s := NewService(nil, nil, "", DefaultOptions())
	tests := []struct {
		name string
		in   CreatePaymentInput
		key  string
	}{
		{"below Stripe minimum", CreatePaymentInput{AmountCents: 49}, ""},
		{"too large", CreatePaymentInput{AmountCents: 100_000_000}, ""},
		{"bad currency", CreatePaymentInput{AmountCents: 500, Currency: "us1"}, ""},
		{"key too long", CreatePaymentInput{AmountCents: 500}, string(make([]byte, 201))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.CreatePayment(context.Background(), 1, tt.key, tt.in)
			require.ErrorIs(t, err, service.ErrInvalid)
		})
	}
}

func TestFakeStripeIdempotency(t *testing.T) {
	ctx := context.Background()

	f := NewFakeStripe(1, 0)
	a, err := f.CreatePaymentIntent(ctx, CreateIntentParams{AmountCents: 500, Currency: "usd", IdempotencyKey: "k1"})
	require.NoError(t, err)
	b, err := f.CreatePaymentIntent(ctx, CreateIntentParams{AmountCents: 500, Currency: "usd", IdempotencyKey: "k1"})
	require.NoError(t, err)
	assert.Equal(t, a.ID, b.ID, "same key replays the same intent")
	assert.Len(t, f.Events(), 3, "replay emits no new events")

	_, err = f.CreatePaymentIntent(ctx, CreateIntentParams{AmountCents: 500, Currency: "usd"})
	require.NoError(t, err)
	_, err = f.CreatePaymentIntent(ctx, CreateIntentParams{AmountCents: 500, Currency: "usd"})
	require.NoError(t, err)
	all, err := f.ListPaymentIntents(ctx, time.Time{})
	require.NoError(t, err)
	assert.Len(t, all, 3, "no key: every call creates a new intent")

	lossy := NewFakeStripe(1, 1)
	_, err = lossy.CreatePaymentIntent(ctx, CreateIntentParams{AmountCents: 500, Currency: "usd"})
	require.ErrorIs(t, err, ErrTransient)
	created, _ := lossy.ListPaymentIntents(ctx, time.Time{})
	assert.Len(t, created, 1, "a lost response still created (charged) the intent")
}
