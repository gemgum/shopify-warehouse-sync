package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/internal/services"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// WebhookVerifier checks the signature on a webhook body.
//
// An interface this small so the HTTP layer never holds the API secret.
type WebhookVerifier interface {
	VerifyWebhook(body []byte, signature string) bool
}

type WebhookHandler struct {
	verifier WebhookVerifier
	sync     *services.SyncService
	install  *services.InstallService
	logger   *slog.Logger
}

func NewWebhookHandler(verifier WebhookVerifier, sync *services.SyncService,
	install *services.InstallService, logger *slog.Logger) *WebhookHandler {

	return &WebhookHandler{verifier: verifier, sync: sync, install: install, logger: logger}
}

// verify reads a webhook body **after** proving its signature.
//
// The order must not be reversed, and the body must be the raw one: JSON that
// has been decoded and re-encoded can differ by a single space from what
// Shopify signed, and one space is enough to reject a valid webhook forever.
//
// The limit is 1 MB. Shopify's webhooks sit far below it, and anything larger
// is almost certainly not from Shopify.
func (h *WebhookHandler) verify(w http.ResponseWriter, r *http.Request) (shop string, body []byte, err error) {
	body, err = httpx.ReadBody(w, r, 1<<20)
	if err != nil {
		return "", nil, err
	}

	if !h.verifier.VerifyWebhook(body, r.Header.Get("X-Shopify-Hmac-Sha256")) {
		return "", nil, httpx.Unauthorized("The webhook signature is not valid.")
	}

	// The store name comes from a header, because the inventory_levels/update
	// body never mentions it.
	//
	// **Headers are not covered by the signature** — only the body is. So the
	// value is treated as input that has not been trusted yet: its shape is
	// checked here, and whatever passes still has to match an installed store's
	// row before a single query runs.
	shop = r.Header.Get("X-Shopify-Shop-Domain")
	if !models.ValidShopDomain(shop) {
		return "", nil, httpx.BadRequest("The shop domain header is missing or malformed.")
	}
	return shop, body, nil
}

// InventoryLevelsUpdate receives word that stock changed on Shopify's side.
//
// All it does is put a sync on the queue and reply 202. Shopify drops a
// connection it cannot finish in five seconds and re-delivers; a full sync is
// past that limit, and a webhook kept being re-sent would pile up into a run of
// syncs that achieve nothing.
func (h *WebhookHandler) InventoryLevelsUpdate(w http.ResponseWriter, r *http.Request) error {
	shop, _, err := h.verify(w, r)
	if err != nil {
		return err
	}

	h.sync.Enqueue(shop)
	httpx.Accepted(w, "Sync queued.")
	return nil
}

// AppUninstalled marks a store that removed the app.
//
// Its token is already dead by the time this arrives. Without the mark, every
// later sync would hit a 401 and look like a broken service.
func (h *WebhookHandler) AppUninstalled(w http.ResponseWriter, r *http.Request) error {
	shop, _, err := h.verify(w, r)
	if err != nil {
		return err
	}

	if err := h.install.Uninstall(r.Context(), shop); err != nil {
		return err
	}

	httpx.OK(w, "Shop marked as uninstalled.", nil)
	return nil
}
