package services

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gemgum/shopify-warehouse-sync/internal/models"
)

// The stubs below are the proof that the layers really are separate: the entire
// sync flow is tested without Postgres and without a single call to Shopify.

type directRunner struct{}

func (directRunner) Run(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type shopStub struct {
	shop models.Shop
	err  error
}

func (s shopStub) Save(context.Context, models.Shop) (models.Shop, error) { return s.shop, nil }
func (s shopStub) Find(context.Context, string) (models.Shop, error)      { return s.shop, s.err }
func (s shopStub) MarkUninstalled(context.Context, string) error          { return nil }

type historyStub struct {
	started int
	logged  []models.Update
	closed  bool
	checked int
	updated int
	failure error
}

func (h *historyStub) Start(context.Context, string, string) (int64, error) {
	h.started++
	return 1, nil
}

func (h *historyStub) Finish(_ context.Context, _ int64, checked, updated int, failure error) error {
	h.closed, h.checked, h.updated, h.failure = true, checked, updated, failure
	return nil
}

func (h *historyStub) LogChanges(_ context.Context, _ int64, updates []models.Update) error {
	h.logged = append(h.logged, updates...)
	return nil
}

func (h *historyStub) History(context.Context, string, int) ([]models.SyncRun, error) {
	return nil, nil
}

type storeStub struct {
	items    []models.InventoryItem
	reads    int
	written  []models.Update
	errList  error
	errApply error
}

func (s *storeStub) List(context.Context, models.Shop) ([]models.InventoryItem, error) {
	s.reads++
	return s.items, s.errList
}

func (s *storeStub) Apply(_ context.Context, _ models.Shop, updates []models.Update) error {
	if s.errApply != nil {
		return s.errApply
	}
	s.written = append(s.written, updates...)
	return nil
}

type warehouseStub struct {
	stock map[string]int
	err   error
}

func (w warehouseStub) Stock(context.Context) (map[string]int, error) { return w.stock, w.err }

func build(store *storeStub, warehouse warehouseStub, history *historyStub) *SyncService {
	return NewSyncService(
		directRunner{},
		shopStub{shop: models.Shop{Domain: "test.myshopify.com", AccessToken: "t"}},
		history, store, warehouse,
		slog.New(slog.DiscardHandler),
	)
}

func TestRunWritesChangesAndClosesTheHistory(t *testing.T) {
	store := &storeStub{items: []models.InventoryItem{
		{SKU: "A", InventoryItemID: "gid://i/1", LocationID: "gid://l/1", Quantity: 5},
		{SKU: "B", InventoryItemID: "gid://i/2", LocationID: "gid://l/1", Quantity: 3},
	}}
	history := &historyStub{}
	s := build(store, warehouseStub{stock: map[string]int{"A": 12, "B": 3}}, history)

	run, err := s.Run(context.Background(), "test.myshopify.com", models.TriggerManual)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	if run.Checked != 2 || run.Updated != 1 {
		t.Errorf("got checked=%d updated=%d, want 2 and 1", run.Checked, run.Updated)
	}
	if len(store.written) != 1 || store.written[0].SKU != "A" || store.written[0].NewQuantity != 12 {
		t.Errorf("what was written to Shopify: %+v", store.written)
	}
	if len(history.logged) != 1 || history.logged[0].Quantity != 5 {
		t.Errorf("the change log must keep the previous quantity, got %+v", history.logged)
	}
	if !history.closed || history.failure != nil {
		t.Errorf("history closed=%v error=%v", history.closed, history.failure)
	}
}

// A sync Shopify refused must not leave behind a change log claiming success —
// and its row must still be closed with the reason.
func TestRunFailureRecordsTheCauseWithoutAFalseLog(t *testing.T) {
	refused := errors.New("shopify refused")
	store := &storeStub{
		items:    []models.InventoryItem{{SKU: "A", InventoryItemID: "gid://i/1", LocationID: "gid://l/1", Quantity: 5}},
		errApply: refused,
	}
	history := &historyStub{}
	s := build(store, warehouseStub{stock: map[string]int{"A": 12}}, history)

	if _, err := s.Run(context.Background(), "test.myshopify.com", models.TriggerManual); err == nil {
		t.Fatal("a sync Shopify refused was reported as successful")
	}
	if len(history.logged) != 0 {
		t.Errorf("a change log was written even though the mutation failed: %+v", history.logged)
	}
	if !history.closed || !errors.Is(history.failure, refused) {
		t.Errorf("history closed=%v error=%v", history.closed, history.failure)
	}
}

// The warehouse feed is read first. If the warehouse is unreachable, the bulk
// operation — which can take minutes on Shopify's side — must not start at all.
func TestRunDoesNotTouchShopifyWhenTheWarehouseIsDown(t *testing.T) {
	store := &storeStub{}
	history := &historyStub{}
	s := build(store, warehouseStub{err: errors.New("warehouse down")}, history)

	if _, err := s.Run(context.Background(), "test.myshopify.com", models.TriggerManual); err == nil {
		t.Fatal("a dead warehouse feed was reported as successful")
	}
	if store.reads != 0 {
		t.Errorf("Shopify was read %d times while the warehouse was down", store.reads)
	}
	if !history.closed {
		t.Error("the history was not closed")
	}
}

// One large stock upload on the merchant's side sends hundreds of webhooks.
// What is needed is one sync.
func TestEnqueueDoesNotQueueTheSameStoreTwice(t *testing.T) {
	s := build(&storeStub{}, warehouseStub{}, &historyStub{})

	for range 10 {
		s.Enqueue("test.myshopify.com")
	}
	s.Enqueue("other.myshopify.com")

	if len(s.queue) != 2 {
		t.Fatalf("the queue holds %d entries, want 2", len(s.queue))
	}

	// Once the worker has taken its entry, that store must be able to queue
	// again — stock changing partway through a sync may not be picked up by it.
	<-s.queue
	s.release("test.myshopify.com")
	s.Enqueue("test.myshopify.com")

	if len(s.queue) != 2 {
		t.Fatalf("the store could not queue again after its turn was taken")
	}
}

func TestRunRejectsADomainThatIsNotMyshopify(t *testing.T) {
	s := build(&storeStub{}, warehouseStub{}, &historyStub{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if _, err := s.Run(ctx, "attacker.example.com", models.TriggerManual); err == nil {
		t.Fatal("a domain outside myshopify.com was accepted")
	}
}
