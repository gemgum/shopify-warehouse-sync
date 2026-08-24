# How this fits together

Written for someone who has the code in front of them and is still asking:
*why does Shopify need a tunnel, and what is a development store anyway?*

Everything here describes the app in this repository. Where a concept has a
counterpart in the code, the file is named.

---

## The four parties

```
   your laptop                the internet                  Shopify
┌────────────────┐        ┌────────────────┐        ┌────────────────────┐
│ this Go service│◀──────▶│     tunnel     │◀──────▶│  lophin-store      │
│  :8080         │        │ https://….com  │        │  .myshopify.com    │
│                │                                  │                    │
│ PostgreSQL     │─────────── Admin API ───────────▶│  products, stock   │
│  :55432        │◀────────── webhooks ─────────────│                    │
└────────────────┘                                  └────────────────────┘
        ▲
        │ SKU → quantity
┌────────────────┐
│ warehouse.json │   the "external warehouse system", simulated
└────────────────┘
```

Traffic runs **both ways**, and that is the whole reason a tunnel exists.

---

## Why a tunnel

Calling Shopify is easy: `https://lophin-store.myshopify.com/admin/api/...` is a
public address, and your laptop can reach it from behind any router.

The other direction is the problem. Shopify has to reach **you** — twice:

1. **The OAuth callback.** After a merchant approves the install, Shopify
   redirects their browser to *your* address with a one-time code.
2. **Webhooks.** When stock changes in the store, Shopify POSTs to *your*
   address.

`http://localhost:8080` means nothing to Shopify — every machine on earth has a
localhost, and none of them is yours from the outside. So a tunnel gives your
local port a real public HTTPS address:

```
https://random-words-1234.trycloudflare.com  ──▶  http://localhost:8080
```

HTTPS is not optional here. Shopify refuses a plain-HTTP callback address, even
in development, because the authorization code travels in that redirect.

The address changes every time you restart the tunnel. That is the single most
common way an install breaks: Shopify still holds yesterday's address, sends the
merchant there, and nothing answers. `internal/config/config.go` guards against
one version of this by letting the CLI's live `HOST` override a stale `APP_URL`
sitting in `.env`.

---

## Shopify's three different places

They are easy to confuse, and each has a different job.

| Place | Address | What lives there |
|---|---|---|
| **Dev dashboard** | `dev.shopify.com` | Your *app*: client id, secret, versions, configuration |
| **Store admin** | `admin.shopify.com/store/lophin-store` | The *store*: products, inventory, locations, orders |
| **The store itself** | `lophin-store.myshopify.com` | The storefront, and the Admin API this service calls |

A **development store** is a free, fully functional store meant for building
against. It cannot process real payments.

Not every store in your store list is one. A store created the ordinary way — a
trial, or a real shop — will not be offered by `shopify app dev`, which says so
plainly: *"you don't have any dev stores associated with this Dev Dashboard"*.
Dev stores are created from **Dev Dashboard → Stores**, and only from there.

`lophin-store.myshopify.com` is the value this service means by `shop`
everywhere — in `make sync SHOP=…`, in the `shops` table, and in the
`X-Shopify-Shop-Domain` header. `models.ValidShopDomain` refuses anything that
is not a `.myshopify.com` domain, because that value becomes the host this
service calls and the OAuth redirect target — see the comment in
`internal/models/shop.go` for what a crafted value would otherwise do.

---

## Custom app or public app

Two ways to get credentials, and they lead to different code.

**A custom app**, created inside one store's admin, hands you an Admin API
access token directly. No OAuth, no tunnel, no callback. Simple — and it only
ever works for that one store.

**A public app**, created in the dev dashboard, has no token until a merchant
installs it. Getting one means the OAuth handshake, which means the tunnel.

This repository implements the second. It is more work, and it is what an app
distributed to more than one store must do.

---

## The OAuth 2.0 handshake

What actually happens when a merchant installs, and where each step lives:

Shopify opens an app at its `application_url` — the root — with `shop` and
`hmac` appended, so the root and `/install` are the same handler. An app that
serves only `/install` leaves that arrival at a 404, and the merchant never
reaches the consent screen at all.

