// Package middleware holds HTTP wrappers that know nothing about inventory —
// and must not.
//
// They all have the same shape: take an http.Handler, return an http.Handler.
// That is what lets their order be rearranged in one place.
package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Chain composes middleware from the outside in: the first one named is the
// first to touch a request.
func Chain(h http.Handler, wrappers ...func(http.Handler) http.Handler) http.Handler {
	for i := len(wrappers) - 1; i >= 0; i-- {
		h = wrappers[i](h)
	}
	return h
}

// Recover keeps a panic from taking the service down for every shop because
// one request was broken.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if problem := recover(); problem != nil {
					logger.Error("panic", "path", r.URL.Path, "problem", problem)
					httpx.Fail(w, logger, httpx.ServerError(nil))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Log records one line per request.
//
// The query string is deliberately left out. The OAuth callback carries an
// authorization code there, and a code recorded in a log is a secret leaked
// into the one place people paste around while chasing a problem.
func Log(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"took", time.Since(start).String(),
			)
		})
	}
}

// recorder remembers the status code a handler wrote.
type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
