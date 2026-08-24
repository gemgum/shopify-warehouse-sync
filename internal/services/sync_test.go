package services

import (
	"testing"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// Diff is the whole of this service's business rules, and the one part that
// can be quietly wrong with nothing to object: Shopify will happily accept an
// instruction to empty an entire catalogue.
func TestDiff(t *testing.T) {
	item := func(sku string, qty int) models.InventoryItem {
		return models.InventoryItem{
			SKU:             sku,
			InventoryItemID: "gid://shopify/InventoryItem/" + sku,
			LocationID:      "gid://shopify/Location/1",
			Quantity:        qty,
		}
	}

	changed := item("A", 5)
	unchanged := item("B", 3)
	unknownToWarehouse := item("C", 9)
	negative := item("E", 2)

	noSKU := item("", 1)
	noSKU.SKU = ""

	noLocation := item("D", 1)
	noLocation.LocationID = ""

	store := []models.InventoryItem{changed, unchanged, unknownToWarehouse, noSKU, noLocation, negative}
	warehouse := map[string]int{"A": 12, "B": 3, "D": 4, "E": -8}

	got := Diff(store, warehouse)

	want := []models.Update{
		{InventoryItem: changed, NewQuantity: 12},
		{InventoryItem: negative, NewQuantity: 0}, // negative translates to zero
	}

	if len(got) != len(want) {
		t.Fatalf("got %d updates %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("update %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// An empty warehouse feed must not mean "empty everything". This is the most
// expensive mistake a system like this can make, so it gets a test of its own.
func TestDiffEmptyWarehouseDoesNotEmptyTheStore(t *testing.T) {
	store := []models.InventoryItem{{
		SKU: "A", InventoryItemID: "gid://1", LocationID: "gid://L", Quantity: 40,
	}}

	if got := Diff(store, map[string]int{}); len(got) != 0 {
		t.Fatalf("an empty warehouse feed produced %d updates: %+v", len(got), got)
	}
}
