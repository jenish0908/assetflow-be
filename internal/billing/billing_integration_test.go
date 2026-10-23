package billing_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/service"
	"github.com/jenish0908/assetflow-be/internal/store"
)

// Integration tests: need TEST_DATABASE_URL pointing at a migrated database.
// Every test creates uniquely named rows, so they can share it.

const secret = "whsec_test"

type env struct {
	pool   *pgxpool.Pool
	store  *store.BillingStore
	userID int64
	sfx    string
}

func setup(t *testing.T) env {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := store.NewPool(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sfx := fmt.Sprintf("%d", time.Now().UnixNano())
	u, err := store.New(pool).CreateUser(ctx, service.NewUser{
		Email: "billing-" + sfx + "@test.dev", Name: "Billing", PasswordHash: "x", Role: auth.RoleViewer,
	})
	require.NoError(t, err)
	return env{pool: pool, store: store.NewBilling(pool), userID: u.ID, sfx: sfx}
}

// paymentWithIntent inserts a payment that already has a Stripe intent.
func (e env) paymentWithIntent(t *testing.T) billing.Payment {
	t.Helper()
	ctx := context.Background()
	p, err := e.store.CreatePayment(ctx, billing.NewPayment{
		UserID: e.userID, IdempotencyKey: "key-" + e.sfx, AmountCents: 1500, Currency: "usd",
	})
	require.NoError(t, err)
	require.NoError(t, e.store.InTx(ctx, func(tx billing.Tx) error {
		return tx.ApplyPaymentStatus(ctx, p.ID, "pi_"+e.sfx, "processing", nil)
	}))
	p, err = e.store.GetPayment(ctx, p.ID)
	require.NoError(t, err)
	return p
}

func (e env) event(t *testing.T, id, eventType, status string, at time.Time, p billing.Payment) ([]byte, string) {
	t.Helper()
	intentID := "pi_" + e.sfx
	payload := billing.EventPayload(id, eventType, at, billing.PaymentIntent{
		ID: intentID, AmountCents: p.AmountCents, Currency: p.Currency, PaymentID: p.ID, Created: at,
	}, status)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret})
	return payload, signed.Header
}

func (e env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func (e env) succeededJobs(t *testing.T, paymentID int64) int {
	return e.count(t, `SELECT count(*) FROM notification_jobs
		WHERE kind = 'payment_succeeded' AND (payload->>'payment_id')::bigint = $1`, paymentID)
}

func (e env) status(t *testing.T, paymentID int64) string {
	t.Helper()
	p, err := e.store.GetPayment(context.Background(), paymentID)
	require.NoError(t, err)
	return p.Status
}

// TestDuplicateWebhookProcessedOnce delivers the same signed event several
// times at exactly the same moment. With dedupe, the side effects (status
// change + notification job) happen once. The guards-off case proves the test
// would catch a regression.
func TestDuplicateWebhookProcessedOnce(t *testing.T) {
	tests := []struct {
		name       string
		guards     bool
		deliveries int
		wantJobs   int
	}{
		{"same webhook twice, dedupe on", true, 2, 1},
		{"same webhook ten times, dedupe on", true, 10, 1},
		{"same webhook twice, dedupe off (the bug)", false, 2, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			p := e.paymentWithIntent(t)
			opts := billing.DefaultOptions()
			opts.WebhookGuards = tt.guards
			svc := billing.NewService(e.store, nil, secret, opts)

			eventID := "evt_dup_" + e.sfx
			payload, header := e.event(t, eventID, "payment_intent.succeeded", billing.StatusSucceeded, time.Now(), p)

			start := make(chan struct{})
			errs := make([]error, tt.deliveries)
			var wg sync.WaitGroup
			for i := range tt.deliveries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start // release all goroutines together
					errs[i] = svc.HandleWebhook(context.Background(), payload, header)
				}()
			}
			close(start)
			wg.Wait()

			for _, err := range errs {
				require.NoError(t, err, "every delivery is acknowledged, duplicates included")
			}
			assert.Equal(t, tt.wantJobs, e.succeededJobs(t, p.ID))
			assert.Equal(t, billing.StatusSucceeded, e.status(t, p.ID))
			if tt.guards {
				assert.Equal(t, 1, e.count(t, "SELECT count(*) FROM stripe_events WHERE id = $1", eventID))
			}
		})
	}
}

// TestOutOfOrderEvents delivers events newest-first; stale events must not
// move the payment backwards.
func TestOutOfOrderEvents(t *testing.T) {
	t0 := time.Now().Truncate(time.Second)
	type delivery struct {
		eventType, status string
		at                time.Time
	}

	tests := []struct {
		name       string
		deliveries []delivery
		wantStatus string
		wantJobs   int
	}{
		{
			name: "succeeded arrives first, older events ignored",
			deliveries: []delivery{
				{"payment_intent.succeeded", billing.StatusSucceeded, t0.Add(2 * time.Second)},
				{"payment_intent.processing", "processing", t0.Add(time.Second)},
				{"payment_intent.created", "requires_confirmation", t0},
			},
			wantStatus: billing.StatusSucceeded,
			wantJobs:   1,
		},
		{
			name: "stale event cannot overwrite a newer non-terminal state",
			deliveries: []delivery{
				{"payment_intent.processing", "processing", t0.Add(time.Second)},
				{"payment_intent.created", "requires_confirmation", t0},
			},
			wantStatus: "processing",
		},
		{
			name: "in-order events all apply",
			deliveries: []delivery{
				{"payment_intent.created", "requires_confirmation", t0},
				{"payment_intent.payment_failed", "requires_payment_method", t0.Add(time.Second)},
			},
			wantStatus: "requires_payment_method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := setup(t)
			p := e.paymentWithIntent(t)
			svc := billing.NewService(e.store, nil, secret, billing.DefaultOptions())

			for i, d := range tt.deliveries {
				payload, header := e.event(t, fmt.Sprintf("evt_ooo_%s_%d", e.sfx, i), d.eventType, d.status, d.at, p)
				require.NoError(t, svc.HandleWebhook(context.Background(), payload, header))
			}
			assert.Equal(t, tt.wantStatus, e.status(t, p.ID))
			assert.Equal(t, tt.wantJobs, e.succeededJobs(t, p.ID))
		})
	}
}

