// Package problem translates Postgres errors into service errors.
//
// One place, so that Postgres's own wording — table names, constraint names,
// port numbers — never reaches a caller. To a caller it means nothing; to an
// attacker it is a map.
package problem

import (
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// SQLSTATE codes that carry a specific meaning in this service.
const (
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
)

// FromPostgres classifies a database error.
//
// Anything unrecognised falls to database_error rather than server_error: if
// the source was a query, the database is what an operator needs to look at.
func FromPostgres(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("That record does not exist.")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case uniqueViolation:
			return httpx.BadRequest("That record already exists.").WithCause(err)
		case foreignKeyViolation:
			// In this service it always means the same thing: something tried
			// to record a sync for a shop that is not installed.
			return httpx.NotFound("That shop has not installed this app.").WithCause(err)
		}
	}
	return httpx.DatabaseError(err)
}
