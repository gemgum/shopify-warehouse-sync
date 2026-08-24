package shopify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// redirector points calls at a fake store.
//
// A store's address is assembled from its domain (`https://<shop>/admin/...`),
// so this is the only way to test the client without changing production code
// purely for a test.
type redirector struct{ base string }

func (a redirector) RoundTrip(r *http.Request) (*http.Response, error) {
	target, err := url.Parse(a.base)
	if err != nil {
		return nil, err
	}
	clone := r.Clone(r.Context())
	clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, models.Shop) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	c := New(Options{APIVersion: "2026-07", APISecret: "hush"}, slog.New(slog.DiscardHandler))
	c.http = &http.Client{Transport: redirector{server.URL}}

	return c, models.Shop{Domain: "uji.myshopify.com", AccessToken: "shpat_uji"}
}

// writeGraphQL replies with a complete GraphQL answer, budget status included.
func writeGraphQL(w http.ResponseWriter, data string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":`+data+
		`,"extensions":{"cost":{"throttleStatus":{"currentlyAvailable":1000,"restoreRate":100}}}}`)
}

// Shopify answers 200 even when the query was refused — the error is in the
// body. Anything that only checks the HTTP status will conclude all went well.
func TestGraphQLErrorInBodyIsStillAnError(t *testing.T) {
	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errors":[{"message":"Access denied for productVariants field"}]}`)
	})

	var out struct{}
	err := c.graphql(context.Background(), shop, "{ x }", nil, &out)
	if err == nil {
		t.Fatal("an error in the response body was reported as success")
	}
	if !strings.Contains(err.Error(), "Shopify") {
		t.Errorf("the error should be classified upstream, got: %v", err)
	}
}

// A 200 with neither data nor errors must not surface in the log as
// "unexpected end of JSON input" — that misleads whoever reads it.
func TestGraphQLWithoutDataIsNotAJSONError(t *testing.T) {
	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})

	var out struct{}
	err := c.graphql(context.Background(), shop, "{ x }", nil, &out)
	if err == nil {
		t.Fatal("a response with no data was reported as success")
	}
	if strings.Contains(err.Error(), "unexpected end of JSON") {
		t.Errorf("the raw error leaked through: %v", err)
	}
}

// 429 is the last-resort backstop and must be retried — not passed on as a
// failed sync.
func TestGraphQLRetriesAfter429(t *testing.T) {
	var calls atomic.Int32

	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0.01")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeGraphQL(w, `{"result":"ok"}`)
	})

	var out struct {
		Result string `json:"result"`
	}
	if err := c.graphql(context.Background(), shop, "{ x }", nil, &out); err != nil {
		t.Fatalf("429 was not retried: %v", err)
	}
	if out.Result != "ok" {
		t.Errorf("the result did not parse after the retry: %+v", out)
	}
	if calls.Load() != 2 {
		t.Errorf("called %d times, want 2", calls.Load())
	}
}

