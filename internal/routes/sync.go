package routes

import (
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/handlers"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Sync registers the operator's addresses.
//
// Shopify does not call either of these, so there is no HMAC to check — what
// guards them is a token in the header, checked in the handler.
func Sync(mux *http.ServeMux, route func(httpx.Route) http.HandlerFunc, h *handlers.SyncHandler) {
	mux.HandleFunc("POST /sync", route(h.Run))
	mux.HandleFunc("GET /sync/history", route(h.History))
}
