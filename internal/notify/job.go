// Package notify runs notification jobs from the notification_jobs table:
// a claimer goroutine leases batches with FOR UPDATE SKIP LOCKED and feeds a
// buffered channel; a bounded pool of goroutines handles them, retrying with
// backoff and dead-lettering after max attempts.
package notify

import (
	"context"
	"errors"
	"time"
)

// Job is a leased notification job. Attempts already includes this attempt.
type Job struct {
	ID          int64
	Kind        string
	Payload     []byte
	Attempts    int
	MaxAttempts int
}

// ErrPermanent marks failures that retrying cannot fix (bad payload, unknown
// kind). Such jobs are dead-lettered at once.
var ErrPermanent = errors.New("notify: permanent failure")

// Store is the job queue. Every method that changes a claimed job takes the
// worker ID and must affect nothing if another worker now owns the job.
type Store interface {
	ClaimJobs(ctx context.Context, workerID string, kinds []string, limit int, lease time.Duration) ([]Job, error)
	CompleteJob(ctx context.Context, id int64, workerID string) error
	RetryJob(ctx context.Context, id int64, workerID string, runAt time.Time, lastErr string) error
	DeadLetterJob(ctx context.Context, id int64, workerID string, lastErr string) error
	// ReleaseJobs returns claimed jobs that never started to the queue and
	// refunds their attempt.
	ReleaseJobs(ctx context.Context, ids []int64, workerID string) error
}

// Handler processes one job. It must respect ctx cancellation.
type Handler interface {
	Handle(ctx context.Context, job Job) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, job Job) error

func (f HandlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }
