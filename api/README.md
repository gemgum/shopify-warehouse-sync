# The Postman collection

`shopify-warehouse-sync.postman_collection.json` — every address this service
answers, with the signatures computed for you.

The requests sign themselves. Pre-request scripts compute the HMAC that Shopify
would send: base64 over the raw body for webhooks, hex over the sorted query
string for the OAuth flow. That is what makes these real requests rather than
stubs that only pass because verification was skipped.

## Before anything works

Four collection variables. Click the collection name in the sidebar — not a
folder, not a request — then the **Variables** tab.

| Variable | Value |
|---|---|
| `app_url` | `http://localhost:8080` under `make dev`. Under `make cli` the Shopify CLI picks a random port; read it from the `service listening` log line |
| `api_secret` | The app's **Secret** from the dev dashboard, the same value as `SHOPIFY_API_SECRET` in `.env`. Not the Client ID |
| `sync_token` | `SYNC_TOKEN` from `.env` |
| `shop` | The development store's `.myshopify.com` domain |

Where you put them does not matter — the scripts read with `pm.variables`,
which looks through every scope, so a value typed into an environment instead of
the collection still works.

Postman's two **columns** do matter. **Current value** is the one used when a
request runs; filling only *Initial value* leaves every signed request answering
`401`.

## What each request is for

### 0 · Open

**Health** — called by monitoring and by Docker's healthcheck. Confirms the
service is up *and* its database is reachable; a service that cannot record
anything must not sync anything. Needs `app_url`. Answers `200`.

### 1 · Install (OAuth 2.0)

**Begin install (app root)** — called by Shopify when a merchant launches the
app. Verifies the signature, then bounces the merchant to the consent screen on
their own store. Needs `api_secret` and `shop`. Answers `302`, with `Location`
pointing at `…/admin/oauth/authorize`.

The body of that `302` is a small HTML link. Browsers never show it — they
follow the `Location` header. Seeing it in Postman means it worked.

**Begin install (/install)** — the identical handler at a second address,
reachable without Shopify launching it.

**Callback** — called by Shopify after the merchant approves, carrying a
one-time code. Trades it for an access token and stores it.

**Cannot be run from Postman**, and deliberately so: it needs a code Shopify
issues once and a cookie from the same browser session. Forging both is exactly
what the flow exists to prevent. It is here to document the shape.

### 2 · Operator

**Run a sync** — called by you or a scheduler. Reads the warehouse feed, reads
the store's stock, and writes back the differences. Needs `sync_token` and
`shop`. Answers `200` with `checked` and `updated`.

`updated: 0` is a success, not a failure — it means the two sides already
agree, which is what a second run should report.

**Sync history** — called when something looks wrong. Lists past runs, newest
first, failures included with their cause. Needs `sync_token` and `shop`.

### 3 · Webhooks

Every request in this folder impersonates Shopify, so all of them need
`api_secret`.

**inventory_levels/update** — Shopify reporting that stock moved on its side.
Queues a sync and answers `202` immediately; Shopify drops a connection it
cannot finish in five seconds, and a full sync is far longer than that. Run
**Sync history** a few seconds later and a new run appears with
`trigger: "webhook"`.

**app/uninstalled** — ⚠️ **changes state.** Marks the shop uninstalled and
clears its token. After this, `Run a sync` answers `404` until the app is
installed again.

**customers/data_request (GDPR)** — a merchant asking for a customer's data.
This app stores none, and says so. Changes nothing.

**customers/redact (GDPR)** — a customer asking to be erased. Same answer,
same nothing changed.

**shop/redact (GDPR)** — ⚠️ **changes state.** Arrives 48 hours after an
uninstall; clears the shop's credentials.

All three GDPR topics are mandatory for a public app, whether or not it holds
any customer data. An address answering `404` fails App Store review.

### 4 · Must fail

Five requests that must be **refused**. Red is the passing result here, and
none of them change anything.

| Request | Answers | What it proves |
|---|---|---|
| Sync without a token | `401` | An address that can rewrite a store's whole inventory is not open |
| Sync with the wrong token | `401` | The comparison is constant time, and a near-miss is still a miss |
| Sync a domain that is not myshopify.com | `400` | The service cannot be lured into calling an attacker's host |
| Webhook with a forged signature | `401` | A stranger cannot trigger syncs by impersonating Shopify |
| Webhook without a shop header | `400` | The header is *not* covered by the signature, so its shape is checked separately |

A collection where everything passes only proves the happy path. This folder is
what proves the doors are shut.

## Suggested order

1. **Health** — if this fails, nothing else will work
2. **Run a sync**
3. **Sync history**
4. **inventory_levels/update**, then **Sync history** again to see the
   webhook-triggered run
5. **4 · Must fail** — all five should be red

Leave the two ⚠️ requests for last, or skip them. Recovering means installing
the app again through `make cli`.

## A note on running it as a suite

There is no `newman` step in CI yet. Installing it (`npm i -g newman`) makes the
whole collection runnable from the command line:

```bash
newman run api/shopify-warehouse-sync.postman_collection.json \
  --env-var app_url=http://localhost:8080 \
  --env-var shop=… --env-var api_secret=… --env-var sync_token=…
```

`newman` supplies these as environment variables, and the scripts read through
`pm.variables`, so they resolve without any change to the collection.
