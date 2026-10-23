-- Leases: a claimed job is 'running' until locked_until. If the worker dies,
-- the lease expires and another worker reclaims the job.
ALTER TABLE notification_jobs
  ADD COLUMN locked_by    TEXT,
  ADD COLUMN locked_until TIMESTAMPTZ;

CREATE INDEX notification_jobs_lease_idx ON notification_jobs (locked_until)
  WHERE status = 'running';
