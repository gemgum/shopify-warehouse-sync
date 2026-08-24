package shopify

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// The Bulk Operations API is what reads **every** variant in a store.
//
// The reason is not speed but budget. Reading twenty thousand variants through
// ordinary queries means two hundred pages, each one spending from the leaky
// bucket, and half the wall-clock spent waiting for it to refill. A bulk
// operation runs on Shopify's own side: one query goes out, one JSONL file
// comes back, and the budget is barely touched.
//
// The price: **one store may have only one bulk query running.** That is
// Shopify's rule, not a choice made here, and callers must be ready to be
// refused while another sync is still going.

const bulkRunMutation = `
mutation Run($query: String!) {
  bulkOperationRunQuery(query: $query) {
    bulkOperation { id status }
    userErrors { field message }
  }
}`

const bulkPollQuery = `
query {
  currentBulkOperation(type: QUERY) {
    id
    status
    errorCode
    objectCount
    url
  }
}`

type bulkOperation struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	ErrorCode   string `json:"errorCode"`
	ObjectCount string `json:"objectCount"`
	URL         string `json:"url"`
}

// runBulkQuery runs one bulk query and returns the address of its result file.
// An empty address means the query matched no rows at all — Shopify does not
// produce a file for an empty result.
func (c *Client) runBulkQuery(ctx context.Context, shop models.Shop, query string) (string, error) {
	var started struct {
		BulkOperationRunQuery struct {
			BulkOperation bulkOperation `json:"bulkOperation"`
			UserErrors    []struct {
				Field   []string `json:"field"`
				Message string   `json:"message"`
			} `json:"userErrors"`
		} `json:"bulkOperationRunQuery"`
	}

	if err := c.graphql(ctx, shop, bulkRunMutation, map[string]any{"query": query}, &started); err != nil {
		return "", err
	}
	if errs := started.BulkOperationRunQuery.UserErrors; len(errs) > 0 {
		// What turns up here most often: a bulk query is already running.
		// Passed through as-is so the operator knows to wait rather than retry.
		return "", httpx.UpstreamError(fmt.Errorf("bulkOperationRunQuery: %s", errs[0].Message))
	}

	return c.awaitBulk(ctx, shop, started.BulkOperationRunQuery.BulkOperation.ID)
}

// awaitBulk waits for a bulk operation to finish.
//
// The interval grows, from one second to five. A small store is done on the
// first poll; a large one does not finish sooner for being asked every second,
// and every poll still costs budget.
//
// **The id we started is carried in, and that is not excess caution.**
// `currentBulkOperation` returns the store's most recent operation, not
// specifically ours — if the answer is still the previous sync's operation,
// already COMPLETED, then without this check we would immediately download the
// stale result file. Yesterday's stock gets written back to the store, and not
// a single error appears.
func (c *Client) awaitBulk(ctx context.Context, shop models.Shop, id string) (string, error) {
	pause := time.Second
	const maxPause = 5 * time.Second

	for {
		var polled struct {
			CurrentBulkOperation *bulkOperation `json:"currentBulkOperation"`
		}
		if err := c.graphql(ctx, shop, bulkPollQuery, nil, &polled); err != nil {
			return "", err
		}
		if polled.CurrentBulkOperation == nil {
			return "", httpx.UpstreamError(fmt.Errorf("bulk operation vanished before finishing"))
		}

		op := polled.CurrentBulkOperation

		// Not our operation yet — the store still holds the previous one. Wait
		// for it rather than treating it as done.
		if id != "" && op.ID != id {
			c.logger.Debug("another bulk operation is still on record",
				"shop", shop.Domain, "waiting_for", id, "saw", op.ID)
			op = &bulkOperation{Status: "RUNNING"}
		}

		switch op.Status {
		case "COMPLETED":
			c.logger.Info("bulk operation finished",
				"shop", shop.Domain, "objects", op.ObjectCount)
			return op.URL, nil

		case "FAILED", "CANCELED", "EXPIRED":
			return "", httpx.UpstreamError(fmt.Errorf("bulk operation %s: %s", op.Status, op.ErrorCode))
		}

		// The deadline belongs to the caller's context. Without it this loop
		// could wait forever if Shopify stopped answering.
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", httpx.UpstreamError(ctx.Err())
		case <-timer.C:
		}

		if pause < maxPause {
			pause += time.Second
		}
	}
}

// eachBulkLine fetches the result file and calls fn for each of its lines.
//
// Read line by line rather than loaded whole: a large store's result file can
// run to hundreds of megabytes, and this service runs in a small container.
func (c *Client) eachBulkLine(ctx context.Context, url string, fn func([]byte) error) error {
	if url == "" {
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return httpx.ServerError(err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return httpx.UpstreamError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return httpx.UpstreamError(fmt.Errorf("downloading the bulk result: %s", resp.Status))
	}

	lines := bufio.NewScanner(resp.Body)
	// A single JSONL line can be long when a variant carries many fields.
	// bufio's default is 64 KB, and anything past it stops silently.
	lines.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for lines.Scan() {
		line := lines.Bytes()
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	return lines.Err()
}

// decodeLine reads one JSONL line into target.
func decodeLine[T any](line []byte) (T, error) {
	var out T
	err := json.Unmarshal(line, &out)
	return out, err
}
