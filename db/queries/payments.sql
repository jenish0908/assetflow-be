-- name: CreatePayment :one
INSERT INTO payments (user_id, idempotency_key, amount_cents, currency)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments
WHERE id = $1;

-- name: GetPaymentByIdempotencyKey :one
SELECT * FROM payments
WHERE idempotency_key = $1;

-- name: GetPaymentForUpdate :one
SELECT * FROM payments
WHERE id = $1
FOR UPDATE;

-- name: GetPaymentByIntentForUpdate :one
SELECT * FROM payments
WHERE payment_intent_id = $1
FOR UPDATE;

-- ApplyPaymentStatus is the only statement that changes a payment's status.
-- event_at is the Stripe event time; NULL (status from an API response)
-- leaves last_event_at unchanged.
-- name: ApplyPaymentStatus :exec
UPDATE payments SET
  status            = sqlc.arg(status),
  payment_intent_id = COALESCE(payment_intent_id, sqlc.arg(payment_intent_id)),
  last_event_at     = COALESCE(sqlc.narg(event_at), last_event_at),
  updated_at        = now()
WHERE id = sqlc.arg(id);

-- name: ListPaymentsSince :many
SELECT * FROM payments
WHERE created_at >= $1
ORDER BY id;

-- PaymentSucceededJobCounts returns, per payment, how many
-- payment_succeeded notification jobs exist (0 for none).
-- name: PaymentSucceededJobCounts :many
SELECT p.id AS payment_id, count(j.id) AS jobs
FROM payments p
LEFT JOIN notification_jobs j
  ON j.kind = 'payment_succeeded'
 AND (j.payload ->> 'payment_id')::bigint = p.id
WHERE p.created_at >= $1
GROUP BY p.id;

-- Dedupe: the primary key on id makes a second insert of the same event a
-- no-op. Zero affected rows means "already processed".
-- name: RecordStripeEvent :execrows
INSERT INTO stripe_events (id, type)
VALUES ($1, $2)
ON CONFLICT (id) DO NOTHING;

-- name: EnqueueNotification :exec
INSERT INTO notification_jobs (kind, payload)
VALUES ($1, $2);

-- name: ListPayments :many
SELECT * FROM payments
WHERE sqlc.narg(user_id)::bigint IS NULL OR user_id = sqlc.narg(user_id)
ORDER BY id DESC
LIMIT sqlc.arg(lim) OFFSET sqlc.arg(off);

-- name: CountPayments :one
SELECT count(*) FROM payments
WHERE sqlc.narg(user_id)::bigint IS NULL OR user_id = sqlc.narg(user_id);
