package routes

import (
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/handlers"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Install registers the OAuth 2.0 flow.
//
// Both are open without a token — they have to be, since the callers are
// Shopify and the merchant's browser. What guards them is the HMAC signature,
// checked in the service layer before anything is done.
//
// /auth/callback must match the allowed redirection URL in the Partner
// dashboard exactly. One slash apart and Shopify refuses the whole install.
func Install(mux *http.ServeMux, route func(httpx.Route) http.HandlerFunc, h *handlers.InstallHandler) {
	mux.HandleFunc("GET /install", route(h.Begin))
	mux.HandleFunc("GET /auth/callback", route(h.Callback))
}
