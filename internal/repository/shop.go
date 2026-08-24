package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// Shop satisfies models.ShopRepository.
type Shop struct{}

var _ models.ShopRepository = Shop{}

const shopColumns = `domain, access_token, scopes, installed_at, uninstalled_at`

func scanShop(row pgx.Row) (models.Shop, error) {
	var s models.Shop
	err := row.Scan(&s.Domain, &s.AccessToken, &s.Scopes, &s.InstalledAt, &s.UninstalledAt)
	return s, err
}

// Save stores the token obtained through OAuth.
//
// An upsert, because a merchant may reinstall the app at any time — and a
// reinstall issues a new token that has to replace the old one. uninstalled_at
// is cleared in the same breath: a store that reinstalled is no longer a store
// that removed the app.
func (Shop) Save(ctx context.Context, shop models.Shop) (models.Shop, error) {
	const q = `
		insert into shops (domain, access_token, scopes)
		values ($1, $2, $3)
		on conflict (domain) do update
			set access_token   = excluded.access_token,
			    scopes         = excluded.scopes,
			    uninstalled_at = null
		returning ` + shopColumns

	return One(ctx, scanShop, q, shop.Domain, shop.AccessToken, shop.Scopes)
}

// Find returns a store that is still installed.
//
// Stores that removed the app are filtered out here rather than in the service:
// their token is already dead, so calling Shopify with it only produces a
// confusing 401 in the log.
func (Shop) Find(ctx context.Context, domain string) (models.Shop, error) {
	const q = `select ` + shopColumns + ` from shops
		where domain = $1 and uninstalled_at is null`

	shop, err := MaybeOne(ctx, scanShop, q, domain)
	if err == pgx.ErrNoRows {
		return models.Shop{}, httpx.NotFound("That shop has not installed this app.")
	}
	return shop, err
}

// MarkUninstalled revokes rather than deletes.
//
// The sync history points at this row. Deleting it throws away every trace of
// that store's stock changes — including the ones a merchant may ask about
// after reinstalling.
func (Shop) MarkUninstalled(ctx context.Context, domain string) error {
	const q = `update shops
		set uninstalled_at = now(), access_token = ''
		where domain = $1 and uninstalled_at is null`

	return Exec(ctx, q, domain)
}