// TestCreatePaymentIdempotency covers the outgoing side end to end: Stripe
// is down (every response lost), the client gets an error and retries with
// the same Idempotency-Key after Stripe recovers. Exactly one intent exists.
func TestCreatePaymentIdempotency(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fake := billing.NewFakeStripe(1, 1) // every response lost
	opts := billing.DefaultOptions()
	opts.RetryBackoff = time.Millisecond
	svc := billing.NewService(e.store, fake, secret, opts)
	in := billing.CreatePaymentInput{AmountCents: 2500, Currency: "usd"}

	_, err := svc.CreatePayment(ctx, e.userID, "client-key", in)
	require.ErrorIs(t, err, billing.ErrGatewayUnavailable)

	fake.SetLostResponseRate(0) // Stripe recovers
	first, err := svc.CreatePayment(ctx, e.userID, "client-key", in)
	require.NoError(t, err)
	assert.True(t, first.Replayed, "the pending row from the failed attempt is reused")
	require.NotNil(t, first.Payment.PaymentIntentID)

	again, err := svc.CreatePayment(ctx, e.userID, "client-key", in)
	require.NoError(t, err)
	assert.Equal(t, first.Payment.ID, again.Payment.ID)

	intents, err := fake.ListPaymentIntents(ctx, time.Time{})
	require.NoError(t, err)
	assert.Len(t, intents, 1, "3 lost attempts + 2 client retries still created one charge")

	in.AmountCents = 9999
	_, err = svc.CreatePayment(ctx, e.userID, "client-key", in)
	require.ErrorIs(t, err, service.ErrConflict, "same key with different parameters is rejected")
}

// TestSucceededResponseThenWebhook is the regression test for a bug found by
// cmd/billingsim: with confirm=true Stripe's create response already says
// "succeeded", the webhook then sees a terminal payment and is skipped, so
// the notification was never enqueued. It must be enqueued exactly once
// whichever message arrives first.
func TestSucceededResponseThenWebhook(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fake := billing.NewFakeStripe(1, 0)
	svc := billing.NewService(e.store, fake, secret, billing.DefaultOptions())

	res, err := svc.CreatePayment(ctx, e.userID, "resp-"+e.sfx, billing.CreatePaymentInput{AmountCents: 1200})
	require.NoError(t, err)
	require.Equal(t, billing.StatusSucceeded, res.Payment.Status)
	assert.Equal(t, 1, e.succeededJobs(t, res.Payment.ID), "response path enqueues")

	for _, ev := range fake.Events() {
		header := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: ev.Payload, Secret: secret}).Header
		require.NoError(t, svc.HandleWebhook(ctx, ev.Payload, header))
	}
	assert.Equal(t, 1, e.succeededJobs(t, res.Payment.ID), "webhook does not enqueue again")
	assert.Equal(t, billing.StatusSucceeded, e.status(t, res.Payment.ID))
}

// TestWebhookBeforeIntentSaved covers the race where Stripe's webhook
// arrives before CreatePayment stored the intent ID on the row.
func TestWebhookBeforeIntentSaved(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	svc := billing.NewService(e.store, nil, secret, billing.DefaultOptions())

	p, err := e.store.CreatePayment(ctx, billing.NewPayment{
		UserID: e.userID, IdempotencyKey: "race-" + e.sfx, AmountCents: 700, Currency: "usd",
	})
	require.NoError(t, err)

	payload, header := e.event(t, "evt_early_"+e.sfx, "payment_intent.succeeded", billing.StatusSucceeded, time.Now(), p)
	require.NoError(t, svc.HandleWebhook(ctx, payload, header))

	got, err := e.store.GetPayment(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, billing.StatusSucceeded, got.Status)
	require.NotNil(t, got.PaymentIntentID, "intent ID taken from the event via metadata")

	// An event for one of our intents whose row does not exist yet is
	// rejected without being recorded, so Stripe's redelivery is processed.
	payload = billing.EventPayload("evt_missing_"+e.sfx, "payment_intent.succeeded", time.Now(), billing.PaymentIntent{
		ID: "pi_missing_" + e.sfx, AmountCents: 700, Currency: "usd", PaymentID: 1 << 40,
	}, billing.StatusSucceeded)
	header = webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret}).Header
	require.Error(t, svc.HandleWebhook(ctx, payload, header))
	assert.Equal(t, 0, e.count(t, "SELECT count(*) FROM stripe_events WHERE id = $1", "evt_missing_"+e.sfx))
}
