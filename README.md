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
```

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
from the resulting JSONL — never loaded whole into memory.

**Rate limiting that reads the actual budget.** Every GraphQL response carries
`extensions.cost.throttleStatus`. The client waits on the reported remaining
budget and restore rate rather than a guessed sleep, so a Plus store with a
larger bucket is not slowed to a small store's pace. HTTP 429 is a last-resort
backstop, not the normal path.

**Webhooks are answered in milliseconds.** Shopify drops a connection it can't
finish in five seconds and re-delivers, so an inventory webhook only enqueues a
sync and replies `202`. A single background worker drains the queue — matching
Shopify's rule that one shop may run one bulk operation at a time.

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
GET  /install                            OAuth 2.0 install
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

```bash
cp .env.example .env    # fill in the app credentials
make db                 # Postgres on :55432
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

- **One location per store.** Stock is written to the first active location.
  Multi-warehouse stores need the location mapping to come from the warehouse
  feed itself — a change to the feed's shape, not to the sync rules.
- **One background worker for all shops.** Correct while a handful of stores are
  installed; beyond that the fix is one worker per shop, not more workers.
- Metafields, Metaobjects, and the Storefront API are not used here.

## Status

Functional prototype, validated against a Shopify development store: install
flow, bulk inventory read, warehouse-driven update, webhook-triggered sync, and
the persisted change log all verified in the complete workflow.
