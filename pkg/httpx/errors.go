package httpx

import (
	"errors"
	"net/http"
	"strconv"
)

// Error is the only error type allowed to reach a caller.
//
//   - Code    — the HTTP status number, the same one in the header.
//   - Status  — the category, which answers "is retrying worth it?".
//   - Message — an English sentence naming the exact cause.
//
// `cause` is deliberately unexported: that is the original error, and its only
// place is the log.
type Error struct {
	Code    int
	Status  Status
	Message string
	cause   error
}

func (e *Error) Error() string { return strconv.Itoa(e.Code) + ": " + e.Message }

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches the original error so it reaches the log, without
// changing anything the caller sees.
func (e *Error) WithCause(cause error) *Error {
	clone := *e
	clone.cause = cause
	return &clone
}

// HTTPStatus is the number written into the header.
//
// Taken from Code rather than derived again from the category: two places
// deciding the same number will disagree one day.
func (e *Error) HTTPStatus() int {
	if e.Code == 0 {
		return http.StatusInternalServerError
	}
	return e.Code
}

// BadRequest is for input whose shape is wrong.
func BadRequest(message string) *Error {
	return &Error{Status: StatusInvalidInput, Code: http.StatusBadRequest, Message: message}
}

// Unauthorized is for a signature that does not match, or a wrong token.
//
// The message deliberately does not say which one was wrong. Whoever calls
// with a forged signature has no right to learn how close their guess was.
func Unauthorized(message string) *Error {
	return &Error{Status: StatusUnauthorized, Code: http.StatusUnauthorized, Message: message}
}

// NotFound is for a shop that has not installed this app.
func NotFound(message string) *Error {
	return &Error{Status: StatusNotFound, Code: http.StatusNotFound, Message: message}
}

// DatabaseError is for a database that refused or could not be reached.
func DatabaseError(cause error) *Error {
	return &Error{
		Status:  StatusDatabaseError,
		Code:    http.StatusInternalServerError,
		Message: "The database is not responding. Please try again shortly.",
		cause:   cause,
	}
}

// UpstreamError is for Shopify refusing, throttling, or not answering.
//
// 502, not 500. The number is what tells an operator to go look at Shopify's
// status rather than at this service's logs.
func UpstreamError(cause error) *Error {
	return &Error{
		Status:  StatusUpstreamError,
		Code:    http.StatusBadGateway,
		Message: "Shopify did not accept the request. Please try again shortly.",
		cause:   cause,
	}
}

// ServerError is for everything else. Our bug.
func ServerError(cause error) *Error {
	return &Error{
		Status:  StatusServerError,
		Code:    http.StatusInternalServerError,
		Message: "Something went wrong on our side. Please try again shortly.",
		cause:   cause,
	}
}

// From makes sure whatever reaches the HTTP layer has the same shape.
//
// An unrecognised error **always** becomes server_error with a uniform
// message. That is what keeps the promise that Postgres error text and
// Shopify's response bodies never leak outward just because one path was
// missed.
func From(err error) *Error {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	return ServerError(err)
}
