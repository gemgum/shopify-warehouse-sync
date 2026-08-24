// Package models holds the domain's data shapes and **the interfaces the
// service layer needs** — not their implementations.
//
// Dependencies point one way, inward: repository, shopify, and warehouse import
// this package to satisfy its interfaces; this package imports none of them.
// That is what lets the sync rules be tested without Postgres and without a
// real Shopify store.
package models

import (
	"context"
	"regexp"
	"time"
)

// shopifyDomain limits which `shop` values may be used.
//
// Not tidiness: that value becomes the host this service calls
// (`https://<shop>/admin/...`) and the OAuth redirect target. Without this
// limit, a request carrying `shop=attacker.example.com` makes the service hand
// the authorization code — and later the token — to somewhere else.
var shopifyDomain = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*\.myshopify\.com$`)

// ValidShopDomain is used at every entry point that accepts a `shop`.
func ValidShopDomain(domain string) bool { return shopifyDomain.MatchString(domain) }

// Shop is one store that has installed this app.
type Shop struct {
	Domain        string     `json:"domain"`
	AccessToken   string     `json:"-"` // never travels out over HTTP
	Scopes        string     `json:"scopes"`
	InstalledAt   time.Time  `json:"installed_at"`
	UninstalledAt *time.Time `json:"uninstalled_at"`
}

type ShopRepository interface {
	Save(ctx context.Context, shop Shop) (Shop, error)
	Find(ctx context.Context, domain string) (Shop, error)

	// MarkUninstalled is called by the app/uninstalled and shop/redact
	// webhooks. The token is cleared in the same row: once the app is removed
	// that token is already dead, and storing a dead secret serves nothing.
	MarkUninstalled(ctx context.Context, domain string) error
}