```
1. Shopify  ──▶  GET /?shop=…&hmac=…                   handlers.InstallHandler.Begin
                 verify the signature                   services.InstallService.Begin
                 mint a random `state`, set as cookie
                 302 to the store's authorize page

2. merchant approves the scopes in their own store admin

3. Shopify  ──▶  GET /auth/callback?shop=…&code=…&state=…&hmac=…
                 verify the signature                   services.InstallService.Complete
                 compare `state` against the cookie, constant time
                 POST the code back to Shopify          shopify.Client.Exchange
                 ◀── access token
                 store it                               repository.Shop.Save
                 subscribe to webhooks                  shopify.Client.RegisterWebhooks
```

Three things are worth understanding rather than copying:

**The HMAC.** Every OAuth request from Shopify carries a signature computed over
the query string with your API secret. Anything unsigned did not come from
Shopify. The check walks the **raw** query string rather than Go's
`url.Values` — Go re-encodes with its own rules, so what would be hashed is no
longer the text Shopify signed, and a perfectly valid signature would be
rejected. `internal/shopify/hmac.go` says this at the point it matters.

**The `state` nonce.** A random value set as a cookie in step 1 and compared in
step 3. Without it, an attacker could walk a merchant through completing an
install for a *different* store, and the token you end up storing belongs to a
store that merchant never approved.

**Offline token.** The token this app asks for outlives any browser session,
because syncs run from cron and from webhooks — moments when nobody has the app
open. An online token would die with the session and make the nightly sync fail
for no visible reason.

---

## Webhooks, and why they are signed

A webhook is Shopify calling you when something happens. This app answers five:

| Topic | Route | Registered by |
|---|---|---|
| `inventory_levels/update` | `/webhooks/inventory_levels_update` | the app itself, at install |
| `app/uninstalled` | `/webhooks/app_uninstalled` | the app itself, at install |
| `customers/data_request` | `/webhooks/customers_data_request` | `shopify.app.toml` |
| `customers/redact` | `/webhooks/customers_redact` | `shopify.app.toml` |
| `shop/redact` | `/webhooks/shop_redact` | `shopify.app.toml` |

The first two are subscribed through the API once an install completes
(`internal/shopify/webhook.go`). The last three are **mandatory for every public
app**, are declared in the config file, and must be answered even by an app that
holds no customer data at all — this one does not, and its handlers say so
plainly rather than inventing something to return.

These addresses are open on the internet. What stands between them and a
stranger triggering syncs at will is one thing: **the signature**. Shopify
HMACs the request body with your API secret and sends the result in
`X-Shopify-Hmac-Sha256`.

Two details that bite:

- The signature is **base64 over the raw body**, while OAuth's is **hex over the
  query string**. One implementation used for both will always reject one of
  them.
- It must be computed over the body **exactly as received**. JSON that has been
  decoded and re-encoded can differ by a single space, and one space is enough
  to reject every valid webhook forever. That is why
  `handlers.WebhookHandler.verify` reads the raw bytes before anything else
  touches them.

A webhook must also be answered in **under five seconds**. Shopify drops a
slower connection and re-delivers, so a full sync — minutes, for a large
catalogue — cannot happen inside the request. The handler queues the work and
replies `202`; `services.SyncService` drains that queue in the background.

---

## Where the configuration lives

Not in the dashboard. In `shopify.app.toml`, at the repository root.

That file holds the app URL, the allowed redirect URLs, the scopes, and the
compliance webhook addresses. `shopify app deploy` turns it into a new **app
version**, and the dashboard's Versions card is where that version appears. A
version has to be *released* to take effect — editing settings without releasing
leaves Shopify using the old ones, and the callback gets refused with nothing
that points at why.

Keeping the file authoritative is the point: the configuration lands in git,
next to the code it describes, and can be reviewed the same way.

Two quirks of Shopify CLI 4.x, worth knowing before they cost you an hour.

**Both `[webhooks]` and `[events]` are required.** They overlap — `[events]` is
the newer module that replaced `[webhooks]` — but the validator checks each one
separately, and leaving either out fails with a bare `Required` that names
neither schema. `[events]` also insists on its `subscription` key being present;
an empty list satisfies it, which is what this repository does, because the
topics it cares about are subscribed at runtime instead.

