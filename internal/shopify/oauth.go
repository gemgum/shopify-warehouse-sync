package shopify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// AuthorizeURL is where the merchant goes to grant permission.
//
// `state` is not decoration: the same value is also set as a cookie, and the
// callback refuses if the two differ. Without it, an attacker could walk a
// merchant through completing an install on behalf of another store — and the
// token that gets stored would belong to a store that merchant never approved.
func (c *Client) AuthorizeURL(shopDomain, state string) string {
	query := url.Values{
		"client_id":    {c.opts.APIKey},
		"scope":        {c.opts.Scopes},
		"redirect_uri": {c.opts.CallbackURL},
		"state":        {state},
	}
	return "https://" + shopDomain + "/admin/oauth/authorize?" + query.Encode()
}

// Exchange trades the authorization code for an offline access token.
//
// Offline, not online: syncs run from cron and from webhooks, at moments when
// no merchant has the app open. An online token dies with the browser session
// and would make the nightly sync fail for no visible reason.
func (c *Client) Exchange(ctx context.Context, shopDomain, code string) (token, scopes string, err error) {
	payload, err := json.Marshal(map[string]string{
		"client_id":     c.opts.APIKey,
		"client_secret": c.opts.APISecret,
		"code":          code,
	})
	if err != nil {
		return "", "", httpx.ServerError(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+shopDomain+"/admin/oauth/access_token", bytes.NewReader(payload))
	if err != nil {
		return "", "", httpx.ServerError(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", httpx.UpstreamError(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", httpx.UpstreamError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", "", httpx.UpstreamError(fmt.Errorf("token exchange %s: %s", resp.Status, truncate(body)))
	}

	var out struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", "", httpx.UpstreamError(err)
	}
	// A 200 with no token has happened, when the code had already been used
	// once. Storing an empty token would leave the shop recorded as installed
	// while every later sync gets a 401.
	if out.AccessToken == "" {
		return "", "", httpx.UpstreamError(fmt.Errorf("token exchange: empty access_token"))
	}
	return out.AccessToken, out.Scope, nil
}

// VerifyQuery and VerifyWebhook are exposed as methods so the handler layer
// never has to hold the API secret at all. The secret stops in this package.
func (c *Client) VerifyQuery(rawQuery string) bool {
	return VerifyQuery(rawQuery, c.opts.APISecret)
}

func (c *Client) VerifyWebhook(body []byte, signature string) bool {
	return VerifyWebhook(body, signature, c.opts.APISecret)
}
