# Shopify Warehouse Inventory Sync

A custom Shopify app that keeps a Shopify store's inventory in sync with an
external warehouse system. Written in Go, talks to the Shopify GraphQL Admin
API, logs every sync to PostgreSQL.

Built and tested end-to-end against a Shopify development store.

---

## Problem

A merchant's real stock lives in a warehouse system, not in Shopify. Without a
sync layer the two drift apart: Shopify oversells items the warehouse no longer
has, and under-sells items that were restocked. Manual CSV updates don't scale
and leave no audit trail when numbers look wrong.

## What it does

- Installs into a Shopify store via the standard OAuth 2.0 flow, HMAC-verified
  on both the install request and the callback.
- Reads every variant's stock through the **Bulk Operations API**, so a
  20,000-variant catalogue is one job instead of 200 throttled pages.
- Reads current stock from an external (simulated) warehouse source.
- Reconciles the two and pushes inventory adjustments back to Shopify.
- Subscribes to `inventory_levels/update` and `app/uninstalled`, and answers
  the three GDPR webhooks Shopify requires of public apps.
- Records every sync run and every individual quantity change in PostgreSQL, so
  any stock number can be traced back to when and why it changed.

## Architecture

```
                        ┌──────────────── webhooks (HMAC) ────────────────┐
                        ▼                                                 │
warehouse feed ──▶ sync service (Go) ──▶ Shopify Admin API (GraphQL + REST)
                        │
                        ▼
                    PostgreSQL
              (sync runs + change log)
```

Dependencies point inward. `internal/models` defines the interfaces the
business rules need; `repository`, `shopify`, and `warehouse` implement them.
The business layer imports none of them — which is why the reconciliation rules
are unit-tested without Postgres and without a live store.

```
cmd/api/                 start, graceful shutdown, container healthcheck
internal/api/build.go    composition root — the only place that picks implementations
internal/routes/         every URL the service answers, in one screen
internal/handlers/       HTTP in, HTTP out. No rules, no queries
internal/services/       the rules: what changes, and what must commit together
internal/models/         domain types + the interfaces the services depend on
internal/repository/     the only place that writes SQL
internal/shopify/        OAuth, GraphQL/REST client, bulk operations, HMAC
internal/warehouse/      the external stock feed
internal/database/       pool, transactions, embedded migrations
internal/problem/        Postgres errors → service errors
pkg/httpx/               one response envelope, one error type
pkg/middleware/          recover, request log

shopify.app.toml         app config: urls, scopes, compliance webhooks
shopify.web.toml         the command `shopify app dev` runs
docs/                    how the pieces fit together, for a new reader
```

## What runs where

The diagram above says what talks to what. This one says what *runs* where —
and where the line falls between what this repository owns and what it only
borrows.

```
┌──────────── one machine ─────────────┐      ┌─────── Shopify's servers ───────┐
│                                      │      │                                 │
│  sync service (Go)      :8080        │─────▶│  Admin API                      │
│  PostgreSQL (Docker)    :55432       │      │  the store's products, variants,│
│  cloudflared            when needed  │◀─────│  SKUs, stock, orders            │
│  warehouse.json         on disk      │      │  the dev dashboard              │
│                                      │      │                                 │
└──────────────────────────────────────┘      └─────────────────────────────────┘
```

Everything in this repository runs as one process next to one database. Shopify
is rented, not owned: the store, its catalogue, and its inventory numbers live
on Shopify's servers, and this service is a client that corrects those numbers.
It holds no products and serves no storefront.

**Traffic runs both ways, and that asymmetry is the whole reason a tunnel
appears anywhere in this project.** Calls *out* to the Admin API work from any
machine with a network connection. Calls *in* — the OAuth callback, and every
webhook — are Shopify reaching the service, and `localhost` means nothing to
Shopify. See [docs/how-it-fits-together.md](docs/how-it-fits-together.md) for
how the tunnel closes that gap.

### What the database is for

Not a copy of the catalogue. Three tables, each holding something Shopify does
not keep on our behalf:

| Table | Holds | Why it cannot live in Shopify |
|---|---|---|
| `shops` | shop domain and its offline access token | Without a stored token, every sync would need a merchant sitting at a browser. This is what lets a sync run from cron at 3am |
| `sync_runs` | every run, successful or not | Shopify has no idea a sync was attempted — least of all one that failed halfway |
| `inventory_changes` | SKU, quantity before, quantity after | Shopify stores the number a variant has now, never the history of how it got there |

That last row is the one that earns its keep. "Why does this show 12?" becomes a
query rather than a guess.

### Deployment

Nothing here is deployed; it runs on a developer machine. Production would put
the service in the container the `Dockerfile` already builds, swap the local
Postgres for a managed one, replace the tunnel with a real domain, and point
`WAREHOUSE_SOURCE` at the warehouse system's own endpoint.

None of that touches the sync rules — which is why the feed is an interface
(`models.WarehouseFeed`) rather than a file reader wired straight into the
service.

## Technical highlights

