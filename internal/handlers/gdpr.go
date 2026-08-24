package handlers

import (
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// The three addresses every public app on the Shopify App Store must serve.
//
// **Required even when the app stores no customer data at all** — and this one
// does not: what it keeps is a shop domain, a token, and per-SKU stock changes.
// Shopify still delivers all three topics, and an address answering 404 gets an
// app submission rejected.
//
// So the answers are honest and short: the signature is still checked — these
// addresses are open on the internet like any other webhook — and then a 200
// says plainly that there is no customer data held.
//
// What is **not** done here: inventing data to return, or quietly answering 200
// without checking who called.

// CustomersDataRequest: a merchant asks for a copy of one customer's data.
func (h *WebhookHandler) CustomersDataRequest(w http.ResponseWriter, r *http.Request) error {
	shop, _, err := h.verify(w, r)
	if err != nil {
		return err
	}

	h.logger.Info("customer data request (GDPR)", "shop", shop)
	httpx.OK(w, "This app stores no customer data.", nil)
	return nil
}

// CustomersRedact: a customer asks to have their data erased.
func (h *WebhookHandler) CustomersRedact(w http.ResponseWriter, r *http.Request) error {
	shop, _, err := h.verify(w, r)
	if err != nil {
		return err
	}

	h.logger.Info("customer data erasure (GDPR)", "shop", shop)
	httpx.OK(w, "This app stores no customer data.", nil)
	return nil
}

// ShopRedact: 48 hours after an uninstall, all of a store's data must go.
//
// What happens here is real, unlike the two above: the store is marked
// uninstalled and its token is cleared. The sync history stays — it holds SKUs
// and quantities, nobody's personal data.
func (h *WebhookHandler) ShopRedact(w http.ResponseWriter, r *http.Request) error {
	shop, _, err := h.verify(w, r)
	if err != nil {
		return err
	}

	if err := h.install.Uninstall(r.Context(), shop); err != nil {
		return err
	}

	h.logger.Info("shop data erasure (GDPR)", "shop", shop)
	httpx.OK(w, "Shop credentials removed.", nil)
	return nil
}
