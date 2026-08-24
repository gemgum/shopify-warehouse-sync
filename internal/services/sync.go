package services

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
	"github.com/gemgum/shopify-warehouse-sync/pkg/httpx"
)

type SyncService struct {
	base
	shops     models.ShopRepository
	runs      models.SyncRepository
	store     models.StoreInventory
	warehouse models.WarehouseFeed

	// queue is the path for webhook-triggered syncs.
	//
	// A webhook must be answered in under five seconds — Shopify drops
	// anything slower and re-delivers, and a full sync is far past that. So the
	// handler only drops the shop name in here and replies immediately; a
	// single worker behind it does the work.
	queue chan string

	// pending keeps a store to one outstanding entry in the queue.
	//
	// Without it, one large stock upload on the merchant's side sends hundreds
	// of webhooks in a minute and the queue fills with the same shop name —
	// each one triggering a full bulk operation whose result is identical. What
	// is needed is one sync, after the wave has passed.
	mu      sync.Mutex
	waiting map[string]bool
}

func NewSyncService(db runner, shops models.ShopRepository, runs models.SyncRepository,
	store models.StoreInventory, warehouse models.WarehouseFeed, logger *slog.Logger) *SyncService {

	return &SyncService{
		base:      base{db: db, logger: logger},
		shops:     shops,
		runs:      runs,
		store:     store,
		warehouse: warehouse,
		// The buffer is small on purpose. One store whose stock changes
		// hundreds of times in a minute does not need hundreds of full syncs —
		// it needs one, once the wave has passed.
		queue:   make(chan string, 16),
		waiting: map[string]bool{},
	}
}

// Diff reconciles store stock against warehouse stock.
//
// This is the whole of this service's business rules, and deliberately a pure
// function: it touches no database and no network, so it can be tested without
// either.
//
// A SKU the warehouse does not mention is **skipped, not zeroed**. A feed that
// does not name a SKU means "unknown", and translating not-knowing into zero
// would empty every product that happens not to be registered in the warehouse
// yet — precisely the most expensive mistake a system like this can make.
func Diff(store []models.InventoryItem, warehouse map[string]int) []models.Update {
	var updates []models.Update

	for _, item := range store {
		// A variant with no SKU cannot be matched against anything. One with no
		// id or location cannot be written back.
		if item.SKU == "" || item.InventoryItemID == "" || item.LocationID == "" {
			continue
		}

		wanted, known := warehouse[item.SKU]
		if !known {
			continue
		}
		// Negative stock means nothing to Shopify, and a warehouse feed that
		// sends it is usually counting unfulfilled orders. Zero is the correct
		// translation: the item is genuinely out.
		if wanted < 0 {
			wanted = 0
		}
		if wanted == item.Quantity {
			continue
		}

		updates = append(updates, models.Update{InventoryItem: item, NewQuantity: wanted})
	}

	// Sorted so change logs can be compared between syncs. Shopify does not
	// promise a stable order.
	sort.Slice(updates, func(i, j int) bool { return updates[i].SKU < updates[j].SKU })
	return updates
}

// Run performs one full sync for a store.
//
// Its sync_runs row is written **before** the work begins and closed through a
// defer, so a sync that fails midway still leaves a full trace with its cause.
// If the row were only written on completion, the failures would be the least
// visible thing of all.
func (s *SyncService) Run(ctx context.Context, domain, trigger string) (run models.SyncRun, err error) {
	defer func() {
		err = logError(ctx, s.logger, "SyncService.Run", []any{"shop", domain, "trigger", trigger}, err)
	}()

	if !models.ValidShopDomain(domain) {
		return models.SyncRun{}, httpx.BadRequest("The shop parameter must be a myshopify.com domain.")
	}

	shop, err := queryOne(ctx, s.base, func(ctx context.Context) (models.Shop, error) {
		return s.shops.Find(ctx, domain)
	})
	if err != nil {
		return models.SyncRun{}, err
	}

	runID, err := queryOne(ctx, s.base, func(ctx context.Context) (int64, error) {
		return s.runs.Start(ctx, domain, trigger)
	})
	if err != nil {
		return models.SyncRun{}, err
	}

	checked, updates, syncErr := s.reconcile(ctx, shop)

	// Closing the history runs whatever happened above, and with a context of
	// its own: if what killed the sync was an expired context, that same
	// context would refuse the closing query too — and the row would hang
	// without a finished_at forever.
	closing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	if err := s.db.Run(closing, func(ctx context.Context) error {
		if syncErr == nil {
			// Written in the same transaction that closes the history. A row
			// claiming "twelve SKUs changed" while storing only seven of them
			// is worse than storing nothing — whoever reads it has no reason to
			// be suspicious.
			if err := s.runs.LogChanges(ctx, runID, updates); err != nil {
				return err
			}
		}
		return s.runs.Finish(ctx, runID, checked, len(updates), syncErr)
	}); err != nil {
		s.logger.Error("could not close the sync history", "shop", domain, "run", runID, "error", err)
	}

	if syncErr != nil {
		return models.SyncRun{}, syncErr
	}
	return models.SyncRun{
		ID: runID, Shop: domain, Trigger: trigger,
		Checked: checked, Updated: len(updates),
	}, nil
}