**OAuth 2.0 install flow.** Redirect to the merchant's store for consent,
validate the HMAC on the callback, compare the `state` nonce in constant time,
exchange the code for an offline access token, and persist it per shop. The
signature is checked over the *raw* query string rather than through
`url.Values` — Go re-encodes with its own rules, and a valid signature
computed over re-encoded text will never match.

**Bulk Operations over pagination.** Shopify's GraphQL API is cost-throttled
with a leaky bucket, so reading a full catalogue page by page spends most of
its wall-clock waiting for the bucket to refill. The full read is submitted as
a bulk query, polled with a backing-off interval, and streamed line by line
from the resulting JSONL.

The catalogue is never assembled. Variants are handed to the caller one at a
time as their lines arrive, so a store of any size passes through while only
the handful that actually differ are kept. A caller can stop the walk partway,
which a slice-returning read could not offer.

**Rate limiting that reads the actual budget.** Every GraphQL response carries
`extensions.cost.throttleStatus`. The client waits on the reported remaining
budget and restore rate rather than a guessed sleep, so a Plus store with a
larger bucket is not slowed to a small store's pace. HTTP 429 is a last-resort
backstop, not the normal path.

**Webhooks are answered in milliseconds.** Shopify drops a connection it can't
finish in five seconds and re-delivers, so an inventory webhook only enqueues a
sync and replies `202`. A single background worker drains the queue — matching
Shopify's rule that one shop may run one bulk operation at a time.

**Written against a live API, not from memory.** `inventorySetQuantities` on
2026-07 requires `changeFromQuantity` — every write states the quantity it read,
so a stock movement between the bulk read and the mutation is refused rather
than overwritten — and an `@idempotent` key, minted once per batch so the
throttle retry cannot apply the same change twice.

**Auditable sync.** The `sync_runs` row is written *before* the work starts, so
a run that dies halfway still leaves a trace. The change log and the run's
closing row commit in one transaction: a run that claims twelve changes can
never store only seven of them.

**An empty warehouse feed never empties the store.** A SKU the feed doesn't
mention means "unknown", not "zero" — the single most expensive mistake a sync
like this can make, and the one rule with a test of its own.

## Stack

Go · Shopify GraphQL Admin API · Bulk Operations · OAuth 2.0 · Webhooks + HMAC ·
PostgreSQL · Docker · GitHub Actions

## Routes

```
GET  /healthz                            liveness — touches the database
GET  /                                   OAuth 2.0 install — where Shopify opens the app
GET  /install                            OAuth 2.0 install, called directly
GET  /auth/callback                      OAuth 2.0 callback
POST /sync?shop=…                        full sync, waits for the result
GET  /sync/history?shop=…                what changed, and when
POST /webhooks/inventory_levels_update   queues a sync
POST /webhooks/app_uninstalled           revokes the stored token
POST /webhooks/customers_data_request    GDPR
POST /webhooks/customers_redact          GDPR
POST /webhooks/shop_redact               GDPR
```

`/sync` and `/sync/history` are operator endpoints, guarded by `X-Sync-Token`.
Everything else is either signed by Shopify or open by design.

## Running it

New to Shopify apps, tunnels, or why either is needed?
[docs/how-it-fits-together.md](docs/how-it-fits-together.md) explains the whole
picture before the commands.

The short path, letting the Shopify CLI open the tunnel and inject credentials:

```bash
cp .env.example .env    # DATABASE_URL and SYNC_TOKEN only
make db                 # Postgres on :55432
make test
make cli                # shopify app dev, tunnel and credentials included
```

Or run every piece yourself:

```bash
cp .env.example .env    # fill in the app credentials too
make db
make test
make dev
```

Shopify requires an HTTPS callback, so expose the service while developing:

```bash
make tunnel             # cloudflared tunnel --url http://localhost:8080
```

Put that URL in `APP_URL`, and register `$APP_URL/auth/callback` as an allowed
redirection URL in the Partner dashboard. Install from the dashboard link, then:

```bash
make sync SHOP=my-store.myshopify.com
```

`WAREHOUSE_SOURCE` points at the external stock feed — a JSON object of SKU to
quantity, read from a file (`warehouse.json` is included as a sample) or an
HTTP endpoint.

Inspect what a run did:

```sql
select sku, qty_before, qty_after, changed_at
from inventory_changes
where run_id = (select max(id) from sync_runs where shop = 'my-store.myshopify.com');
```

## Known limits

- **A variant is synced only where its location is unambiguous.** Stock split
  across two locations cannot be set from a feed that gives one number per SKU
  — dividing it would be a guess, and a wrong guess quietly moves real
  inventory. Such variants are counted as examined and left alone. Supporting
  them means the feed itself has to name locations, which is a change to the
  feed's shape rather than to the sync rules.
- **Untracked variants are skipped.** Shopify refuses to set a quantity on
  them, and one refusal fails the whole batch.
- **One background worker for all shops.** Correct while a handful of stores are
  installed; beyond that the fix is one worker per shop, not more workers.
- Metafields, Metaobjects, and the Storefront API are not used here.

## Status

Functional prototype, validated against a Shopify development store: install
flow, bulk inventory read, warehouse-driven update, webhook-triggered sync, and
the persisted change log all verified in the complete workflow.
