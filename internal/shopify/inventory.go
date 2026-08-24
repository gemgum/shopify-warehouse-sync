package shopify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Client satisfies models.StoreInventory.
var _ models.StoreInventory = (*Client)(nil)

// The query submitted as a bulk operation.
//
// The nested `inventoryLevels` connection is the point of it. An earlier
// version asked only for `inventoryQuantity` and paired every variant with the
// store's first active location — which is wrong twice over on a real store:
// `inventoryQuantity` is the total across locations, and the first location is
// very often not one where the item is stocked at all. Shopify answers such a
// write with "The specified inventory item is not stocked at the location" and
// refuses the whole batch.
const bulkVariantQuery = `
{
  productVariants {
    edges {
      node {
        id
        sku
        inventoryItem {
          id
          tracked
          inventoryLevels {
            edges {
              node {
                location { id }
                quantities(names: ["available"]) { quantity }
              }
            }
          }
        }
      }
    }
  }
}`

// bulkLine is one line of the result file — either a variant or one of its
// inventory levels. Which one it is, is told by the fields that are present.
type bulkLine struct {
	ID            string `json:"id"`
	SKU           string `json:"sku"`
	InventoryItem *struct {
		ID      string `json:"id"`
		Tracked bool   `json:"tracked"`
	} `json:"inventoryItem"`

	Location *struct {
		ID string `json:"id"`
	} `json:"location"`
	Quantities []struct {
		Quantity int `json:"quantity"`
	} `json:"quantities"`

	// Present on the child lines only, and it holds the **variant's** id — the
	// inventory item is inlined into the variant line rather than emitted as a
	// line of its own, so it is not what the children point at.
	ParentID string `json:"__parentId"`
}

// Each reads a store's entire stock and hands over one variant at a time.
//
// A callback rather than a returned slice, and that is the whole point: a
// catalogue of any size passes through without ever being held. The JSONL is
// read line by line, each variant is folded up as its lines arrive, and it is
// gone again before the next one starts. What the caller keeps is its own
// business — the sync keeps only the handful of variants that actually differ.
func (c *Client) Each(ctx context.Context, shop models.Shop, fn func(models.InventoryItem) error) error {
	url, err := c.runBulkQuery(ctx, shop, bulkVariantQuery)
	if err != nil {
		return err
	}

	seen := 0
	collector := variants{emit: func(item models.InventoryItem) error {
		seen++
		return fn(item)
	}}

	err = c.eachBulkLine(ctx, url, func(raw []byte) error {
		line, err := decodeLine[bulkLine](raw)
		if err != nil {
			return httpx.UpstreamError(fmt.Errorf("a bulk result line could not be read: %w", err))
		}
		return collector.add(line)
	})
	if err != nil {
		return err
	}
	// The last variant has no following line to close it, so it is closed here.
	if err := collector.done(); err != nil {
		return err
	}

	c.logger.Debug("stock read", "shop", shop.Domain, "variants", seen)
	return nil
}

// variants turns the flat JSONL back into one item per variant.
//
// Shopify emits a variant, then that variant's inventory levels, then the next
// variant — children always follow their parent. So a variant is complete the
// moment the next one begins, and can be handed on immediately instead of
// being collected.
type variants struct {
	emit    func(models.InventoryItem) error
	current *models.InventoryItem
	levels  int
	tracked bool
}

// add folds one line into the variant being built, emitting the previous one
// once it is complete.
func (v *variants) add(line bulkLine) error {
	if line.InventoryItem != nil {
		// A variant line: the previous variant is finished.
		if err := v.flush(); err != nil {
			return err
		}
		v.current = &models.InventoryItem{
			SKU:             line.SKU,
			InventoryItemID: line.InventoryItem.ID,
		}
		v.levels, v.tracked = 0, line.InventoryItem.Tracked
		return nil
	}

	if v.current == nil || line.Location == nil {
		return nil
	}
	v.levels++

	// The **first** stocked location wins, and a second one disqualifies the
	// variant entirely — see flush.
	if v.levels == 1 {
		v.current.LocationID = line.Location.ID
		if len(line.Quantities) > 0 {
			v.current.Quantity = line.Quantities[0].Quantity
		}
	}
	return nil
}

// flush hands the finished variant to the callback.
//
// **A variant is only syncable when its location is unambiguous.** Stock that
// lives at two locations cannot be set from a feed that gives one number per
// SKU — splitting it would be a guess, and guessing wrong quietly moves real
// inventory. An untracked variant cannot be set at all; Shopify refuses the
// mutation and takes the rest of the batch down with it.
//
// Both are still emitted, with no LocationID, so they count as examined — and
// Decide leaves them alone on the rule it already had.
func (v *variants) flush() error {
	if v.current == nil {
		return nil
	}
	if !v.tracked || v.levels != 1 {
		v.current.LocationID = ""
		v.current.Quantity = 0
	}

	item := *v.current
	v.current = nil

	return v.emit(item)
}

func (v *variants) done() error { return v.flush() }

// @idempotent is required on this mutation from API 2026-07 onward, and the
// call is refused without it.
//
// The key is what lets Shopify recognise a repeat. It is minted once per batch
// and travels as a variable, so the retry loop in graphql() — which re-sends
// the identical payload after a 429 — carries the same key and cannot apply the
// same stock change twice. A key generated per attempt would defeat the whole
// mechanism while looking perfectly correct.
const setQuantitiesMutation = `
mutation Set($input: InventorySetQuantitiesInput!, $key: String!) {
  inventorySetQuantities(input: $input) @idempotent(key: $key) {
    userErrors { field message }
  }
}`

// Apply writes the decided quantities back to Shopify.
//
// `changeFromQuantity` carries the quantity this sync read, and Shopify insists
// on it — the mutation is refused outright without it, whatever introspection
// says about the field being nullable.
//
// It makes every write an optimistic check: if the stock moved between the bulk
// read and this mutation, Shopify refuses rather than overwriting a number it
// can see is newer than ours. That is the right behaviour for a stock writer,
// and the loss is nothing — the next sync judges afresh from the current
// state and puts through whatever is still owed.
//
// (Before 2026-07 the same call could opt out of the comparison entirely with
// `ignoreCompareQuantity: true`. That field is gone, and so is the option.)
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
				"inventoryItemId":    u.InventoryItemID,
				"locationId":         u.LocationID,
				"quantity":           u.NewQuantity,
				"changeFromQuantity": u.Quantity,
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

		vars := map[string]any{
			"key": idempotencyKey(),
			"input": map[string]any{
				"name":       "available",
				"reason":     "correction",
				"quantities": quantities,
			},
		}

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

// idempotencyKey mints the key for one batch.
//
// crypto/rand rather than a counter or a timestamp: two syncs running close
// together must not produce the same key, or Shopify would treat the second
// one's genuinely different stock change as a repeat of the first and silently
// drop it.
func idempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
