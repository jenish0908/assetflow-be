package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/stripe/stripe-go/v81"
)

// FakeStripe is an in-memory payment gateway that follows Stripe's
// documented behaviour where it matters here:
//   - a repeated idempotency key returns the original PaymentIntent;
//   - confirm=true with a good card ends in "succeeded", emitting
//     payment_intent.created, .processing and .succeeded events, each with a
//     later created timestamp.
//
// It can also lose responses: the intent is created (the card is charged) but
// the caller gets ErrTransient, the classic "did my request go through?" case.
type FakeStripe struct {
	mu               sync.Mutex
	prefix           string // unique per instance, so IDs never collide across runs
	rng              *rand.Rand
	lostResponseRate float64
	clock            time.Time
	intents          []*PaymentIntent
	byKey            map[string]*PaymentIntent
	events           []FakeEvent
}

// FakeEvent is a webhook event as Stripe would POST it (unsigned).
type FakeEvent struct {
	ID      string
	Payload []byte
}

func NewFakeStripe(seed int64, lostResponseRate float64) *FakeStripe {
	return &FakeStripe{
		prefix:           strconv.FormatInt(time.Now().UnixNano(), 36),
		rng:              rand.New(rand.NewSource(seed)), //nolint:gosec // simulation, not security
		lostResponseRate: lostResponseRate,
		clock:            time.Now().UTC().Truncate(time.Second),
		byKey:            make(map[string]*PaymentIntent),
	}
}

func (f *FakeStripe) CreatePaymentIntent(_ context.Context, p CreateIntentParams) (PaymentIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if p.IdempotencyKey != "" {
		if pi, ok := f.byKey[p.IdempotencyKey]; ok {
			return f.respond(*pi)
		}
	}

	pi := &PaymentIntent{
		ID:          fmt.Sprintf("pi_fake_%s_%06d", f.prefix, len(f.intents)+1),
		AmountCents: p.AmountCents,
		Currency:    p.Currency,
		Status:      StatusSucceeded,
		PaymentID:   p.PaymentID,
		Created:     f.clock,
	}
	f.intents = append(f.intents, pi)
	if p.IdempotencyKey != "" {
		f.byKey[p.IdempotencyKey] = pi
	}

	f.emit(*pi, "payment_intent.created", "requires_confirmation", f.clock)
	f.emit(*pi, "payment_intent.processing", "processing", f.clock.Add(time.Second))
	f.emit(*pi, "payment_intent.succeeded", StatusSucceeded, f.clock.Add(2*time.Second))
	f.clock = f.clock.Add(3 * time.Second)

	return f.respond(*pi)
}

// SetLostResponseRate changes the failure rate, e.g. to model an outage
// that ends.
func (f *FakeStripe) SetLostResponseRate(rate float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lostResponseRate = rate
}

// respond returns pi, or loses the response with probability lostResponseRate.
func (f *FakeStripe) respond(pi PaymentIntent) (PaymentIntent, error) {
	if f.rng.Float64() < f.lostResponseRate {
		return PaymentIntent{}, fmt.Errorf("%w: simulated timeout (intent %s was created)", ErrTransient, pi.ID)
	}
	return pi, nil
}

func (f *FakeStripe) ListPaymentIntents(_ context.Context, since time.Time) ([]PaymentIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PaymentIntent
	for _, pi := range f.intents {
		if !pi.Created.Before(since) {
			out = append(out, *pi)
		}
	}
	return out, nil
}

// Events returns every webhook event emitted so far, in emission order.
func (f *FakeStripe) Events() []FakeEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]FakeEvent(nil), f.events...)
}

func (f *FakeStripe) emit(pi PaymentIntent, eventType, status string, at time.Time) {
	id := fmt.Sprintf("evt_fake_%s_%06d", f.prefix, len(f.events)+1)
	f.events = append(f.events, FakeEvent{ID: id, Payload: EventPayload(id, eventType, at, pi, status)})
}

// EventPayload builds the JSON body of a payment_intent.* webhook event in
// the shape webhook.ConstructEvent expects, including the API version
// stripe-go checks for.
func EventPayload(eventID, eventType string, created time.Time, pi PaymentIntent, status string) []byte {
	event := map[string]any{
		"id":               eventID,
		"object":           "event",
		"api_version":      stripe.APIVersion,
		"created":          created.Unix(),
		"type":             eventType,
		"livemode":         false,
		"pending_webhooks": 1,
		"data": map[string]any{
			"object": map[string]any{
				"id":       pi.ID,
				"object":   "payment_intent",
				"amount":   pi.AmountCents,
				"currency": pi.Currency,
				"status":   status,
				"created":  pi.Created.Unix(),
				"metadata": map[string]string{"payment_id": strconv.FormatInt(pi.PaymentID, 10)},
			},
		},
	}
	b, err := json.Marshal(event)
	if err != nil {
		panic(err) // only plain maps, strings and numbers: cannot fail
	}
	return b
}