**Keep the API version moving.** Shopify supports each dated version for twelve
months. A pin that was current when the code was written becomes a retired
version a year later, and the calls stop behaving as documented. `[events]` is
the exception — it is a preview module and takes `unstable`, nothing else.

**`roles` in `shopify.web.toml` decides what the dev proxy forwards.** The CLI
routes `/api/*` to a `backend` and `/` to a `frontend`. An app with only a
backend never receives the root, and the browser gets the proxy's own
`Invalid path` instead of the OAuth redirect.

**The tunnel binary may fail with `EACCES`.** `shopify app dev` downloads its
own `cloudflared` into the CLI's install directory, which is owned by root when
the CLI came from `npm -g`. Rather than reaching for sudo, point it at a copy you
own: `SHOPIFY_CLI_CLOUDFLARED_PATH=~/.local/bin/cloudflared`. `make cli` does
this, and `make cloudflared` fetches the binary.

**`dev_store_url` is better left out.** Pinning a domain that turns out to be
wrong produces *"could not find store"*, which says nothing about what the right
domain would be. Without it the CLI asks, and remembers the answer.

---

## What the CLI does for you

`shopify app dev` is worth understanding because it collapses four manual steps
into one:

1. Opens a tunnel, and gives you its HTTPS address.
2. Rewrites `application_url` and `redirect_urls` to match — this is what
   `automatically_update_urls_on_dev = true` in `shopify.app.toml` permits.
3. Starts this service with the app credentials already in its environment.
4. Prints an install link for the development store.

It passes the service these variables, which `internal/config/config.go` knows
how to read:

| Variable | Used as |
|---|---|
| `SHOPIFY_API_KEY` | `SHOPIFY_API_KEY` |
| `SHOPIFY_API_SECRET` | `SHOPIFY_API_SECRET` |
| `HOST` | `APP_URL` — **wins over `.env`**, since the tunnel is new every run |
| `BACKEND_PORT` | the port to listen on — the tunnel points at exactly this one |
| `SCOPES` | `SHOPIFY_SCOPES` |

The command the CLI runs is declared in `shopify.web.toml`. For this repository
it is simply `go run ./cmd/api`.

---

## Two ways to run

**With the CLI** — nothing to copy by hand:

```bash
make db     # Postgres on :55432
make cli    # shopify app dev, with a cloudflared it is allowed to run
```

**Without it** — when you want to control every part:

```bash
make db
make tunnel                       # cloudflared, prints an https address
# paste that address into APP_URL in .env
# register <APP_URL>/auth/callback in the dashboard, and release the version
make dev
```

Either way, `SYNC_TOKEN` and `DATABASE_URL` come from `.env`. `SYNC_TOKEN`
guards `/sync`, which Shopify never calls and which can rewrite a store's whole
inventory — the service refuses to start if it is shorter than 32 characters.

Then, once installed:

```bash
make sync SHOP=lophin-store.myshopify.com
```

For the sync to change anything, the store's products need SKUs that appear in
`warehouse.json`. A SKU the feed does not mention is deliberately left alone —
"unknown" is not "zero", and translating one into the other would empty every
product not yet registered in the warehouse.

---

## When it goes wrong

| What you see | What it means |
|---|---|
| `invalid_input` on install | `shop` is not a `.myshopify.com` domain |
| `unauthorized` on install or callback | The HMAC did not match — usually the wrong `SHOPIFY_API_SECRET` |
| `The install session has expired` | The `state` cookie is gone or does not match. Start from `/install`, not from a bookmarked callback |
| Shopify refuses the redirect | `redirect_urls` does not contain `<APP_URL>/auth/callback` exactly, or the version was never released |
| `not_found` on `/sync` | The store has not installed the app, or it uninstalled — see `uninstalled_at` in `shops` |
| `upstream_error` | Shopify refused or did not answer. Not a bug here; retry later |
| Sync succeeds with `updated: 0` | Working correctly — no SKU in the store matched the warehouse feed, or the numbers already agree |
| `not stocked at the location` | The variant is not stocked where the write was aimed. This is what a guessed "primary location" produces |
| `must include the following argument: changeFromQuantity` | From 2026-07 every set states the quantity it read. Introspection calls the field nullable; the API refuses without it anyway |
| `The @idempotent directive is required` | 2026-07 wants a key on this mutation, so a repeat cannot be applied twice |
