# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make db                      # Postgres on :55432 (docker compose)
make dev                     # go run ./cmd/api — reads .env
make test                    # go test ./...
make lint                    # gofmt -l . && go vet ./... && golangci-lint (optional)
make cli                     # shopify app dev — tunnel + credentials injected
make tunnel                  # cloudflared by hand, when not using the CLI
make sync SHOP=x.myshopify.com
```

One test: `go test ./internal/services -run TestDecide`. CI runs `go test -race ./...`,
so reproduce race failures with `-race`. No test needs Postgres or a live store — if a
new test does, it is in the wrong layer.

Go 1.26. The only direct dependency is `pgx/v5`; keep it that way.

## Architecture

Dependencies point inward. `internal/models` declares the interfaces the business rules
need (`ShopRepository`, `SyncRepository`, `StoreInventory`, `WarehouseFeed`);
`repository`, `shopify`, and `warehouse` implement them and are never imported by
`services`. `internal/api/build.go` is the **only** file that picks implementations —
new wiring goes there, not into a package's `init` or a global.

`internal/routes/routes.go` is the whole HTTP surface in one screen; handlers never know
their own address.

### Rules that hold across files

- **Handlers return `error`, they never write one.** `httpx.Wrap` renders it. Only
  `*httpx.Error` reaches a caller; the underlying cause is unexported and log-only.
  Pick the constructor by category (`UpstreamError` → 502, so an operator looks at
  Shopify rather than at us).
- **The transaction rides in the `context`.** `database.DB.Run` opens it,
  `database.Tx(ctx)` retrieves it. That is why domain interfaces mention only
  `context.Context` and never `pgx`. Touching the DB outside `Run` is `ErrNoTransaction`
  — a bug, not a state.
- **`internal/repository` is the only place SQL is written**, and every error leaves it
  through `problem.FromPostgres`. Use its `One` / `MaybeOne` / `Many` helpers; they do
  both steps.
- **`services.Decide` is pure** — one variant in, an update or nothing out. All sync
  rules belong there so they stay testable without a database. A SKU the warehouse feed
  does not mention is **left alone, never zeroed**; that rule has its own test and must
  keep it.
- **Webhooks reply in milliseconds.** Shopify drops anything over five seconds and
  re-delivers. The handler enqueues onto `SyncService.queue` and returns 202; one worker
  (started in `build.go`) drains it, and the `waiting` map collapses a burst of webhooks
  for one shop into a single sync.
- **Migrations are embedded and run on every startup**, so every statement must be
  `create ... if not exists`. Add a numbered file in `internal/database/migrations/`.
- **Config: the environment wins, `.env` only fills in blanks.** A missing required
  setting stops the service at startup by design. `APIVersion` is pinned deliberately —
  bumping it is a decision, and Admin API mutations differ between versions
  (`inventorySetQuantities` needs `changeFromQuantity` and an idempotency key).
- Every write states the quantity it read, so a concurrent stock movement is refused
  rather than overwritten. Do not drop that argument to simplify a call.

`/sync` and `/sync/history` are operator endpoints behind `X-Sync-Token`; everything else
is either HMAC-signed by Shopify or open by design. Verify the signature over the raw
body before parsing it.

## Documentation

`README.md` covers the problem, routes, and known limits. `docs/how-it-fits-together.md`
explains tunnels, dev stores, and why traffic runs both ways. Both are prose-heavy and
current — read before rewriting, and keep new comments in the same register: they explain
*why*, not *what*.
