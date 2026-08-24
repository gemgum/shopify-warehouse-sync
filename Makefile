# One door to the whole service.
#
# Not for tidiness: a command that lives only in someone's head leaves with that
# person, and the next one is left guessing.

DB_URL ?= postgres://postgres:dev@127.0.0.1:55432/shopify_sync?sslmode=disable

.PHONY: help
help:                     ## Show this list of commands
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | column -t -s "$$(printf '\t')"

.PHONY: db
db:                       ## Start a local Postgres for development
	docker compose up -d postgres

.PHONY: dev
dev:                      ## Run the service. Settings are read from .env
	go run ./cmd/api

.PHONY: tunnel
tunnel:                   ## Open a public HTTPS address for the Shopify OAuth callback
	cloudflared tunnel --url http://localhost:8080

.PHONY: test
test:                     ## Run every test
	go test ./...

.PHONY: lint
lint:                     ## Check style, and mistakes the compiler lets through
	gofmt -l .
	go vet ./...
	@command -v golangci-lint >/dev/null && golangci-lint run || \
		echo "golangci-lint is not installed — skipped. Install: https://golangci-lint.run/welcome/install/"

.PHONY: sync
sync:                     ## Run one sync. SHOP=store.myshopify.com
	@test -n "$(SHOP)" || (echo "usage: make sync SHOP=store.myshopify.com"; exit 1)
	curl -sS -X POST -H "X-Sync-Token: $$SYNC_TOKEN" \
		"http://localhost:8080/sync?shop=$(SHOP)" | jq .
