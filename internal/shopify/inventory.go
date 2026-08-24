package shopify

import (
	"context"
	"fmt"
	"strings"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Client satisfies models.StoreInventory.
var _ models.StoreInventory = (*Client)(nil)

// The query submitted as a bulk operation.
//
// Deliberately **free of nested connections**. A bulk operation splits every
// nested connection into its own JSONL line, linked by `__parentId`, and
// stitching those back together is easy-to-get-wrong code for a benefit that
// does not exist here. `inventoryQuantity` is already the variant's available
// total, and the location is fetched once by the separate query below.
const bulkVariantQuery = `
{
  productVariants {
    edges {
      node {
        id
        sku
        inventoryQuantity
        inventoryItem { id }
      }
    }
  }
}`

const primaryLocationQuery = `
query {
  locations(first: 1, includeInactive: false, includeLegacy: false) {
    nodes { id }
  }
}`

type variantLine struct {
	SKU               string `json:"sku"`
	InventoryQuantity int    `json:"inventoryQuantity"`
	InventoryItem     struct {
		ID string `json:"id"`
	} `json:"inventoryItem"`
}

// List reads a store's entire stock through the Bulk Operations API.
func (c *Client) List(ctx context.Context, shop models.Shop) ([]models.InventoryItem, error) {
	location, err := c.primaryLocation(ctx, shop)
	if err != nil {
		return nil, err
	}

	url, err := c.runBulkQuery(ctx, shop, bulkVariantQuery)
	if err != nil {
		return nil, err
	}

	var items []models.InventoryItem
	err = c.eachBulkLine(ctx, url, func(line []byte) error {
		variant, err := decodeLine[variantLine](line)
		if err != nil {
			return httpx.UpstreamError(fmt.Errorf("a bulk result line could not be read: %w", err))
		}
		items = append(items, models.InventoryItem{
			SKU:             variant.SKU,
			InventoryItemID: variant.InventoryItem.ID,
			LocationID:      location,
			Quantity:        variant.InventoryQuantity,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// primaryLocation fetches the location stock is written to.
//
// ponytail: one location. A store with several warehouses needs a location map
// coming from its own warehouse feed, and that is a change to the feed's shape
// — not to the code here. Fetched every sync rather than stored: a merchant can
// change locations at any time, and caching it means writing stock into a
// warehouse that is no longer in use, with nothing to signal it.
func (c *Client) primaryLocation(ctx context.Context, shop models.Shop) (string, error) {
	var out struct {
		Locations struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"locations"`
	}
	if err := c.graphql(ctx, shop, primaryLocationQuery, nil, &out); err != nil {
		return "", err
	}
	if len(out.Locations.Nodes) == 0 {
		return "", httpx.UpstreamError(fmt.Errorf("store %s has no active location", shop.Domain))
	}
	return out.Locations.Nodes[0].ID, nil
}

const setQuantitiesMutation = `
mutation Set($input: InventorySetQuantitiesInput!) {
  inventorySetQuantities(input: $input) {
    userErrors { field message }
  }
}`

// Apply writes the decided quantities back to Shopify.
//
// `ignoreCompareQuantity: true` is deliberate: the warehouse is the source of
// truth in this system, so whatever Shopify has recorded at the moment the
// mutation lands must not veto the write. If Shopify ever becomes a source of
// truth too, this is the first thing that has to change — along with every rule
// in internal/services.
func (c *Client) Apply(ctx context.Context, shop models.Shop, updates []models.Update) error {
	// Shopify's per-mutation limit. Sent in chunks, sequentially: concurrent
	// mutations drain the budget bucket faster than it refills, so all that is
	// gained is more 429s.
	const perBatch = 250

	for start := 0; start < len(updates); start += perBatch {
		batch := updates[start:min(start+perBatch, len(updates))]

		quantities := make([]map[string]any, 0, len(batch))
		for _, u := range batch {
			quantities = append(quantities, map[string]any{
				"inventoryItemId": u.InventoryItemID,
				"locationId":      u.LocationID,
				"quantity":        u.NewQuantity,
			})
		}

		var out struct {
			InventorySetQuantities struct {
				UserErrors []struct {
					Field   []string `json:"field"`
					Message string   `json:"message"`
				} `json:"userErrors"`
			} `json:"inventorySetQuantities"`
		}

		vars := map[string]any{"input": map[string]any{
			"name":                  "available",
			"reason":                "correction",
			"ignoreCompareQuantity": true,
			"quantities":            quantities,
		}}

		if err := c.graphql(ctx, shop, setQuantitiesMutation, vars, &out); err != nil {
			return err
		}
		if errs := out.InventorySetQuantities.UserErrors; len(errs) > 0 {
			return httpx.UpstreamError(fmt.Errorf("inventorySetQuantities: %s %s",
				strings.Join(errs[0].Field, "."), errs[0].Message))
		}
	}
	return nil
}
