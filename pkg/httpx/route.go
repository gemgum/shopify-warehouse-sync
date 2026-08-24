package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
)

// Route is the shape of every handler in this service: it returns an error
// rather than writing one itself.
//
// One place that writes errors means one place that could get the leaking
// wrong — and that place has been reviewed.
type Route func(http.ResponseWriter, *http.Request) error

// Wrap connects a Route to net/http.
func Wrap(logger *slog.Logger, fn Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			Fail(w, logger, err)
		}
	}
}

// ReadBody reads the whole request body, with a size limit.
//
// Whole rather than streamed, because a Shopify webhook must have its
// signature proven over the entire body before a single byte of it may be
// trusted. The limit is not excess caution: without it one request can exhaust
// the service's memory.
func ReadBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, BadRequest("The request body could not be read.").WithCause(err)
	}
	return body, nil
}

// Decode reads a JSON body with a size limit.
func Decode[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var body T

	raw, err := ReadBody(w, r, 1<<20)
	if err != nil {
		return body, err
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return body, BadRequest("The request body could not be read.").WithCause(err)
	}
	return body, nil
}
