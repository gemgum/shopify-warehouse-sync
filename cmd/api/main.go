// The Shopify ↔ warehouse inventory synchronisation service.
//
// One binary: it serves the OAuth install, receives Shopify's webhooks, and
// runs syncs on an operator's request. The full flow is in the README; how the
// layers fit together is in internal/api/build.go.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/api"
	"github.com/gemgum/shopify-warehouse-sync/internal/config"
	"github.com/gemgum/shopify-warehouse-sync/internal/database"
)

func main() {
	// The production container has no shell, no curl, and no wget — so the
	// healthcheck is done by this binary itself. One flag, and no extra tooling
	// has to ship in the image merely so it can check on itself.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("failed to start", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	opening, cancelOpening := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpening()

	db, err := database.Open(opening, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	// The service's lifetime. The webhook queue worker stops when this is
	// cancelled.
	lifetime, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	server := &http.Server{
		Addr:         cfg.Address,
		Handler:      api.Build(lifetime, db, cfg, logger),
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	go func() {
		logger.Info("service listening", "address", cfg.Address, "api", cfg.APIVersion)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for a stop signal, then give the sync in flight time to finish. A
	// sync cut off midway leaves a store half updated, and a history row that
	// never gets closed.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	shutdown, done := context.WithTimeout(context.Background(), 30*time.Second)
	defer done()

	return server.Shutdown(shutdown)
}

// healthcheck calls /healthz on the service running in the same container and
// returns an exit code Docker understands.
//
// The address comes from ADDRESS so it stays right if the port is changed.
func healthcheck() int {
	address := os.Getenv("ADDRESS")
	if address == "" {
		address = ":8080"
	}

	client := http.Client{Timeout: 4 * time.Second}

	resp, err := client.Get("http://127.0.0.1" + address + "/healthz")
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
