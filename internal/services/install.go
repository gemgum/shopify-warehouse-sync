package services

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/url"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

// ShopifyOAuth is what this service needs to know about Shopify.
//
// Declared here rather than in models because only this service uses it — and
// an interface defined where it is consumed does not grow because of somebody
// else's needs.
type ShopifyOAuth interface {
	AuthorizeURL(shopDomain, state string) string
	Exchange(ctx context.Context, shopDomain, code string) (token, scopes string, err error)
	VerifyQuery(rawQuery string) bool
	RegisterWebhooks(ctx context.Context, shop models.Shop, callbackBase string) error
	Describe(ctx context.Context, shop models.Shop) (string, error)
}

type InstallService struct {
	base
	shopify      ShopifyOAuth
	shops        models.ShopRepository
	callbackBase string
}

func NewInstallService(db runner, shops models.ShopRepository, shopify ShopifyOAuth,
	callbackBase string, logger *slog.Logger) *InstallService {

	return &InstallService{
		base:         base{db: db, logger: logger},
		shopify:      shopify,
		shops:        shops,
		callbackBase: callbackBase,
	}
}

// Begin checks an install request, then prepares the redirect to Shopify.
//
// state is returned so the handler can set it as a cookie. The value is
// compared again in Complete — that is what stops CSRF on the install flow.
func (s *InstallService) Begin(ctx context.Context, shopDomain, rawQuery string) (redirect, state string, err error) {
	defer func() {
		err = logError(ctx, s.logger, "InstallService.Begin", []any{"shop", shopDomain}, err)
	}()

	if !models.ValidShopDomain(shopDomain) {
		return "", "", httpx.BadRequest("The shop parameter must be a myshopify.com domain.")
	}
	// Shopify signs the install request too, not only the callback. Anything
	// unsigned did not come from Shopify.
	if !s.shopify.VerifyQuery(rawQuery) {
		return "", "", httpx.Unauthorized("The request signature is not valid.")
	}

	state = nonce()
	return s.shopify.AuthorizeURL(shopDomain, state), state, nil
}

// Complete finishes the install: verify, exchange the token, store it, then set
// up the webhook subscriptions.
//
// The order matters. The token is stored before webhooks are registered,
// because registering them uses that token — and if registration fails, the
// store is still installed and manual syncs still work. The reverse does not
// hold: webhooks registered for a store whose token was never stored produce
// nothing but incoming calls that cannot be served.
func (s *InstallService) Complete(ctx context.Context, query url.Values, rawQuery, cookieState string) (shop models.Shop, err error) {
	domain := query.Get("shop")

	defer func() {
		err = logError(ctx, s.logger, "InstallService.Complete", []any{"shop", domain}, err)
	}()

	code := query.Get("code")
	if !models.ValidShopDomain(domain) || code == "" {
		return models.Shop{}, httpx.BadRequest("The callback is missing a shop or code parameter.")
	}
	if !s.shopify.VerifyQuery(rawQuery) {
		return models.Shop{}, httpx.Unauthorized("The request signature is not valid.")
	}
	// Compared in constant time. An ordinary comparison leaks how many leading
	// characters were already right, and that is enough to guess it piece by
	// piece.
	if cookieState == "" || subtle.ConstantTimeCompare([]byte(cookieState), []byte(query.Get("state"))) != 1 {
		return models.Shop{}, httpx.Unauthorized("The install session has expired. Please start again.")
	}

	token, scopes, err := s.shopify.Exchange(ctx, domain, code)
	if err != nil {
		return models.Shop{}, err
	}

	saved, err := queryOne(ctx, s.base, func(ctx context.Context) (models.Shop, error) {
		return s.shops.Save(ctx, models.Shop{Domain: domain, AccessToken: token, Scopes: scopes})
	})
	if err != nil {
		return models.Shop{}, err
	}

	// The next two steps are outside the transaction, and their failure does
	// not undo the install. Both are repeatable: reinstalling the app registers
	// the webhooks again.
	if err := s.shopify.RegisterWebhooks(ctx, saved, s.callbackBase); err != nil {
		s.logger.Warn("could not register webhook subscriptions", "shop", domain, "error", err)
	}
	if name, err := s.shopify.Describe(ctx, saved); err == nil {
		s.logger.Info("app installed", "shop", domain, "store", name, "scopes", scopes)
	}

	return saved, nil
}

// Uninstall marks a store that removed the app.
//
// Called by the app/uninstalled and shop/redact webhooks. Idempotent on
// purpose: Shopify re-delivers a webhook it did not get an answer to, and a
// repeat must have no effect beyond the same outcome.
func (s *InstallService) Uninstall(ctx context.Context, domain string) (err error) {
	defer func() {
		err = logError(ctx, s.logger, "InstallService.Uninstall", []any{"shop", domain}, err)
	}()

	if !models.ValidShopDomain(domain) {
		return httpx.BadRequest("The shop parameter must be a myshopify.com domain.")
	}
	return s.db.Run(ctx, func(ctx context.Context) error {
		return s.shops.MarkUninstalled(ctx, domain)
	})
}

// nonce produces the OAuth state value.
//
// crypto/rand, not math/rand: a predictable value makes the state check hold
// nothing back.
func nonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
