package billing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"

	"github.com/jenish0908/assetflow-be/internal/auth"
	"github.com/jenish0908/assetflow-be/internal/notify"
	"github.com/jenish0908/assetflow-be/internal/service"
)

// Options controls retries and the two safeguards. Production uses
// DefaultOptions; turning a safeguard off exists only so cmd/billingsim can
// reproduce the "before" numbers in docs/perf.md.
type Options struct {
	// Idempotency sends an idempotency key on every Stripe create and honours
	// the client's Idempotency-Key header.
	Idempotency bool
	// WebhookGuards deduplicates events by ID and ignores stale or
	// post-terminal events.
	WebhookGuards bool
	MaxAttempts   int
	RetryBackoff  time.Duration
}

func DefaultOptions() Options {
	return Options{Idempotency: true, WebhookGuards: true, MaxAttempts: 3, RetryBackoff: 200 * time.Millisecond}
}

type Service struct {
	store         Store
	gateway       Gateway
	webhookSecret string
	opts          Options
}

func NewService(store Store, gateway Gateway, webhookSecret string, opts Options) *Service {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	return &Service{store: store, gateway: gateway, webhookSecret: webhookSecret, opts: opts}
}

type CreatePaymentInput struct {
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

type CreatePaymentResult struct {
	Payment Payment
	// Replayed is true when the Idempotency-Key matched an earlier request
	// and that request's payment is returned instead of a new one.
	Replayed bool
}

// CreatePayment charges the user's saved card.
//
//  1. Insert a pending payment row holding the idempotency key (the client's
//     Idempotency-Key header, or a generated one), so the key exists before
//     Stripe is called.
//  2. Create the PaymentIntent with that key, retrying transient failures
//     with the same key, so Stripe charges at most once.
//  3. Store the PaymentIntent ID on the row.
//
// A client that gets a 5xx can resend the same request with the same
// Idempotency-Key: step 1 finds the existing row and step 2 replays.
func (s *Service) CreatePayment(ctx context.Context, userID int64, clientKey string, in CreatePaymentInput) (CreatePaymentResult, error) {
	in.Currency = strings.ToLower(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "usd"
	}
	if err := in.validate(); err != nil {
		return CreatePaymentResult{}, err
	}
	if len(clientKey) > 200 {
		return CreatePaymentResult{}, service.NewError(service.ErrInvalid, "Idempotency-Key must be at most 200 characters")
	}

	key := clientKey
	if key == "" || !s.opts.Idempotency {
		key = newKey()
	}
	// Scope keys per user so two users cannot collide on the same value.
	key = fmt.Sprintf("user:%d:%s", userID, key)

	res := CreatePaymentResult{}
	p, err := s.store.CreatePayment(ctx, NewPayment{UserID: userID, IdempotencyKey: key, AmountCents: in.AmountCents, Currency: in.Currency})
	switch {
	case errors.Is(err, service.ErrConflict):
		if p, err = s.store.GetPaymentByIdempotencyKey(ctx, key); err != nil {
			return CreatePaymentResult{}, fmt.Errorf("create payment: load replay: %w", err)
		}
		if p.AmountCents != in.AmountCents || p.Currency != in.Currency {
			return CreatePaymentResult{}, service.NewError(service.ErrConflict, "Idempotency-Key was already used with different parameters")
		}
		res.Replayed = true
		if p.PaymentIntentID != nil {
			res.Payment = p
			return res, nil
		}
	case err != nil:
		return CreatePaymentResult{}, fmt.Errorf("create payment: %w", err)
	}

	intent, err := s.createIntent(ctx, p)
	if err != nil {
		return CreatePaymentResult{}, fmt.Errorf("create payment %d: %w", p.ID, err)
	}
	if err := s.saveIntent(ctx, p.ID, intent); err != nil {
		return CreatePaymentResult{}, fmt.Errorf("create payment %d: save intent: %w", p.ID, err)
	}
	if res.Payment, err = s.store.GetPayment(ctx, p.ID); err != nil {
		return CreatePaymentResult{}, fmt.Errorf("create payment %d: %w", p.ID, err)
	}
	return res, nil
}

// saveIntent records the intent and the status from Stripe's response. With
// confirm=true that status is often already "succeeded", so this path must
// fire the same side effects as the webhook would. If a webhook already
// updated the row, its status wins: it carries an event timestamp.
func (s *Service) saveIntent(ctx context.Context, paymentID int64, intent PaymentIntent) error {
	return s.store.InTx(ctx, func(tx Tx) error {
		cur, err := tx.GetPaymentForUpdate(ctx, paymentID)
		if err != nil {
			return err
		}
		status := intent.Status
		if cur.Status != StatusPending {
			status = cur.Status
		}
		return s.applyStatus(ctx, tx, cur, intent.ID, status, nil)
	})
}

// applyStatus is the single place a payment's status changes, for both API
// responses and webhooks, always under the row lock. The payment_succeeded
// notification is tied to the transition into succeeded, not to whichever
// message reported it, so it is enqueued exactly once.
func (s *Service) applyStatus(ctx context.Context, tx Tx, cur Payment, intentID, status string, eventAt *time.Time) error {
	if err := tx.ApplyPaymentStatus(ctx, cur.ID, intentID, status, eventAt); err != nil {
		return fmt.Errorf("apply status %s to payment %d: %w", status, cur.ID, err)
	}
	if status != StatusSucceeded {
		return nil
	}
	// The naive (guards off) version notifies on every succeeded message.
	if s.opts.WebhookGuards && cur.Status == StatusSucceeded {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"payment_id":   cur.ID,
		"user_id":      cur.UserID,
		"amount_cents": cur.AmountCents,
		"currency":     cur.Currency,
	})
	if err != nil {
		return err
	}
	return tx.EnqueueNotification(ctx, notify.KindPaymentSucceeded, payload)
}

