// Package routes mounts every address onto one mux and wraps it in middleware.
//
// A handler does not know its own address, and this file does not know the
// inside of any handler. The only thing joining them is the list below — so the
// service's entire HTTP surface can be read in one screen.
package routes

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/handlers"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
	"github.com/gemgum/shopify-warehouse-sync/pkg/middleware"
)

type Options struct {
	Logger *slog.Logger

	// Alive is called by /healthz. Nil means health is not checked — used by
	// tests that assemble the router without a database.
	Alive func(ctx context.Context) error

	Install *handlers.InstallHandler
	Sync    *handlers.SyncHandler
	Webhook *handlers.WebhookHandler
}

func NewRouter(opts Options) http.Handler {
	mux := http.NewServeMux()

	route := func(fn httpx.Route) http.HandlerFunc {
		return httpx.Wrap(opts.Logger, fn)
	}

	healthz(mux, route, opts.Alive)

	Install(mux, route, opts.Install)
	Sync(mux, route, opts.Sync)
	Webhook(mux, route, opts.Webhook)

	// Ordered from the outside in: the first one named is the first to touch a
	// request. Recover is outermost so a panic in any middleware is caught too.
	return middleware.Chain(mux,
		middleware.Recover(opts.Logger),
		middleware.Log(opts.Logger),
	)
}

// healthz touches the database on its way through.
//
// An address that only reports "alive" without checking anything would still
// say healthy while the database is down — and at that moment no sync may run
// at all, because none of it could be recorded.
func healthz(mux *http.ServeMux, route func(httpx.Route) http.HandlerFunc,
	alive func(context.Context) error) {

	mux.HandleFunc("GET /healthz", route(func(w http.ResponseWriter, r *http.Request) error {
		if alive != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()

			if err := alive(ctx); err != nil {
				return httpx.DatabaseError(err)
			}
		}
		httpx.OK(w, "Service is running.", nil)
		return nil
	}))
}
