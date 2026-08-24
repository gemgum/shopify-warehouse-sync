// Package repository is the only place in this service that writes SQL.
//
// Each type here satisfies an interface owned by `internal/models`. The models
// themselves never import this folder: the only thing joining the two is
// `internal/api/build.go`. That is what keeps dependencies pointing one way,
// and what lets services be tested with stubs.
package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gemgum/shopify-warehouse-sync/internal/database"
	"github.com/gemgum/shopify-warehouse-sync/internal/problem"
)

// The three functions below replace the preamble that used to be repeated in
// every query: take the transaction out of the context, then translate the
// Postgres error. Whoever forgets the translation leaks Postgres's own wording
// to the caller.

// One runs a query that **must** return exactly one row.
//
// No rows is an error, translated to NotFound. When "no rows" is a reasonable
// outcome, use MaybeOne instead.
func One[T any](ctx context.Context, scan func(pgx.Row) (T, error), sql string, args ...any) (T, error) {
	result, err := MaybeOne(ctx, scan, sql, args...)
	if err == pgx.ErrNoRows {
		var zero T
		return zero, problem.FromPostgres(pgx.ErrNoRows)
	}
	return result, err
}

// MaybeOne is One, but it passes pgx.ErrNoRows through untouched.
//
// Used when "no rows" means something particular at that spot — the shop has
// not installed the app, for instance. The caller is the one who knows, so the
// caller is the one who decides.
func MaybeOne[T any](ctx context.Context, scan func(pgx.Row) (T, error), sql string, args ...any) (T, error) {
	var zero T

	tx, err := database.Tx(ctx)
	if err != nil {
		return zero, err
	}

	result, err := scan(tx.QueryRow(ctx, sql, args...))
	if err == pgx.ErrNoRows {
		return zero, err
	}
	if err != nil {
		return zero, problem.FromPostgres(err)
	}
	return result, nil
}

// Many runs a query that returns several rows.
//
// The result is never nil — a caller receives `[]`, not `null`. "Never synced"
// and "failed to load the history" must not look the same.
func Many[T any](ctx context.Context, scan func(pgx.Rows) (T, error), sql string, args ...any) ([]T, error) {
	tx, err := database.Tx(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, problem.FromPostgres(err)
	}
	defer rows.Close()

	out := []T{}
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, problem.FromPostgres(err)
		}
		out = append(out, item)
	}
	// A missed rows.Err() makes a query that broke mid-stream look exactly like
	// a query that simply found nothing.
	return out, problem.FromPostgres(rows.Err())
}

// Exec runs a statement that returns no rows.
func Exec(ctx context.Context, sql string, args ...any) error {
	tx, err := database.Tx(ctx)
	if err != nil {
		return err
	}

	_, err = tx.Exec(ctx, sql, args...)
	return problem.FromPostgres(err)
}
