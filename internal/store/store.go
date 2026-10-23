// Package store owns database access: the connection pool and the
// sqlc-generated queries in store/db. It implements service.Store.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jenish0908/assetflow/internal/service"
	"github.com/jenish0908/assetflow/internal/store/db"
)

// NewPool opens a pgx connection pool and verifies the database is reachable,
// so the process fails fast at startup instead of on the first request.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("store: parse database url: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping database: %w", err)
	}

	return pool, nil
}

// Store implements service.Store on top of sqlc queries.
type Store struct {
	pool *pgxpool.Pool // nil when the Store is bound to a transaction
	q    *db.Queries
}

var _ service.Store = (*Store)(nil)

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: db.New(pool)}
}

// InTx runs fn in a transaction: committed if fn returns nil, rolled back
// otherwise. Calling InTx on a Store already bound to a transaction reuses it.
func (s *Store) InTx(ctx context.Context, fn func(service.Store) error) error {
	if s.pool == nil {
		return fn(s)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Store{q: s.q.WithTx(tx)})
	})
}

// Postgres error codes, see https://www.postgresql.org/docs/16/errcodes-appendix.html
const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
)

// translate maps driver errors to the service error kinds while keeping the
// original error in the chain for logging.
func translate(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", service.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation, pgForeignKeyViolation:
			return fmt.Errorf("%w: %w", service.ErrConflict, err)
		case pgCheckViolation:
			return fmt.Errorf("%w: %w", service.ErrInvalid, err)
		}
	}
	return err
}
