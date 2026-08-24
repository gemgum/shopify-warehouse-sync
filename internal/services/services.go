// Package services holds the rules: what has to change, and which steps must
// fall together.
//
// What is not here: SQL (repository's job), the shape of an HTTP answer
// (handlers'), and not one line of GraphQL (shopify's). This package knows only
// the interfaces in internal/models — which is why the sync rules can be tested
// without Postgres and without a real store.
package services

import (
	"context"
	"log/slog"

	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// base is what every service has: something to run transactions, and a logger.
type base struct {
	db     runner
	logger *slog.Logger
}

// runner mirrors database.Runner so this package need not import the database
// package at all.
type runner interface {
	Run(ctx context.Context, fn func(context.Context) error) error
}

// logError records one error, then returns it untouched.
//
// **Its level is chosen from the error's category, and that is deliberate.**
// Ordinary refusals — a shop that has not installed the app, a signature that
// does not match — happen every day and are not signs of damage; recording them
// as Error floods the log until the real failures drown in it.
func logError(ctx context.Context, logger *slog.Logger, op string, fields []any, err error) error {
	if err == nil || logger == nil {
		return err
	}

	appErr := httpx.From(err)

	level := slog.LevelInfo
	if appErr.HTTPStatus() >= 500 {
		level = slog.LevelError
	}

	logger.Log(ctx, level, "operation failed",
		append([]any{
			"op", op,
			"status", string(appErr.Status),
			"code", appErr.Code,
			"error", err.Error(),
		}, fields...)...)

	return err
}

// queryOne runs one transaction and returns its result.
//
// It exists so no service method repeats the same six lines of transaction
// boilerplate. A function rather than a method, because Go does not allow
// generic methods.
func queryOne[T any](ctx context.Context, b base, fn func(context.Context) (T, error)) (T, error) {
	var result T

	err := b.db.Run(ctx, func(ctx context.Context) error {
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
