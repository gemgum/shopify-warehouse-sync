package shopify

import (
	"context"
	"strings"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// The topics this service subscribes to at install time.
//
// The ones mandatory for public apps — customers/data_request,
// customers/redact, shop/redact — are **not** here: those are configured in
// the Partner dashboard, not through the API. Their addresses are still served
// by this service.
var subscribedTopics = []string{
	// Stock changed on Shopify's side. If it was not us who changed it, the
	// store and the warehouse have begun to diverge — which is exactly when a
	// sync is worth running.
	"INVENTORY_LEVELS_UPDATE",

	// The merchant removed the app. The token dies immediately, so the row has
	// to be marked; otherwise every later sync hits a 401 and looks like a
	// malfunction.
	"APP_UNINSTALLED",
}

const webhookCreateMutation = `
mutation Register($topic: WebhookSubscriptionTopic!, $sub: WebhookSubscriptionInput!) {
  webhookSubscriptionCreate(topic: $topic, webhookSubscription: $sub) {
    userErrors { field message }
  }
}`

// RegisterWebhooks sets up the webhook subscriptions for one store.
//
// Called every time an install completes, reinstalls included. Shopify rejects
// a duplicate subscription with "address for this topic has already been
// taken" — that is not a failure, it is the state we wanted, so the message is
// skipped rather than turned into an error.
func (c *Client) RegisterWebhooks(ctx context.Context, shop models.Shop, callbackBase string) error {
	for _, topic := range subscribedTopics {
		var out struct {
			WebhookSubscriptionCreate struct {
				UserErrors []struct {
					Field   []string `json:"field"`
					Message string   `json:"message"`
				} `json:"userErrors"`
			} `json:"webhookSubscriptionCreate"`
		}

		vars := map[string]any{
			"topic": topic,
			"sub": map[string]any{
				"callbackUrl": callbackBase + "/webhooks/" + strings.ToLower(topic),
				"format":      "JSON",
			},
		}
		if err := c.graphql(ctx, shop, webhookCreateMutation, vars, &out); err != nil {
			return err
		}

		for _, e := range out.WebhookSubscriptionCreate.UserErrors {
			if strings.Contains(e.Message, "has already been taken") {
				continue
			}
			c.logger.Warn("webhook subscription refused",
				"shop", shop.Domain, "topic", topic, "message", e.Message)
		}
	}
	return nil
}
