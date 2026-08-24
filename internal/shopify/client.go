package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Options holds the credentials and addresses this adapter needs.
//
// Plain values, not config.Config: this package must not know where the
// service reads its settings from.
type Options struct {
	APIKey      string
	APISecret   string
	Scopes      string
	CallbackURL string

	// APIVersion is stated explicitly. Shopify ships a new version every
	// quarter and retires the old one after a year; silently following "the
	// latest" means this service can change behaviour without a single line of
	// code changing.
	APIVersion string
}

type Client struct {
	opts   Options
	http   *http.Client
	logger *slog.Logger
}

func New(opts Options, logger *slog.Logger) *Client {
	return &Client{
		opts: opts,
		// The deadline is generous: a bulk operation waits on a result file
		// that can run to tens of megabytes. What keeps a request from hanging
		// forever is the caller's context, not this number.
		http:   &http.Client{Timeout: 120 * time.Second},
		logger: logger,
	}
}

func (c *Client) endpoint(shop models.Shop, path string) string {
	return "https://" + shop.Domain + "/admin/api/" + c.opts.APIVersion + path
}

type gqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Extensions struct {
		Cost struct {
			ThrottleStatus struct {
				CurrentlyAvailable float64 `json:"currentlyAvailable"`
				RestoreRate        float64 `json:"restoreRate"`
			} `json:"throttleStatus"`
		} `json:"cost"`
	} `json:"extensions"`
}

// graphql runs one Admin API query and puts the result in out.
//
// **Shopify's GraphQL budget is a leaky bucket**, not a request-per-minute
// count: every query costs according to what it asks for, and the bucket
// refills at a steady rate. So what governs the wait here is the remaining
// budget Shopify itself reports, not a guess of ours — and 429 is treated as a
// last-resort backstop, not as the normal path.
func (c *Client) graphql(ctx context.Context, shop models.Shop, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return httpx.ServerError(err)
	}

	const maxAttempts = 5

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.endpoint(shop, "/graphql.json"), bytes.NewReader(payload))
		if err != nil {
			return httpx.ServerError(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Shopify-Access-Token", shop.AccessToken)

		resp, err := c.http.Do(req)
		if err != nil {
			return httpx.UpstreamError(err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		_ = resp.Body.Close()
		if err != nil {
			return httpx.UpstreamError(err)
		}

		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxAttempts {
			c.sleep(ctx, retryAfter(resp))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return httpx.UpstreamError(fmt.Errorf("graphql %s: %s", resp.Status, truncate(body)))
		}

		var envelope gqlEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			return httpx.UpstreamError(fmt.Errorf("graphql: response could not be read: %w", err))
		}
		// Shopify answers 200 even when the query was refused — the error is in
		// the body. Anything that only checks the HTTP status will conclude
		// everything went fine.
		if len(envelope.Errors) > 0 {
			if envelope.Errors[0].Message == "Throttled" && attempt < maxAttempts {
				c.sleep(ctx, c.pause(envelope))
				continue
			}
			return httpx.UpstreamError(fmt.Errorf("graphql: %s", envelope.Errors[0].Message))
		}

		c.sleep(ctx, c.pause(envelope))

		// A 200 with neither `data` nor `errors` has happened when Shopify cut
		// off mid-response. Passed on as an upstream error rather than left to
		// surface as "unexpected end of JSON input", which misleads whoever
		// reads it in the log.
		if len(envelope.Data) == 0 {
			return httpx.UpstreamError(fmt.Errorf("graphql: response carried no data"))
		}
		return json.Unmarshal(envelope.Data, out)
	}
}

// pause works out how long to wait before the next query.
//
// The threshold is remaining budget, not elapsed time: while the bucket is
// above the threshold the next query may go straight out. Only the shortfall is
// waited on, divided by the refill rate Shopify reports — so the number adapts
// on its own for a store with a larger bucket (Shopify Plus).
func (c *Client) pause(envelope gqlEnvelope) time.Duration {
	const threshold = 200

	status := envelope.Extensions.Cost.ThrottleStatus
	if status.RestoreRate <= 0 || status.CurrentlyAvailable >= threshold {
		return 0
	}
	return time.Duration((threshold - status.CurrentlyAvailable) / status.RestoreRate * float64(time.Second))
}

func (c *Client) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	c.logger.Debug("waiting on the Shopify budget", "pause", d.String())

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s > 0 {
		return time.Duration(s * float64(time.Second))
	}
	return 2 * time.Second
}

// rest calls the REST Admin API.
//
// Used for what genuinely is not in GraphQL, or not worth a GraphQL round trip
// — fetching the store's details once at install time. The rest of this
// service goes through GraphQL, because that is where query cost can be
// controlled.
func (c *Client) rest(ctx context.Context, shop models.Shop, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(shop, path), nil)
	if err != nil {
		return httpx.ServerError(err)
	}
	req.Header.Set("X-Shopify-Access-Token", shop.AccessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return httpx.UpstreamError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return httpx.UpstreamError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.UpstreamError(fmt.Errorf("rest %s %s: %s", path, resp.Status, truncate(body)))
	}
	return json.Unmarshal(body, out)
}

// Describe fetches the store's name and plan over REST.
//
// Called once at install, purely for the log. A store that installs the app
// then appears under the name its merchant knows, rather than as a domain.
func (c *Client) Describe(ctx context.Context, shop models.Shop) (string, error) {
	var out struct {
		Shop struct {
			Name string `json:"name"`
			Plan string `json:"plan_display_name"`
		} `json:"shop"`
	}
	if err := c.rest(ctx, shop, "/shop.json", &out); err != nil {
		return "", err
	}
	return out.Shop.Name + " (" + out.Shop.Plan + ")", nil
}

func truncate(b []byte) string {
	const limit = 500
	if len(b) > limit {
		return string(b[:limit]) + "…"
	}
	return string(b)
}
