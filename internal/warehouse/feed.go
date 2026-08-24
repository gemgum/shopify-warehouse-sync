// Package warehouse is the stock system outside Shopify — simulated here.
//
// Its shape is deliberately as plain as possible: a JSON object of SKUs and
// their quantities. What sits behind it may be a file, an HTTP address, or one
// day a real WMS, and none of those may change the sync rules in
// internal/services.
package warehouse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Feed satisfies models.WarehouseFeed.
type Feed struct {
	source string
	http   *http.Client
}

var _ models.WarehouseFeed = (*Feed)(nil)

func NewFeed(source string) *Feed {
	return &Feed{source: source, http: &http.Client{Timeout: 30 * time.Second}}
}

// Stock reads current warehouse stock.
//
// A feed that cannot be read comes back as an error, **not as an empty map**.
// An empty map means "no SKU is known", and the sync rules would read that as
// "nothing needs changing" — quietly leaving the store on stale numbers with
// nothing to signal that the warehouse was unreachable.
func (f *Feed) Stock(ctx context.Context) (map[string]int, error) {
	var raw []byte
	var err error

	if strings.HasPrefix(f.source, "http://") || strings.HasPrefix(f.source, "https://") {
		raw, err = f.fetch(ctx)
	} else {
		raw, err = os.ReadFile(f.source)
	}
	if err != nil {
		return nil, httpx.UpstreamError(fmt.Errorf("warehouse feed: %w", err))
	}

	stock := map[string]int{}
	if err := json.Unmarshal(raw, &stock); err != nil {
		return nil, httpx.UpstreamError(fmt.Errorf("warehouse feed could not be read: %w", err))
	}
	return stock, nil
}

func (f *Feed) fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.source, nil)
	if err != nil {
		return nil, err
	}

	resp, err := f.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
