package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

type Config struct {
	WorkerID    string
	Kinds       []string      // job kinds this worker handles
	Concurrency int           // N handler goroutines
	BatchSize   int           // max jobs per claim query
	Lease       time.Duration // how long a claimed job is reserved
	JobTimeout  time.Duration // per-job handler deadline
	// PollInterval is the wait between claims when the queue is empty.
	PollInterval time.Duration
	// ShutdownTimeout is how long in-flight jobs may run after shutdown
	// starts before their context is cancelled.
	ShutdownTimeout time.Duration
	// Backoff returns the delay before retry number attempt (1-based).
	Backoff func(attempt int) time.Duration
	// MaxClaimFailures consecutive claim errors make Run fail, so the
	// process exits and its supervisor can restart it.
	MaxClaimFailures int
}

func DefaultConfig(workerID string, kinds []string) Config {
	return Config{
		WorkerID:         workerID,
		Kinds:            kinds,
		Concurrency:      8,
		BatchSize:        32,
		Lease:            2 * time.Minute,
		JobTimeout:       30 * time.Second,
		PollInterval:     time.Second,
		ShutdownTimeout:  20 * time.Second,
		Backoff:          ExponentialBackoff(time.Second, 5*time.Minute),
		MaxClaimFailures: 10,
	}
}

// ExponentialBackoff returns base*2^(attempt-1), capped at maxDelay, with
// ±20% jitter so jobs that failed together do not retry in lockstep.
func ExponentialBackoff(base, maxDelay time.Duration) func(int) time.Duration {
	return func(attempt int) time.Duration {
		d := maxDelay
		if attempt < 32 {
			d = min(base<<(attempt-1), maxDelay)
		}
		jitter := (rand.Float64()*0.4 - 0.2) * float64(d) //nolint:gosec // jitter, not security
		return d + time.Duration(jitter)
	}
}

// Stats counts job outcomes since the worker started.
type Stats struct {
	Done, Retried, Dead, Released int64
}

type Worker struct {
	store   Store
	handler Handler
	cfg     Config
	logger  *slog.Logger

	done, retried, dead, released atomic.Int64
}

func NewWorker(store Store, handler Handler, cfg Config, logger *slog.Logger) *Worker {
	return &Worker{store: store, handler: handler, cfg: cfg, logger: logger}
}

func (w *Worker) Stats() Stats {
	return Stats{Done: w.done.Load(), Retried: w.retried.Load(), Dead: w.dead.Load(), Released: w.released.Load()}
}

// Run processes jobs until ctx is cancelled, then shuts down gracefully:
//
//  1. stop claiming new jobs;
//  2. let in-flight jobs finish, for up to ShutdownTimeout, then cancel them
//     (a cancelled job is re-queued for retry);
//  3. return claimed jobs that never started to the queue.
//
// It returns nil after a clean shutdown, or an error if claiming kept failing.
func (w *Worker) Run(ctx context.Context) error {
	jobs := make(chan Job, w.cfg.Concurrency*2)

	// In-flight jobs get a context that SIGTERM does not cancel, so they can
	// finish; it is cancelled only when the shutdown deadline passes.
	jobCtx, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()

	// stop is cancelled by shutdown (ctx) or by the first goroutine error.
	g, stop := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(jobs)
		return w.claimLoop(stop, jobs)
	})
	for range w.cfg.Concurrency {
		g.Go(func() error {
			w.workLoop(stop, jobCtx, jobs)
			return nil
		})
	}

	// Enforce the shutdown deadline. This goroutine is outside the group so
	// that g.Wait does not wait for it; finished tells it to exit.
	finished := make(chan struct{})
	deadlineDone := make(chan struct{})
	go func() {
		defer close(deadlineDone)
		select {
		case <-stop.Done():
		case <-finished:
			return
		}
		timer := time.NewTimer(w.cfg.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			w.logger.Warn("shutdown deadline passed; cancelling in-flight jobs")
			cancelJobs()
		case <-finished:
		}
	}()

	err := g.Wait()
	close(finished)
	<-deadlineDone

	// The claimer closed the channel; whatever is left never started.
	var unstarted []Job
	for job := range jobs {
		unstarted = append(unstarted, job)
	}
	w.release(unstarted)

	w.logger.Info("worker stopped", "stats", w.Stats())
	return err
}

