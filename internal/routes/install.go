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
	// The root is the install flow too, and for a non-embedded app that is not
	// a convenience — it is the entry point.
	//
	// Shopify opens an app at its `application_url`, signed, with `shop` and
	// `hmac` appended. Serving only /install leaves that arrival at a 404, and
	// the merchant simply never reaches the consent screen. `{$}` matches the
	// root exactly, so this does not become a catch-all that swallows typos in
	// every other path.
	mux.HandleFunc("GET /{$}", route(h.Begin))
	mux.HandleFunc("GET /install", route(h.Begin))

	mux.HandleFunc("GET /auth/callback", route(h.Callback))
}
