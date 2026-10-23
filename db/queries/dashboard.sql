-- Read-only queries for the overview screen and the notification feed.

-- name: AssetStatusCounts :many
SELECT status, count(*) AS total FROM assets GROUP BY status;

-- name: JobStatusCounts :many
SELECT status, count(*) AS total FROM notification_jobs GROUP BY status;

-- name: PaymentStatusTotals :many
SELECT status, count(*) AS total, coalesce(sum(amount_cents), 0)::bigint AS amount_cents
FROM payments GROUP BY status;

-- name: CountActiveAllocations :one
SELECT count(*) FROM allocations WHERE returned_at IS NULL;

-- name: RecentAllocations :many
SELECT al.id, al.asset_id, a.tag AS asset_tag, a.name AS asset_name,
       al.user_id, u.name AS user_name, al.allocated_at, al.returned_at
FROM allocations al
JOIN assets a ON a.id = al.asset_id
JOIN users u ON u.id = al.user_id
ORDER BY greatest(al.allocated_at, coalesce(al.returned_at, al.allocated_at)) DESC, al.id DESC
LIMIT sqlc.arg(lim);

-- ListNotifications is the notification feed: one user's jobs, or all of them
-- when user_id is NULL (admins). Newest first.
-- name: ListNotifications :many
SELECT id, kind, payload, status, attempts, max_attempts, run_at, last_error, created_at, updated_at
FROM notification_jobs
WHERE sqlc.narg(user_id)::bigint IS NULL OR (payload ->> 'user_id')::bigint = sqlc.narg(user_id)
ORDER BY id DESC
LIMIT sqlc.arg(lim);
