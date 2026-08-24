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

# Where cloudflared lives.
#
# The Shopify CLI wants to download its own copy into its install directory,
# which is root-owned when the CLI came from `npm -g` — so it fails with EACCES
# and no way forward that does not involve sudo. SHOPIFY_CLI_CLOUDFLARED_PATH
# points it at a user-owned copy instead, and the whole problem disappears.
CLOUDFLARED ?= $(HOME)/.local/bin/cloudflared

.PHONY: cloudflared
cloudflared:              ## Fetch the tunnel binary the Shopify CLI expects (no sudo)
	@test -x $(CLOUDFLARED) && echo "already at $(CLOUDFLARED)" || ( \
		mkdir -p $(dir $(CLOUDFLARED)) && \
		curl -fsSL -o $(CLOUDFLARED) https://github.com/cloudflare/cloudflared/releases/download/2024.8.2/cloudflared-linux-amd64 && \
		chmod +x $(CLOUDFLARED) && echo "installed to $(CLOUDFLARED)" )

.PHONY: cli
cli: cloudflared          ## Tunnel + credentials + install link, all from the Shopify CLI
	SHOPIFY_CLI_CLOUDFLARED_PATH=$(CLOUDFLARED) shopify app dev

.PHONY: deploy-config
deploy-config:            ## Push shopify.app.toml as a new app version
	shopify app deploy

.PHONY: tunnel
tunnel: cloudflared       ## Open a public HTTPS address by hand, without the CLI
	$(CLOUDFLARED) tunnel --url http://localhost:8080

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
