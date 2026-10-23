package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine running.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testConfig() Config {
	cfg := DefaultConfig("w1", []string{"test"})
	cfg.Concurrency = 4
	cfg.PollInterval = 5 * time.Millisecond
	cfg.ShutdownTimeout = time.Second
	cfg.Backoff = func(int) time.Duration { return 0 }
	return cfg
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// runWorker starts w.Run and returns a stop function that cancels it and
// returns Run's error.
func runWorker(t *testing.T, w *Worker) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- w.Run(ctx) }()
	return func() error {
		cancel()
		select {
		case err := <-errc:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not stop")
			return nil
		}
	}
}

func TestWorkerProcessesEveryJobOnce(t *testing.T) {
	store := newMemStore(200, 5)
	var mu sync.Mutex
	calls := map[int64]int{}
	w := NewWorker(store, HandlerFunc(func(_ context.Context, j Job) error {
		mu.Lock()
		calls[j.ID]++
		mu.Unlock()
		return nil
	}), testConfig(), discard())

	stop := runWorker(t, w)
	require.Eventually(t, func() bool { return store.count("done") == 200 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, stop())

	require.Len(t, calls, 200)
	for id, n := range calls {
		assert.Equal(t, 1, n, "job %d handled %d times", id, n)
	}
	assert.Equal(t, int64(200), w.Stats().Done)
}

func TestRetryAndDeadLetter(t *testing.T) {
	transient := errors.New("smtp timeout")

	tests := []struct {
		name         string
		failures     int   // how many attempts fail before success (-1: always)
		err          error // the failure returned
		wantStatus   string
		wantAttempts int
	}{
		{"succeeds first time", 0, nil, "done", 1},
		{"succeeds on third attempt", 2, transient, "done", 3},
		{"dead-lettered after max attempts", -1, transient, "dead", 3},
		{"permanent error dead-letters at once", -1, ErrPermanent, "dead", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore(1, 3)
			var attempts atomic.Int32
			w := NewWorker(store, HandlerFunc(func(context.Context, Job) error {
				n := attempts.Add(1)
				if tt.failures < 0 || int(n) <= tt.failures {
					return tt.err
				}
				return nil
			}), testConfig(), discard())

			stop := runWorker(t, w)
			require.Eventually(t, func() bool { return store.count(tt.wantStatus) == 1 }, 2*time.Second, 2*time.Millisecond)
			require.NoError(t, stop())

			job := store.snapshot()[0]
			assert.Equal(t, tt.wantAttempts, job.Attempts)
			assert.Equal(t, int32(tt.wantAttempts), attempts.Load())
		})
	}
}

// TestGracefulShutdownFinishesInFlight: on shutdown, jobs already running
// finish normally (their context is not cancelled) and claimed jobs that
// never started go back to the queue with their attempt refunded.
func TestGracefulShutdownFinishesInFlight(t *testing.T) {
	store := newMemStore(20, 5)
	cfg := testConfig()
	cfg.Concurrency = 2

	started := make(chan struct{}, cfg.Concurrency)
	release := make(chan struct{})
	var ctxErrs atomic.Int32
	w := NewWorker(store, HandlerFunc(func(ctx context.Context, _ Job) error {
		started <- struct{}{}
		<-release
		if ctx.Err() != nil {
			ctxErrs.Add(1)
		}
		return nil
	}), cfg, discard())

	stop := runWorker(t, w)
	for range cfg.Concurrency {
		<-started
	}

	stopped := make(chan error, 1)
	go func() { stopped <- stop() }()
	time.Sleep(50 * time.Millisecond) // give shutdown time to begin while jobs are in flight
	close(release)
	require.NoError(t, <-stopped)

	assert.Equal(t, 2, store.count("done"), "both in-flight jobs completed")
	assert.Equal(t, 18, store.count("pending"), "everything else is back in the queue")
	assert.Zero(t, ctxErrs.Load(), "in-flight jobs were not cancelled by the shutdown signal")
	for _, j := range store.snapshot() {
		if j.status == "pending" {
			assert.Zero(t, j.Attempts, "job %d: unstarted jobs get their attempt refunded", j.ID)
		}
	}
}

// TestShutdownDeadlineCancelsInFlight: a job that outlives the shutdown
// deadline is cancelled and re-queued, and Run returns promptly.
func TestShutdownDeadlineCancelsInFlight(t *testing.T) {
	store := newMemStore(1, 5)
	cfg := testConfig()
	cfg.ShutdownTimeout = 100 * time.Millisecond
	started := make(chan struct{})
	w := NewWorker(store, HandlerFunc(func(ctx context.Context, _ Job) error {
		close(started)
		<-ctx.Done() // a handler that only stops when told to
		return ctx.Err()
	}), cfg, discard())

	stop := runWorker(t, w)
	<-started
	begin := time.Now()
	require.NoError(t, stop())
	elapsed := time.Since(begin)

	assert.GreaterOrEqual(t, elapsed, cfg.ShutdownTimeout, "waited for the deadline")
	assert.Less(t, elapsed, cfg.ShutdownTimeout+time.Second, "did not hang")
	job := store.snapshot()[0]
	assert.Equal(t, "pending", job.status, "interrupted job is re-queued")
	assert.Contains(t, job.lastErr, context.Canceled.Error())
}

func TestClaimFailuresStopWorker(t *testing.T) {
	dbDown := errors.New("connection refused")
	store := newMemStore(0, 5)
	store.claimErr = dbDown
	cfg := testConfig()
	cfg.MaxClaimFailures = 3

	err := NewWorker(store, HandlerFunc(func(context.Context, Job) error { return nil }), cfg, discard()).
		Run(context.Background())
	require.ErrorIs(t, err, dbDown)
}

func TestExponentialBackoff(t *testing.T) {
	backoff := ExponentialBackoff(time.Second, time.Minute)
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{4, 8 * time.Second},
		{7, time.Minute}, // 64s capped
		{40, time.Minute},
	}
	for _, tt := range tests {
		for range 20 {
			got := backoff(tt.attempt)
			assert.InDelta(t, float64(tt.want), float64(got), 0.2*float64(tt.want), "attempt %d", tt.attempt)
		}
	}
}