func (in CreatePaymentInput) validate() error {
	// Stripe's minimum charge is 50 cents (USD); the maximum is 8 digits.
	if in.AmountCents < 50 || in.AmountCents > 99_999_999 {
		return service.NewError(service.ErrInvalid, "amount_cents must be between 50 and 99999999")
	}
	if len(in.Currency) != 3 || strings.Trim(in.Currency, "abcdefghijklmnopqrstuvwxyz") != "" {
		return service.NewError(service.ErrInvalid, "currency must be a 3-letter ISO code")
	}
	return nil
}

// createIntent calls Stripe, retrying transient errors with backoff. With
// idempotency on, every attempt sends the same key, so a retry after a lost
// response gets back the intent that was already created.
func (s *Service) createIntent(ctx context.Context, p Payment) (PaymentIntent, error) {
	params := CreateIntentParams{AmountCents: p.AmountCents, Currency: p.Currency, PaymentID: p.ID}
	if s.opts.Idempotency {
		params.IdempotencyKey = p.IdempotencyKey
	}

	var lastErr error
	for attempt := 1; attempt <= s.opts.MaxAttempts; attempt++ {
		intent, err := s.gateway.CreatePaymentIntent(ctx, params)
		if err == nil {
			return intent, nil
		}
		if !errors.Is(err, ErrTransient) {
			return PaymentIntent{}, err
		}
		lastErr = err
		if attempt < s.opts.MaxAttempts {
			if err := sleep(ctx, s.opts.RetryBackoff*time.Duration(attempt)); err != nil {
				return PaymentIntent{}, err
			}
		}
	}
	return PaymentIntent{}, fmt.Errorf("%w after %d attempts: %w", ErrGatewayUnavailable, s.opts.MaxAttempts, lastErr)
}

// GetPayment returns a payment to its owner or an admin. Other callers get
// not-found rather than forbidden, so IDs cannot be probed.
func (s *Service) GetPayment(ctx context.Context, actor auth.Claims, id int64) (Payment, error) {
	p, err := s.store.GetPayment(ctx, id)
	if errors.Is(err, service.ErrNotFound) || (err == nil && p.UserID != actor.UserID && actor.Role != auth.RoleAdmin) {
		return Payment{}, service.NewError(service.ErrNotFound, "payment %d not found", id)
	}
	if err != nil {
		return Payment{}, fmt.Errorf("get payment: %w", err)
	}
	return p, nil
}

var (
	// errRetryLater rolls back the transaction (including the event record)
	// and makes the handler return 5xx, so Stripe redelivers later.
	errRetryLater = errors.New("billing: payment for this intent is not stored yet; retry later")
	errNotOurs    = errors.New("billing: payment intent was not created by this service")
)

