package handlers

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/internal/services"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

type SyncHandler struct {
	service *services.SyncService
	token   string
	logger  *slog.Logger
}

func NewSyncHandler(service *services.SyncService, token string, logger *slog.Logger) *SyncHandler {
	return &SyncHandler{service: service, token: token, logger: logger}
}

// authorize checks the operator token.
//
// The sync address can rewrite a store's entire inventory, and its caller is
// not Shopify — so there is no HMAC to check. The comparison is constant time:
// an ordinary one leaks how many leading characters were already right.
func (h *SyncHandler) authorize(r *http.Request) error {
	given := r.Header.Get("X-Sync-Token")
	if subtle.ConstantTimeCompare([]byte(given), []byte(h.token)) != 1 {
		return httpx.Unauthorized("A valid sync token is required.")
	}
	return nil
}

// Run performs a sync and waits for it to finish.
//
// Waits, unlike the webhook path, because the caller here is an operator or a
// scheduler that asked precisely in order to learn the outcome.
func (h *SyncHandler) Run(w http.ResponseWriter, r *http.Request) error {
	if err := h.authorize(r); err != nil {
		return err
	}

	run, err := h.service.Run(r.Context(), r.URL.Query().Get("shop"), models.TriggerManual)
	if err != nil {
		return err
	}

	httpx.OK(w, "Inventory synchronised.", run)
	return nil
}

// History returns a store's sync history.
//
// This is what answers "why is the stock that number?" without opening the
// database.
func (h *SyncHandler) History(w http.ResponseWriter, r *http.Request) error {
	if err := h.authorize(r); err != nil {
		return err
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	runs, err := h.service.History(r.Context(), r.URL.Query().Get("shop"), limit)
	if err != nil {
		return err
	}

	httpx.OK(w, "Sync history loaded.", runs)
	return nil
}
