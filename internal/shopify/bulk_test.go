package shopify

import (
	"errors"
	"testing"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// The lines below are copied verbatim from a real bulk result file, escaped
// slashes and all. Hand-written fixtures would have quietly agreed with
// whatever the parser did; these are what Shopify actually sends.
func TestVariantsFoldsRealBulkOutput(t *testing.T) {
	lines := []string{
		// Tracked, stocked at exactly one location — syncable.
		`{"id":"gid:\/\/shopify\/ProductVariant\/1","sku":"sku-hosted-1","inventoryItem":{"id":"gid:\/\/shopify\/InventoryItem\/11","tracked":true}}`,
		`{"location":{"id":"gid:\/\/shopify\/Location\/78313291879"},"quantities":[{"quantity":20}],"__parentId":"gid:\/\/shopify\/ProductVariant\/1"}`,

		// Tracked, but split across two locations. One number per SKU cannot be
		// divided between them without guessing.
		`{"id":"gid:\/\/shopify\/ProductVariant\/2","sku":"sku-managed-1","inventoryItem":{"id":"gid:\/\/shopify\/InventoryItem\/22","tracked":true}}`,
		`{"location":{"id":"gid:\/\/shopify\/Location\/78313226343"},"quantities":[{"quantity":50}],"__parentId":"gid:\/\/shopify\/ProductVariant\/2"}`,
		`{"location":{"id":"gid:\/\/shopify\/Location\/78313291879"},"quantities":[{"quantity":50}],"__parentId":"gid:\/\/shopify\/ProductVariant\/2"}`,

		// Not tracked. Shopify refuses the mutation and fails the whole batch
		// along with it.
		`{"id":"gid:\/\/shopify\/ProductVariant\/3","sku":"sku-untracked-1","inventoryItem":{"id":"gid:\/\/shopify\/InventoryItem\/33","tracked":false}}`,
		`{"location":{"id":"gid:\/\/shopify\/Location\/78313226343"},"quantities":[{"quantity":0}],"__parentId":"gid:\/\/shopify\/ProductVariant\/3"}`,

		// No SKU at all — nothing to match a warehouse feed against.
		`{"id":"gid:\/\/shopify\/ProductVariant\/4","sku":null,"inventoryItem":{"id":"gid:\/\/shopify\/InventoryItem\/44","tracked":true}}`,
		`{"location":{"id":"gid:\/\/shopify\/Location\/78313226343"},"quantities":[{"quantity":9}],"__parentId":"gid:\/\/shopify\/ProductVariant\/4"}`,
	}

	var items []models.InventoryItem
	v := variants{emit: func(item models.InventoryItem) error {
		items = append(items, item)
		return nil
	}}

	for _, raw := range lines {
		line, err := decodeLine[bulkLine]([]byte(raw))
		if err != nil {
			t.Fatalf("line did not parse: %v", err)
		}
		if err := v.add(line); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if err := v.done(); err != nil {
		t.Fatalf("done: %v", err)
	}

	// Every variant is still counted as examined, even the ones left alone.
	if len(items) != 4 {
		t.Fatalf("got %d variants, want 4: %+v", len(items), items)
	}

	syncable := items[0]
	if syncable.SKU != "sku-hosted-1" ||
		syncable.LocationID != "gid://shopify/Location/78313291879" ||
		syncable.Quantity != 20 ||
		syncable.InventoryItemID != "gid://shopify/InventoryItem/11" {
		t.Errorf("single-location variant folded wrong: %+v", syncable)
	}

	// The quantity must come from the location, not from a total: writing a
	// two-location total into one location doubles that location's stock.
	for i, want := range map[int]string{1: "sku-managed-1", 2: "sku-untracked-1"} {
		if items[i].SKU != want {
			t.Fatalf("item %d is %q, want %q", i, items[i].SKU, want)
		}
		if items[i].LocationID != "" {
			t.Errorf("%s must not be syncable, got location %q", want, items[i].LocationID)
		}
	}

	if items[3].SKU != "" {
		t.Errorf("a variant without a SKU should stay empty, got %q", items[3].SKU)
	}
}

// A file that ends right after a variant line, with its levels never arriving,
// must not drop that variant on the floor.
func TestVariantsHandlesATrailingVariant(t *testing.T) {
	var items []models.InventoryItem
	v := variants{emit: func(item models.InventoryItem) error {
		items = append(items, item)
		return nil
	}}

	line, err := decodeLine[bulkLine]([]byte(
		`{"id":"gid:\/\/shopify\/ProductVariant\/9","sku":"A","inventoryItem":{"id":"gid:\/\/x\/1","tracked":true}}`))
	if err != nil {
		t.Fatalf("line did not parse: %v", err)
	}
	if err := v.add(line); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := v.done(); err != nil {
		t.Fatalf("done: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0].LocationID != "" {
		t.Errorf("a variant stocked nowhere must not be syncable: %+v", items[0])
	}
}

// Variants are handed over as they are read, not collected first. The proof is
// that a caller can stop the walk partway: a slice-returning API would already
// have built the whole catalogue before the caller ever saw the first item.
func TestVariantsStopsWhenTheCallerStops(t *testing.T) {
	lines := []string{
		`{"id":"gid://v/1","sku":"A","inventoryItem":{"id":"gid://i/1","tracked":true}}`,
		`{"location":{"id":"gid://l/1"},"quantities":[{"quantity":1}],"__parentId":"gid://v/1"}`,
		`{"id":"gid://v/2","sku":"B","inventoryItem":{"id":"gid://i/2","tracked":true}}`,
		`{"location":{"id":"gid://l/1"},"quantities":[{"quantity":2}],"__parentId":"gid://v/2"}`,
		`{"id":"gid://v/3","sku":"C","inventoryItem":{"id":"gid://i/3","tracked":true}}`,
	}

	enough := errors.New("that is enough")

	var seen []string
	v := variants{emit: func(item models.InventoryItem) error {
		seen = append(seen, item.SKU)
		if item.SKU == "A" {
			return enough
		}
		return nil
	}}

	var stopped error
	for _, raw := range lines {
		line, err := decodeLine[bulkLine]([]byte(raw))
		if err != nil {
			t.Fatalf("line did not parse: %v", err)
		}
		if stopped = v.add(line); stopped != nil {
			break
		}
	}

	if !errors.Is(stopped, enough) {
		t.Fatalf("the caller's error did not stop the walk: %v", stopped)
	}
	if len(seen) != 1 || seen[0] != "A" {
		t.Fatalf("saw %v, want only the first variant", seen)
	}
}
