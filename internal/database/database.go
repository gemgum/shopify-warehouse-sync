// Package database opens the connection to Postgres and runs every query
// inside a transaction.
//
// The transaction rides in the `context` rather than being passed as a
// parameter. That is what lets each domain's interface take nothing but a
// `context.Context` and never mention `pgx` — the service layer does not know
// the database is Postgres, and does not need to.
//
// One sync writes two things that must fall together: the closing of its
// sync_runs row, and every inventory_changes row. History claiming "twelve SKUs
// changed" while storing only seven of them is worse than storing nothing,
// because whoever reads it has no reason to be suspicious.
package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNoTransaction means some code tried to touch the database outside DB.Run.
// That is a bug, not a state reachable through normal use.
var ErrNoTransaction = errors.New("query run outside a transaction")

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("database url could not be parsed: %w", err)
	}

	// This service syncs one store at a time, so the connection count is not
	// the interesting part. Having a ceiling is: managed providers cut
	// connections that pile up, and a pool without a limit will hit that.
	cfg.MaxConns = 10
	cfg.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("could not prepare the connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database could not be reached: %w", err)
	}

	db := &DB{pool: pool}
	return db, db.migrate(ctx)
}

func (d *DB) Close() { d.pool.Close() }

// Ping checks that the database still answers.
//
// Used by /healthz. A service that is alive but cannot touch its database
// cannot record a single stock change — and a sync that cannot be recorded is
// a sync that must not run.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// migrate runs the files in migrations/ in order.
//
// All of them are `create ... if not exists`, so running them on every start is
// safe. A separate migration tool only earns its place once there is a breaking
// change; until then it is one more thing to install on the server.
func (d *DB) migrate(ctx context.Context) error {
	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}

	for _, f := range files {
		sql, err := migrations.ReadFile("migrations/" + f.Name())
		if err != nil {
			return err
		}
		if _, err := d.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", f.Name(), err)
		}
	}
	return nil
}

type contextKey struct{}

// Run executes fn inside a single transaction.
//
// If fn returns an error, the transaction is rolled back entirely.
func (d *DB) Run(ctx context.Context, fn func(context.Context) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(context.WithValue(ctx, contextKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Tx returns the transaction currently in flight.
//
// Used **only** by internal/repository.
func Tx(ctx context.Context) (pgx.Tx, error) {
	tx, ok := ctx.Value(contextKey{}).(pgx.Tx)
	if !ok {
		return nil, ErrNoTransaction
	}
	return tx, nil
}

// Runner is the only thing the service layer needs to know about the database:
// how to run something inside a transaction.
//
// An interface rather than *DB, so services can be tested without Postgres.
type Runner interface {
	Run(ctx context.Context, fn func(context.Context) error) error
}

// Query runs one transaction and returns its result.
//
// It exists so no service method repeats the same six lines of transaction
// boilerplate.
func Query[T any](ctx context.Context, run Runner, fn func(context.Context) (T, error)) (T, error) {
	var result T

	err := run.Run(ctx, func(ctx context.Context) error {
		var err error
		result, err = fn(ctx)
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}
