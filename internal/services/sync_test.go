package services

import (
	"testing"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// Decide is the whole of this service's business rules, and the one part that
// can be quietly wrong with nothing to object: Shopify will happily accept an
// instruction to empty an entire catalogue.
func TestDecide(t *testing.T) {
	item := func(sku string, qty int) models.InventoryItem {
		return models.InventoryItem{
			SKU:             sku,
			InventoryItemID: "gid://shopify/InventoryItem/" + sku,
			LocationID:      "gid://shopify/Location/1",
			Quantity:        qty,
		}
	}

	noSKU := item("x", 1)
	noSKU.SKU = ""

	noLocation := item("D", 1)
	noLocation.LocationID = ""

	warehouse := map[string]int{"A": 12, "B": 3, "D": 4, "E": -8}

	cases := []struct {
		name   string
		item   models.InventoryItem
		want   int
		needed bool
	}{
		{"quantity differs", item("A", 5), 12, true},
		{"negative becomes zero", item("E", 2), 0, true},
		{"already agrees", item("B", 3), 0, false},
		{"warehouse never mentions it", item("C", 9), 0, false},
		{"no SKU to match on", noSKU, 0, false},
		{"stocked nowhere unambiguous", noLocation, 0, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			update, needed := Decide(c.item, warehouse)

			if needed != c.needed {
				t.Fatalf("needed = %v, want %v", needed, c.needed)
			}
			if !needed {
				return
			}
			if update.NewQuantity != c.want {
				t.Errorf("NewQuantity = %d, want %d", update.NewQuantity, c.want)
			}
			// The item travels with the decision: the change log needs the
			// quantity it had before, and the mutation needs both ids.
			if update.InventoryItem != c.item {
				t.Errorf("the item did not travel with the decision: %+v", update)
			}
		})
	}
}

// An empty warehouse feed must not mean "empty everything". This is the most
// expensive mistake a system like this can make, so it gets a test of its own.
func TestDecideEmptyWarehouseDoesNotEmptyTheStore(t *testing.T) {
	item := models.InventoryItem{
		SKU: "A", InventoryItemID: "gid://1", LocationID: "gid://L", Quantity: 40,
	}

	if _, needed := Decide(item, map[string]int{}); needed {
		t.Fatal("an empty warehouse feed produced an update")
	}
}
