package routes

import (
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/handlers"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Webhook registers every address Shopify calls.
//
// The first two are registered by the service itself at install time (see
// internal/shopify/webhook.go). The last three are mandatory for public apps
// and are configured in the Partner dashboard — their addresses must match
// these exactly.
//
// All POST, all with their signature verified before a single byte is trusted.
func Webhook(mux *http.ServeMux, route func(httpx.Route) http.HandlerFunc, h *handlers.WebhookHandler) {
	mux.HandleFunc("POST /webhooks/inventory_levels_update", route(h.InventoryLevelsUpdate))
	mux.HandleFunc("POST /webhooks/app_uninstalled", route(h.AppUninstalled))

	mux.HandleFunc("POST /webhooks/customers_data_request", route(h.CustomersDataRequest))
	mux.HandleFunc("POST /webhooks/customers_redact", route(h.CustomersRedact))
	mux.HandleFunc("POST /webhooks/shop_redact", route(h.ShopRedact))
}
