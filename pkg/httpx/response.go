// Package httpx holds the shape every HTTP answer takes: one envelope for all
// replies, and one error type for the whole service.
//
// The callers here are not people — they are Shopify, and an operator's
// scripts. That is precisely why the shape has to be fixed: what reads these
// answers is a log or a tool, and neither can guess.
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// Status is the category of an answer, and what the caller uses to decide
// **what to do next** — not merely whose fault it was.
//
// A category is added when the follow-up action differs. `upstream_error`
// exists for exactly that reason: Shopify refusing or not answering is neither
// a bug here nor the caller's mistake, and the right move is to retry later —
// not to fix the request, and not to open this service's logs.
type Status string

const (
	StatusSuccess Status = "success"

	// Input that cannot be processed: a shop that is not a myshopify domain, a
	// missing code. Repeating the same request will fail the same way.
	StatusInvalidInput Status = "invalid_input"

	// The signature does not match, or the sync token is wrong. In this
	// service both mean the same thing: the caller has not proven who it is.
	StatusUnauthorized Status = "unauthorized"

	// The shop has not installed this app.
	StatusNotFound Status = "not_found"

	// The database refused or could not be reached. Worth retrying later.
	StatusDatabaseError Status = "database_error"

	// Shopify refused, throttled, or did not answer. Not a bug here.
	StatusUpstreamError Status = "upstream_error"

	// Everything else — our bug.
	StatusServerError Status = "server_error"
)

// Response is the shape of every reply the service sends, successful or not.
//
//	{"code":200,"status":"success","message":"...","data":{...}}
type Response struct {
	Code    int    `json:"code"`
	Status  Status `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func write(w http.ResponseWriter, httpStatus int, body Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(body)
}

// OK replies 200 with data.
func OK(w http.ResponseWriter, message string, data any) {
	write(w, http.StatusOK, Response{
		Code: http.StatusOK, Status: StatusSuccess, Message: message, Data: data,
	})
}

// Accepted replies 202: taken, to be done shortly.
//
// Used by webhooks. Shopify drops a connection it cannot finish in five
// seconds and then re-delivers, and a full sync takes far longer than that —
// so all that is promised here is "it is queued".
func Accepted(w http.ResponseWriter, message string) {
	write(w, http.StatusAccepted, Response{
		Code: http.StatusAccepted, Status: StatusSuccess, Message: message, Data: nil,
	})
}

// Fail replies with an error, and this is **the only place** that decides
// whether an error's contents may leave the service.
//
// The underlying cause always stops at the log. Table names, database
// addresses, and Shopify's own refusal text never travel outward.
func Fail(w http.ResponseWriter, logger *slog.Logger, err error) {
	appErr := From(err)

	if appErr.cause != nil {
		// 4xx is already recorded by the service layer, complete with the
		// operation name, so a line here would only repeat it — dropped to
		// Debug. 5xx stays at Error: an error born in middleware has nowhere
		// else to be recorded.
		level := slog.LevelDebug
		if appErr.HTTPStatus() >= 500 {
			level = slog.LevelError
		}

		logger.Log(context.Background(), level, "request failed",
			"code", appErr.Code,
			"status", string(appErr.Status),
			"cause", appErr.cause.Error(),
		)
	}

	write(w, appErr.HTTPStatus(), Response{
		Code:    appErr.Code,
		Status:  appErr.Status,
		Message: appErr.Message,
		Data:    nil,
	})
}