// HandleWebhook verifies the Stripe-Signature header against the raw body,
// then processes the event.
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signature string) error {
	event, err := webhook.ConstructEvent(payload, signature, s.webhookSecret)
	if err != nil {
		return &service.Error{Kind: service.ErrInvalid, Msg: "invalid webhook signature or payload"}
	}
	return s.processEvent(ctx, event)
}

// processEvent applies one event exactly once. Everything happens in one
// transaction: recording the event ID, updating the payment and enqueueing
// the notification commit together or not at all. Two concurrent deliveries
// of the same event both try to insert its ID; the second insert waits on
// the first transaction's unique-index entry, then finds the conflict and
// does nothing.
func (s *Service) processEvent(ctx context.Context, event stripe.Event) error {
	isIntent := strings.HasPrefix(string(event.Type), "payment_intent.")
	var pi stripe.PaymentIntent
	if isIntent {
		if event.Data == nil || json.Unmarshal(event.Data.Raw, &pi) != nil {
			return service.NewError(service.ErrInvalid, "malformed payment_intent event")
		}
	}
	eventAt := time.Unix(event.Created, 0).UTC()

	return s.store.InTx(ctx, func(tx Tx) error {
		if s.opts.WebhookGuards {
			fresh, err := tx.RecordEvent(ctx, event.ID, string(event.Type))
			if err != nil {
				return fmt.Errorf("record event %s: %w", event.ID, err)
			}
			if !fresh {
				return nil // already processed
			}
		}
		if !isIntent {
			return nil
		}

		p, err := findPaymentForUpdate(ctx, tx, &pi)
		if errors.Is(err, errNotOurs) {
			return nil
		}
		if err != nil {
			return err
		}

		status := string(pi.Status)
		if s.opts.WebhookGuards {
			if p.PaymentIntentID != nil && *p.PaymentIntentID != pi.ID {
				return nil // a different intent for this payment; reconciliation reports it
			}
			if !shouldApply(p, eventAt) {
				return nil
			}
		}

		return s.applyStatus(ctx, tx, p, pi.ID, status, &eventAt)
	})
}

// findPaymentForUpdate locks the payment an intent belongs to. The webhook
// can arrive before CreatePayment has saved the intent ID, so it falls back
// to the payment_id we put in the intent's metadata.
func findPaymentForUpdate(ctx context.Context, tx Tx, pi *stripe.PaymentIntent) (Payment, error) {
	p, err := tx.GetPaymentByIntentForUpdate(ctx, pi.ID)
	if !errors.Is(err, service.ErrNotFound) {
		return p, err
	}

	id, convErr := strconv.ParseInt(pi.Metadata["payment_id"], 10, 64)
	if convErr != nil {
		return Payment{}, errNotOurs
	}
	p, err = tx.GetPaymentForUpdate(ctx, id)
	if errors.Is(err, service.ErrNotFound) {
		return Payment{}, errRetryLater
	}
	return p, err
}

// shouldApply decides whether an event may change the payment:
//   - terminal states (succeeded, canceled) never change again;
//   - events older than the last applied one are stale and skipped.
//
// Stripe's event.created has one-second resolution, so two events in the
// same second cannot be ordered by time; the later arrival wins. Fetching
// the PaymentIntent from the API on each event removes that limit.
func shouldApply(p Payment, eventAt time.Time) bool {
	if p.Status == StatusSucceeded || p.Status == StatusCanceled {
		return false
	}
	return p.LastEventAt == nil || !eventAt.Before(*p.LastEventAt)
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ListPayments returns the caller's payments, newest first; admins see all.
func (s *Service) ListPayments(ctx context.Context, actor auth.Claims, limit, offset int) (PaymentPage, error) {
	limit, offset, err := service.Paginate(limit, offset)
	if err != nil {
		return PaymentPage{}, err
	}
	var userID *int64
	if actor.Role != auth.RoleAdmin {
		userID = &actor.UserID
	}
	items, err := s.store.ListPayments(ctx, userID, limit, offset)
	if err != nil {
		return PaymentPage{}, fmt.Errorf("list payments: %w", err)
	}
	total, err := s.store.CountPayments(ctx, userID)
	if err != nil {
		return PaymentPage{}, fmt.Errorf("count payments: %w", err)
	}
	return PaymentPage{Items: items, Limit: limit, Offset: offset, Total: total}, nil
}
