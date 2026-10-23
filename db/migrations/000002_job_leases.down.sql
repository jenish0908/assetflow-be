DROP INDEX IF EXISTS notification_jobs_lease_idx;

ALTER TABLE notification_jobs
  DROP COLUMN IF EXISTS locked_until,
  DROP COLUMN IF EXISTS locked_by;
