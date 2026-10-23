package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jenish0908/assetflow/internal/notify"
	"github.com/jenish0908/assetflow/internal/store/db"
)

// NotifyStore implements notify.Store.
type NotifyStore struct {
	q *db.Queries
}

var _ notify.Store = (*NotifyStore)(nil)

func NewNotify(pool *pgxpool.Pool) *NotifyStore {
	return &NotifyStore{q: db.New(pool)}
}

// ClaimJobs expects small, validated limits and leases (int32 is plenty).
func (s *NotifyStore) ClaimJobs(ctx context.Context, workerID string, kinds []string, limit int, lease time.Duration) ([]notify.Job, error) {
	rows, err := s.q.ClaimJobs(ctx, db.ClaimJobsParams{
		WorkerID:     &workerID,
		Kinds:        kinds,
		BatchSize:    int32(limit),
		LeaseSeconds: int32(lease / time.Second),
	})
	if err != nil {
		return nil, translate(err)
	}
	jobs := make([]notify.Job, len(rows))
	for i, r := range rows {
		jobs[i] = notify.Job{
			ID:          r.ID,
			Kind:        r.Kind,
			Payload:     r.Payload,
			Attempts:    int(r.Attempts),
			MaxAttempts: int(r.MaxAttempts),
		}
	}
	return jobs, nil
}

// The methods below ignore "0 rows affected": it means the lease expired and
// another worker owns the job now, which is exactly what the fence is for.

func (s *NotifyStore) CompleteJob(ctx context.Context, id int64, workerID string) error {
	_, err := s.q.CompleteJob(ctx, db.CompleteJobParams{ID: id, WorkerID: &workerID})
	return translate(err)
}

func (s *NotifyStore) RetryJob(ctx context.Context, id int64, workerID string, runAt time.Time, lastErr string) error {
	_, err := s.q.RetryJob(ctx, db.RetryJobParams{ID: id, WorkerID: &workerID, RunAt: runAt, LastError: &lastErr})
	return translate(err)
}

func (s *NotifyStore) DeadLetterJob(ctx context.Context, id int64, workerID string, lastErr string) error {
	_, err := s.q.DeadLetterJob(ctx, db.DeadLetterJobParams{ID: id, WorkerID: &workerID, LastError: &lastErr})
	return translate(err)
}

func (s *NotifyStore) ReleaseJobs(ctx context.Context, ids []int64, workerID string) error {
	_, err := s.q.ReleaseJobs(ctx, db.ReleaseJobsParams{Ids: ids, WorkerID: &workerID})
	return translate(err)
}
