// Command notifybench measures worker throughput (jobs/sec) against real
// Postgres. The handler only sleeps for -latency, standing in for a call to
// an email provider, so the numbers show the worker's own overhead and how
// throughput scales with the goroutine pool size.
//
// For each pool size it enqueues -jobs jobs, starts -instances workers, and
// times how long it takes until every job is done. It also checks that no job
// was handled twice. Requires DATABASE_URL for a migrated database whose name
// ends in "_bench" (the jobs table is truncated).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jenish0908/assetflow-be/internal/notify"
	"github.com/jenish0908/assetflow-be/internal/store"
)

const kind = "bench"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	jobs := flag.Int("jobs", 5000, "jobs per run")
	concurrencies := flag.String("concurrency", "1,4,16,64", "comma-separated pool sizes per worker")
	instances := flag.Int("instances", 1, "worker instances sharing the table")
	latency := flag.Duration("latency", 20*time.Millisecond, "simulated send latency per job")
	header := flag.Bool("header", false, "print the markdown table header first")
	flag.Parse()

	ctx := context.Background()
	pool, err := store.NewPool(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	var dbName string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		return err
	}
	if !strings.HasSuffix(dbName, "_bench") {
		return fmt.Errorf("refusing to truncate %q: database name must end in _bench", dbName)
	}

	if *header {
		fmt.Println("| send latency | instances | goroutines per instance | jobs | seconds | jobs/sec | handled twice |")
		fmt.Println("|---:|---:|---:|---:|---:|---:|---:|")
	}
	for _, s := range strings.Split(*concurrencies, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || c < 1 {
			return fmt.Errorf("bad concurrency %q", s)
		}
		elapsed, dupes, err := benchOnce(ctx, pool, *jobs, *instances, c, *latency)
		if err != nil {
			return err
		}
		fmt.Printf("| %s | %d | %d | %d | %.2f | %.0f | %d |\n",
			*latency, *instances, c, *jobs, elapsed.Seconds(), float64(*jobs)/elapsed.Seconds(), dupes)
	}
	return nil
}

func benchOnce(ctx context.Context, pool *pgxpool.Pool, jobs, instances, concurrency int, latency time.Duration) (time.Duration, int, error) {
	if _, err := pool.Exec(ctx, "TRUNCATE notification_jobs RESTART IDENTITY"); err != nil {
		return 0, 0, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_jobs (kind, payload)
		SELECT $1, '{}'::jsonb FROM generate_series(1, $2::int)`, kind, jobs); err != nil {
		return 0, 0, err
	}

	calls := make([]atomic.Int32, jobs+1) // indexed by job ID (1..jobs after RESTART IDENTITY)
	handler := notify.HandlerFunc(func(ctx context.Context, j notify.Job) error {
		calls[j.ID].Add(1)
		t := time.NewTimer(latency)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	workers := make([]*notify.Worker, instances)
	var wg sync.WaitGroup
	errs := make([]error, instances)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	start := time.Now()
	for i := range workers {
		cfg := notify.DefaultConfig(fmt.Sprintf("bench-%d", i), []string{kind})
		cfg.Concurrency = concurrency
		cfg.PollInterval = 20 * time.Millisecond
		workers[i] = notify.NewWorker(store.NewNotify(pool), handler, cfg, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = workers[i].Run(runCtx)
		}()
	}

	deadline := time.Now().Add(10 * time.Minute)
	for {
		var done int64
		for _, w := range workers {
			done += w.Stats().Done
		}
		if done >= int64(jobs) {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			return 0, 0, fmt.Errorf("timed out with %d/%d jobs done", done, jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	elapsed := time.Since(start)
	cancel()
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return 0, 0, err
	}

	var doneInDB int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM notification_jobs WHERE status = 'done'").Scan(&doneInDB); err != nil {
		return 0, 0, err
	}
	if doneInDB != jobs {
		return 0, 0, fmt.Errorf("db reports %d/%d jobs done", doneInDB, jobs)
	}
	dupes := 0
	for i := 1; i <= jobs; i++ {
		if calls[i].Load() > 1 {
			dupes++
		}
	}
	return elapsed, dupes, nil
}
