package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// Job kinds produced by the API (enqueued in the same transaction as the
// change they describe: the transactional outbox pattern).
const (
	KindPaymentSucceeded = "payment_succeeded"
	KindAssetAllocated   = "asset_allocated"
)

// Kinds lists every kind Notifier handles.
var Kinds = []string{KindPaymentSucceeded, KindAssetAllocated}

// Message is a notification to one user.
type Message struct {
	UserID  int64
	Subject string
	Body    string
	// Key identifies the message for de-duplication at the provider. Jobs run
	// at least once, so a retry after a timeout may send twice unless the
	// provider dedupes on this key.
	Key string
}

// Sender delivers messages (email, Slack, push...).
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// LogSender "delivers" by logging; it stands in for a real provider.
type LogSender struct{ Logger *slog.Logger }

func (s LogSender) Send(ctx context.Context, m Message) error {
	s.Logger.InfoContext(ctx, "notification sent", "user_id", m.UserID, "subject", m.Subject, "key", m.Key)
	return nil
}

// Notifier turns jobs into messages.
type Notifier struct {
	sender Sender
}

func NewNotifier(sender Sender) *Notifier {
	return &Notifier{sender: sender}
}

func (n *Notifier) Handle(ctx context.Context, job Job) error {
	m, err := buildMessage(job)
	if err != nil {
		return err
	}
	m.Key = fmt.Sprintf("job-%d", job.ID)
	return n.sender.Send(ctx, m)
}

func buildMessage(job Job) (Message, error) {
	switch job.Kind {
	case KindPaymentSucceeded:
		var p struct {
			PaymentID   int64  `json:"payment_id"`
			UserID      int64  `json:"user_id"`
			AmountCents int64  `json:"amount_cents"`
			Currency    string `json:"currency"`
		}
		if err := decode(job, &p); err != nil || p.UserID == 0 {
			return Message{}, fmt.Errorf("%w: bad %s payload", ErrPermanent, job.Kind)
		}
		return Message{
			UserID:  p.UserID,
			Subject: "Payment received",
			Body:    fmt.Sprintf("We received your payment #%d of %d.%02d %s.", p.PaymentID, p.AmountCents/100, p.AmountCents%100, p.Currency),
		}, nil

	case KindAssetAllocated:
		var p struct {
			AssetID  int64  `json:"asset_id"`
			AssetTag string `json:"asset_tag"`
			UserID   int64  `json:"user_id"`
		}
		if err := decode(job, &p); err != nil || p.UserID == 0 {
			return Message{}, fmt.Errorf("%w: bad %s payload", ErrPermanent, job.Kind)
		}
		return Message{
			UserID:  p.UserID,
			Subject: "Asset allocated to you",
			Body:    fmt.Sprintf("Asset %s (#%d) has been allocated to you.", p.AssetTag, p.AssetID),
		}, nil
	}
	return Message{}, fmt.Errorf("%w: unknown job kind %q", ErrPermanent, job.Kind)
}

func decode(job Job, v any) error {
	return json.Unmarshal(job.Payload, v)
}