// reconcile reads both sides, decides, then writes.
//
// The warehouse is read first, and deliberately so: if its feed is unreachable
// there is no point starting a bulk operation that can take minutes on
// Shopify's side.
func (s *SyncService) reconcile(ctx context.Context, shop models.Shop) (checked int, updates []models.Update, err error) {
	stock, err := s.warehouse.Stock(ctx)
	if err != nil {
		return 0, nil, err
	}

	items, err := s.store.List(ctx, shop)
	if err != nil {
		return 0, nil, err
	}
	checked = len(items)

	updates = Diff(items, stock)
	if len(updates) == 0 {
		return checked, nil, nil
	}

	if err := s.store.Apply(ctx, shop, updates); err != nil {
		// Some batches may already have landed. What is returned is the error,
		// and the history keeps its cause; the next sync finishes the rest,
		// because Diff is always recomputed from the current state.
		return checked, nil, err
	}
	return checked, updates, nil
}

// History returns a store's sync history.
func (s *SyncService) History(ctx context.Context, domain string, limit int) (runs []models.SyncRun, err error) {
	defer func() {
		err = logError(ctx, s.logger, "SyncService.History", []any{"shop", domain}, err)
	}()

	if !models.ValidShopDomain(domain) {
		return nil, httpx.BadRequest("The shop parameter must be a myshopify.com domain.")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	return queryOne(ctx, s.base, func(ctx context.Context) ([]models.SyncRun, error) {
		return s.runs.History(ctx, domain, limit)
	})
}

// Enqueue hands a sync off to be done in the background.
//
// Never blocks, and never queues the same store twice. A sync always recomputes
// the entire state, so two consecutive entries for one store produce exactly
// the same work — the second only spends Shopify budget without changing
// anything.
func (s *SyncService) Enqueue(domain string) {
	s.mu.Lock()
	if s.waiting[domain] {
		s.mu.Unlock()
		s.logger.Debug("sync request skipped, store is already queued", "shop", domain)
		return
	}
	s.waiting[domain] = true
	s.mu.Unlock()

	select {
	case s.queue <- domain:
	default:
		// A full queue means a dozen other stores are waiting. The mark is
		// released again so this store's next webhook still has a chance to get
		// in.
		s.release(domain)
		s.logger.Warn("sync queue is full, request dropped", "shop", domain)
	}
}

func (s *SyncService) release(domain string) {
	s.mu.Lock()
	delete(s.waiting, domain)
	s.mu.Unlock()
}

// StartWorker runs the worker that drains the webhook queue.
//
// One worker, not a pool. Shopify allows only one bulk operation per store at a
// time, so concurrent syncs for the same store are guaranteed to be refused;
// and sequential syncs across different stores are already far faster than any
// reasonable rate of stock change.
//
// ponytail: one worker for every store. Serving dozens of stores would call for
// one worker per store — not for more workers.
func (s *SyncService) StartWorker(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return

			case domain := <-s.queue:
				// The mark is released **before** the sync runs, not after.
				// Stock that changes partway through this sync may not be
				// picked up by it, so a webhook arriving then must still be
				// able to queue the next one.
				s.release(domain)

				// A deadline of its own: a worker hanging on one store means
				// every other store stops being synced.
				job, cancel := context.WithTimeout(ctx, 10*time.Minute)
				if _, err := s.Run(job, domain, models.TriggerWebhook); err != nil {
					s.logger.Error("webhook-triggered sync failed", "shop", domain, "error", err)
				}
				cancel()
			}
		}
	}()
}