// claimLoop leases jobs and sends them to the pool. It only claims as many
// jobs as the channel has free slots, so leased jobs never queue up in memory.
func (w *Worker) claimLoop(ctx context.Context, jobs chan<- Job) error {
	failures := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		free := min(cap(jobs)-len(jobs), w.cfg.BatchSize)
		if free == 0 {
			if !sleepCtx(ctx, w.cfg.PollInterval/10) {
				return nil
			}
			continue
		}

		batch, err := w.store.ClaimJobs(ctx, w.cfg.WorkerID, w.cfg.Kinds, free, w.cfg.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			failures++
			w.logger.Error("claim jobs failed", "err", err, "consecutive", failures)
			if failures >= w.cfg.MaxClaimFailures {
				return fmt.Errorf("notify: %d consecutive claim failures: %w", failures, err)
			}
			if !sleepCtx(ctx, w.cfg.PollInterval) {
				return nil
			}
			continue
		}
		failures = 0

		for i, job := range batch {
			select {
			case jobs <- job:
			case <-ctx.Done():
				w.release(batch[i:])
				return nil
			}
		}
		if len(batch) == 0 && !sleepCtx(ctx, w.cfg.PollInterval) {
			return nil
		}
	}
}

// workLoop handles jobs until stop is cancelled or the channel is closed.
func (w *Worker) workLoop(stop, jobCtx context.Context, jobs <-chan Job) {
	for {
		select {
		case <-stop.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}
			// select picks randomly when both cases are ready, so check
			// again: a job received after shutdown began is not started.
			if stop.Err() != nil {
				w.release([]Job{job})
				return
			}
			w.runJob(jobCtx, job)
		}
	}
}

func (w *Worker) runJob(ctx context.Context, job Job) {
	hctx, cancel := context.WithTimeout(ctx, w.cfg.JobTimeout)
	err := w.handler.Handle(hctx, job)
	cancel()

	// Record the outcome even if ctx was just cancelled by the shutdown
	// deadline; otherwise the job would wait for its lease to expire.
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	log := w.logger.With("job_id", job.ID, "kind", job.Kind, "attempt", job.Attempts)

	switch {
	case err == nil:
		if serr := w.store.CompleteJob(bctx, job.ID, w.cfg.WorkerID); serr != nil {
			log.Error("mark job done failed; it will be retried after its lease", "err", serr)
			return
		}
		w.done.Add(1)

	case errors.Is(err, ErrPermanent) || job.Attempts >= job.MaxAttempts:
		if serr := w.store.DeadLetterJob(bctx, job.ID, w.cfg.WorkerID, err.Error()); serr != nil {
			log.Error("dead-letter job failed", "err", serr)
			return
		}
		w.dead.Add(1)
		log.Warn("job dead-lettered", "err", err)

	default:
		runAt := time.Now().Add(w.cfg.Backoff(job.Attempts))
		if serr := w.store.RetryJob(bctx, job.ID, w.cfg.WorkerID, runAt, err.Error()); serr != nil {
			log.Error("schedule retry failed", "err", serr)
			return
		}
		w.retried.Add(1)
		log.Info("job will be retried", "err", err, "run_at", runAt)
	}
}

func (w *Worker) release(jobs []Job) {
	if len(jobs) == 0 {
		return
	}
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.store.ReleaseJobs(ctx, ids, w.cfg.WorkerID); err != nil {
		w.logger.Error("release jobs failed; they will be reclaimed after their lease", "err", err, "count", len(ids))
		return
	}
	w.released.Add(int64(len(ids)))
}

// sleepCtx waits for d or until ctx is done, and reports whether the full
// wait elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