// The most dangerous bug in the bulk path: `currentBulkOperation` returns the
// store's **most recent** operation, not specifically ours. If the previous
// sync is still on record as COMPLETED, a client that does not match the id
// will download the stale result file — yesterday's stock written back to the
// store, with not a single error raised.
func TestBulkDoesNotUseThePreviousOperationsResult(t *testing.T) {
	var poll atomic.Int32

	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fresh-result.jsonl" {
			_, _ = io.WriteString(w,
				`{"id":"gid://v/1","sku":"A","inventoryItem":{"id":"gid://i/1","tracked":true}}`+"\n"+
					`{"location":{"id":"gid://l/1"},"quantities":[{"quantity":5}],"__parentId":"gid://v/1"}`+"\n")
			return
		}

		body, _ := io.ReadAll(r.Body)
		query := string(body)

		switch {
		case strings.Contains(query, "bulkOperationRunQuery"):
			writeGraphQL(w, `{"bulkOperationRunQuery":{"bulkOperation":{"id":"gid://bulk/BARU","status":"CREATED"},"userErrors":[]}}`)

		case strings.Contains(query, "currentBulkOperation"):
			// The first poll still answers with yesterday's operation: already
			// COMPLETED, its url pointing at the stale file.
			if poll.Add(1) == 1 {
				writeGraphQL(w, `{"currentBulkOperation":{"id":"gid://bulk/LAMA","status":"COMPLETED","url":"http://x/stale-result.jsonl","objectCount":"9"}}`)
				return
			}
			writeGraphQL(w, `{"currentBulkOperation":{"id":"gid://bulk/BARU","status":"COMPLETED","url":"http://x/fresh-result.jsonl","objectCount":"1"}}`)

		default:
			t.Errorf("unexpected query: %s", query)
		}
	})

	items, err := c.List(context.Background(), shop)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if poll.Load() < 2 {
		t.Fatal("the client accepted a bulk operation belonging to the previous sync")
	}
	if len(items) != 1 || items[0].SKU != "A" || items[0].Quantity != 5 {
		t.Fatalf("the bulk result did not parse correctly: %+v", items)
	}
	if items[0].LocationID != "gid://l/1" || items[0].InventoryItemID != "gid://i/1" {
		t.Errorf("the ids needed to write back are incomplete: %+v", items[0])
	}
}

// A bulk operation that failed on Shopify's side must not read as a store
// whose stock is genuinely empty — that would wipe the entire catalogue.
func TestFailedBulkIsReportedAsAnError(t *testing.T) {
	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		query := string(body)

		switch {
		case strings.Contains(query, "bulkOperationRunQuery"):
			writeGraphQL(w, `{"bulkOperationRunQuery":{"bulkOperation":{"id":"gid://bulk/1","status":"CREATED"},"userErrors":[]}}`)
		case strings.Contains(query, "currentBulkOperation"):
			writeGraphQL(w, `{"currentBulkOperation":{"id":"gid://bulk/1","status":"FAILED","errorCode":"INTERNAL_SERVER_ERROR"}}`)
		}
	})

	if _, err := c.List(context.Background(), shop); err == nil {
		t.Fatal("a FAILED bulk operation was reported as an empty store")
	}
}

// A mutation Shopify refuses through userErrors still comes back as 200.
// Not checking it means recording changes that never happened.
func TestApplyChecksUserErrors(t *testing.T) {
	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeGraphQL(w, `{"inventorySetQuantities":{"userErrors":[{"field":["input","quantities"],"message":"Inventory item not stocked at location"}]}}`)
	})

	err := c.Apply(context.Background(), shop, []models.Update{{
		InventoryItem: models.InventoryItem{
			SKU: "A", InventoryItemID: "gid://i/1", LocationID: "gid://l/1", Quantity: 1,
		},
		NewQuantity: 9,
	}})
	if err == nil {
		t.Fatal("userErrors were ignored")
	}
}

// The 250-per-mutation limit is Shopify's rule. Anything past it is refused
// wholesale — the first 250 included.
func TestApplySplitsLargeBatches(t *testing.T) {
	var batches []int

	c, shop := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Variables struct {
				Input struct {
					Quantities []json.RawMessage `json:"quantities"`
				} `json:"input"`
			} `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		batches = append(batches, len(sent.Variables.Input.Quantities))

		writeGraphQL(w, `{"inventorySetQuantities":{"userErrors":[]}}`)
	})

	updates := make([]models.Update, 600)
	for i := range updates {
		updates[i] = models.Update{
			InventoryItem: models.InventoryItem{
				SKU: "A", InventoryItemID: "gid://i/1", LocationID: "gid://l/1",
			},
			NewQuantity: i,
		}
	}

	if err := c.Apply(context.Background(), shop, updates); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if len(batches) != 3 || batches[0] != 250 || batches[1] != 250 || batches[2] != 100 {
		t.Fatalf("batch splitting is wrong: %v", batches)
	}
}
