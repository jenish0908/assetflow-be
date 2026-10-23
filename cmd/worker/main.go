// Command worker delivers notification jobs from the notification_jobs table.
// Run as many copies as you like: FOR UPDATE SKIP LOCKED gives each job to
// exactly one of them. On SIGTERM it stops claiming, lets in-flight jobs
// finish (up to SHUTDOWN_TIMEOUT) and returns unstarted jobs to the queue.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jenish0908/assetflow-be/internal/config"
	"github.com/jenish0908/assetflow-be/internal/notify"
	"github.com/jenish0908/assetflow-be/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	host, _ := os.Hostname()
	cfg, err := config.LoadWorker(os.Getenv, fmt.Sprintf("%s-%d", host, os.Getpid()))
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("worker_id", cfg.WorkerID)

	pool, err := store.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	wcfg := notify.DefaultConfig(cfg.WorkerID, notify.Kinds)
	wcfg.Concurrency = cfg.Concurrency
	wcfg.ShutdownTimeout = cfg.ShutdownTimeout

	w := notify.NewWorker(store.NewNotify(pool), notify.NewNotifier(notify.LogSender{Logger: logger}), wcfg, logger)
	logger.Info("worker starting", "concurrency", wcfg.Concurrency, "kinds", wcfg.Kinds)
	return w.Run(ctx)
}
