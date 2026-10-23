package notify_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jenish0908/assetflow-be/internal/notify"
	"github.com/jenish0908/assetflow-be/internal/store"
)

// Integration tests: need TEST_DATABASE_URL pointing at a migrated database.
// Each test uses its own job kind, so it only ever sees its own rows.

func setup(t *testing.T, jobs int) (*pgxpool.Pool, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := store.NewPool(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	kind := fmt.Sprintf("it_%d", time.Now().UnixNano())
	_, err = pool.Exec(ctx, `
		INSERT INTO notification_jobs (kind, payload)
		SELECT $1, jsonb_build_object('n', g) FROM generate_series(1, $2::int) AS g`, kind, jobs)
	require.NoError(t, err)
	return pool, kind
}

func countStatus(t *testing.T, pool *pgxpool.Pool, kind, status string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		"SELECT count(*) FROM notification_jobs WHERE kind = $1 AND status = $2", kind, status).Scan(&n))
	return n
}

// TestTwoWorkersNeverShareAJob runs two workers against the same table.
// SKIP LOCKED must hand every job to exactly one of them.
func TestTwoWorkersNeverShareAJob(t *testing.T) {
	const jobs = 500
	pool, kind := setup(t, jobs)

	var mu sync.Mutex
	handled := map[int64]int{}
	perWorker := map[string]int{}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, id := range []string{"worker-a", "worker-b"} {
		cfg := notify.DefaultConfig(id, []string{kind})
		cfg.PollInterval = 10 * time.Millisecond
		w := notify.NewWorker(store.NewNotify(pool), notify.HandlerFunc(func(_ context.Context, j notify.Job) error {
			mu.Lock()
			handled[j.ID]++
			perWorker[id]++
			mu.Unlock()
			time.Sleep(time.Millisecond) // a little work, so both workers stay busy
			return nil
		}), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, w.Run(ctx))
		}()
	}

	require.Eventually(t, func() bool { return countStatus(t, pool, kind, "done") == jobs }, 30*time.Second, 50*time.Millisecond)
	cancel()
	wg.Wait()

	require.Len(t, handled, jobs)
	for id, n := range handled {
		require.Equal(t, 1, n, "job %d was handled %d times", id, n)
	}
	t.Logf("jobs per worker: %v", perWorker)
}

// TestExpiredLeaseIsReclaimedAndFenced: a worker that claims a job and dies
// loses it when the lease expires; another worker completes it; the dead
// worker's late update is ignored because it no longer holds the lease.
func TestExpiredLeaseIsReclaimedAndFenced(t *testing.T) {
	pool, kind := setup(t, 1)
	st := store.NewNotify(pool)
	ctx := context.Background()

	claimed, err := st.ClaimJobs(ctx, "crashed-worker", []string{kind}, 10, time.Second)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	again, err := st.ClaimJobs(ctx, "healthy-worker", []string{kind}, 10, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again, "a live lease protects the job")

	time.Sleep(1100 * time.Millisecond)
	again, err = st.ClaimJobs(ctx, "healthy-worker", []string{kind}, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, again, 1, "the expired lease is reclaimed")
	assert.Equal(t, 2, again[0].Attempts, "the crashed attempt still counts")
	require.NoError(t, st.CompleteJob(ctx, again[0].ID, "healthy-worker"))

	// The crashed worker wakes up and tries to report a failure.
	require.NoError(t, st.RetryJob(ctx, claimed[0].ID, "crashed-worker", time.Now(), "late"))
	assert.Equal(t, 1, countStatus(t, pool, kind, "done"), "stale worker cannot overwrite the result")
}
