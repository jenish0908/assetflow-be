package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jenish0908/assetflow-be/internal/billing"
	"github.com/jenish0908/assetflow-be/internal/store/db"
)

// BillingStore implements billing.Store and, inside a transaction, billing.Tx.
type BillingStore struct {
	pool *pgxpool.Pool // nil when bound to a transaction
	q    *db.Queries
}

var (
	_ billing.Store = (*BillingStore)(nil)
	_ billing.Tx    = (*BillingStore)(nil)
)

func NewBilling(pool *pgxpool.Pool) *BillingStore {
	return &BillingStore{pool: pool, q: db.New(pool)}
}

func (s *BillingStore) InTx(ctx context.Context, fn func(billing.Tx) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&BillingStore{q: s.q.WithTx(tx)})
	})
}

func (s *BillingStore) CreatePayment(ctx context.Context, p billing.NewPayment) (billing.Payment, error) {
	row, err := s.q.CreatePayment(ctx, db.CreatePaymentParams{
		UserID:         p.UserID,
		IdempotencyKey: p.IdempotencyKey,
		AmountCents:    p.AmountCents,
		Currency:       p.Currency,
	})
	return toPayment(row), translate(err)
}

func (s *BillingStore) GetPayment(ctx context.Context, id int64) (billing.Payment, error) {
	row, err := s.q.GetPayment(ctx, id)
	return toPayment(row), translate(err)
}

func (s *BillingStore) GetPaymentByIdempotencyKey(ctx context.Context, key string) (billing.Payment, error) {
	row, err := s.q.GetPaymentByIdempotencyKey(ctx, key)
	return toPayment(row), translate(err)
}

func (s *BillingStore) ListPaymentsSince(ctx context.Context, since time.Time) ([]billing.Payment, error) {
	rows, err := s.q.ListPaymentsSince(ctx, since)
	if err != nil {
		return nil, translate(err)
	}
	out := make([]billing.Payment, len(rows))
	for i, r := range rows {
		out[i] = toPayment(r)
	}
	return out, nil
}

func (s *BillingStore) SucceededJobCounts(ctx context.Context, since time.Time) (map[int64]int, error) {
	rows, err := s.q.PaymentSucceededJobCounts(ctx, since)
	if err != nil {
		return nil, translate(err)
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.PaymentID] = int(r.Jobs)
	}
	return out, nil
}

func (s *BillingStore) RecordEvent(ctx context.Context, id, eventType string) (bool, error) {
	n, err := s.q.RecordStripeEvent(ctx, db.RecordStripeEventParams{ID: id, Type: eventType})
	return n == 1, translate(err)
}

func (s *BillingStore) GetPaymentForUpdate(ctx context.Context, id int64) (billing.Payment, error) {
	row, err := s.q.GetPaymentForUpdate(ctx, id)
	return toPayment(row), translate(err)
}

func (s *BillingStore) GetPaymentByIntentForUpdate(ctx context.Context, intentID string) (billing.Payment, error) {
	row, err := s.q.GetPaymentByIntentForUpdate(ctx, &intentID)
	return toPayment(row), translate(err)
}

func (s *BillingStore) ApplyPaymentStatus(ctx context.Context, id int64, intentID, status string, eventAt *time.Time) error {
	return translate(s.q.ApplyPaymentStatus(ctx, db.ApplyPaymentStatusParams{
		ID:              id,
		PaymentIntentID: &intentID,
		Status:          status,
		EventAt:         eventAt,
	}))
}

func (s *BillingStore) EnqueueNotification(ctx context.Context, kind string, payload []byte) error {
	return translate(s.q.EnqueueNotification(ctx, db.EnqueueNotificationParams{Kind: kind, Payload: payload}))
}

func toPayment(r db.Payment) billing.Payment {
	return billing.Payment{
		ID:              r.ID,
		UserID:          r.UserID,
		PaymentIntentID: r.PaymentIntentID,
		AmountCents:     r.AmountCents,
		Currency:        r.Currency,
		Status:          r.Status,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
		IdempotencyKey:  r.IdempotencyKey,
		LastEventAt:     r.LastEventAt,
	}
}

func (s *BillingStore) ListPayments(ctx context.Context, userID *int64, limit, offset int) ([]billing.Payment, error) {
	rows, err := s.q.ListPayments(ctx, db.ListPaymentsParams{UserID: userID, Lim: int32(limit), Off: int32(offset)})
	if err != nil {
		return nil, translate(err)
	}
	out := make([]billing.Payment, len(rows))
	for i, r := range rows {
		out[i] = toPayment(r)
	}
	return out, nil
}

func (s *BillingStore) CountPayments(ctx context.Context, userID *int64) (int64, error) {
	n, err := s.q.CountPayments(ctx, userID)
	return n, translate(err)
}
