-- ClaimJobs leases up to batch_size runnable jobs to one worker.
--
-- FOR UPDATE SKIP LOCKED: rows another worker is claiming right now are
-- skipped instead of waited on, so workers never block each other and never
-- get the same job. The claim commits immediately; the lease (locked_until)
-- protects the job while it runs, and an expired lease makes it claimable
-- again. attempts is counted at claim time, so a job that crashes its worker
-- still moves toward the dead-letter limit.
-- name: ClaimJobs :many
UPDATE notification_jobs SET
  status       = 'running',
  attempts     = attempts + 1,
  locked_by    = sqlc.arg(worker_id),
  locked_until = now() + sqlc.arg(lease_seconds)::int * interval '1 second',
  updated_at   = now()
WHERE id IN (
  SELECT id FROM notification_jobs
  WHERE kind = ANY(sqlc.arg(kinds)::text[])
    AND ((status = 'pending' AND run_at <= now())
      OR (status = 'running' AND locked_until < now()))
  ORDER BY run_at, id
  LIMIT sqlc.arg(batch_size)
  FOR UPDATE SKIP LOCKED
)
RETURNING id, kind, payload, attempts, max_attempts;

-- The "AND locked_by = worker_id" in the statements below is a fence: if this
-- worker's lease expired and another worker reclaimed the job, the update
-- matches no row and the stale worker cannot overwrite the new owner.

-- name: CompleteJob :execrows
UPDATE notification_jobs SET
  status = 'done', locked_by = NULL, locked_until = NULL, last_error = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running' AND locked_by = sqlc.arg(worker_id);

-- name: RetryJob :execrows
UPDATE notification_jobs SET
  status = 'pending', run_at = sqlc.arg(run_at), last_error = sqlc.arg(last_error),
  locked_by = NULL, locked_until = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running' AND locked_by = sqlc.arg(worker_id);

-- name: DeadLetterJob :execrows
UPDATE notification_jobs SET
  status = 'dead', last_error = sqlc.arg(last_error),
  locked_by = NULL, locked_until = NULL, updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'running' AND locked_by = sqlc.arg(worker_id);

-- ReleaseJobs hands claimed-but-never-started jobs back on shutdown and
-- refunds the attempt counted at claim time.
-- name: ReleaseJobs :execrows
UPDATE notification_jobs SET
  status = 'pending', attempts = attempts - 1,
  locked_by = NULL, locked_until = NULL, updated_at = now()
WHERE id = ANY(sqlc.arg(ids)::bigint[]) AND status = 'running' AND locked_by = sqlc.arg(worker_id);
