// Package api assembles the whole service from its parts.
//
// **The only place that knows which implementation is in use.** Everything
// above it knows only interfaces, so swapping the warehouse source or dropping
// in a stub for a test is done here and nowhere else.
//
// Kept apart from `routes` deliberately: `routes` answers "which addresses
// exist", this file answers "who uses whom". They change for different reasons.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gemgum/shopify-warehouse-sync/internal/config"
	"github.com/gemgum/shopify-warehouse-sync/internal/database"
	"github.com/gemgum/shopify-warehouse-sync/internal/handlers"
	"github.com/gemgum/shopify-warehouse-sync/internal/repository"
	"github.com/gemgum/shopify-warehouse-sync/internal/routes"
	"github.com/gemgum/shopify-warehouse-sync/internal/services"
	"github.com/gemgum/shopify-warehouse-sync/internal/shopify"
	"github.com/gemgum/shopify-warehouse-sync/internal/warehouse"
)

// Build assembles the service and starts the webhook queue worker.
//
// The ctx passed in is the service's lifetime: when it is cancelled, the worker
// stops with it.
func Build(ctx context.Context, db *database.DB, cfg config.Config, logger *slog.Logger) http.Handler {
	client := shopify.New(shopify.Options{
		APIKey:      cfg.APIKey,
		APISecret:   cfg.APISecret,
		Scopes:      cfg.Scopes,
		CallbackURL: cfg.CallbackURL(),
		APIVersion:  cfg.APIVersion,
	}, logger)

	shops := repository.Shop{}
	runs := repository.Sync{}
	feed := warehouse.NewFeed(cfg.WarehouseSource)

	installService := services.NewInstallService(db, shops, client, cfg.AppURL, logger)
	syncService := services.NewSyncService(db, shops, runs, client, feed, logger)

	// The worker that drains webhook-queued syncs. Started here because this is
	// where the service is built — not in main, which does not know which
	// service owns a queue.
	syncService.StartWorker(ctx)

	return routes.NewRouter(routes.Options{
		Logger: logger,
		Alive:  db.Ping,

		Install: handlers.NewInstallHandler(installService, logger),
		Sync:    handlers.NewSyncHandler(syncService, cfg.SyncToken, logger),
		Webhook: handlers.NewWebhookHandler(client, syncService, installService, logger),
	})
}
