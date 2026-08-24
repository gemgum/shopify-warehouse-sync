package models

import "context"

// InventoryItem is one SKU's stock as Shopify currently records it.
type InventoryItem struct {
	SKU string `json:"sku"`

	// These two ids are what a write-back needs. Both are GraphQL GIDs
	// (`gid://shopify/InventoryItem/123`), not REST numbers — kept verbatim on
	// purpose so no id is ever assembled somewhere else.
	InventoryItemID string `json:"inventory_item_id"`
	LocationID      string `json:"location_id"`

	Quantity int `json:"quantity"`
}

// Update is a decision made by the sync: set this SKU to NewQuantity.
type Update struct {
	InventoryItem
	NewQuantity int `json:"new_quantity"`
}

// StoreInventory is what the service layer needs to know about Shopify: how to
// read stock, and how to write it back.
//
// Shop is passed per call rather than held inside the implementation, because
// one service serves many stores and the token belongs to the store — not to
// the HTTP client talking to it.
//
// Each hands over one variant at a time instead of returning them all. A store
// may hold a hundred thousand variants, and only the handful that differ are
// worth keeping — so the ones that match stream past and are forgotten. An
// error returned by fn stops the walk and comes back from Each.
type StoreInventory interface {
	Each(ctx context.Context, shop Shop, fn func(InventoryItem) error) error
	Apply(ctx context.Context, shop Shop, updates []Update) error
}

// WarehouseFeed is the stock system outside Shopify: a map of SKU to quantity.
//
// Deliberately this simple. What sits behind it may be a file, an HTTP address,
// or one day a real WMS — and none of those may change the sync rules.
type WarehouseFeed interface {
	Stock(ctx context.Context) (map[string]int, error)
}
